package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// withFixGCPRunner swaps the package-level fix runner for the test's
// duration. Restores on cleanup.
func withFixGCPRunner(t *testing.T, fn func(ctx context.Context, w io.Writer, name string, args ...string) error) {
	t.Helper()
	orig := fixGCPRunner
	fixGCPRunner = fn
	t.Cleanup(func() { fixGCPRunner = orig })
}

func makeFixContext(t *testing.T, preflightErr error) *Context {
	t.Helper()
	cfg := config.Default()
	cfg.ProjectSlug = "argus"
	st := state.New()
	return &Context{
		Config:   cfg,
		State:    st,
		Provider: &fakeProvider{preflightErr: preflightErr},
	}
}

func TestTryFixComputeAPIMatchesAndRuns(t *testing.T) {
	preflightErr := errors.New(`gcp preflight failed:
  - Compute Engine API not enabled on project "latent-advisory"
    fix: gcloud services enable compute.googleapis.com --project=latent-advisory`)
	c := makeFixContext(t, preflightErr)

	var ranArgs []string
	withFixGCPRunner(t, func(_ context.Context, _ io.Writer, name string, args ...string) error {
		ranArgs = append([]string{name}, args...)
		return nil
	})

	var out bytes.Buffer
	applied, err := tryFixComputeAPI(context.Background(), &out, c)
	if err != nil {
		t.Fatalf("tryFixComputeAPI: %v", err)
	}
	if !applied {
		t.Error("expected applied=true when error matches API-disabled pattern")
	}
	want := []string{"gcloud", "services", "enable", "compute.googleapis.com", "--project=latent-advisory"}
	if len(ranArgs) != len(want) {
		t.Fatalf("ran args = %v, want %v", ranArgs, want)
	}
	for i := range want {
		if ranArgs[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, ranArgs[i], want[i])
		}
	}
}

func TestTryFixComputeAPISkipsUnrelatedError(t *testing.T) {
	// Error is about gcloud auth, not API — fix should NOT trigger.
	preflightErr := errors.New("gcp preflight failed:\n  - no active gcloud account; run: gcloud auth login")
	c := makeFixContext(t, preflightErr)

	called := false
	withFixGCPRunner(t, func(context.Context, io.Writer, string, ...string) error {
		called = true
		return nil
	})

	var out bytes.Buffer
	applied, err := tryFixComputeAPI(context.Background(), &out, c)
	if err != nil {
		t.Fatalf("tryFixComputeAPI: %v", err)
	}
	if applied {
		t.Error("applied=true on unrelated error; should be false")
	}
	if called {
		t.Error("fix runner was called for an unrelated error")
	}
}

func TestTryFixComputeAPINoErrorIsNoOp(t *testing.T) {
	c := makeFixContext(t, nil)
	var out bytes.Buffer
	applied, err := tryFixComputeAPI(context.Background(), &out, c)
	if err != nil {
		t.Fatalf("tryFixComputeAPI: %v", err)
	}
	if applied {
		t.Error("applied=true when there's no error")
	}
}

func TestTryFixComputeAPIRunnerErrorPropagates(t *testing.T) {
	preflightErr := errors.New(`Compute Engine API not enabled on project "p"`)
	c := makeFixContext(t, preflightErr)

	myErr := errors.New("permission denied")
	withFixGCPRunner(t, func(context.Context, io.Writer, string, ...string) error {
		return myErr
	})

	var out bytes.Buffer
	applied, err := tryFixComputeAPI(context.Background(), &out, c)
	if !applied {
		t.Error("applied should be true even when fix command fails")
	}
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrap of %v", err, myErr)
	}
}

func TestTryFixComputeAPIExtractsCorrectProject(t *testing.T) {
	// Project name has dashes — must round-trip through the regex.
	preflightErr := errors.New(`Compute Engine API not enabled on project "my-org-staging-2026"`)
	c := makeFixContext(t, preflightErr)

	var ranArgs []string
	withFixGCPRunner(t, func(_ context.Context, _ io.Writer, name string, args ...string) error {
		ranArgs = append([]string{name}, args...)
		return nil
	})

	var out bytes.Buffer
	if _, err := tryFixComputeAPI(context.Background(), &out, c); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range ranArgs {
		if a == "--project=my-org-staging-2026" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("project not extracted correctly: %v", ranArgs)
	}
}

func TestComputeAPIDisabledRegexBoundary(t *testing.T) {
	tests := []struct {
		input string
		match string
	}{
		{`Compute Engine API not enabled on project "p"`, "p"},
		{`...preceding noise... Compute Engine API not enabled on project "abc-123" trailing`, "abc-123"},
		{`unrelated error`, ""},
		{`Compute Engine API not enabled (no project quoted)`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			m := computeAPIDisabledRE.FindStringSubmatch(tc.input)
			got := ""
			if m != nil {
				got = m[1]
			}
			if got != tc.match {
				t.Errorf("regex match for %q = %q, want %q", tc.input, got, tc.match)
			}
			_ = strings.Contains(tc.input, "x") // keep strings imported
		})
	}
}
