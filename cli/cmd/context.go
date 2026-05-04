package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	_ "github.com/latent-advisory/moorpost/cli/internal/agent/claudecode" // register claude-code
	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	_ "github.com/latent-advisory/moorpost/cli/internal/provider/gcp" // register gcp
	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
	_ "github.com/latent-advisory/moorpost/cli/internal/sync/mutagen" // register mutagen
)

// Context bundles everything a subcommand needs at runtime: project config,
// per-machine state, the constructed Provider/Agent/Sync trio, and IO sinks.
//
// Built by loadProjectContext from disk; built directly in tests.
type Context struct {
	ProjectDir    string                  // dir containing .moorpost/config.yaml
	ConfigPath    string                  // absolute path to config file
	StatePath     string                  // absolute path to state.json
	Config        *config.ProjectConfig
	State         *state.State
	Provider      provider.Provider
	Agent         agent.Agent
	Sync          mpsync.Sync
	Stdout        io.Writer
	Stderr        io.Writer
}

// ContextOptions controls how loadProjectContext builds a Context.
type ContextOptions struct {
	// CWD is the directory to start searching for .moorpost/config.yaml.
	// Walks up to filesystem root. Defaults to os.Getwd().
	CWD string

	// RequireConfig — if true, returns an error when no config file is found.
	// `init` sets this false; everyone else sets it true.
	RequireConfig bool

	// StatePath overrides the default ~/.moorpost/state.json. Tests use this.
	StatePath string

	// Stdout / Stderr override the default os.Stdout / os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
}

// ErrConfigNotFound indicates no `.moorpost/config.yaml` was located in or
// above the working directory. Commands should suggest `moorpost init`.
var ErrConfigNotFound = errors.New("moorpost: project config not found (run `moorpost init`)")

// loadProjectContext walks up from opts.CWD looking for `.moorpost/config.yaml`,
// loads it (when present), constructs the Provider/Agent/Sync from the
// registries, and loads ~/.moorpost/state.json (initializing it on first use).
func loadProjectContext(opts ContextOptions) (*Context, error) {
	cwd := opts.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getwd: %w", err)
		}
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	configPath := findConfigUp(cwd)
	c := &Context{
		ProjectDir: cwd,
		Stdout:     stdout,
		Stderr:     stderr,
	}
	if configPath != "" {
		c.ConfigPath = configPath
		c.ProjectDir = filepath.Dir(filepath.Dir(configPath)) // .moorpost/.. → project dir
	} else if opts.RequireConfig {
		return nil, ErrConfigNotFound
	}

	if c.ConfigPath != "" {
		cfg, err := config.Load(c.ConfigPath)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", c.ConfigPath, err)
		}
		c.Config = cfg

		// Construct interface impls from the config. Each registered
		// constructor receives the typed sub-section of the config map.
		providerCfg := pickSubsection(cfg.Provider.Raw, cfg.Provider.Type)
		p, err := provider.Get(cfg.Provider.Type, providerCfg)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", cfg.Provider.Type, err)
		}
		c.Provider = p

		agentCfg := pickSubsection(cfg.Agent.Raw, cfg.Agent.Type)
		a, err := agent.Get(cfg.Agent.Type, agentCfg)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", cfg.Agent.Type, err)
		}
		c.Agent = a

		s, err := mpsync.Get(cfg.Sync.Engine, cfg.Sync.Raw)
		if err != nil {
			return nil, fmt.Errorf("sync %q: %w", cfg.Sync.Engine, err)
		}
		c.Sync = s
	}

	// State file (per-machine). Always load — even `init` benefits from a
	// state record so subsequent commands don't have to bootstrap it.
	statePath := opts.StatePath
	if statePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home: %w", err)
		}
		statePath = filepath.Join(home, ".moorpost", "state.json")
	}
	c.StatePath = statePath
	st, err := state.Open(statePath)
	if err != nil {
		return nil, fmt.Errorf("load state %s: %w", statePath, err)
	}
	c.State = st

	return c, nil
}

// findConfigUp walks up from start looking for .moorpost/config.yaml. Returns
// the absolute path or "" if not found.
func findConfigUp(start string) string {
	dir := start
	for {
		candidate := filepath.Join(dir, ".moorpost", "config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// pickSubsection extracts the per-impl config block from a Raw map. If the
// nested block doesn't exist, returns the whole Raw map (so trivial flat
// configs continue to work).
func pickSubsection(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	if v, ok := raw[key]; ok {
		if m, ok := v.(map[string]any); ok {
			return m
		}
	}
	return raw
}
