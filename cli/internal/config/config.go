// Package config defines the schema for `.moorpost/config.yaml` (per-project,
// committed to git) and provides Load/Save/Validate.
//
// The Provider/Agent/Sync subsections carry arbitrary keys via Raw fields so
// new providers and agents can be added without touching this package.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// CurrentSchemaVersion is the schema version this build understands. Bumping
// this requires writing migration code from the previous version.
const CurrentSchemaVersion = 1

// ProjectConfig is the top-level schema. See PLUGIN.md §8.1.
type ProjectConfig struct {
	SchemaVersion int    `yaml:"schema_version"`
	ProjectSlug   string `yaml:"project_slug"`

	Provider Provider `yaml:"provider"`
	Agent    Agent    `yaml:"agent"`
	Sync     Sync     `yaml:"sync"`

	Mode    Mode    `yaml:"mode"`
	Handoff Handoff `yaml:"handoff"`
	Cost    Cost    `yaml:"cost"`
}

// Provider holds the cloud-provider selection plus a free-form subsection.
// Implementations look up the Type in the provider registry and parse Raw
// according to their own schema.
type Provider struct {
	Type string         `yaml:"type"`
	Raw  map[string]any `yaml:",inline"`
}

// Agent holds the AI-tool selection plus a free-form subsection.
type Agent struct {
	Type string         `yaml:"type"`
	Raw  map[string]any `yaml:",inline"`
}

// Sync configures the file-sync engine plus shared options applied across engines.
type Sync struct {
	Engine         string   `yaml:"engine"`
	ConflictPolicy string   `yaml:"conflict_policy"`
	Ignore         []string `yaml:"ignore"`
	Raw            map[string]any `yaml:",inline"`
}

// Mode selects the operational mode of the VM.
type Mode string

const (
	ModeLocalFirst Mode = "local-first"
	ModePersistent Mode = "persistent"
)

// Handoff controls the handoff/return UX prompts.
type Handoff struct {
	PauseTimeoutSeconds int            `yaml:"pause_timeout_seconds"`
	Prompts             HandoffPrompts `yaml:"prompts"`
}

// HandoffPrompts controls when smart-prompts fire (per PLUGIN.md §0.3).
type HandoffPrompts struct {
	OnLidClose      bool `yaml:"on_lid_close"`
	OnIdleMinutes   int  `yaml:"on_idle_minutes"`
	OnBatteryBelow  int  `yaml:"on_battery_below"`
	OnVSCodeQuit    bool `yaml:"on_vscode_quit"`
}

// Cost controls cost guardrails.
type Cost struct {
	MonthlyCapUSD   float64   `yaml:"monthly_cap_usd"`
	AlertThresholds []float64 `yaml:"alert_thresholds"`
}

// Default returns a sensible default config for `moorpost init`. Callers are
// expected to fill in ProjectSlug and provider-specific fields before saving.
func Default() *ProjectConfig {
	return &ProjectConfig{
		SchemaVersion: CurrentSchemaVersion,
		Provider: Provider{
			Type: "gcp",
			Raw:  make(map[string]any),
		},
		Agent: Agent{
			Type: "claude-code",
			Raw:  make(map[string]any),
		},
		Sync: Sync{
			Engine:         "mutagen",
			ConflictPolicy: "alpha-wins",
			Ignore: []string{
				"**/node_modules",
				"**/.venv",
				"**/__pycache__",
				"**/dist",
				"**/.next",
				"**/.cache",
			},
		},
		Mode: ModeLocalFirst,
		Handoff: Handoff{
			PauseTimeoutSeconds: 30,
			Prompts: HandoffPrompts{
				OnLidClose:     true,
				OnIdleMinutes:  30,
				OnBatteryBelow: 20,
				OnVSCodeQuit:   true,
			},
		},
		Cost: Cost{
			MonthlyCapUSD:   50,
			AlertThresholds: []float64{10, 25},
		},
	}
}

var (
	// ErrUnsupportedSchema indicates schema_version != current. Caller may
	// migrate or refuse based on context.
	ErrUnsupportedSchema = errors.New("config: unsupported schema_version")

	// projectSlugRE matches the v1 project-slug grammar: lowercase ASCII,
	// digits, and single hyphens; must start with a letter and end with
	// alphanumeric. 1-63 chars (DNS-label safe, future-proofs naming
	// machines/buckets after the slug).
	projectSlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$|^[a-z]$`)

	// validConflictPolicies are the v1 allowed conflict_policy values.
	validConflictPolicies = map[string]bool{
		"alpha-wins":       true,
		"two-way-resolved": true,
		"manual":           true,
	}
)

// Load reads, parses, and validates a YAML config file.
func Load(path string) (*ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c ProjectConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return &c, nil
}

// LoadOrInit returns the parsed config at path, or a default config if the
// file does not exist. Other read/parse errors propagate.
func LoadOrInit(path string) (*ProjectConfig, bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("config: stat %s: %w", path, err)
	}
	c, err := Load(path)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// Save serializes the config to YAML at path, creating parent dirs as needed.
// The file is written atomically via a temp file + rename.
func (c *ProjectConfig) Save(path string) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("config: refuse to save invalid config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".moorpost-config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // safe even if rename succeeds

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("config: rename temp to %s: %w", path, err)
	}
	return nil
}

// Validate checks all required invariants. Aggregates errors so the caller
// can show all problems at once.
func (c *ProjectConfig) Validate() error {
	var errs []string
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, c.SchemaVersion, CurrentSchemaVersion)
	}
	if !projectSlugRE.MatchString(c.ProjectSlug) {
		errs = append(errs, fmt.Sprintf("project_slug %q must match %s", c.ProjectSlug, projectSlugRE))
	}
	if c.Provider.Type == "" {
		errs = append(errs, "provider.type is required")
	}
	if c.Agent.Type == "" {
		errs = append(errs, "agent.type is required")
	}
	if c.Sync.Engine == "" {
		errs = append(errs, "sync.engine is required")
	}
	if c.Sync.ConflictPolicy != "" && !validConflictPolicies[c.Sync.ConflictPolicy] {
		errs = append(errs, fmt.Sprintf("sync.conflict_policy %q is not one of: alpha-wins, two-way-resolved, manual", c.Sync.ConflictPolicy))
	}
	switch c.Mode {
	case "", ModeLocalFirst, ModePersistent:
		// ok; empty is treated as the default at use-site
	default:
		errs = append(errs, fmt.Sprintf("mode %q must be local-first or persistent", c.Mode))
	}
	if c.Cost.MonthlyCapUSD < 0 {
		errs = append(errs, "cost.monthly_cap_usd must be non-negative")
	}
	for i, threshold := range c.Cost.AlertThresholds {
		if threshold < 0 {
			errs = append(errs, fmt.Sprintf("cost.alert_thresholds[%d] must be non-negative", i))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.New("config validation failed:\n  - " + joinErrs(errs))
}

func joinErrs(errs []string) string {
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += "\n  - "
		}
		out += e
	}
	return out
}
