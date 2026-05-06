// Package state manages ~/.moorpost/state.json — the per-machine cache of
// project ↔ VM mappings, active-side flag, machine_id, and recent cost data.
//
// State is per-machine and never synced (laptop and desktop each have their
// own file). Writes are atomic (tmpfile + fsync + rename) and serialized via
// a file lock so two `moorpost` invocations on the same machine cannot race.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

// CurrentSchemaVersion is the version this build understands.
const CurrentSchemaVersion = 1

// State is the on-disk schema. See PLUGIN.md §8.2.
type State struct {
	SchemaVersion   int                       `json:"schema_version"`
	MachineID       string                    `json:"machine_id"`
	TelemetryOptIn  bool                      `json:"telemetry_opt_in"`
	Projects        map[string]ProjectState   `json:"projects"` // keyed by project absolute path
	VMs             map[string]VMRecord       `json:"vms"`      // keyed by VM ID
}

// ProjectState records what Moorpost knows about a project on this machine.
type ProjectState struct {
	Slug             string    `json:"slug"`
	VMID             string    `json:"vm_id"`
	VMZone           string    `json:"vm_zone"`
	ActiveSide       Side      `json:"active_side"`
	LastHandoff      time.Time `json:"last_handoff,omitempty"`
	LastReturn      time.Time `json:"last_return,omitempty"`
	AgentSessionID   string    `json:"agent_session_id"`
	SyncSessionID    string    `json:"sync_session_id"`

	// PendingResumeSID is a single-use hand-off baton: the CLI sets it
	// after a successful `moorpost handoff` to record the session ID the
	// next plugin-spawned claude should resume. The claude-wrapper reads
	// it, injects `--resume <sid>` into the remote claude argv, and then
	// atomically clears the field. Empty = no pending migration.
	//
	// This is the mechanism behind the extension's "Migrate this
	// conversation to remote" button: trigger
	// claude-vscode.newConversation → plugin spawns fresh claude →
	// wrapper sees pending → injects --resume → remote claude reads the
	// (already synced) JSONL and continues the conversation with full
	// model context. Panel scrollback resets because newConversation
	// allocates a fresh CLAUDE_CONFIG_DIR, but the conversation itself
	// is preserved on the model side and accessible via the plugin's
	// session-history list.
	PendingResumeSID string `json:"pending_resume_sid,omitempty"`

	// LastSessionSyncHash is the SHA-256 manifest hash of
	// ~/.claude/projects/<encoded>/ as of the last successful handoff or
	// return. Used by `moorpost handoff` / `moorpost return` to detect
	// the session-state-conflict case spec'd in PLUGIN.md §6.5 line 261.
	// Empty means "no successful sync yet" — first handoff will set it.
	LastSessionSyncHash string `json:"last_session_sync_hash,omitempty"`

	// LastPluginsSyncHash is the SHA-256 of a {path,size,mtime} manifest
	// over ~/.claude/plugins/ at the time of the last successful plugin
	// rsync to this project's VM. Handoff compares the current local
	// hash to this value and skips the plugin rsync when they match,
	// saving ~3-5s on warm handoffs where the user hasn't installed/
	// removed plugins. Empty = no prior sync, always re-rsync.
	//
	// Project-scoped (not machine-scoped) because each VM is its own
	// destination — re-syncing is the only way to know that a NEW VM
	// has the plugins installed. Mild inefficiency: if you have 3
	// projects and update plugins, the first handoff for each project
	// re-rsyncs. Negligible in practice.
	LastPluginsSyncHash string `json:"last_plugins_sync_hash,omitempty"`

	// RemoteSIDs is the set of session IDs currently routed to the remote VM.
	// Per-session routing replaces the project-level active_side dichotomy
	// for sessions with a known SID: the wrapper checks if --resume <sid>
	// is in this set and routes accordingly. Sessions NOT in this set
	// (including fresh spawns with no --resume) fall back to active_side.
	//
	// Handoff(sid) appends to this set; Return(sid) removes from it; the
	// VM is safe to stop only when this set is empty (no live remote work).
	// Order is insertion-order; uniqueness is enforced by callers.
	RemoteSIDs []string `json:"remote_sids,omitempty"`
}

// HasRemoteSID returns whether sid is currently routed to the remote VM.
func (p ProjectState) HasRemoteSID(sid string) bool {
	for _, s := range p.RemoteSIDs {
		if s == sid {
			return true
		}
	}
	return false
}

// VMRecord caches metadata about a VM. The provider's API is the source of
// truth; this is a hint to avoid round-trips.
type VMRecord struct {
	Provider        string    `json:"provider"`
	ExternalIP      string    `json:"external_ip"`
	StateCache      string    `json:"state_cache"`
	StateCacheAt    time.Time `json:"state_cache_at,omitempty"`
	MonthToDateUSD  float64   `json:"month_to_date_usd"`
}

// Side indicates which side of a handoff is currently active.
type Side string

const (
	SideLocal  Side = "local"
	SideRemote Side = "remote"
)

// ErrUnsupportedSchema indicates state.json schema_version is unknown.
var ErrUnsupportedSchema = errors.New("state: unsupported schema_version")

// Open loads the state file, creating it (with a fresh machine_id) if missing.
// Use WithLock for read-modify-write sequences.
func Open(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", path, err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, s.SchemaVersion, CurrentSchemaVersion)
	}
	if s.Projects == nil {
		s.Projects = make(map[string]ProjectState)
	}
	if s.VMs == nil {
		s.VMs = make(map[string]VMRecord)
	}
	return &s, nil
}

// New constructs an empty State with a fresh machine_id. Used for first-run.
func New() *State {
	return &State{
		SchemaVersion: CurrentSchemaVersion,
		MachineID:     uuid.NewString(),
		Projects:      make(map[string]ProjectState),
		VMs:           make(map[string]VMRecord),
	}
}

// Save writes the state to path atomically. Callers under WithLock are
// already serialized; callers outside WithLock SHOULD NOT use Save directly.
func (s *State) Save(path string) error {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = CurrentSchemaVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("state: mkdir %s: %w", filepath.Dir(path), err)
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".moorpost-state-*.tmp")
	if err != nil {
		return fmt.Errorf("state: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("state: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("state: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("state: chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("state: rename: %w", err)
	}
	return nil
}

// WithLock runs fn under an exclusive file lock at path+".lock", reading
// state from path before fn and writing it back after if fn returns nil.
// This is the canonical read-modify-write entry point.
//
// If fn returns an error, the state is NOT written back. This makes it safe
// to validate inside fn and abort on bad state without persisting it.
func WithLock(path string, fn func(*State) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("state: mkdir %s: %w", filepath.Dir(path), err)
	}
	lockPath := path + ".lock"
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("state: acquire lock %s: %w", lockPath, err)
	}
	defer func() { _ = lock.Unlock() }()

	s, err := Open(path)
	if err != nil {
		return err
	}
	if err := fn(s); err != nil {
		return err
	}
	return s.Save(path)
}

// SetActiveSide records which side (local|remote) currently owns the agent
// session for the given project absolute path.
func (s *State) SetActiveSide(projectAbsPath string, side Side) {
	p := s.Projects[projectAbsPath]
	p.ActiveSide = side
	s.Projects[projectAbsPath] = p
}

// GetProject returns the recorded state for a project, or zero value + false
// if absent. Callers should treat zero value as "not yet provisioned."
func (s *State) GetProject(projectAbsPath string) (ProjectState, bool) {
	p, ok := s.Projects[projectAbsPath]
	return p, ok
}

// SetProject stores the project record. Used by `moorpost provision` /
// `moorpost handoff` after they've assigned a VM and started a session.
func (s *State) SetProject(projectAbsPath string, p ProjectState) {
	if s.Projects == nil {
		s.Projects = make(map[string]ProjectState)
	}
	s.Projects[projectAbsPath] = p
}

// RecordHandoff updates LastHandoff and ActiveSide=remote in one shot.
func (s *State) RecordHandoff(projectAbsPath string, now time.Time) {
	p := s.Projects[projectAbsPath]
	p.LastHandoff = now
	p.ActiveSide = SideRemote
	s.Projects[projectAbsPath] = p
}

// RecordReturn updates LastReturn and ActiveSide=local in one shot.
func (s *State) RecordReturn(projectAbsPath string, now time.Time) {
	p := s.Projects[projectAbsPath]
	p.LastReturn = now
	p.ActiveSide = SideLocal
	s.Projects[projectAbsPath] = p
}

// AddRemoteSID idempotently appends sid to the project's RemoteSIDs set.
// Returns true if the set changed (sid was not already present). Empty
// sid is silently ignored.
func (s *State) AddRemoteSID(projectAbsPath, sid string) bool {
	if sid == "" {
		return false
	}
	p := s.Projects[projectAbsPath]
	for _, existing := range p.RemoteSIDs {
		if existing == sid {
			return false
		}
	}
	p.RemoteSIDs = append(p.RemoteSIDs, sid)
	s.Projects[projectAbsPath] = p
	return true
}

// RemoveRemoteSID removes sid from the project's RemoteSIDs set. Returns
// true if the set changed (sid was present). Empty sid is silently
// ignored.
func (s *State) RemoveRemoteSID(projectAbsPath, sid string) bool {
	if sid == "" {
		return false
	}
	p := s.Projects[projectAbsPath]
	for i, existing := range p.RemoteSIDs {
		if existing == sid {
			p.RemoteSIDs = append(p.RemoteSIDs[:i], p.RemoteSIDs[i+1:]...)
			s.Projects[projectAbsPath] = p
			return true
		}
	}
	return false
}
