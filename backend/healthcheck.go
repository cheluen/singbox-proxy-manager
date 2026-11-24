package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

func isHealthcheck(args []string) bool {
	return len(args) == 1 && (args[0] == "--healthcheck" || args[0] == "-healthcheck")
}

func healthcheckURLs(host, port string) []string {
	var hosts []string
	switch host {
	case "", "0.0.0.0", "*":
		hosts = []string{"127.0.0.1"}
	case "::":
		hosts = []string{"127.0.0.1", "::1"}
	default:
		hosts = []string{host}
	}
	urls := make([]string, 0, len(hosts))
	for _, candidate := range hosts {
		endpoint := url.URL{Scheme: "http", Host: net.JoinHostPort(candidate, port), Path: "/api/health"}
		urls = append(urls, endpoint.String())
	}
	return urls
}

func healthcheckURL(host, port string) string {
	return healthcheckURLs(host, port)[0]
}

func probeHealth(host, port string) error {
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	var lastErr error
	for _, endpoint := range healthcheckURLs(host, port) {
		response, err := client.Get(endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			return nil
		}
		lastErr = fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	return lastErr
}
