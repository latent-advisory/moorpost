package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjectContextMissingConfigRequired(t *testing.T) {
	dir := t.TempDir()
	_, err := loadProjectContext(ContextOptions{
		CWD:           dir,
		RequireConfig: true,
		StatePath:     filepath.Join(dir, "state.json"),
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	})
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("err = %v, want ErrConfigNotFound", err)
	}
}

func TestLoadProjectContextMissingConfigOptional(t *testing.T) {
	dir := t.TempDir()
	c, err := loadProjectContext(ContextOptions{
		CWD:           dir,
		RequireConfig: false,
		StatePath:     filepath.Join(dir, "state.json"),
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("loadProjectContext (optional config): %v", err)
	}
	if c.Config != nil {
		t.Error("expected nil Config when none on disk")
	}
	if c.State == nil {
		t.Error("expected State to be initialized even without config")
	}
}

func TestLoadProjectContextWalksUp(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a config at root using our init helper.
	if err := RunInit(&bytes.Buffer{}, InitOptions{
		Dir: root, Slug: "x", GCPProject: "p",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := loadProjectContext(ContextOptions{
		CWD:           deep,
		RequireConfig: true,
		StatePath:     filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatalf("walk-up failed: %v", err)
	}
	if c.Config == nil || c.Config.ProjectSlug != "x" {
		t.Errorf("Config not loaded from parent: %+v", c.Config)
	}
}

func TestPickSubsection(t *testing.T) {
	raw := map[string]any{
		"gcp": map[string]any{"project": "x"},
	}
	got := pickSubsection(raw, "gcp")
	if got["project"] != "x" {
		t.Errorf("expected nested gcp.project, got %v", got)
	}
	// Missing key falls back to whole map.
	got2 := pickSubsection(raw, "hetzner")
	if _, ok := got2["gcp"]; !ok {
		t.Errorf("missing key fallback should return the whole map")
	}
}
