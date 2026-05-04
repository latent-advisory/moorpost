package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/config"
)

// withDetectGCPProject swaps the package-level detector for the test's
// duration. Restores on cleanup.
func withDetectGCPProject(t *testing.T, fn func() string) {
	t.Helper()
	orig := detectGCPProject
	detectGCPProject = fn
	t.Cleanup(func() { detectGCPProject = orig })
}

func TestInitAutoDetectsGCPProject(t *testing.T) {
	withDetectGCPProject(t, func() string { return "auto-detected-proj" })
	dir := t.TempDir()
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{Dir: dir, Slug: "x"}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	if !strings.Contains(out.String(), "Auto-detected") {
		t.Errorf("output should mention auto-detection: %q", out.String())
	}
	cfg, err := config.Load(filepath.Join(dir, ".moorpost", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	gcp, _ := cfg.Provider.Raw["gcp"].(map[string]any)
	if gcp == nil || gcp["project"] != "auto-detected-proj" {
		t.Errorf("auto-detected project not in config: %+v", gcp)
	}
}

func TestInitFlagOverridesAutoDetect(t *testing.T) {
	withDetectGCPProject(t, func() string { return "from-gcloud" })
	dir := t.TempDir()
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{
		Dir:        dir,
		Slug:       "x",
		GCPProject: "explicit-flag-value",
	}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	cfg, _ := config.Load(filepath.Join(dir, ".moorpost", "config.yaml"))
	gcp, _ := cfg.Provider.Raw["gcp"].(map[string]any)
	if gcp["project"] != "explicit-flag-value" {
		t.Errorf("flag should override gcloud auto-detect; got %v", gcp["project"])
	}
}

func TestInitAutoDetectEmptyFallsThrough(t *testing.T) {
	// gcloud config has no project — init should still succeed and print
	// the edit-it-later hint (existing behavior).
	withDetectGCPProject(t, func() string { return "" })
	dir := t.TempDir()
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{Dir: dir, Slug: "x"}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	if strings.Contains(out.String(), "Auto-detected") {
		t.Errorf("should NOT auto-detect when gcloud returns empty: %q", out.String())
	}
	if !strings.Contains(out.String(), "edit provider.gcp.project") {
		t.Errorf("expected 'edit it later' hint when no project: %q", out.String())
	}
}

func TestInitAutoDetectUnsetSentinel(t *testing.T) {
	// gcloud prints "(unset)" for an unset project; should be treated as empty.
	withDetectGCPProject(t, func() string { return "" }) // detectGCPProject already strips (unset)
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{Dir: dir, Slug: "x"}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
}
