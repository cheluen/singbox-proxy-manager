package version

import (
	"context"
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
		left  string
		right string
		want  int
	}{
		{left: "v1.4.0", right: "1.3.17", want: 1},
		{left: "1.3.17", right: "1.3.17", want: 0},
		{left: "1.3.9", right: "1.3.17", want: -1},
		{left: "1.4.0", right: "1.4.0-beta.1", want: 1},
		{left: "1.4.0-beta.1", right: "1.4.0", want: -1},
	}
	for _, tc := range cases {
		got := CompareVersions(tc.left, tc.right)
		if (got > 0 && tc.want <= 0) || (got == 0 && tc.want != 0) || (got < 0 && tc.want >= 0) {
			t.Fatalf("CompareVersions(%q,%q)=%d want sign %d", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestUpdateCheckerCheckUsesLatestRelease(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/repos/owner/repo/releases/latest" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if req.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("missing github accept header")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v1.4.0","html_url":"https://github.com/owner/repo/releases/v1.4.0","published_at":"2026-05-15T00:00:00Z"}`)),
			Header:     http.Header{},
		}, nil
	})}
	checker := NewUpdateChecker("owner", "repo", client, time.Hour)
	checker.now = func() time.Time { return time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC) }

	info, err := checker.Check(context.Background(), "1.3.17")
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !info.Available || info.LatestVersion != "1.4.0" || info.ReleaseURL == "" {
		t.Fatalf("unexpected update info: %+v", info)
	}
}
