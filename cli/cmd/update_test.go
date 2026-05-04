package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/release"
)

// updateFakeFetcher matches release.Fetcher for cmd-level tests.
type updateFakeFetcher struct {
	body []byte
	stat int
	err  error
}

func (f *updateFakeFetcher) Get(_ context.Context, _ string) ([]byte, int, error) {
	return f.body, f.stat, f.err
}

func TestRunUpdateUpToDate(t *testing.T) {
	f := &updateFakeFetcher{
		stat: http.StatusOK,
		body: []byte(`{"tag_name":"v0.1.0","html_url":"https://example/v0.1.0"}`),
	}
	var out bytes.Buffer
	if err := RunUpdate(context.Background(), &out, f, "", "v0.1.0"); err != nil {
		t.Fatalf("RunUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "on the latest") {
		t.Errorf("expected up-to-date message; got %q", out.String())
	}
}

func TestRunUpdateNewerAvailable(t *testing.T) {
	f := &updateFakeFetcher{
		stat: http.StatusOK,
		body: []byte(`{"tag_name":"v0.2.0","html_url":"https://example/v0.2.0"}`),
	}
	var out bytes.Buffer
	if err := RunUpdate(context.Background(), &out, f, "", "v0.1.0"); err != nil {
		t.Fatalf("RunUpdate: %v", err)
	}
	for _, want := range []string{"v0.2.0", "v0.1.0", "Release notes:", "Install with"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunUpdateNetworkErrorIsFriendly(t *testing.T) {
	f := &updateFakeFetcher{err: errors.New("dns: no such host")}
	var out bytes.Buffer
	// Should NOT propagate as a hard error — update is informational.
	if err := RunUpdate(context.Background(), &out, f, "", "v0.1.0"); err != nil {
		t.Errorf("RunUpdate should swallow network errors; got %v", err)
	}
	if !strings.Contains(out.String(), "Could not check") {
		t.Errorf("expected friendly error message: %q", out.String())
	}
	if !strings.Contains(out.String(), "github.com/latent-advisory/moorpost/releases") {
		t.Errorf("expected release-page hint: %q", out.String())
	}
}

func TestRunUpdateUsesCurrentVersionForComparison(t *testing.T) {
	f := &updateFakeFetcher{
		stat: http.StatusOK,
		body: []byte(`{"tag_name":"v0.2.0"}`),
	}
	// User is on dev — never current.
	var out bytes.Buffer
	if err := RunUpdate(context.Background(), &out, f, "", "dev"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "on the latest") {
		t.Errorf("'dev' should never be considered up-to-date: %q", out.String())
	}
}

// Compile-time assertion that updateFakeFetcher satisfies release.Fetcher.
var _ release.Fetcher = (*updateFakeFetcher)(nil)
