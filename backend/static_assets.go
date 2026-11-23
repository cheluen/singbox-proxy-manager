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

func readRegularStaticFile(assetsFS fs.FS, fileName string) ([]byte, error) {
	file, _, err := openRegularStaticFile(assetsFS, fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func openRegularStaticFile(assetsFS fs.FS, fileName string) (fs.File, fs.FileInfo, error) {
	if !validStaticPath(fileName) {
		return nil, nil, fs.ErrNotExist
	}
	file, err := assetsFS.Open(path.Clean(fileName))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fs.ErrNotExist
	}
	return file, info, nil
}

func readStaticVariants(assetsFS fs.FS, fileName string) map[string][]byte {
	variants := make(map[string][]byte)
	for encoding, name := range staticVariantNames(assetsFS, fileName) {
		if content, err := readRegularStaticFile(assetsFS, name); err == nil && len(content) > 0 {
			variants[encoding] = content
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
	encoding := selectStaticRequestEncoding(request, variants["br"] != nil, variants["gzip"] != nil)
	if encoding != "" {
		return staticRepresentation{encoding: encoding, content: variants[encoding]}
	}
	return staticRepresentation{content: original}
}

func selectStaticRequestEncoding(request *http.Request, hasBrotli, hasGzip bool) string {
	if request.Method == http.MethodGet && request.Header.Get("Range") != "" {
		return ""
	}
	return selectStaticEncoding(strings.Join(request.Header.Values("Accept-Encoding"), ","), hasBrotli, hasGzip)
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

func serveStaticRepresentation(writer http.ResponseWriter, request *http.Request, fileName, contentType string, modTime time.Time, representation staticRepresentation) {
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
	original, info, err := openRegularStaticFile(assetsFS, fileName)
	if err != nil {
		return err
	}
	defer original.Close()
	variants := staticVariantNames(assetsFS, fileName)
	var representation staticRepresentation
	for {
		encoding := selectStaticRequestEncoding(request, variants["br"] != "", variants["gzip"] != "")
		if encoding == "" {
			representation.content, err = io.ReadAll(original)
			if err != nil {
				return err
			}
			break
		}
		content, readErr := readRegularStaticFile(assetsFS, variants[encoding])
		if readErr == nil && len(content) > 0 {
			representation = staticRepresentation{encoding: encoding, content: content}
			break
		}
		delete(variants, encoding)
	}
	contentType := mime.TypeByExtension(path.Ext(fileName))
	if contentType == "" {
		sample := representation.content
		if representation.encoding != "" {
			sample, err = io.ReadAll(io.LimitReader(original, 512))
			if err != nil {
				return err
			}
		}
		contentType = http.DetectContentType(sample)
	}
	if len(variants) > 0 {
		addVaryAcceptEncoding(writer.Header())
	}
	serveStaticRepresentation(writer, request, fileName, contentType, info.ModTime(), representation)
	return nil
}

func staticVariantNames(assetsFS fs.FS, fileName string) map[string]string {
	if strings.HasSuffix(fileName, ".br") || strings.HasSuffix(fileName, ".gz") {
		return nil
	}
	variants := make(map[string]string, 2)
	for encoding, suffix := range map[string]string{"br": ".br", "gzip": ".gz"} {
		name := path.Clean(fileName + suffix)
		info, err := fs.Stat(assetsFS, name)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			variants[encoding] = name
		}
	}
	return variants
}
