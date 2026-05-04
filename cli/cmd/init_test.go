package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/config"
)

func TestRunInitWritesValidConfig(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := RunInit(&out, InitOptions{
		Dir:        dir,
		Slug:       "argus",
		GCPProject: "latent-advisory",
		Region:     "us-central1",
	})
	if err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	target := filepath.Join(dir, ".moorpost", "config.yaml")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	cfg, err := config.Load(target)
	if err != nil {
		t.Fatalf("Load roundtrip: %v", err)
	}
	if cfg.ProjectSlug != "argus" {
		t.Errorf("project_slug = %q", cfg.ProjectSlug)
	}
	if cfg.Provider.Type != "gcp" {
		t.Errorf("provider.type = %q", cfg.Provider.Type)
	}
	if cfg.Agent.Type != "claude-code" {
		t.Errorf("agent.type = %q", cfg.Agent.Type)
	}
	gcp, _ := cfg.Provider.Raw["gcp"].(map[string]any)
	if gcp == nil {
		t.Fatal("provider.gcp subsection missing")
	}
	if gcp["project"] != "latent-advisory" {
		t.Errorf("provider.gcp.project = %v", gcp["project"])
	}
	if gcp["zone"] != "us-central1-a" {
		t.Errorf("zone defaulted incorrectly: %v", gcp["zone"])
	}
}

func TestRunInitRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{Dir: dir, Slug: "x", GCPProject: "p"}); err != nil {
		t.Fatal(err)
	}
	err := RunInit(&out, InitOptions{Dir: dir, Slug: "x", GCPProject: "p"})
	if err == nil {
		t.Error("RunInit overwrote existing config without --force")
	}
}

func TestRunInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{Dir: dir, Slug: "first", GCPProject: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := RunInit(&out, InitOptions{Dir: dir, Slug: "second", GCPProject: "p", Force: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(filepath.Join(dir, ".moorpost", "config.yaml"))
	if cfg.ProjectSlug != "second" {
		t.Errorf("force overwrite failed: slug = %q", cfg.ProjectSlug)
	}
}

func TestDeriveSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"argus", "argus"},
		{"My Project", "my-project"},
		{"AI M&A", "ai-m-a"},
		{"123abc", "p-123abc"},
		{"---weird", "weird"},
		{"", "moorpost-project"},
		{"UPPERCASE", "uppercase"},
		{"with_underscores", "with-underscores"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := deriveSlug(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunInitWithoutGCPProjectStillSucceeds(t *testing.T) {
	// User can edit the file later; init shouldn't block on this.
	// Stub the auto-detector so the test is deterministic regardless of the
	// host's gcloud config.
	withDetectGCPProject(t, func() string { return "" })
	dir := t.TempDir()
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{Dir: dir, Slug: "x"}); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	if !strings.Contains(out.String(), "edit provider.gcp.project") {
		t.Errorf("output should hint at editing project: %q", out.String())
	}
}

func TestRunInitDerivesSlugFromDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "my-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunInit(&out, InitOptions{Dir: dir, GCPProject: "p"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(filepath.Join(dir, ".moorpost", "config.yaml"))
	if cfg.ProjectSlug != "my-project" {
		t.Errorf("slug derivation failed: %q", cfg.ProjectSlug)
	}
}
