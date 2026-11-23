package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
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
	owner     string
	repo      string
	client    *http.Client
	ttl       time.Duration
	timeout   time.Duration
	disabled  bool
	now       func() time.Time
	mu        sync.Mutex
	cached    UpdateInfo
	cachedAt  time.Time
	cachedErr error
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

func (c *UpdateChecker) Check(ctx context.Context) (UpdateInfo, error) {
	info := UpdateInfo{CurrentVersion: Version()}
	if c.disabled {
		info.CheckedAt = c.now().UTC().Format(time.RFC3339)
		return info, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	info.CheckedAt = now.UTC().Format(time.RFC3339)
	if err := ctx.Err(); err != nil {
		info.Error = err.Error()
		return info, err
	}
	if !c.cachedAt.IsZero() && now.Sub(c.cachedAt) < c.ttl {
		return c.cached, c.cachedErr
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	latest, err := c.fetchLatestRelease(checkCtx)
	if requestErr := ctx.Err(); requestErr != nil {
		info.Error = requestErr.Error()
		return info, requestErr
	}
	if err != nil {
		info.Error = err.Error()
	} else {
		info.LatestVersion = normalizeVersion(latest.TagName)
		info.ReleaseURL = latest.HTMLURL
		info.PublishedAt = latest.PublishedAt
		info.Available = CompareVersions(info.LatestVersion, info.CurrentVersion) > 0
	}
	c.cached = info
	c.cachedErr = err
	c.cachedAt = c.now()
	return info, err
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
	return semver.Compare(toSemver(left), toSemver(right))
}

func toSemver(value string) string {
	value = normalizeVersion(value)
	if value == "" {
		return ""
	}
	return "v" + value
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
