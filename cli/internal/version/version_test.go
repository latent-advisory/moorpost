package version

import (
	"strings"
	"testing"
)

func TestInfoDevDefault(t *testing.T) {
	// Save originals and restore.
	origV, origC, origD := Version, Commit, Date
	defer func() { Version, Commit, Date = origV, origC, origD }()

	Version = "dev"
	Commit = "abc1234"
	Date = "2026-05-05T00:00:00Z"
	got := Info()
	if !strings.HasPrefix(got, "dev ") {
		t.Errorf("expected dev-prefixed output, got %q", got)
	}
	if !strings.Contains(got, "abc1234") {
		t.Errorf("expected commit in output: %q", got)
	}
}

func TestInfoTaggedRelease(t *testing.T) {
	origV, origC, origD := Version, Commit, Date
	defer func() { Version, Commit, Date = origV, origC, origD }()

	Version = "v0.1.0"
	Commit = "deadbee"
	Date = "2026-05-05T00:00:00Z"
	got := Info()
	if !strings.HasPrefix(got, "v0.1.0 ") {
		t.Errorf("expected version-prefixed output, got %q", got)
	}
	if strings.Contains(got, "dev") {
		t.Errorf("non-dev output should not contain 'dev': %q", got)
	}
}

func TestInfoUnknownDefaults(t *testing.T) {
	origV, origC, origD := Version, Commit, Date
	defer func() { Version, Commit, Date = origV, origC, origD }()

	Version = "dev"
	Commit = "unknown"
	Date = "unknown"
	got := Info()
	if !strings.Contains(got, "unknown") {
		t.Errorf("expected 'unknown' placeholders to surface: %q", got)
	}
}

func TestInfoIsSingleLine(t *testing.T) {
	got := Info()
	if strings.Contains(got, "\n") {
		t.Errorf("Info() must be a single line; got %q", got)
	}
}
