package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/proxy"

	"sb-proxy/backend/models"
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

var ErrProxyNotReady = errors.New("proxy not ready")

type ipCheckDiagnosticError struct {
	cause   error
	message string
}

func (err ipCheckDiagnosticError) Error() string { return err.message }

func (err ipCheckDiagnosticError) Unwrap() error { return err.cause }

const maxIPCheckDiagnosticBytes = 2000

const ipCheckCredentialField = `(?:[\w-]*(?:password|passwd|token|secret|[_-]key)|privatekey|presharedkey|apikey|key|pass|pwd|user(?:name)?|uuid|auth|(?:proxy-)?authorization|cookie)`

var (
	ipCheckURL        = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*:(?://|\\/\\/)\S+`)
	ipCheckUserinfo   = regexp.MustCompile(`[^\s"'@,:]+:[^\s"'@]+@[^\s"']+`)
	ipCheckSecretKey  = regexp.MustCompile(`(?i)^` + ipCheckCredentialField + `$`)
	ipCheckAssignment = regexp.MustCompile(`(?i)(["']?` + ipCheckCredentialField + `["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:basic\s+|bearer\s+)?[^\s,;}]+)`)
	ipCheckAuthScheme = regexp.MustCompile(`(?i)\b(basic|bearer)\s+[^\s,;"']+`)
	ipCheckWord       = regexp.MustCompile(`[\pL\pN_]+`)
)

func NodeIPCheckDiagnostic(message string, node models.ProxyNode) string {
	if message == "" {
		return ""
	}
	credentials := appendIPCheckConfigCredentials([]string{node.Username, node.Password}, node.Config, node.UpstreamConfig)
	return boundIPCheckDiagnostic(redactIPCheckDiagnostic(message, credentials...))
}

func SafeDiagnostic(message string) string {
	return boundIPCheckDiagnostic(redactIPCheckDiagnostic(message))
}

func appendIPCheckConfigCredentials(credentials []string, configs ...string) []string {
	var collect func(any, bool)
	collect = func(value any, sensitive bool) {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				collect(item, sensitive || ipCheckSecretKey.MatchString(key))
			}
		case []any:
			for _, item := range typed {
				collect(item, sensitive)
			}
		case string:
			if sensitive && typed != "" {
				credentials = append(credentials, typed)
			}
		}
	}
	for _, config := range configs {
		var fields map[string]any
		if config != "" && json.Unmarshal([]byte(config), &fields) == nil {
			collect(fields, false)
		}
	}
	return credentials
}

func redactIPCheckError(err error, credentials []string) error {
	if err != nil {
		if message := redactIPCheckDiagnostic(err.Error(), credentials...); message != err.Error() {
			return ipCheckDiagnosticError{cause: err, message: message}
		}
	}
	return err
}

func redactIPCheckDiagnostic(message string, credentials ...string) string {
	message = strings.ToValidUTF8(strings.ReplaceAll(message, "\x00", ""), "?")
	message = ipCheckURL.ReplaceAllString(message, "[redacted]")
	message = ipCheckUserinfo.ReplaceAllString(message, "[redacted]")
	message = ipCheckAssignment.ReplaceAllString(message, "${1}[redacted]")
	message = ipCheckAuthScheme.ReplaceAllString(message, "${1} [redacted]")
	var short []string
	for _, credential := range credentials {
		if credential == "" {
			continue
		}
		if len(credential) < 4 && ipCheckWord.FindString(credential) == credential {
			short = append(short, credential)
		} else {
			message = strings.ReplaceAll(message, credential, "[redacted]")
		}
	}
	if len(short) > 0 {
		message = ipCheckWord.ReplaceAllStringFunc(message, func(word string) string {
			for _, credential := range short {
				if word == credential {
					return "[redacted]"
				}
			}
			return word
		})
	}
	return message
}

func boundIPCheckDiagnostic(message string) string {
	if len(message) > maxIPCheckDiagnosticBytes {
		end := maxIPCheckDiagnosticBytes
		for !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end]
	}
	return message
}

type IPCheckHTTPStatusError struct {
	Service    string
	StatusCode int
	Status     string
	RetryAfter string
}

func (e *IPCheckHTTPStatusError) Error() string {
	if e == nil {
		return "IP check service returned an HTTP error"
	}
	service := strings.TrimSpace(e.Service)
	if service == "" {
		service = "unknown service"
	}
	status := strings.TrimSpace(e.Status)
	if status == "" {
		status = http.StatusText(e.StatusCode)
	}
	if e.StatusCode == http.StatusTooManyRequests {
		message := fmt.Sprintf("IP check service %s rate limited the request (HTTP 429", service)
		if status != "" && !strings.Contains(status, "429") {
			message += " " + status
		}
		message += ")"
		if retry := validIPCheckRetryAfter(e.RetryAfter); retry != "" {
			message += "; retry after " + retry
		}
		return message
	}
	if status != "" {
		return fmt.Sprintf("IP check service %s returned HTTP %d %s", service, e.StatusCode, status)
	}
	return fmt.Sprintf("IP check service %s returned HTTP %d", service, e.StatusCode)
}

func IsIPCheckRateLimited(err error) bool {
	return ipCheckRateLimitError(err) != nil
}

func IPCheckRetryAfter(err error) string {
	if status := ipCheckRateLimitError(err); status != nil {
		return validIPCheckRetryAfter(status.RetryAfter)
	}
	return ""
}

func ipCheckRateLimitError(err error) *IPCheckHTTPStatusError {
	for err != nil {
		switch typed := err.(type) {
		case *IPCheckHTTPStatusError:
			if typed != nil && typed.StatusCode == http.StatusTooManyRequests {
				return typed
			}
		case interface{ Unwrap() []error }:
			for _, child := range typed.Unwrap() {
				if status := ipCheckRateLimitError(child); status != nil {
					return status
				}
			}
		}
		err = errors.Unwrap(err)
	}
	return nil
}

func validIPCheckRetryAfter(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	if strings.Trim(value, "0123456789") == "" {
		return value
	}
	if _, err := http.ParseTime(value); err == nil {
		return value
	}
	return ""
}

const maxIPCheckResponseBytes = 1024 * 1024

const (
	ipCheckServicesMaxConcurrency = 3
	ipCheckServiceTimeout         = 4 * time.Second
	ipCheckTotalDeadline          = 10 * time.Second
	ipCheckReadyTimeout           = 2 * time.Second
	ProxyIPCheckTimeout           = ipCheckReadyTimeout + 2*ipCheckTotalDeadline
	ipCheckPreferGeoWait          = 600 * time.Millisecond
	ipCheckGlobalMaxConcurrency   = 6
)

var ipCheckGlobalSlots = make(chan struct{}, ipCheckGlobalMaxConcurrency)

func ipCheckServiceURLs() []string {
	if fromEnv := parseIPCheckServiceURLsFromEnv(); len(fromEnv) > 0 {
		return fromEnv
	}

	return []string{
		"https://api.ip2location.io/?format=json",
		"https://api.ip.sb/geoip",
		"https://ipwho.is/",
		"https://ipapi.co/json/",
		"https://api.myip.com",
		"https://api64.ipify.org?format=json",
		"http://ip-api.com/json/",
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

func waitForTCPReady(ctx context.Context, addr string, maxWait time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()
	backoff := 50 * time.Millisecond
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	var lastErr error

	for {
		conn, err := dialer.DialContext(waitCtx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err

		timer := time.NewTimer(backoff)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return lastErr
		case <-timer.C:
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}

// CheckProxyIPContext 通过节点入站检测出口 IP；proxyAddr 使用 host:port 或 [IPv6]:port。
// 认证信息单独传入，无认证时两项都留空；ctx 支持取消，direct 节点也使用此通道。
func CheckProxyIPContext(
	ctx context.Context,
	proxyAddr string,
	username string,
	password string,
) (*IPInfo, error) {
	log.Printf("[IPCheck] Starting IP check for proxy: %s (auth: %v)", proxyAddr, username != "")

	if err := waitForTCPReady(ctx, proxyAddr, ipCheckReadyTimeout); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProxyNotReady, err)
	}

	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		log.Printf("[IPCheck] Failed to parse proxy URL: %v", err)
		return nil, fmt.Errorf("invalid proxy address: %v", err)
	}
	if username != "" && password != "" {
		proxyURL.User = url.UserPassword(username, password)
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

	info, err := runIPCheckAttempt(ctx, ipCheckTotalDeadline, func(attemptCtx context.Context) (*IPInfo, error) {
		return checkWithServicesRacing(attemptCtx, client, services)
	})
	if err == nil && info != nil && info.IP != "" {
		info.Transport = "http"
		log.Printf("[IPCheck] Success! IP: %s, Location: %s, Latency: %dms", info.IP, info.Location, info.Latency)
		return info, nil
	}
	httpErr := fmt.Errorf("all HTTP IP check services failed: %w", err)

	// Try with SOCKS5 if HTTP fails
	httpMessage := redactIPCheckDiagnostic(httpErr.Error(), username, password)
	log.Printf("[IPCheck] HTTP proxy failed (%s), trying SOCKS5...", boundIPCheckDiagnostic(httpMessage))
	result, socksErr := runIPCheckAttempt(ctx, ipCheckTotalDeadline, func(attemptCtx context.Context) (*IPInfo, error) {
		return checkWithSOCKS5(attemptCtx, proxyAddr, username, password, services)
	})
	if socksErr == nil {
		result.Transport = "socks5"
		result.HTTPError = httpMessage
		log.Printf("[IPCheck] SOCKS5 success! IP: %s, Location: %s", result.IP, result.Location)
		return result, nil
	}

	log.Printf("[IPCheck] All methods failed. Last error: %s", boundIPCheckDiagnostic(redactIPCheckDiagnostic(socksErr.Error(), username, password)))
	return nil, fmt.Errorf("all IP check methods failed: %w", errors.Join(httpErr, socksErr))
}

func runIPCheckAttempt(
	ctx context.Context,
	timeout time.Duration,
	attempt func(context.Context) (*IPInfo, error),
) (*IPInfo, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return attempt(attemptCtx)
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
	}

	// Create SOCKS5 dialer
	dialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, proxy.Direct)
	if err != nil {
		log.Printf("[IPCheck] Failed to create SOCKS5 dialer: %v", err)
		return nil, fmt.Errorf("failed to create SOCKS5 dialer: %v", err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS5 dialer does not support context cancellation")
	}

	transport := &http.Transport{
		DialContext: func(reqCtx context.Context, network string, addr string) (net.Conn, error) {
			return contextDialer.DialContext(reqCtx, network, addr)
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
	return checkWithServicesRacingLimited(ctx, client, services, ipCheckGlobalSlots)
}

func checkWithServicesRacingLimited(
	ctx context.Context,
	client *http.Client,
	services []string,
	globalSlots chan struct{},
) (*IPInfo, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("no ip check services configured")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan ipCheckServiceResult, len(services))

	startService := func(service string) {
		go func() {
			if globalSlots != nil {
				select {
				case globalSlots <- struct{}{}:
					defer func() { <-globalSlots }()
				case <-ctx.Done():
					return
				}
			}

			reqCtx, reqCancel := context.WithTimeout(ctx, ipCheckServiceTimeout)
			startedAt := time.Now()
			info, err := checkWithService(reqCtx, client, service)
			latency := time.Since(startedAt)
			reqCancel()

			res := ipCheckServiceResult{
				info:    info,
				err:     err,
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
	var rateLimitErr error
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
				if rateLimitErr != nil {
					return nil, rateLimitErr
				}
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
				if rateLimitErr != nil {
					return nil, fmt.Errorf("%v: %w", ctx.Err(), rateLimitErr)
				}
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
				if IsIPCheckRateLimited(res.err) {
					rateLimitErr = res.err
				}
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
		return nil, errors.New("invalid IP check service URL")
	}
	req.Header.Set("User-Agent", "sb-proxy-manager/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		service := serviceURL
		if parsed, parseErr := url.Parse(serviceURL); parseErr == nil && parsed.Hostname() != "" {
			service = parsed.Hostname()
		}
		return nil, &IPCheckHTTPStatusError{
			Service:    service,
			StatusCode: resp.StatusCode,
			Status:     http.StatusText(resp.StatusCode),
			RetryAfter: validIPCheckRetryAfter(resp.Header.Get("Retry-After")),
		}
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
