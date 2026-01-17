package services

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

type IPInfo struct {
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	City        string `json:"city"`
	Region      string `json:"region"`
	Location    string `json:"location"`
	Latency     int    `json:"latency"`   // in milliseconds
	Transport   string `json:"transport"` // http or socks5
	HTTPError   string `json:"http_error,omitempty"`
}

const maxIPCheckResponseBytes = 1024 * 1024

func waitForTCPReady(addr string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	backoff := 50 * time.Millisecond

	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		if time.Now().After(deadline) {
			return err
		}

		time.Sleep(backoff)
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}

// CheckProxyIP checks the IP and location through a proxy
// proxyAddr should be in format "host:port" or "username:password@host:port"
func CheckProxyIP(proxyAddr string, username string, password string) (*IPInfo, error) {
	log.Printf("[IPCheck] Starting IP check for proxy: %s (auth: %v)", proxyAddr, username != "")

	// Measure latency start time
	startTime := time.Now()

	if err := waitForTCPReady(proxyAddr, 2*time.Second); err != nil {
		return nil, fmt.Errorf("proxy not ready: %w", err)
	}

	// Build proxy URL with authentication if provided
	proxyURLStr := "http://"
	if username != "" && password != "" {
		proxyURLStr += url.QueryEscape(username) + ":" + url.QueryEscape(password) + "@"
		log.Printf("[IPCheck] Using HTTP proxy with authentication (user: %s)", username)
	}
	proxyURLStr += proxyAddr

	// Create HTTP client with HTTP proxy
	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		log.Printf("[IPCheck] Failed to parse proxy URL: %v", err)
		return nil, fmt.Errorf("invalid proxy address: %v", err)
	}

	transport := &http.Transport{
		Proxy:              http.ProxyURL(proxyURL),
		DisableKeepAlives:  true,
		DisableCompression: false,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
	defer transport.CloseIdleConnections()

	// Use ip2location.io API (free tier, no API key needed for basic usage)
	services := []string{
		"https://api.ip2location.io/?format=json",
		"http://ip-api.com/json/",
	}

	var lastErr error
	var httpErr error
	for _, service := range services {
		log.Printf("[IPCheck] Trying service: %s", service)
		info, err := checkWithService(client, service)
		if err == nil && info.IP != "" {
			// Calculate latency in milliseconds
			info.Latency = int(time.Since(startTime).Milliseconds())
			info.Transport = "http"
			log.Printf("[IPCheck] Success! IP: %s, Location: %s, Latency: %dms", info.IP, info.Location, info.Latency)
			return info, nil
		}
		lastErr = err
		log.Printf("[IPCheck] Service %s failed: %v", service, err)
	}
	if lastErr != nil {
		httpErr = fmt.Errorf("all HTTP IP check services failed: %v", lastErr)
	}

	// Try with SOCKS5 if HTTP fails
	log.Printf("[IPCheck] HTTP proxy failed (%v), trying SOCKS5...", httpErr)
	result, err := checkWithSOCKS5(proxyAddr, username, password, services, startTime)
	if err == nil {
		result.Transport = "socks5"
		if httpErr != nil {
			result.HTTPError = httpErr.Error()
		}
		log.Printf("[IPCheck] SOCKS5 success! IP: %s, Location: %s (HTTP failed: %v)", result.IP, result.Location, httpErr)
		return result, nil
	}

	log.Printf("[IPCheck] All methods failed. Last error: %v", err)
	return nil, fmt.Errorf("all IP check methods failed - HTTP error: %v, SOCKS5 error: %v", httpErr, err)
}

func checkWithSOCKS5(proxyAddr string, username string, password string, services []string, startTime time.Time) (*IPInfo, error) {
	log.Printf("[IPCheck] Trying SOCKS5 dialer for: %s (auth: %v)", proxyAddr, username != "")

	// Create SOCKS5 auth if username/password provided
	var auth *proxy.Auth
	if username != "" && password != "" {
		auth = &proxy.Auth{
			User:     username,
			Password: password,
		}
		log.Printf("[IPCheck] Using SOCKS5 with authentication (user: %s)", username)
	}

	// Create SOCKS5 dialer
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, proxy.Direct)
	if err != nil {
		log.Printf("[IPCheck] Failed to create SOCKS5 dialer: %v", err)
		return nil, fmt.Errorf("failed to create SOCKS5 dialer: %v", err)
	}

	transport := &http.Transport{
		Dial:              dialer.Dial,
		DisableKeepAlives: true,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	var lastErr error
	for _, service := range services {
		log.Printf("[IPCheck] SOCKS5 trying service: %s", service)
		info, err := checkWithService(client, service)
		if err == nil && info.IP != "" {
			// Calculate latency in milliseconds
			info.Latency = int(time.Since(startTime).Milliseconds())
			transport.CloseIdleConnections()
			return info, nil
		}
		lastErr = err
		log.Printf("[IPCheck] SOCKS5 service %s failed: %v", service, err)
	}

	transport.CloseIdleConnections()
	return nil, fmt.Errorf("all SOCKS5 IP check services failed: %v", lastErr)
}

func checkWithService(client *http.Client, serviceURL string) (*IPInfo, error) {
	resp, err := client.Get(serviceURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIPCheckResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}
	if len(body) > maxIPCheckResponseBytes {
		return nil, fmt.Errorf("response too large")
	}

	// Parse response based on service
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %v", err)
	}

	info := &IPInfo{}

	// Extract IP
	// ip2location.io uses "ip", ip-api uses "query"
	if ip, ok := result["ip"].(string); ok {
		info.IP = ip
	} else if query, ok := result["query"].(string); ok {
		info.IP = query
	}

	// Extract country name
	// ip2location.io uses "country_name", ip-api uses "country"
	if countryName, ok := result["country_name"].(string); ok {
		info.Country = countryName
	} else if country, ok := result["country"].(string); ok {
		info.Country = country
	}

	// Extract country code
	// ip2location.io uses "country_code", ip-api uses "countryCode"
	if countryCode, ok := result["country_code"].(string); ok {
		info.CountryCode = countryCode
	} else if countryCode, ok := result["countryCode"].(string); ok {
		info.CountryCode = countryCode
	}

	// Extract city
	// Both APIs use "city_name" or "city"
	if cityName, ok := result["city_name"].(string); ok {
		info.City = cityName
	} else if city, ok := result["city"].(string); ok {
		info.City = city
	}

	// Extract region
	// ip2location.io uses "region_name", ip-api uses "regionName" or "region"
	if regionName, ok := result["region_name"].(string); ok {
		info.Region = regionName
	} else if regionName, ok := result["regionName"].(string); ok {
		info.Region = regionName
	} else if region, ok := result["region"].(string); ok {
		info.Region = region
	}

	// Build location string
	if info.City != "" && info.Country != "" {
		info.Location = fmt.Sprintf("%s, %s", info.City, info.Country)
	} else if info.Country != "" {
		info.Location = info.Country
	}

	// Ensure we have at least an IP
	if info.IP == "" {
		return nil, fmt.Errorf("no IP found in response")
	}

	log.Printf("[IPCheck] Parsed response - IP: %s, Country: %s (%s), City: %s, Region: %s",
		info.IP, info.Country, info.CountryCode, info.City, info.Region)

	return info, nil
}

// CheckDirectIP checks the IP without proxy
func CheckDirectIP() (*IPInfo, error) {
	log.Printf("[IPCheck] Checking direct IP (no proxy)")

	transport := &http.Transport{
		DisableKeepAlives: true,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	services := []string{
		"https://api.ip2location.io/?format=json",
		"http://ip-api.com/json/",
	}

	var lastErr error
	for _, service := range services {
		log.Printf("[IPCheck] Direct check trying service: %s", service)
		info, err := checkWithService(client, service)
		if err == nil && info.IP != "" {
			transport.CloseIdleConnections()
			log.Printf("[IPCheck] Direct check success! IP: %s", info.IP)
			return info, nil
		}
		lastErr = err
		log.Printf("[IPCheck] Direct check service %s failed: %v", service, err)
	}

	transport.CloseIdleConnections()
	log.Printf("[IPCheck] Direct check failed completely: %v", lastErr)
	return nil, fmt.Errorf("all IP check services failed: %v", lastErr)
}
