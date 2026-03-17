package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
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

const (
	ipCheckServicesMaxConcurrency = 3
	ipCheckServiceTimeout         = 4 * time.Second
	ipCheckTotalDeadline          = 10 * time.Second
	ipCheckPreferGeoWait          = 600 * time.Millisecond
)

func ipCheckServiceURLs() []string {
	if fromEnv := parseIPCheckServiceURLsFromEnv(); len(fromEnv) > 0 {
		return fromEnv
	}

	return []string{
		"https://api.ip2location.io/?format=json",
		"http://ip-api.com/json/",
		"https://api.ip.sb/geoip",
		"https://ipwho.is/",
		"https://ipapi.co/json/",
		"https://api.myip.com",
		"https://api64.ipify.org?format=json",
	}
}

func parseIPCheckServiceURLsFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("SBPM_IPCHECK_URLS"))
	if raw == "" {
		return nil
	}

	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})

	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, "http://") && !strings.HasPrefix(part, "https://") {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}

	return out
}

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

	if err := waitForTCPReady(proxyAddr, 2*time.Second); err != nil {
		return nil, fmt.Errorf("proxy not ready: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), ipCheckTotalDeadline)
	defer cancel()

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
	}
	defer transport.CloseIdleConnections()

	services := ipCheckServiceURLs()

	info, err := checkWithServicesRacing(ctx, client, services)
	if err == nil && info != nil && info.IP != "" {
		info.Transport = "http"
		log.Printf("[IPCheck] Success! IP: %s, Location: %s, Latency: %dms", info.IP, info.Location, info.Latency)
		return info, nil
	}
	httpErr := fmt.Errorf("all HTTP IP check services failed: %w", err)

	// Try with SOCKS5 if HTTP fails
	log.Printf("[IPCheck] HTTP proxy failed (%v), trying SOCKS5...", httpErr)
	result, socksErr := checkWithSOCKS5(ctx, proxyAddr, username, password, services)
	if socksErr == nil {
		result.Transport = "socks5"
		result.HTTPError = httpErr.Error()
		log.Printf("[IPCheck] SOCKS5 success! IP: %s, Location: %s (HTTP failed: %v)", result.IP, result.Location, httpErr)
		return result, nil
	}

	log.Printf("[IPCheck] All methods failed. Last error: %v", socksErr)
	return nil, fmt.Errorf("all IP check methods failed - HTTP error: %v, SOCKS5 error: %v", httpErr, socksErr)
}

func checkWithSOCKS5(ctx context.Context, proxyAddr string, username string, password string, services []string) (*IPInfo, error) {
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
		DialContext: func(reqCtx context.Context, network string, addr string) (net.Conn, error) {
			type dialResult struct {
				conn net.Conn
				err  error
			}

			done := make(chan dialResult, 1)
			go func() {
				conn, err := dialer.Dial(network, addr)
				done <- dialResult{conn: conn, err: err}
			}()

			select {
			case <-reqCtx.Done():
				return nil, reqCtx.Err()
			case res := <-done:
				if reqCtx.Err() != nil {
					if res.conn != nil {
						_ = res.conn.Close()
					}
					return nil, reqCtx.Err()
				}
				return res.conn, res.err
			}
		},
		DisableKeepAlives:  true,
		DisableCompression: false,
	}

	client := &http.Client{
		Transport: transport,
	}

	info, err := checkWithServicesRacing(ctx, client, services)
	transport.CloseIdleConnections()
	if err != nil {
		return nil, fmt.Errorf("all SOCKS5 IP check services failed: %w", err)
	}
	return info, nil
}

type ipCheckServiceResult struct {
	info      *IPInfo
	err       error
	service   string
	latency   time.Duration
	hasGeo    bool
	hasIPOnly bool
}

func hasPreferredGeoInfo(info *IPInfo) bool {
	if info == nil {
		return false
	}
	if strings.TrimSpace(info.CountryCode) != "" {
		return true
	}
	if strings.TrimSpace(info.Location) != "" {
		return true
	}
	return false
}

func checkWithServicesRacing(ctx context.Context, client *http.Client, services []string) (*IPInfo, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("no ip check services configured")
	}

	ctx, cancel := context.WithTimeout(ctx, ipCheckTotalDeadline)
	defer cancel()

	results := make(chan ipCheckServiceResult, len(services))
	semaphore := make(chan struct{}, ipCheckServicesMaxConcurrency)

	startService := func(service string) {
		go func() {
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()

			reqCtx, reqCancel := context.WithTimeout(ctx, ipCheckServiceTimeout)
			startedAt := time.Now()
			info, err := checkWithService(reqCtx, client, service)
			latency := time.Since(startedAt)
			reqCancel()

			res := ipCheckServiceResult{
				info:    info,
				err:     err,
				service: service,
				latency: latency,
			}
			if err == nil && info != nil && info.IP != "" {
				res.hasGeo = hasPreferredGeoInfo(info)
				res.hasIPOnly = !res.hasGeo
			}

			select {
			case results <- res:
			case <-ctx.Done():
			}
		}()
	}

	started := 0
	for started < len(services) && started < ipCheckServicesMaxConcurrency {
		startService(services[started])
		started++
	}

	completed := 0
	var lastErr error
	var ipOnlyCandidate *ipCheckServiceResult
	var ipOnlyTimer *time.Timer
	var ipOnlyTimerC <-chan time.Time

	stopTimer := func() {
		if ipOnlyTimer == nil {
			return
		}
		if !ipOnlyTimer.Stop() {
			select {
			case <-ipOnlyTimer.C:
			default:
			}
		}
		ipOnlyTimer = nil
		ipOnlyTimerC = nil
	}
	defer stopTimer()

	for {
		if completed >= started && started >= len(services) {
			if ipOnlyCandidate != nil {
				stopTimer()
				ipOnlyCandidate.info.Latency = int(ipOnlyCandidate.latency.Milliseconds())
				return ipOnlyCandidate.info, nil
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("all ip check services failed")
		}

		select {
		case <-ctx.Done():
			if ipOnlyCandidate != nil {
				stopTimer()
				ipOnlyCandidate.info.Latency = int(ipOnlyCandidate.latency.Milliseconds())
				return ipOnlyCandidate.info, nil
			}
			if lastErr != nil {
				return nil, fmt.Errorf("%w: %v", ctx.Err(), lastErr)
			}
			return nil, ctx.Err()
		case <-ipOnlyTimerC:
			if ipOnlyCandidate != nil {
				stopTimer()
				ipOnlyCandidate.info.Latency = int(ipOnlyCandidate.latency.Milliseconds())
				return ipOnlyCandidate.info, nil
			}
		case res := <-results:
			completed++

			if res.err != nil {
				lastErr = res.err
			}

			if res.err == nil && res.info != nil && res.info.IP != "" {
				if res.hasGeo {
					stopTimer()
					res.info.Latency = int(res.latency.Milliseconds())
					return res.info, nil
				}

				if res.hasIPOnly && ipOnlyCandidate == nil {
					candidate := res
					ipOnlyCandidate = &candidate
					ipOnlyTimer = time.NewTimer(ipCheckPreferGeoWait)
					ipOnlyTimerC = ipOnlyTimer.C
				}
			}

			if started < len(services) {
				startService(services[started])
				started++
			}
		}
	}
}

func checkWithService(ctx context.Context, client *http.Client, serviceURL string) (*IPInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serviceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "sb-proxy-manager/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
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
	} else if countryCode, ok := result["cc"].(string); ok {
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

	ctx, cancel := context.WithTimeout(context.Background(), ipCheckTotalDeadline)
	defer cancel()

	transport := &http.Transport{
		DisableKeepAlives: true,
	}

	client := &http.Client{
		Transport: transport,
	}

	services := ipCheckServiceURLs()

	info, err := checkWithServicesRacing(ctx, client, services)
	transport.CloseIdleConnections()
	if err != nil {
		log.Printf("[IPCheck] Direct check failed completely: %v", err)
		return nil, fmt.Errorf("all IP check services failed: %w", err)
	}
	log.Printf("[IPCheck] Direct check success! IP: %s", info.IP)
	return info, nil
}
