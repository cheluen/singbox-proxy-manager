package services

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultSingBoxLogMaxBytes = 10 << 20
	defaultSingBoxLogBackups  = 3
)

type noOpWriteCloser struct{}

func (noOpWriteCloser) Close() error { return nil }

func (s *SingBoxService) openProcessLog() (io.Writer, io.Writer, io.Closer, error) {
	stdout, stderr, closer, err := s.processLogOutput()
	if err != nil {
		return nil, nil, nil, err
	}
	s.features.mu.RLock()
	observer, wiring := s.features.observer, s.features.wiring
	s.features.mu.RUnlock()
	wrap := func(output io.Writer) *logLineWriter {
		var afterAuthFailure bool
		return &logLineWriter{emit: func(line string) error {
			line = strings.TrimRight(logANSI.ReplaceAllString(line, ""), "\r")
			if afterAuthFailure && !currentKernelLogHeader(line, time.Now()) {
				return nil
			}
			afterAuthFailure = strings.Contains(line, "authentication failed, ") || line == omittedLogLine
			_, err := fmt.Fprintln(output, observer.line("singbox", line, wiring))
			return err
		}}
	}
	out, errOut := wrap(stdout), wrap(stderr)
	return out, errOut, processLogCloser{out, errOut, closer}, nil
}

var logPasswordTail = regexp.MustCompile(`(?i)\b(password\s*[:=]\s*).*`)

const kernelTimestamp = "-0700 2006-01-02 15:04:05"

var kernelLogHeader = regexp.MustCompile(`^[+-]\d{4} \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} (?:TRACE|DEBUG|INFO|WARN|ERROR|FATAL|PANIC) `)

func currentKernelLogHeader(line string, now time.Time) bool {
	if !kernelLogHeader.MatchString(line) {
		return false
	}
	stamp, err := time.Parse(kernelTimestamp, line[:len(kernelTimestamp)])
	return err == nil && !stamp.Before(now.Add(-10*time.Second)) && !stamp.After(now.Add(10*time.Second))
}

func cleanLogLine(line string, wiring *runtimeWiring) string {
	if prefix, _, found := strings.Cut(line, "authentication failed, "); found {
		line = prefix + "authentication failed, [redacted]"
	} else {
		line = logPasswordTail.ReplaceAllString(line, "${1}[redacted]")
	}
	if wiring != nil && wiring.redactSecrets != nil {
		line = wiring.redactSecrets.Replace(line)
	}
	return redactIPCheckDiagnostic(line)
}

type processLogCloser struct {
	stdout, stderr *logLineWriter
	output         io.Closer
}

func (c processLogCloser) Close() error {
	return errors.Join(c.stdout.Close(), c.stderr.Close(), c.output.Close())
}

type logLineWriter struct {
	emit    func(string) error
	mu      sync.Mutex
	tail    []byte
	discard bool
}

const omittedLogLine = "Overlong log line omitted"

func (w *logLineWriter) finish() error {
	line := string(w.tail)
	if w.discard {
		line = omittedLogLine
	}
	w.tail, w.discard = w.tail[:0], false
	return w.emit(line)
}

func (w *logLineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := len(data)
	for len(data) > 0 {
		line, rest, end := bytes.Cut(data, []byte{'\n'})
		if !w.discard {
			if len(line)+len(w.tail) > 16384 {
				w.discard = true
			} else {
				w.tail = append(w.tail, line...)
			}
		}
		if end {
			if err := w.finish(); err != nil {
				return count - len(rest), err
			}
		}
		data = rest
	}
	return count, nil
}

func (w *logLineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.tail) == 0 && !w.discard {
		return nil
	}
	return w.finish()
}

func (s *SingBoxService) processLogOutput() (io.Writer, io.Writer, io.Closer, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SBPM_SINGBOX_LOG_OUTPUT")))
	if mode == "" {
		mode = "stdout"
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
