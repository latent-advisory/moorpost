// Package agent defines the AI-coding-agent abstraction used by Moorpost.
//
// Each agent (claudecode, cursorcli, aider, ...) implements the Agent
// interface in its own subpackage and registers a constructor with Register.
// v1 ships with one impl: claudecode.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// OSFamily identifies the OS family a remote VM runs. Used by InstallScript
// to emit the correct package-manager invocations.
type OSFamily string

const (
	OSUbuntu       OSFamily = "ubuntu"
	OSDebian       OSFamily = "debian"
	OSAmazonLinux  OSFamily = "amazon-linux"
	OSUnknown      OSFamily = "unknown"
)

// Credential is an opaque agent credential (OAuth token, API key, etc.) plus
// the env-var name the agent expects when invoked. The CLI passes Credential
// values through Keychain/Secret-Service; only InjectCredential renders them
// into a place the remote agent can read.
type Credential struct {
	// EnvVar is the env-var name used to inject the credential into the
	// agent's process (e.g. "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY").
	EnvVar string

	// Value is the credential material. Treat as sensitive; never log.
	Value string

	// Kind tags the credential type for UX ("oauth-subscription", "api-key").
	Kind string
}

// SSHTarget is a copy of provider.SSHTarget kept here to avoid an agent->provider
// dependency. Callers convert at the boundary.
type SSHTarget struct {
	Host         string
	Port         int
	User         string
	IdentityFile string // optional path to private SSH key (-i)
}

// SessionRef identifies a logical agent session per project. The agent
// implementation decides what session-id format means; Moorpost stores it
// opaquely in state.json.
type SessionRef struct {
	ProjectSlug   string // e.g. "argus"
	ProjectAbsDir string // e.g. "/Users/landytang/.../argus" (used for state-path encoding)
	SessionID     string // agent-specific, opaque to the CLI
}

// Agent abstracts an AI coding tool's lifecycle on the remote VM.
type Agent interface {
	// ID returns a stable identifier (e.g. "claude-code", "cursor-cli").
	ID() string

	// InstallScript returns a shell snippet that, when run on the remote VM,
	// installs and pins the agent. Idempotent: rerunning must be a no-op once
	// the agent is installed at the desired version.
	InstallScript(os OSFamily) string

	// AuthenticateLocal runs the agent's auth flow on the user's local
	// machine (e.g. `claude setup-token`) and returns the captured credential.
	// May open a browser. May prompt the user. Should not write to disk.
	AuthenticateLocal(ctx context.Context) (Credential, error)

	// LoadCachedCredential is the passive read of the cached credential
	// (typically the OS keychain). Returns ErrNotAuthenticated when no
	// credential is cached. Use this in handoff preflight, status reports,
	// and other code paths that should NOT trigger an interactive auth flow.
	LoadCachedCredential() (Credential, error)

	// InjectCredential places the credential where the remote agent reads it
	// (typically a 0600 env file consumed by a systemd unit or tmux session).
	InjectCredential(ctx context.Context, target SSHTarget, c Credential) error

	// SessionStatePath returns the path on a host where the agent stores
	// per-project session state, given the project's absolute directory.
	// Used to drive one-shot syncs at handoff/return boundaries.
	SessionStatePath(projectAbsDir string) string

	// Pause asks the running agent to finish its current turn and wait for
	// input. Implementations may use SIGUSR1, tmux send-keys, or an agent-
	// specific control channel. Returns when the agent is paused or after
	// timeout per the calling context.
	Pause(ctx context.Context, target SSHTarget, ref SessionRef) error

	// Resume runs the agent on the remote, attaching to a previous session
	// by SessionID. The remote agent picks up where the prior side left off.
	Resume(ctx context.Context, target SSHTarget, ref SessionRef) error

	// IsActive reports whether the agent is currently running for the given
	// session on the host pointed to by target.
	IsActive(ctx context.Context, target SSHTarget, ref SessionRef) (bool, error)
}

// ErrAgentNotInstalled indicates the remote VM hasn't run the install script
// for this agent yet, so Pause/Resume/IsActive cannot be called.
var ErrAgentNotInstalled = errors.New("agent not installed on remote")

// ErrAuthRequired indicates the agent has no credential cached and cannot
// run on the remote. The CLI should prompt for `moorpost auth`.
var ErrAuthRequired = errors.New("agent credential required")

// ErrNotAuthenticated indicates LoadCachedCredential found no cached
// credential. Callers (status, handoff preflight) should hint the user
// toward `moorpost auth` rather than silently triggering an OAuth flow.
var ErrNotAuthenticated = errors.New("agent not authenticated (run `moorpost auth`)")

// Constructor builds an Agent from a config map.
type Constructor func(config map[string]any) (Agent, error)

type registry struct {
	mu           sync.RWMutex
	constructors map[string]Constructor
}

var defaultRegistry = &registry{constructors: make(map[string]Constructor)}

// Register associates an ID with a Constructor. Panics on empty id, nil
// constructor, or duplicate id (programmer errors, not runtime conditions).
func Register(id string, c Constructor) {
	defaultRegistry.register(id, c)
}

func (r *registry) register(id string, c Constructor) {
	if id == "" {
		panic("agent: Register called with empty id")
	}
	if c == nil {
		panic("agent: Register called with nil Constructor for id " + id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.constructors[id]; dup {
		panic("agent: Register called twice for id " + id)
	}
	r.constructors[id] = c
}

// Get builds an agent from its registered constructor.
func Get(id string, config map[string]any) (Agent, error) {
	return defaultRegistry.get(id, config)
}

func (r *registry) get(id string, config map[string]any) (Agent, error) {
	r.mu.RLock()
	c, ok := r.constructors[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agent: unknown id %q (registered: %v)", id, r.list())
	}
	return c(config)
}

// List returns the IDs of all registered agents, sorted.
func List() []string {
	return defaultRegistry.list()
}

func (r *registry) list() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.constructors))
	for id := range r.constructors {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
