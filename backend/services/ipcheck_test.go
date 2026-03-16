package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	info, err := checkWithService(client, srv.URL)
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
	info, err := checkWithService(client, srv.URL)
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
