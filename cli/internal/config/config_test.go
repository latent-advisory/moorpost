package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureV1 = `schema_version: 1
project_slug: argus
provider:
  type: gcp
  gcp:
    project: latent-advisory
    region: us-central1
    zone: us-central1-a
    machine_type: e2-standard-2
    disk_size_gb: 100
    disk_type: pd-standard
    static_ip: true
    network_tags:
      - moorpost
agent:
  type: claude-code
  claude-code:
    auth_method: oauth-subscription
sync:
  engine: mutagen
  conflict_policy: alpha-wins
  ignore:
    - "**/node_modules"
    - "**/.venv"
mode: local-first
handoff:
  pause_timeout_seconds: 30
  prompts:
    on_lid_close: true
    on_idle_minutes: 30
    on_battery_below: 20
    on_vscode_quit: true
cost:
  monthly_cap_usd: 50
  alert_thresholds:
    - 10
    - 25
`

func writeTemp(t *testing.T, name, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func TestLoadValidFixture(t *testing.T) {
	p := writeTemp(t, "config.yaml", fixtureV1)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProjectSlug != "argus" {
		t.Errorf("project_slug = %q, want argus", c.ProjectSlug)
	}
	if c.Provider.Type != "gcp" {
		t.Errorf("provider.type = %q, want gcp", c.Provider.Type)
	}
	if c.Agent.Type != "claude-code" {
		t.Errorf("agent.type = %q, want claude-code", c.Agent.Type)
	}
	if c.Mode != ModeLocalFirst {
		t.Errorf("mode = %q, want local-first", c.Mode)
	}
	if c.Handoff.Prompts.OnIdleMinutes != 30 {
		t.Errorf("handoff.prompts.on_idle_minutes = %d, want 30", c.Handoff.Prompts.OnIdleMinutes)
	}
	gcpRaw, ok := c.Provider.Raw["gcp"]
	if !ok {
		t.Fatalf("provider.gcp subsection missing from Raw; got keys %v", keys(c.Provider.Raw))
	}
	gcpMap, ok := gcpRaw.(map[string]any)
	if !ok {
		t.Fatalf("provider.gcp not a map[string]any: %T", gcpRaw)
	}
	if gcpMap["project"] != "latent-advisory" {
		t.Errorf("provider.gcp.project = %v, want latent-advisory", gcpMap["project"])
	}
}

func TestRoundTrip(t *testing.T) {
	p := writeTemp(t, "config.yaml", fixtureV1)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.yaml")
	if err := c.Save(out); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c2, err := Load(out)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Compare key invariants rather than full struct equality (yaml may
	// re-order maps, normalize numbers, etc.).
	if !reflect.DeepEqual(c.ProjectSlug, c2.ProjectSlug) {
		t.Errorf("project_slug: %q -> %q", c.ProjectSlug, c2.ProjectSlug)
	}
	if !reflect.DeepEqual(c.Provider.Type, c2.Provider.Type) {
		t.Errorf("provider.type: %q -> %q", c.Provider.Type, c2.Provider.Type)
	}
	if !reflect.DeepEqual(c.Sync.Ignore, c2.Sync.Ignore) {
		t.Errorf("sync.ignore lost in round-trip: %v -> %v", c.Sync.Ignore, c2.Sync.Ignore)
	}
	// Per-provider subsection survives round-trip.
	if _, ok := c2.Provider.Raw["gcp"]; !ok {
		t.Errorf("provider.gcp subsection lost in round-trip")
	}
}

func TestValidateRejectsBadSlug(t *testing.T) {
	tests := []struct {
		slug      string
		shouldErr bool
	}{
		{"argus", false},
		{"latent-advisory", false},
		{"a", false},
		{"", true},
		{"Argus", true},          // uppercase
		{"-foo", true},           // leading hyphen
		{"foo-", true},           // trailing hyphen
		{"foo bar", true},        // space
		{"foo_bar", true},        // underscore
		{"argus2", false},
		{"2argus", true}, // must start with letter
	}
	for _, tc := range tests {
		t.Run(tc.slug, func(t *testing.T) {
			c := Default()
			c.ProjectSlug = tc.slug
			err := c.Validate()
			if tc.shouldErr && err == nil {
				t.Errorf("Validate(%q) = nil, want error", tc.slug)
			}
			if !tc.shouldErr && err != nil {
				t.Errorf("Validate(%q) = %v, want nil", tc.slug, err)
			}
		})
	}
}

func TestValidateRejectsBadMode(t *testing.T) {
	c := Default()
	c.ProjectSlug = "argus"
	c.Mode = Mode("nonsense")
	if err := c.Validate(); err == nil {
		t.Error("Validate accepted bogus mode")
	}
}

func TestValidateRejectsBadConflictPolicy(t *testing.T) {
	c := Default()
	c.ProjectSlug = "argus"
	c.Sync.ConflictPolicy = "smash-em-together"
	if err := c.Validate(); err == nil {
		t.Error("Validate accepted bogus conflict policy")
	}
}

func TestValidateRequiresProviderAndAgent(t *testing.T) {
	c := Default()
	c.ProjectSlug = "argus"
	c.Provider.Type = ""
	c.Agent.Type = ""
	c.Sync.Engine = ""
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate did not report missing required fields")
	}
	for _, want := range []string{"provider.type", "agent.type", "sync.engine"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing field %q", err.Error(), want)
		}
	}
}

func TestValidateSchemaVersionMismatch(t *testing.T) {
	c := Default()
	c.SchemaVersion = 0
	c.ProjectSlug = "argus"
	err := c.Validate()
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("Validate() = %v, want ErrUnsupportedSchema", err)
	}
}

func TestLoadOrInitMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	c, existed, err := LoadOrInit(p)
	if err != nil {
		t.Fatalf("LoadOrInit: %v", err)
	}
	if existed {
		t.Error("existed = true on missing file")
	}
	if c.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("default schema_version = %d, want %d", c.SchemaVersion, CurrentSchemaVersion)
	}
	if c.Mode != ModeLocalFirst {
		t.Errorf("default mode = %q, want local-first", c.Mode)
	}
}

func TestLoadOrInitExisting(t *testing.T) {
	p := writeTemp(t, "config.yaml", fixtureV1)
	c, existed, err := LoadOrInit(p)
	if err != nil {
		t.Fatalf("LoadOrInit: %v", err)
	}
	if !existed {
		t.Error("existed = false on existing file")
	}
	if c.ProjectSlug != "argus" {
		t.Errorf("project_slug = %q, want argus", c.ProjectSlug)
	}
}

func TestSaveRefusesInvalidConfig(t *testing.T) {
	c := Default()
	c.ProjectSlug = "BAD" // uppercase rejected
	out := filepath.Join(t.TempDir(), "bad.yaml")
	if err := c.Save(out); err == nil {
		t.Error("Save accepted invalid config")
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("invalid config should not have written %s", out)
	}
}

func TestSaveCreatesParentDirectory(t *testing.T) {
	c := Default()
	c.ProjectSlug = "argus"
	dir := t.TempDir()
	out := filepath.Join(dir, "deeply", "nested", "config.yaml")
	if err := c.Save(out); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("Save did not create file: %v", err)
	}
}

func TestLoadCorruptYAMLIsAClearError(t *testing.T) {
	p := writeTemp(t, "config.yaml", "::: this isn't yaml :::")
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load accepted garbage YAML")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error %q should mention parse", err.Error())
	}
}

func TestConfigWithUnknownKeysIsForwardCompatible(t *testing.T) {
	// A future schema may add fields. v1 should NOT reject configs that have
	// extra unknown top-level keys — it should preserve the known ones and
	// either round-trip or drop the unknowns. We only require the known ones
	// to load cleanly.
	withExtra := fixtureV1 + "\n# future fields the parser doesn't know about\nfuture_field:\n  some_key: some_value\n"
	p := writeTemp(t, "config.yaml", withExtra)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load with future fields: %v", err)
	}
	if c.ProjectSlug != "argus" {
		t.Errorf("project_slug = %q, want argus", c.ProjectSlug)
	}
}

func TestEmptyOptionalFieldsLoadCleanly(t *testing.T) {
	// Minimal valid config: only required fields, no handoff/cost sections.
	minimal := `schema_version: 1
project_slug: minimal
provider:
  type: gcp
agent:
  type: claude-code
sync:
  engine: mutagen
`
	p := writeTemp(t, "config.yaml", minimal)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ProjectSlug != "minimal" {
		t.Errorf("project_slug = %q", c.ProjectSlug)
	}
	if c.Mode != "" && c.Mode != ModeLocalFirst {
		t.Errorf("Mode default unexpected: %q", c.Mode)
	}
	// Empty AlertThresholds should be acceptable.
	if c.Cost.MonthlyCapUSD != 0 {
		t.Errorf("MonthlyCapUSD = %v, want 0 (default)", c.Cost.MonthlyCapUSD)
	}
}

func TestSlugBoundaryLengths(t *testing.T) {
	// 63 chars is the DNS-label cap; 1 char is the floor.
	tests := []struct {
		name string
		slug string
		ok   bool
	}{
		{"single letter", "a", true},
		{"63-char max", "a" + strings.Repeat("b", 61) + "z", true},
		{"64-char overflow", "a" + strings.Repeat("b", 62) + "z", false},
		{"all digits after first letter", "a123456", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.ProjectSlug = tc.slug
			err := c.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate(%q) = %v, want nil", tc.slug, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("Validate(%q) accepted invalid slug", tc.slug)
			}
		})
	}
}

func TestCostNegativeValuesRejected(t *testing.T) {
	c := Default()
	c.ProjectSlug = "argus"
	c.Cost.MonthlyCapUSD = -1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "monthly_cap_usd") {
		t.Errorf("Validate(neg cap) = %v, want error mentioning monthly_cap_usd", err)
	}
	c2 := Default()
	c2.ProjectSlug = "argus"
	c2.Cost.AlertThresholds = []float64{-5, 10}
	if err := c2.Validate(); err == nil || !strings.Contains(err.Error(), "alert_thresholds") {
		t.Errorf("Validate(neg threshold) = %v, want error mentioning alert_thresholds", err)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
