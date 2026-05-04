// Package release knows how to ask GitHub Releases for the latest moorpost
// version. Used by `moorpost update` to surface "you're behind" hints
// without actually installing anything.
//
// Auto-install is deliberately out of scope: signed binaries, permission
// elevation, and downgrade-attack surface make automated binary
// replacement riskier than the small UX win is worth in v1.0.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Release is the subset of GitHub's release JSON we care about.
type Release struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`

	// FetchedAt is set when we wrote this to the cache. Lets the cache
	// refresh after a TTL.
	FetchedAt time.Time `json:"fetched_at,omitempty"`
}

// Fetcher is the minimum HTTP surface release uses. Tests inject a fake.
type Fetcher interface {
	Get(ctx context.Context, url string) (body []byte, status int, err error)
}

// httpFetcher is a thin Fetcher backed by net/http with a 5-second timeout.
type httpFetcher struct{}

// NewHTTPFetcher returns a Fetcher backed by net/http.
func NewHTTPFetcher() Fetcher { return &httpFetcher{} }

func (h *httpFetcher) Get(ctx context.Context, url string) ([]byte, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "moorpost-cli")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// DefaultRepoURL is the GitHub releases-latest URL used in production.
const DefaultRepoURL = "https://api.github.com/repos/latent-advisory/moorpost/releases/latest"

// CacheTTL is how long a Release stays valid before a fresh fetch.
const CacheTTL = time.Hour

// Now is overridable for tests.
var Now = func() time.Time { return time.Now() }

// LatestRelease returns the latest release. It honors a 1-hour cache at
// `cachePath` (pass empty to disable caching).
func LatestRelease(ctx context.Context, f Fetcher, cachePath, repoURL string) (Release, error) {
	if f == nil {
		return Release{}, errors.New("release: Fetcher is nil")
	}
	if repoURL == "" {
		repoURL = DefaultRepoURL
	}

	if cachePath != "" {
		if r, ok := readCache(cachePath); ok {
			return r, nil
		}
	}

	body, status, err := f.Get(ctx, repoURL)
	if err != nil {
		return Release{}, fmt.Errorf("release: HTTP fetch: %w", err)
	}
	if status != http.StatusOK {
		return Release{}, fmt.Errorf("release: GitHub returned status %d", status)
	}
	var r Release
	if err := json.Unmarshal(body, &r); err != nil {
		return Release{}, fmt.Errorf("release: parse JSON: %w", err)
	}
	if r.TagName == "" {
		return Release{}, errors.New("release: empty tag_name in response")
	}
	r.FetchedAt = Now()

	if cachePath != "" {
		_ = writeCache(cachePath, r) // best-effort
	}
	return r, nil
}

// IsCurrent reports whether `current` matches `latest` by tag name.
// Both are normalized (whitespace trimmed first, then leading 'v' stripped,
// case-insensitive). Anything non-equal is considered "newer available",
// since we don't ship downgrade suggestions.
func IsCurrent(current string, latest Release) bool {
	c := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(current)), "v")
	l := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(latest.TagName)), "v")
	if c == "" || l == "" {
		return false
	}
	// `dev` is the default ldflags placeholder — never equal to any real tag.
	if c == "dev" {
		return false
	}
	return c == l
}

func readCache(path string) (Release, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Release{}, false
	}
	var r Release
	if err := json.Unmarshal(data, &r); err != nil {
		return Release{}, false
	}
	if r.FetchedAt.IsZero() || Now().Sub(r.FetchedAt) > CacheTTL {
		return Release{}, false
	}
	return r, true
}

func writeCache(path string, r Release) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
