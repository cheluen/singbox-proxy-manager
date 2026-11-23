package version

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{name: "major and minor", left: "v1.4.0", right: "1.3.17", want: 1},
		{name: "equal", left: "1.3.17", right: "1.3.17", want: 0},
		{name: "numeric patch", left: "1.3.9", right: "1.3.17", want: -1},
		{name: "release after prerelease", left: "1.4.0", right: "1.4.0-beta.1", want: 1},
		{name: "prerelease before release", left: "1.4.0-beta.1", right: "1.4.0", want: -1},
		{name: "numeric prerelease", left: "1.4.0-beta.10", right: "1.4.0-beta.2", want: 1},
		{name: "numeric before nonnumeric identifier", left: "1.4.0-alpha.1", right: "1.4.0-alpha.beta", want: -1},
		{name: "shorter prerelease has lower precedence", left: "1.4.0-alpha", right: "1.4.0-alpha.1", want: -1},
		{name: "build metadata ignored", left: "1.4.0+build.10", right: "1.4.0+build.2", want: 0},
		{name: "short version canonicalized", left: "V1.4", right: "1.4.0", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareVersions(tc.left, tc.right)
			if (got > 0 && tc.want <= 0) || (got == 0 && tc.want != 0) || (got < 0 && tc.want >= 0) {
				t.Fatalf("CompareVersions(%q,%q)=%d want sign %d", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func TestCompareVersionsFollowsSemverPrereleasePrecedence(t *testing.T) {
	versions := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}

	for i := 1; i < len(versions); i++ {
		if got := CompareVersions(versions[i-1], versions[i]); got >= 0 {
			t.Fatalf("CompareVersions(%q,%q)=%d want < 0", versions[i-1], versions[i], got)
		}
	}
}

func newTestUpdateChecker(t *testing.T, transport roundTripFunc) *UpdateChecker {
	t.Helper()
	t.Setenv("SBPM_UPDATE_CHECK_REPO", "owner/repo")
	t.Setenv("SBPM_UPDATE_CHECK_DISABLED", "false")
	t.Setenv("SBPM_UPDATE_CHECK_TTL", "1h")
	t.Setenv("SBPM_UPDATE_CHECK_TIMEOUT", "5s")
	checker := NewUpdateCheckerFromEnv()
	checker.client = &http.Client{Transport: transport}
	return checker
}

func TestUpdateCheckerCheckUsesLatestRelease(t *testing.T) {
	checker := newTestUpdateChecker(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/repos/owner/repo/releases/latest" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if req.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("missing github accept header")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v99.0.0","html_url":"https://github.com/owner/repo/releases/v99.0.0","published_at":"2026-05-15T00:00:00Z"}`)),
			Header:     http.Header{},
		}, nil
	})
	checker.now = func() time.Time { return time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC) }

	info, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !info.Available || info.CurrentVersion != Version() || info.LatestVersion != "99.0.0" || info.ReleaseURL == "" || info.CheckedAt == "" {
		t.Fatalf("unexpected update info: %+v", info)
	}
}

func TestUpdateCheckerFailureSharesTimestampAndErrorWithCache(t *testing.T) {
	now := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	requests := 0
	checker := newTestUpdateChecker(t, func(_ *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody}, nil
	})
	checker.now = func() time.Time { return now }
	ctx := context.Background()
	first, err := checker.Check(ctx)
	if err == nil || first.Error != err.Error() || first.CheckedAt != now.Format(time.RFC3339) || first.Available || first.CurrentVersion != Version() {
		t.Fatalf("unexpected first failure: %+v, %v", first, err)
	}
	now = now.Add(time.Minute)
	cached, cacheErr := checker.Check(ctx)
	if cached != first || cacheErr != err || requests != 1 {
		t.Fatalf("cached failure changed: %+v, %v, requests=%d", cached, cacheErr, requests)
	}
	now = now.Add(time.Hour)
	expired, err := checker.Check(ctx)
	if err == nil || expired.CheckedAt != now.Format(time.RFC3339) || requests != 2 {
		t.Fatalf("expired failure was not refreshed: %+v, %v, requests=%d", expired, err, requests)
	}
}

func TestUpdateCheckerCancellationDoesNotCacheFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	checker := newTestUpdateChecker(t, func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			cancel()
			return nil, req.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v99.0.0"}`))}, nil
	})
	failed, err := checker.Check(ctx)
	if !errors.Is(err, context.Canceled) || failed.CheckedAt == "" || failed.Error != context.Canceled.Error() {
		t.Fatalf("unexpected cancellation: %+v, %v", failed, err)
	}
	if _, err := checker.Check(ctx); !errors.Is(err, context.Canceled) || requests != 1 {
		t.Fatalf("already canceled request reached HTTP: %v, requests=%d", err, requests)
	}
	info, err := checker.Check(context.Background())
	if err != nil || !info.Available || info.Error != "" || requests != 2 {
		t.Fatalf("cancellation polluted update cache: %+v, %v, requests=%d", info, err, requests)
	}
}
