package services

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	defaultSingBoxLogMaxBytes = 10 << 20
	defaultSingBoxLogBackups  = 3
)

type noOpWriteCloser struct{}

func (noOpWriteCloser) Close() error { return nil }

func (s *SingBoxService) openProcessLog() (io.Writer, io.Writer, io.Closer, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SBPM_SINGBOX_LOG_OUTPUT")))
	if mode == "" {
		mode = "file"
	}

	switch mode {
	case "stdout":
		return os.Stdout, os.Stderr, noOpWriteCloser{}, nil
	case "file", "both":
		writer, err := newRotatingFileWriter(
			filepath.Join(s.configDir, "singbox.log"),
			readPositiveIntEnv("SBPM_SINGBOX_LOG_MAX_BYTES", defaultSingBoxLogMaxBytes),
			readBoundedIntEnv("SBPM_SINGBOX_LOG_BACKUPS", defaultSingBoxLogBackups, 1, 100),
		)
		if err != nil {
			return nil, nil, nil, err
		}
		if mode == "both" {
			return io.MultiWriter(os.Stdout, writer), io.MultiWriter(os.Stderr, writer), writer, nil
		}
		return writer, writer, writer, nil
	default:
		return nil, nil, nil, fmt.Errorf(
			"invalid SBPM_SINGBOX_LOG_OUTPUT %q (expected stdout, file, or both)",
			mode,
		)
	}
}

func readPositiveIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	value, err := strconv.Atoi(raw)
	if raw == "" || err != nil || value <= 0 {
		return fallback
	}
	return value
}

func readBoundedIntEnv(key string, fallback int, minimum int, maximum int) int {
	value := readPositiveIntEnv(key, fallback)
	if value < minimum || value > maximum {
		return fallback
	}
	return value
}

type rotatingFileWriter struct {
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
	mu         sync.Mutex
}

func newRotatingFileWriter(path string, maxBytes int, maxBackups int) (*rotatingFileWriter, error) {
	writer := &rotatingFileWriter{
		path:       path,
		maxBytes:   int64(maxBytes),
		maxBackups: maxBackups,
	}
	if err := writer.open(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *rotatingFileWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *rotatingFileWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	written := 0
	for len(content) > 0 {
		if w.size >= w.maxBytes {
			if err := w.rotate(); err != nil {
				return written, err
			}
		}
		remaining := w.maxBytes - w.size
		chunkSize := int64(len(content))
		if chunkSize > remaining {
			chunkSize = remaining
		}
		count, err := w.file.Write(content[:int(chunkSize)])
		written += count
		w.size += int64(count)
		content = content[count:]
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (w *rotatingFileWriter) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	oldest := fmt.Sprintf("%s.%d", w.path, w.maxBackups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for index := w.maxBackups - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", w.path, index)
		to := fmt.Sprintf("%s.%d", w.path, index+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
