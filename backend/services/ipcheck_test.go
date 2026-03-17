package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckWithService_SendsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "sb-proxy-manager/1.0" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"1.2.3.4","country":"Testland","cc":"TL","city":"Test City","region":"Test Region"}`))
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{}
	info, err := checkWithService(context.Background(), client, srv.URL)
	if err != nil {
		t.Fatalf("checkWithService: %v", err)
	}
	if info.IP != "1.2.3.4" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
}

func TestCheckWithService_ParsesCountryCodeCC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"1.2.3.4","country":"Testland","cc":"TL"}`))
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{}
	info, err := checkWithService(context.Background(), client, srv.URL)
	if err != nil {
		t.Fatalf("checkWithService: %v", err)
	}
	if info.CountryCode != "TL" {
		t.Fatalf("unexpected country code: %q", info.CountryCode)
	}
}

func TestCheckDirectIP_UsesEnvServicesWithFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
		case "/ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"1.2.3.4","country":"Testland","cc":"TL"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("SBPM_IPCHECK_URLS", srv.URL+"/fail,"+srv.URL+"/ok")
	info, err := CheckDirectIP()
	if err != nil {
		t.Fatalf("CheckDirectIP: %v", err)
	}
	if info.IP != "1.2.3.4" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
}

func TestCheckWithServicesRacing_PrefersGeoWhenArrivesSoon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip-only":
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"1.1.1.1"}`))
		case "/geo":
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"2.2.2.2","country":"Testland","cc":"TL"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	startedAt := time.Now()
	info, err := checkWithServicesRacing(ctx, &http.Client{}, []string{srv.URL + "/ip-only", srv.URL + "/geo"})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("checkWithServicesRacing: %v", err)
	}
	if info.IP != "2.2.2.2" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
	if info.CountryCode != "TL" {
		t.Fatalf("unexpected country code: %q", info.CountryCode)
	}
	if info.Latency <= 0 {
		t.Fatalf("expected positive latency, got %d", info.Latency)
	}
	if elapsed > 900*time.Millisecond {
		t.Fatalf("expected fast return, took %v", elapsed)
	}
}

func TestCheckWithServicesRacing_ReturnsIPOnlyAfterGeoWaitWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip-only":
			time.Sleep(80 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"1.1.1.1"}`))
		case "/geo":
			select {
			case <-time.After(900 * time.Millisecond):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ip":"2.2.2.2","country":"Testland","cc":"TL"}`))
			case <-r.Context().Done():
				return
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	startedAt := time.Now()
	info, err := checkWithServicesRacing(ctx, &http.Client{}, []string{srv.URL + "/ip-only", srv.URL + "/geo"})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("checkWithServicesRacing: %v", err)
	}
	if info.IP != "1.1.1.1" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
	if info.Latency <= 0 {
		t.Fatalf("expected positive latency, got %d", info.Latency)
	}
	elapsedMs := elapsed.Milliseconds()
	if elapsed < 550*time.Millisecond {
		t.Fatalf("expected return around geo wait window, took %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected return within context deadline, took %v", elapsed)
	}
	if elapsedMs-int64(info.Latency) < 300 {
		t.Fatalf("expected latency to reflect winning request only, got latency=%dms elapsed=%dms", info.Latency, elapsedMs)
	}
}

func TestCheckWithServicesRacing_NotBlockedByFirstSlowService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			select {
			case <-time.After(5 * time.Second):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ip":"9.9.9.9"}`))
			case <-r.Context().Done():
				return
			}
		case "/geo":
			time.Sleep(100 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"2.2.2.2","country":"Testland","cc":"TL"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	startedAt := time.Now()
	info, err := checkWithServicesRacing(ctx, &http.Client{}, []string{srv.URL + "/slow", srv.URL + "/geo"})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("checkWithServicesRacing: %v", err)
	}
	if info.IP != "2.2.2.2" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected fast return even with slow first service, took %v", elapsed)
	}
}
