package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultUpdateRepo = "cheluen/singbox-proxy-manager"
	githubAPIVersion  = "2026-03-10"
)

type UpdateInfo struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	Available      bool   `json:"available"`
	ReleaseURL     string `json:"release_url,omitempty"`
	PublishedAt    string `json:"published_at,omitempty"`
	CheckedAt      string `json:"checked_at,omitempty"`
	Error          string `json:"error,omitempty"`
}

type UpdateChecker struct {
	owner      string
	repo       string
	client     *http.Client
	ttl        time.Duration
	timeout    time.Duration
	disabled   bool
	now        func() time.Time
	mu         sync.Mutex
	cached     UpdateInfo
	cachedAt   time.Time
	cachedFor  string
	lastErrMsg string
}

func NewUpdateCheckerFromEnv() *UpdateChecker {
	owner, repo := parseRepo(os.Getenv("SBPM_UPDATE_CHECK_REPO"))
	if owner == "" || repo == "" {
		owner, repo = parseRepo(defaultUpdateRepo)
	}
	return &UpdateChecker{
		owner:    owner,
		repo:     repo,
		client:   http.DefaultClient,
		ttl:      readDurationEnv("SBPM_UPDATE_CHECK_TTL", 6*time.Hour),
		timeout:  readDurationEnv("SBPM_UPDATE_CHECK_TIMEOUT", 5*time.Second),
		disabled: envBool("SBPM_UPDATE_CHECK_DISABLED"),
		now:      time.Now,
	}
}

func NewUpdateChecker(owner string, repo string, client *http.Client, ttl time.Duration) *UpdateChecker {
	if client == nil {
		client = http.DefaultClient
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	return &UpdateChecker{
		owner:   strings.TrimSpace(owner),
		repo:    strings.TrimSpace(repo),
		client:  client,
		ttl:     ttl,
		timeout: 5 * time.Second,
		now:     time.Now,
	}
}

func (c *UpdateChecker) Check(ctx context.Context, currentVersion string) (UpdateInfo, error) {
	currentVersion = normalizeVersion(currentVersion)
	info := UpdateInfo{CurrentVersion: currentVersion}
	if c == nil {
		return info, fmt.Errorf("update checker is nil")
	}
	if c.disabled {
		info.CheckedAt = c.now().UTC().Format(time.RFC3339)
		return info, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.cachedFor == currentVersion && !c.cachedAt.IsZero() && now.Sub(c.cachedAt) < c.ttl {
		return c.cached, nil
	}

	if c.owner == "" || c.repo == "" {
		info.CheckedAt = now.UTC().Format(time.RFC3339)
		info.Error = "update repository is not configured"
		c.cache(currentVersion, info, info.Error)
		return info, errors.New(info.Error)
	}

	checkCtx := ctx
	cancel := func() {}
	if c.timeout > 0 {
		checkCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()

	latest, err := c.fetchLatestRelease(checkCtx)
	info.CheckedAt = now.UTC().Format(time.RFC3339)
	if err != nil {
		info.Error = err.Error()
		c.cache(currentVersion, info, info.Error)
		return info, err
	}

	info.LatestVersion = normalizeVersion(latest.TagName)
	info.ReleaseURL = latest.HTMLURL
	info.PublishedAt = latest.PublishedAt
	info.Available = CompareVersions(info.LatestVersion, currentVersion) > 0
	c.cache(currentVersion, info, "")
	return info, nil
}

func (c *UpdateChecker) cache(currentVersion string, info UpdateInfo, errMsg string) {
	c.cachedFor = currentVersion
	c.cached = info
	c.cachedAt = c.now()
	c.lastErrMsg = errMsg
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

func (c *UpdateChecker) fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", urlPathEscape(c.owner), urlPathEscape(c.repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", "singbox-proxy-manager/"+Version())
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("github latest release returned HTTP %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return githubRelease{}, fmt.Errorf("github latest release response missing tag_name")
	}
	return release, nil
}

func CompareVersions(left string, right string) int {
	l := parseComparableVersion(left)
	r := parseComparableVersion(right)
	maxLen := len(l.numbers)
	if len(r.numbers) > maxLen {
		maxLen = len(r.numbers)
	}
	for i := 0; i < maxLen; i++ {
		var lv, rv int
		if i < len(l.numbers) {
			lv = l.numbers[i]
		}
		if i < len(r.numbers) {
			rv = r.numbers[i]
		}
		if lv > rv {
			return 1
		}
		if lv < rv {
			return -1
		}
	}
	if l.prerelease == "" && r.prerelease != "" {
		return 1
	}
	if l.prerelease != "" && r.prerelease == "" {
		return -1
	}
	return strings.Compare(l.prerelease, r.prerelease)
}

type comparableVersion struct {
	numbers    []int
	prerelease string
}

func parseComparableVersion(value string) comparableVersion {
	value = normalizeVersion(value)
	value = strings.SplitN(value, "+", 2)[0]
	mainPart := value
	pre := ""
	if idx := strings.Index(mainPart, "-"); idx >= 0 {
		pre = mainPart[idx+1:]
		mainPart = mainPart[:idx]
	}
	pieces := strings.Split(mainPart, ".")
	numbers := make([]int, 0, len(pieces))
	for _, piece := range pieces {
		if piece == "" {
			numbers = append(numbers, 0)
			continue
		}
		value, err := strconv.Atoi(piece)
		if err != nil {
			break
		}
		numbers = append(numbers, value)
	}
	return comparableVersion{numbers: numbers, prerelease: pre}
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	value = strings.TrimPrefix(value, "V")
	return value
}

func parseRepo(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "https://github.com/")
	raw = strings.TrimPrefix(raw, "http://github.com/")
	raw = strings.TrimSuffix(raw, ".git")
	raw = strings.Trim(raw, "/")
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

func readDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envBool(key string) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func urlPathEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
