package security

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGO20266859ConcurrentBodyLimitAndResponseHeaders(t *testing.T) {
	handlerErrors := make(chan error, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			_, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 8))
			var limitError *http.MaxBytesError
			if errors.As(err, &limitError) {
				status = http.StatusRequestEntityTooLarge
			} else if err != nil {
				handlerErrors <- fmt.Errorf("read request body: %w", err)
				status = http.StatusInternalServerError
			}
		}()

		// A real HTTP/1 writer exercises MaxBytesReader's connection-close path.
		for i := 0; i < 512; i++ {
			w.Header().Set("X-Body-Limit-Test", strconv.Itoa(i))
		}
		<-readDone
		w.WriteHeader(status)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second

	for i := 0; i < 32; i++ {
		body, expectedStatus := strings.Repeat("x", 512), http.StatusRequestEntityTooLarge
		if i == 31 {
			body, expectedStatus = "safe", http.StatusOK
		}
		response, err := client.Post(server.URL, "application/octet-stream", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, readError := io.Copy(io.Discard, response.Body)
		closeError := response.Body.Close()
		if readError != nil || closeError != nil {
			t.Fatalf("read response: %v; close response: %v", readError, closeError)
		}
		if response.StatusCode != expectedStatus || response.Header.Get("X-Body-Limit-Test") != "511" {
			t.Fatalf("request %d: status=%d header=%q", i, response.StatusCode, response.Header.Get("X-Body-Limit-Test"))
		}
	}

	select {
	case err := <-handlerErrors:
		t.Fatal(err)
	default:
	}
}
