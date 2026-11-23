package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	frontendassets "sb-proxy/frontend"

	"github.com/gin-gonic/gin"
)

func TestSelectStaticEncoding(t *testing.T) {
	for _, test := range []struct {
		header   string
		br, gzip bool
		want     string
	}{
		{"", true, true, ""}, {"gzip, deflate, br", true, true, "br"},
		{"gzip", true, true, "gzip"}, {" X-GZIP ; Q = 1.000 , BR ; q = 0.001 ", true, true, "gzip"},
		{"br;q=0, gzip", true, true, "gzip"}, {"br;q=0.5, gzip;q=0.9", true, true, "gzip"},
		{"identity;q=1, br;q=0.5", true, true, ""}, {"identity;q=0.5, br;q=0.5, gzip;q=0.5", true, true, "br"},
		{"*", true, true, "br"}, {"br;q=0, *", true, true, "gzip"}, {"*;q=0", true, true, ""},
		{"*;q=0, gzip", true, true, "gzip"}, {"br;q=0.2, *;q=0.8", true, false, ""},
		{"br;q=0.2, *;q=0.8", true, true, "gzip"}, {"identity;q=0, br;q=0, gzip;q=0", true, true, ""},
		{"br", false, true, ""}, {"br, gzip", false, false, ""}, {"gzip;q=0.001", false, true, "gzip"},
		{"br;q=1.001, gzip", true, true, "gzip"}, {"br;q=0.1234, gzip", true, true, "gzip"},
		{"br;q=NaN, gzip", true, true, "gzip"}, {"br;q=0, br;q=1, gzip", true, true, "gzip"},
	} {
		t.Run(test.header, func(t *testing.T) {
			if got := selectStaticEncoding(test.header, test.br, test.gzip); got != test.want {
				t.Fatalf("encoding=%q, want %q", got, test.want)
			}
		})
	}
}

func gzipTestContent(t *testing.T, content []byte, level int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func staticTestServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dist := writeTestFrontendDist(t)
	for name, content := range files {
		if content == nil {
			if err := os.Remove(filepath.Join(dist, name)); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(filepath.Join(dist, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	if err := registerFrontendRoutes(router, dist, "1.7.23"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	server.Client().Timeout = 5 * time.Second
	server.Client().Transport.(*http.Transport).DisableCompression = true
	t.Cleanup(server.Close)
	return server
}

func requestStaticProtocol(t *testing.T, server *httptest.Server, method, endpoint string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("HTTP body/protocol error: %v", err)
	}
	return response, body
}

func TestPrecompressedStaticFiles(t *testing.T) {
	original := []byte(strings.Repeat("console.log('hello');", 100))
	gz, br := gzipTestContent(t, original, gzip.BestCompression), []byte("brotli bytes")
	server := staticTestServer(t, map[string][]byte{"assets/app.js": original, "assets/app.js.gz": gz, "assets/app.js.br": br, "assets/font.woff2": []byte("font"), "assets/no-ext": []byte("<html>test</html>"), "assets/no-ext.gz": gz})
	for _, test := range []struct {
		method, file, accept, rangeHeader, encoding string
		status, length                              int
		body                                        []byte
	}{
		{"GET", "app.js", "br, gzip", "", "br", 200, len(br), br},
		{"GET", "app.js", "gzip", "", "gzip", 200, len(gz), gz},
		{"GET", "app.js", "", "", "", 200, len(original), original},
		{"HEAD", "app.js", "br", "", "br", 200, len(br), nil},
		{"HEAD", "app.js", "gzip", "", "gzip", 200, len(gz), nil},
		{"HEAD", "app.js", "", "", "", 200, len(original), nil},
		{"HEAD", "app.js", "br", "bytes=0-9", "br", 200, len(br), nil},
		{"GET", "app.js", "br", "bytes=0-9", "", 206, 10, original[:10]},
		{"GET", "app.js", "*;q=0", "bytes=0-9", "", 206, 10, original[:10]},
		{"GET", "app.js", "identity;q=0, gzip", "bytes=0-9", "", 206, 10, original[:10]},
		{"GET", "font.woff2", "br, gzip", "", "", 200, 4, []byte("font")},
		{"GET", "app.js.gz", "br, gzip", "", "", 200, len(gz), gz},
		{"GET", "no-ext", "gzip", "", "gzip", 200, len(gz), gz},
	} {
		t.Run(test.method+test.file+test.accept+test.rangeHeader, func(t *testing.T) {
			response, body := requestStaticProtocol(t, server, test.method, "/assets/"+test.file, map[string]string{"Accept-Encoding": test.accept, "Range": test.rangeHeader})
			if response.StatusCode != test.status || response.Header.Get("Content-Encoding") != test.encoding || response.ContentLength != int64(test.length) || !bytes.Equal(body, test.body) {
				t.Fatalf("status=%d headers=%v body=%d bytes", response.StatusCode, response.Header, len(body))
			}
			if test.file == "app.js" && (!strings.Contains(response.Header.Get("Content-Type"), "javascript") || response.Header.Get("Vary") != "Accept-Encoding") {
				t.Fatal("incorrect original MIME or Vary")
			}
			if test.status == 206 && response.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-9/%d", len(original)) {
				t.Fatal("incorrect Content-Range")
			}
			if test.file == "font.woff2" && response.Header.Get("Vary") != "" {
				t.Fatal("font without variants should not vary")
			}
			if test.file == "no-ext" && !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
				t.Fatal("unknown MIME must be sniffed from the original bytes")
			}
		})
	}
	response, _ := requestStaticProtocol(t, server, "GET", "/assets/app.js", map[string]string{"Accept-Encoding": "br", "Range": "bytes=99999-"})
	if response.StatusCode != 416 || response.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(original)) || response.Header.Get("Content-Encoding") != "" {
		t.Fatal("invalid range did not return identity 416")
	}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Vary", "Origin")
	addVaryAcceptEncoding(recorder.Header())
	addVaryAcceptEncoding(recorder.Header())
	if recorder.Header().Get("Vary") != "Origin" || len(recorder.Header().Values("Vary")) != 2 {
		t.Fatal("existing Vary overwritten or duplicated")
	}
}

func TestStaticRoutesSafetyAndBrandFiles(t *testing.T) {
	svg := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\">logo</svg>")
	server := staticTestServer(t, map[string][]byte{"logo.svg": svg, "logo.svg.br": []byte("br svg"), "logo.svg.gz": gzipTestContent(t, svg, gzip.BestCompression), "brand-mark.svg": nil})
	for _, endpoint := range []string{"/assets/", "/assets/../index.html", "/assets/%2e%2e/index.html", "/assets/%00bad", "/assets/..%5cindex.html", "/assets/missing.js", "/brand-mark.svg"} {
		for _, method := range []string{"GET", "HEAD"} {
			response, body := requestStaticProtocol(t, server, method, endpoint, nil)
			if response.StatusCode != 404 || strings.Contains(response.Header.Get("Cache-Control"), "immutable") || method == "HEAD" && len(body) != 0 {
				t.Fatalf("%s %s: %d %v", method, endpoint, response.StatusCode, response.Header)
			}
		}
	}
	brandServer := staticTestServer(t, map[string][]byte{"brand-mark.svg": svg, "brand-mark.svg.br": []byte("br svg"), "brand-mark.svg.gz": gzipTestContent(t, svg, gzip.BestCompression)})
	for _, test := range []struct {
		server   *httptest.Server
		endpoint string
	}{{server, "/logo.svg"}, {brandServer, "/brand-mark.svg"}} {
		for _, encoding := range []string{"br", "gzip", ""} {
			get, body := requestStaticProtocol(t, test.server, "GET", test.endpoint, map[string]string{"Accept-Encoding": encoding})
			head, headBody := requestStaticProtocol(t, test.server, "HEAD", test.endpoint, map[string]string{"Accept-Encoding": encoding})
			if get.StatusCode != 200 || head.StatusCode != 200 || get.Header.Get("Content-Encoding") != encoding || head.Header.Get("Content-Encoding") != encoding || get.ContentLength != int64(len(body)) || head.ContentLength != get.ContentLength || len(headBody) != 0 || !strings.HasPrefix(get.Header.Get("Content-Type"), "image/svg+xml") || get.Header.Get("Cache-Control") != assetsCacheControlHeader || get.Header.Get("X-App-Version") != "1.7.23" || get.Header.Get("X-Frontend-Fingerprint") == "" {
				t.Fatal("incorrect brand representation headers/body")
			}
		}
	}
	for _, name := range []string{"assets/dir", "assets/dir/", "assets/missing.js", "../index.html", "bad\x00", "..\\index.html"} {
		if _, err := readRegularStaticFile(fstest.MapFS{"assets/dir": {Mode: fs.ModeDir}}, name); err == nil {
			t.Fatalf("unsafe/non-file path accepted: %q", name)
		}
	}
}

type countedStaticFS struct {
	fs.FS
	readBytes map[string]int
}

func (assets countedStaticFS) Open(name string) (fs.File, error) {
	file, err := assets.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &countedStaticFile{File: file, name: name, readBytes: assets.readBytes}, nil
}

type countedStaticFile struct {
	fs.File
	name      string
	readBytes map[string]int
}

func (file *countedStaticFile) Read(buffer []byte) (int, error) {
	read, err := file.File.Read(buffer)
	file.readBytes[file.name] += read
	return read, err
}

func TestServePrecompressedStaticFileReadsOnlySelectedRepresentation(t *testing.T) {
	original := []byte("<!doctype html>" + strings.Repeat("x", 1024))
	for _, test := range []struct {
		name, rangeHeader, wantEncoding string
		wantBody                        []byte
		wantOriginalBytes               int
	}{
		{name: "assets/app.js", wantEncoding: "br", wantBody: []byte("br")},
		{name: "assets/no-extension", wantEncoding: "br", wantBody: []byte("br"), wantOriginalBytes: 512},
		{name: "assets/app.js", rangeHeader: "bytes=0-3", wantBody: original[:4], wantOriginalBytes: len(original)},
	} {
		files := countedStaticFS{
			FS: fstest.MapFS{
				test.name:         {Data: original},
				test.name + ".br": {Data: []byte("br")},
				test.name + ".gz": {Data: []byte("gzip")},
			},
			readBytes: make(map[string]int),
		}
		request := httptest.NewRequest(http.MethodGet, "/"+test.name, nil)
		request.Header.Set("Accept-Encoding", "br, gzip")
		request.Header.Set("Range", test.rangeHeader)
		recorder := httptest.NewRecorder()
		if err := servePrecompressedStaticFile(recorder, request, files, test.name); err != nil {
			t.Fatal(err)
		}
		if recorder.Header().Get("Content-Encoding") != test.wantEncoding || recorder.Header().Get("Vary") != "Accept-Encoding" || !bytes.Equal(recorder.Body.Bytes(), test.wantBody) {
			t.Fatalf("unexpected representation: %v, %q", recorder.Header(), recorder.Body.Bytes())
		}
		wantBrotliBytes := 0
		if test.wantEncoding == "br" {
			wantBrotliBytes = len(test.wantBody)
		}
		if files.readBytes[test.name] != test.wantOriginalBytes || files.readBytes[test.name+".br"] != wantBrotliBytes || files.readBytes[test.name+".gz"] != 0 {
			t.Fatalf("unexpected reads: %v", files.readBytes)
		}
	}
}

func TestStaticPreconditionsWithRealHTTPClient(t *testing.T) {
	server := staticTestServer(t, map[string][]byte{"assets/app.js": []byte("app"), "assets/app.js.gz": gzipTestContent(t, []byte("app"), gzip.BestCompression)})
	for _, endpoint := range []string{"/assets/app.js", "/", "/dashboard/nodes"} {
		ordinary, _ := requestStaticProtocol(t, server, "GET", endpoint, map[string]string{"Accept-Encoding": "gzip"})
		for _, noneMatch := range []string{"", "*", ordinary.Header.Get("ETag")} {
			response, body := requestStaticProtocol(t, server, "GET", endpoint, map[string]string{"Accept-Encoding": "gzip", "If-Match": `"not-current"`, "If-None-Match": noneMatch})
			if response.StatusCode != 412 || len(body) != 0 || response.Header.Get("Content-Encoding") != "" || response.ContentLength > 0 {
				t.Fatalf("unclean 412/precedence: %s %v", endpoint, response.Header)
			}
		}
		if etag := ordinary.Header.Get("ETag"); etag != "" {
			for _, match := range []string{etag, "W/" + etag, `"other", W/` + etag, "*"} {
				response, body := requestStaticProtocol(t, server, "GET", endpoint, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": match})
				if response.StatusCode != 304 || len(body) != 0 || response.Header.Get("Content-Encoding") != "" || response.Header.Get("ETag") != etag || response.Header.Get("Vary") != "Accept-Encoding" {
					t.Fatal("unclean 304")
				}
			}
		}
	}
}

func TestIndexCompressionUsesFinalInjectedContent(t *testing.T) {
	t.Setenv("SBPM_NODES_VIRTUAL_THRESHOLD", "42")
	t.Setenv("SBPM_BATCH_CHECK_IP_CONCURRENCY", "7")
	raw := []byte("<html><head></head><body>" + strings.Repeat("content", 200) + "</body></html>")
	server := staticTestServer(t, map[string][]byte{"index.html": raw, "index.html.gz": gzipTestContent(t, raw, gzip.BestCompression), "index.html.br": []byte("stale brotli")})
	plain, original := requestStaticProtocol(t, server, "GET", "/", nil)
	for _, accept := range []string{"br", "gzip", "br, gzip"} {
		response, body := requestStaticProtocol(t, server, "GET", "/dashboard/nodes", map[string]string{"Accept-Encoding": accept, "If-None-Match": plain.Header.Get("ETag")})
		if response.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else if accept != "br" {
			t.Fatal("final index gzip not enabled")
		}
		if accept == "br" && response.StatusCode != 304 || accept != "br" && (response.StatusCode != 200 || !bytes.Equal(body, original)) {
			t.Fatal("index variants returned stale content or shared ETag")
		}
	}
	for _, meta := range []string{`<meta name="sbpm-nodes-virtual-threshold" content="42" />`, `<meta name="sbpm-batch-check-ip-concurrency" content="7" />`} {
		if !bytes.Contains(original, []byte(meta)) {
			t.Fatalf("missing final meta: %s", meta)
		}
	}
	variants, err := prepareIndexVariants(fstest.MapFS{}, []byte("a"), []byte("b"))
	if err != nil || len(variants) != 0 {
		t.Fatal("gzip larger than original should not be enabled")
	}
}

func TestIndexETagTracksCompressedBytes(t *testing.T) {
	original := []byte("<html><body>" + strings.Repeat("compression levels differ;", 300) + "</body></html>")
	previousETag := ""
	for _, level := range []int{gzip.BestSpeed, gzip.BestCompression} {
		compressed := gzipTestContent(t, original, level)
		server := staticTestServer(t, map[string][]byte{"index.html": original, "index.html.gz": compressed, "index.html.br": []byte("unchanged brotli bytes")})
		response, body := requestStaticProtocol(t, server, "GET", "/", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": previousETag})
		etag := fmt.Sprintf(`"sbpm-%s-gzip"`, calcContentFingerprint(compressed))
		if response.StatusCode != 200 || !bytes.Equal(body, compressed) || response.Header.Get("ETag") != etag || etag == previousETag {
			t.Fatal("changed compressed bytes did not invalidate strong ETag")
		}
		plain, _ := requestStaticProtocol(t, server, "GET", "/", nil)
		if plain.Header.Get("ETag") != fmt.Sprintf(`"sbpm-%s"`, calcContentFingerprint(original)) {
			t.Fatal("identity ETag compatibility changed")
		}
		previousETag = etag
		brotli, body := requestStaticProtocol(t, server, "GET", "/", map[string]string{"Accept-Encoding": "br"})
		if string(body) != "unchanged brotli bytes" || brotli.Header.Get("ETag") != fmt.Sprintf(`"sbpm-%s-br"`, calcContentFingerprint(body)) {
			t.Fatal("unchanged index did not reuse Brotli bytes with their own ETag")
		}
	}
}

func TestEmbeddedPrecompressedAssets(t *testing.T) {
	if !frontendassets.HasEmbeddedAssets {
		t.Skip("requires frontend_dist")
	}
	assetsFS, source, err := frontendAssetFS(filepath.Join(t.TempDir(), "missing"))
	if err != nil || source != "embedded:frontend/dist" {
		t.Fatalf("embedded filesystem unavailable: %v", err)
	}
	files, err := fs.Glob(assetsFS, "assets/*.js.br")
	if err != nil || len(files) == 0 {
		t.Fatal("missing embedded JavaScript variants")
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/"+strings.TrimSuffix(files[0], ".br"), nil)
	request.Header.Set("Accept-Encoding", "br, gzip")
	err = servePrecompressedStaticFile(recorder, request, assetsFS, strings.TrimPrefix(request.URL.Path, "/"))
	want, readErr := fs.ReadFile(assetsFS, files[0])
	if err != nil || readErr != nil || recorder.Code != 200 || recorder.Header().Get("Content-Encoding") != "br" || recorder.Header().Get("Content-Length") != strconv.Itoa(len(want)) || !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatal("embedded compressed bytes/headers differ")
	}
}
