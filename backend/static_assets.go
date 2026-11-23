package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type staticRepresentation struct {
	encoding string
	content  []byte
}

var encodingQualityPattern = regexp.MustCompile(`^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$`)

func selectStaticEncoding(header string, hasBrotli, hasGzip bool) string {
	qualities := make(map[string]float64)
	for _, item := range strings.Split(strings.ToLower(header), ",") {
		encoding, parameter, hasParameter := strings.Cut(strings.TrimSpace(item), ";")
		encoding = strings.TrimSpace(encoding)
		if encoding == "x-gzip" {
			encoding = "gzip"
		}
		quality := 1.0
		if hasParameter {
			name, value, found := strings.Cut(parameter, "=")
			value = strings.TrimSpace(value)
			if !found || strings.TrimSpace(name) != "q" || !encodingQualityPattern.MatchString(value) {
				continue
			}
			quality, _ = strconv.ParseFloat(value, 64)
		}
		if previous, exists := qualities[encoding]; exists {
			quality = min(previous, quality)
		}
		qualities[encoding] = quality
	}
	selected, highest := "", 0.0
	for _, encoding := range []string{"br", "gzip"} {
		quality, explicit := qualities[encoding]
		if !explicit {
			quality = qualities["*"]
		}
		available := encoding == "br" && hasBrotli || encoding == "gzip" && hasGzip
		if available && quality > highest {
			selected, highest = encoding, quality
		}
	}
	identity, explicit := qualities["identity"]
	if !explicit {
		identity = qualities["*"]
	}
	if identity > highest {
		return ""
	}
	return selected
}

func addVaryAcceptEncoding(header http.Header) {
	for _, line := range header.Values("Vary") {
		for _, value := range strings.Split(line, ",") {
			if value = strings.TrimSpace(value); value == "*" || strings.EqualFold(value, "Accept-Encoding") {
				return
			}
		}
	}
	header.Add("Vary", "Accept-Encoding")
}

func validStaticPath(fileName string) bool {
	if strings.ContainsAny(fileName, "\x00\\") || strings.HasSuffix(fileName, "/") {
		return false
	}
	for _, segment := range strings.Split(fileName, "/") {
		if segment == ".." {
			return false
		}
	}
	return fileName != "" && fileName != "." && fs.ValidPath(path.Clean(fileName))
}

func readRegularStaticFile(assetsFS fs.FS, fileName string) ([]byte, time.Time, error) {
	if !validStaticPath(fileName) {
		return nil, time.Time{}, fs.ErrNotExist
	}
	file, err := assetsFS.Open(path.Clean(fileName))
	if err != nil {
		return nil, time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, time.Time{}, fs.ErrNotExist
	}
	content, err := io.ReadAll(file)
	return content, info.ModTime(), err
}

func readStaticVariants(assetsFS fs.FS, fileName string) map[string][]byte {
	variants := make(map[string][]byte)
	if !strings.HasSuffix(fileName, ".br") && !strings.HasSuffix(fileName, ".gz") {
		for encoding, suffix := range map[string]string{"br": ".br", "gzip": ".gz"} {
			if content, _, err := readRegularStaticFile(assetsFS, fileName+suffix); err == nil && len(content) > 0 {
				variants[encoding] = content
			}
		}
	}
	return variants
}

func prepareIndexVariants(assetsFS fs.FS, rawContent, finalContent []byte) (map[string][]byte, error) {
	if bytes.Equal(rawContent, finalContent) {
		return readStaticVariants(assetsFS, "index.html"), nil
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(finalContent); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	variants := make(map[string][]byte)
	if compressed.Len() < len(finalContent) {
		variants["gzip"] = compressed.Bytes()
	}
	return variants, nil
}

func chooseStaticRepresentation(writer http.ResponseWriter, request *http.Request, original []byte, variants map[string][]byte) staticRepresentation {
	if len(variants) > 0 {
		addVaryAcceptEncoding(writer.Header())
	}
	if request.Method == http.MethodGet && request.Header.Get("Range") != "" {
		return staticRepresentation{content: original}
	}
	encoding := selectStaticEncoding(strings.Join(request.Header.Values("Accept-Encoding"), ","), variants["br"] != nil, variants["gzip"] != nil)
	if encoding != "" {
		return staticRepresentation{encoding: encoding, content: variants[encoding]}
	}
	return staticRepresentation{content: original}
}

type encodedStaticResponseWriter struct {
	http.ResponseWriter
	encoding    string
	contentSize int
}

func (writer *encodedStaticResponseWriter) WriteHeader(status int) {
	if status == http.StatusOK {
		writer.Header().Set("Content-Encoding", writer.encoding)
		writer.Header().Set("Content-Length", strconv.Itoa(writer.contentSize))
	} else {
		writer.Header().Del("Content-Encoding")
		writer.Header().Del("Content-Length")
	}
	writer.ResponseWriter.WriteHeader(status)
}

func serveStaticRepresentation(writer http.ResponseWriter, request *http.Request, fileName string, original []byte, modTime time.Time, representation staticRepresentation) {
	contentType := mime.TypeByExtension(path.Ext(fileName))
	if contentType == "" {
		contentType = http.DetectContentType(original)
	}
	writer.Header().Set("Content-Type", contentType)
	if representation.encoding != "" {
		writer = &encodedStaticResponseWriter{ResponseWriter: writer, encoding: representation.encoding, contentSize: len(representation.content)}
	}
	if request.Method != http.MethodGet && request.Header.Get("Range") != "" {
		requestCopy := *request
		requestCopy.Header = request.Header.Clone()
		requestCopy.Header.Del("Range")
		request = &requestCopy
	}
	http.ServeContent(writer, request, fileName, modTime, bytes.NewReader(representation.content))
}

func servePrecompressedStaticFile(writer http.ResponseWriter, request *http.Request, assetsFS fs.FS, fileName string) error {
	original, modTime, err := readRegularStaticFile(assetsFS, fileName)
	if err == nil {
		representation := chooseStaticRepresentation(writer, request, original, readStaticVariants(assetsFS, fileName))
		serveStaticRepresentation(writer, request, fileName, original, modTime, representation)
	}
	return err
}
