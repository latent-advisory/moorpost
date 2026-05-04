package release

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeFetcher is a scriptable Fetcher.
type fakeFetcher struct {
	calls []string
	body  []byte
	stat  int
	err   error
}

func (f *fakeFetcher) Get(_ context.Context, url string) ([]byte, int, error) {
	f.calls = append(f.calls, url)
	return f.body, f.stat, f.err
}

func TestLatestReleaseHappyPath(t *testing.T) {
	f := &fakeFetcher{
		stat: http.StatusOK,
		body: []byte(`{"tag_name":"v0.2.0","html_url":"https://github.com/latent-advisory/moorpost/releases/tag/v0.2.0","published_at":"2026-04-01T00:00:00Z"}`),
	}
	r, err := LatestRelease(context.Background(), f, "", "")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if r.TagName != "v0.2.0" {
		t.Errorf("TagName = %q", r.TagName)
	}
	if r.PublishedAt.IsZero() {
		t.Errorf("PublishedAt zero; expected parsed timestamp")
	}
	if len(f.calls) != 1 {
		t.Errorf("expected 1 fetch call, got %d", len(f.calls))
	}
}

func TestLatestReleaseNon200(t *testing.T) {
	f := &fakeFetcher{stat: http.StatusForbidden, body: []byte("rate limited")}
	_, err := LatestRelease(context.Background(), f, "", "")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want mentioning 403", err)
	}
}

func TestLatestReleaseFetchError(t *testing.T) {
	myErr := errors.New("dns: no such host")
	f := &fakeFetcher{err: myErr}
	_, err := LatestRelease(context.Background(), f, "", "")
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrap of %v", err, myErr)
	}
}

func TestLatestReleaseMissingTag(t *testing.T) {
	f := &fakeFetcher{stat: 200, body: []byte(`{"html_url":"x"}`)}
	_, err := LatestRelease(context.Background(), f, "", "")
	if err == nil || !strings.Contains(err.Error(), "empty tag_name") {
		t.Errorf("err = %v, want empty-tag error", err)
	}
}

func TestLatestReleaseInvalidJSON(t *testing.T) {
	f := &fakeFetcher{stat: 200, body: []byte("not json")}
	_, err := LatestRelease(context.Background(), f, "", "")
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %v, want parse error", err)
	}
}

func TestLatestReleaseUsesCache(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "release-cache.json")

	// Pre-populate cache with a recent FetchedAt.
	cached := Release{
		TagName:   "v0.1.0",
		FetchedAt: time.Now().Add(-30 * time.Minute), // within TTL
	}
	data, _ := jsonMarshal(cached)
	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := &fakeFetcher{} // would error if called (zero stat)
	r, err := LatestRelease(context.Background(), f, cachePath, "")
	if err != nil {
		t.Fatalf("LatestRelease (cache hit): %v", err)
	}
	if r.TagName != "v0.1.0" {
		t.Errorf("expected cached value; got %q", r.TagName)
	}
	if len(f.calls) != 0 {
		t.Errorf("cache hit shouldn't fetch; got %d calls", len(f.calls))
	}
}

func TestLatestReleaseCacheStale(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "release-cache.json")
	cached := Release{
		TagName:   "v0.0.1",
		FetchedAt: time.Now().Add(-2 * time.Hour), // beyond TTL
	}
	data, _ := jsonMarshal(cached)
	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := &fakeFetcher{
		stat: 200,
		body: []byte(`{"tag_name":"v0.2.0"}`),
	}
	r, err := LatestRelease(context.Background(), f, cachePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.TagName != "v0.2.0" {
		t.Errorf("expected fresh fetch; got %q", r.TagName)
	}
}

func TestLatestReleaseWritesCache(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "subdir", "release-cache.json")
	f := &fakeFetcher{stat: 200, body: []byte(`{"tag_name":"v0.3.0"}`)}
	if _, err := LatestRelease(context.Background(), f, cachePath, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("cache file should be created: %v", err)
	}
}

func TestIsCurrent(t *testing.T) {
	tests := []struct {
		current string
		tag     string
		want    bool
	}{
		{"v0.1.0", "v0.1.0", true},
		{"0.1.0", "v0.1.0", true}, // missing leading v
		{"V0.1.0", "v0.1.0", true}, // case-insensitive
		{" v0.1.0 ", "v0.1.0", true}, // surrounding whitespace
		{"v0.1.0", "v0.2.0", false},
		{"dev", "v0.1.0", false},  // dev never current
		{"", "v0.1.0", false},
		{"v0.1.0", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.current+"_vs_"+tc.tag, func(t *testing.T) {
			got := IsCurrent(tc.current, Release{TagName: tc.tag})
			if got != tc.want {
				t.Errorf("IsCurrent(%q, %q) = %v, want %v", tc.current, tc.tag, got, tc.want)
			}
		})
	}
}

func TestNilFetcherErrors(t *testing.T) {
	_, err := LatestRelease(context.Background(), nil, "", "")
	if err == nil {
		t.Error("LatestRelease accepted nil fetcher")
	}
}

// jsonMarshal is a thin wrapper for the cache-pre-populating helper.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
