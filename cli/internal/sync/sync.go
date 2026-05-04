// Package sync defines the file-sync abstraction used by Moorpost.
//
// v1 ships with one impl: mutagen. v2 may add rsync as a fallback.
//
// This package is named "sync" but does NOT shadow the stdlib "sync" — it is
// always imported as a named package, and internal callers refer to it as
// `mpsync` to avoid confusion.
package sync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	stdsync "sync"
)

// Direction is the direction of a one-shot sync. Bidirectional is reserved
// for ongoing sync sessions and rejected by OneShot.
type Direction string

const (
	DirectionLocalToRemote Direction = "local-to-remote"
	DirectionRemoteToLocal Direction = "remote-to-local"
	DirectionBidirectional Direction = "bidirectional"
)

// Endpoint identifies one side of a sync relationship.
type Endpoint struct {
	// SSHHost is the SSH alias or host:port for the remote side. Empty means
	// "this is the local side"; only one Endpoint of a pair may have an
	// empty SSHHost.
	SSHHost string

	// Path is the absolute filesystem path on that side.
	Path string
}

// IsLocal reports whether this endpoint is the local filesystem.
func (e Endpoint) IsLocal() bool { return e.SSHHost == "" }

// SyncSpec describes an ongoing bidirectional sync session to start.
type SyncSpec struct {
	// Alpha and Beta are the two sides. Alpha is treated as authoritative
	// for conflict resolution when ConflictPolicy is "alpha-wins".
	Alpha Endpoint
	Beta  Endpoint

	// ConflictPolicy: "alpha-wins" | "two-way-resolved" | "manual".
	// Mutagen calls these "one-way-safe" / "two-way-resolved" / "manual"
	// internally; the abstraction normalizes the names.
	ConflictPolicy string

	// IgnorePatterns are gitignore-style patterns ignored by the sync. Common
	// entries are documented in PLUGIN.md §8.1.
	IgnorePatterns []string

	// Label is a human-readable session label, used in status output.
	Label string
}

// SyncSessionID is an opaque identifier for an ongoing sync session.
type SyncSessionID string

// SyncStatus captures the runtime state of a sync session.
type SyncStatus struct {
	ID        SyncSessionID
	State     SyncState
	Conflicts int    // count of unresolved conflicts (0 = clean)
	LastError string // empty if no recent error
	AlphaPath string
	BetaPath  string
}

// SyncState is the operational state of a sync session.
type SyncState string

const (
	SyncStateUnknown    SyncState = "unknown"
	SyncStateConnecting SyncState = "connecting"
	SyncStateScanning   SyncState = "scanning"
	SyncStateWatching   SyncState = "watching"
	SyncStatePaused     SyncState = "paused"
	SyncStateConflicted SyncState = "conflicted"
	SyncStateError      SyncState = "error"
)

// Sync abstracts a file-sync engine.
type Sync interface {
	// ID returns a stable identifier (e.g. "mutagen").
	ID() string

	// StartSession starts an ongoing bidirectional sync per spec and returns
	// its session identifier. If a session matching the spec's endpoints
	// already exists, implementations may return its existing ID rather than
	// creating a duplicate.
	StartSession(ctx context.Context, spec SyncSpec) (SyncSessionID, error)

	// Pause suspends the session without tearing it down. Resume picks up
	// where it left off. Idempotent.
	Pause(ctx context.Context, id SyncSessionID) error

	// Resume re-enables a paused session. Idempotent if already active.
	Resume(ctx context.Context, id SyncSessionID) error

	// OneShot performs a single-direction sync from src to dst. dir must be
	// Local↔Remote (bidirectional is rejected). Used at handoff/return for
	// the agent's session-state directory.
	OneShot(ctx context.Context, src, dst Endpoint, dir Direction) error

	// Status returns the current state of the session.
	Status(ctx context.Context, id SyncSessionID) (SyncStatus, error)

	// Stop terminates the session. After Stop, the SyncSessionID is invalid.
	Stop(ctx context.Context, id SyncSessionID) error
}

// ErrInvalidDirection indicates an OneShot call was passed a non-unidirectional
// Direction value.
var ErrInvalidDirection = errors.New("sync: OneShot requires a unidirectional direction")

// ErrSessionNotFound indicates a SyncSessionID is not (or is no longer) known
// to the engine.
var ErrSessionNotFound = errors.New("sync: session not found")

// Constructor builds a Sync from a config map.
type Constructor func(config map[string]any) (Sync, error)

type registry struct {
	mu           stdsync.RWMutex
	constructors map[string]Constructor
}

var defaultRegistry = &registry{constructors: make(map[string]Constructor)}

// Register associates an ID with a Constructor.
func Register(id string, c Constructor) {
	defaultRegistry.register(id, c)
}

func (r *registry) register(id string, c Constructor) {
	if id == "" {
		panic("sync: Register called with empty id")
	}
	if c == nil {
		panic("sync: Register called with nil Constructor for id " + id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.constructors[id]; dup {
		panic("sync: Register called twice for id " + id)
	}
	r.constructors[id] = c
}

// Get builds a Sync from its registered constructor.
func Get(id string, config map[string]any) (Sync, error) {
	return defaultRegistry.get(id, config)
}

func (r *registry) get(id string, config map[string]any) (Sync, error) {
	r.mu.RLock()
	c, ok := r.constructors[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sync: unknown id %q (registered: %v)", id, r.list())
	}
	return c(config)
}

// List returns the IDs of all registered sync engines, sorted.
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
