// Package mutagen implements the Sync interface using the Mutagen CLI.
//
// Each Moorpost project's sync session uses spec.Label as both the
// `--name` and as the SyncSessionID — so callers can drive the session
// using the name they chose rather than tracking opaque mutagen IDs.
package mutagen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
)

// EngineID is the registry identifier.
const EngineID = "mutagen"

// Options controls mutagen runtime behavior.
type Options struct {
	// Executor wraps os/exec; defaults to mpexec.New() if nil.
	Executor mpexec.Executor

	// Binary overrides the mutagen executable name. Default "mutagen".
	Binary string

	// RsyncBinary overrides the rsync executable name (used by OneShot).
	// Default "rsync".
	RsyncBinary string
}

// engine is the concrete Sync.
type engine struct {
	exec        mpexec.Executor
	binary      string
	rsyncBinary string
}

// New constructs a mutagen-backed Sync.
func New(config map[string]any) (mpsync.Sync, error) {
	return NewWithOptions(Options{})
}

// NewWithOptions allows tests to inject fakes.
func NewWithOptions(opts Options) (mpsync.Sync, error) {
	e := &engine{
		exec:        opts.Executor,
		binary:      opts.Binary,
		rsyncBinary: opts.RsyncBinary,
	}
	if e.exec == nil {
		e.exec = mpexec.New()
	}
	if e.binary == "" {
		e.binary = "mutagen"
	}
	if e.rsyncBinary == "" {
		e.rsyncBinary = "rsync"
	}
	return e, nil
}

func init() {
	mpsync.Register(EngineID, New)
}

func (e *engine) ID() string { return EngineID }

// renderEndpoint converts a mpsync.Endpoint to mutagen URL form. Local is
// just the path; remote is `host:path`. Mutagen accepts SSH aliases as host.
func renderEndpoint(ep mpsync.Endpoint) (string, error) {
	if ep.Path == "" {
		return "", errors.New("mutagen: endpoint path must not be empty")
	}
	if ep.IsLocal() {
		return ep.Path, nil
	}
	return ep.SSHHost + ":" + ep.Path, nil
}

// mapMode converts our normalized conflict policies into mutagen's
// `--mode` enum. v1 supports the three policies documented in PLUGIN.md.
func mapMode(policy string) (string, error) {
	switch policy {
	case "alpha-wins", "two-way-resolved":
		return "two-way-resolved", nil
	case "manual":
		return "two-way-safe", nil
	case "":
		return "two-way-resolved", nil // sensible default
	default:
		return "", fmt.Errorf("mutagen: unsupported conflict policy %q (want alpha-wins|two-way-resolved|manual)", policy)
	}
}

func (e *engine) StartSession(ctx context.Context, spec mpsync.SyncSpec) (mpsync.SyncSessionID, error) {
	if spec.Label == "" {
		return "", errors.New("mutagen: SyncSpec.Label must not be empty (used as session name)")
	}
	mode, err := mapMode(spec.ConflictPolicy)
	if err != nil {
		return "", err
	}
	alpha, err := renderEndpoint(spec.Alpha)
	if err != nil {
		return "", fmt.Errorf("mutagen: alpha: %w", err)
	}
	beta, err := renderEndpoint(spec.Beta)
	if err != nil {
		return "", fmt.Errorf("mutagen: beta: %w", err)
	}
	args := []string{"sync", "create",
		"--name", spec.Label,
		"--mode", mode,
	}
	for _, p := range spec.IgnorePatterns {
		args = append(args, "--ignore", p)
	}
	args = append(args, alpha, beta)
	_, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return "", fmt.Errorf("mutagen sync create: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		// If a session with this name already exists, return its ID
		// (idempotent caller contract).
		if strings.Contains(stderrStr, "already exists") {
			return mpsync.SyncSessionID(spec.Label), nil
		}
		return "", fmt.Errorf("mutagen sync create exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	return mpsync.SyncSessionID(spec.Label), nil
}

func (e *engine) Pause(ctx context.Context, id mpsync.SyncSessionID) error {
	return e.simpleSubcommand(ctx, "pause", string(id))
}

func (e *engine) Resume(ctx context.Context, id mpsync.SyncSessionID) error {
	return e.simpleSubcommand(ctx, "resume", string(id))
}

func (e *engine) Stop(ctx context.Context, id mpsync.SyncSessionID) error {
	if id == "" {
		return errors.New("mutagen: Stop requires a non-empty session id")
	}
	_, stderr, code, err := e.exec.Run(ctx, e.binary,
		[]string{"sync", "terminate", string(id)}, nil)
	if err != nil {
		return fmt.Errorf("mutagen sync terminate: %w", err)
	}
	if code == 0 {
		return nil
	}
	stderrStr := string(stderr)
	// Treat "no matching sessions" as idempotent success (already stopped).
	if strings.Contains(stderrStr, "no matching sessions") || strings.Contains(stderrStr, "not found") {
		return nil
	}
	return fmt.Errorf("mutagen sync terminate exit %d: %s", code, strings.TrimSpace(stderrStr))
}

func (e *engine) simpleSubcommand(ctx context.Context, sub, id string) error {
	if id == "" {
		return fmt.Errorf("mutagen: %s requires a non-empty session id", sub)
	}
	_, stderr, code, err := e.exec.Run(ctx, e.binary, []string{"sync", sub, id}, nil)
	if err != nil {
		return fmt.Errorf("mutagen sync %s: %w", sub, err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "no matching sessions") {
			return mpsync.ErrSessionNotFound
		}
		return fmt.Errorf("mutagen sync %s exit %d: %s", sub, code, strings.TrimSpace(stderrStr))
	}
	return nil
}

func (e *engine) Status(ctx context.Context, id mpsync.SyncSessionID) (mpsync.SyncStatus, error) {
	if id == "" {
		return mpsync.SyncStatus{}, errors.New("mutagen: Status requires a non-empty session id")
	}
	// Mutagen's `--template` accepts a Go template against the list output.
	// We fetch <Status>|<#Conflicts> in one round-trip to avoid two calls.
	tmpl := `{{range .}}{{.Status}}|{{len .Conflicts}}|{{(index .AlphaURL.Path)}}|{{(index .BetaURL.Path)}}{{end}}`
	stdout, stderr, code, err := e.exec.Run(ctx, e.binary,
		[]string{"sync", "list", string(id), "--template", tmpl}, nil)
	if err != nil {
		return mpsync.SyncStatus{}, fmt.Errorf("mutagen sync list: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "no matching sessions") {
			return mpsync.SyncStatus{ID: id, State: mpsync.SyncStateUnknown}, mpsync.ErrSessionNotFound
		}
		return mpsync.SyncStatus{}, fmt.Errorf("mutagen sync list exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	out := strings.TrimSpace(string(stdout))
	if out == "" {
		return mpsync.SyncStatus{ID: id, State: mpsync.SyncStateUnknown}, mpsync.ErrSessionNotFound
	}
	parts := strings.SplitN(out, "|", 4)
	st := mpsync.SyncStatus{ID: id}
	if len(parts) >= 1 {
		st.State = parseMutagenState(parts[0])
	}
	if len(parts) >= 2 {
		// Best-effort numeric parse; if it fails, treat as 0.
		var n int
		_, _ = fmt.Sscanf(parts[1], "%d", &n)
		st.Conflicts = n
		if n > 0 {
			st.State = mpsync.SyncStateConflicted
		}
	}
	if len(parts) >= 3 {
		st.AlphaPath = parts[2]
	}
	if len(parts) >= 4 {
		st.BetaPath = parts[3]
	}
	return st, nil
}

// listJSONSession is the subset of `mutagen sync list --json` output that
// we consume. Mutagen's full schema has many more fields; we only declare
// what's needed to surface conflict details.
type listJSONSession struct {
	Conflicts []listJSONConflict `json:"conflicts"`
}

type listJSONConflict struct {
	// Mutagen reports a list of changes per side rather than a single
	// path. In moorpost's surface we collapse to the first change's path
	// (which is the conflict locus) and use the latest mtime + the
	// dominant kind across the side's changes.
	AlphaChanges []listJSONChange `json:"alphaChanges"`
	BetaChanges  []listJSONChange `json:"betaChanges"`
}

type listJSONChange struct {
	Path string             `json:"path"`
	New  *listJSONEntry     `json:"new,omitempty"`
	Old  *listJSONEntry     `json:"old,omitempty"`
}

type listJSONEntry struct {
	// Modified is RFC3339; absent means mutagen didn't track an mtime.
	Modified string `json:"modified,omitempty"`
}

// ListConflicts implements mpsync.Sync.ListConflicts via
// `mutagen sync list <id> --json`.
func (e *engine) ListConflicts(ctx context.Context, id mpsync.SyncSessionID) ([]mpsync.Conflict, error) {
	if id == "" {
		return nil, errors.New("mutagen: ListConflicts requires a non-empty session id")
	}
	stdout, stderr, code, err := e.exec.Run(ctx, e.binary,
		[]string{"sync", "list", string(id), "--json"}, nil)
	if err != nil {
		return nil, fmt.Errorf("mutagen sync list --json: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "no matching sessions") {
			return nil, mpsync.ErrSessionNotFound
		}
		return nil, fmt.Errorf("mutagen sync list --json exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	out := strings.TrimSpace(string(stdout))
	if out == "" {
		return nil, mpsync.ErrSessionNotFound
	}
	return parseConflictsJSON(out)
}

// parseConflictsJSON is the pure-data entry point for tests.
func parseConflictsJSON(s string) ([]mpsync.Conflict, error) {
	// Mutagen's --json output is a JSON array (one entry per session in
	// the matching list). When the user passes an explicit session id the
	// array length is 0 or 1.
	var sessions []listJSONSession
	if err := json.Unmarshal([]byte(s), &sessions); err != nil {
		return nil, fmt.Errorf("mutagen list --json: parse: %w", err)
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	var out []mpsync.Conflict
	for _, c := range sessions[0].Conflicts {
		conflict := mpsync.Conflict{
			Path:      pickConflictPath(c),
			AlphaKind: dominantKind(c.AlphaChanges),
			BetaKind:  dominantKind(c.BetaChanges),
		}
		conflict.AlphaModifiedAt = latestMTime(c.AlphaChanges)
		conflict.BetaModifiedAt = latestMTime(c.BetaChanges)
		out = append(out, conflict)
	}
	return out, nil
}

// pickConflictPath returns the conflict's representative path. Mutagen's
// alphaChanges/betaChanges may each list multiple paths (rare in practice
// for a single-file conflict). We pick the first non-empty path from
// either side. Empty path is reported as "" — caller can choose how to
// display.
func pickConflictPath(c listJSONConflict) string {
	for _, ch := range c.AlphaChanges {
		if ch.Path != "" {
			return ch.Path
		}
	}
	for _, ch := range c.BetaChanges {
		if ch.Path != "" {
			return ch.Path
		}
	}
	return ""
}

// dominantKind translates a side's change list into a single ChangeKind.
// Heuristic: if any change is "deleted" (Old set, New nil) — deleted;
// else if any is "created" (New set, Old nil) — created;
// else modified; else unknown.
func dominantKind(changes []listJSONChange) mpsync.ChangeKind {
	if len(changes) == 0 {
		return mpsync.ChangeKindUnknown
	}
	hasDelete := false
	hasCreate := false
	hasModify := false
	for _, ch := range changes {
		switch {
		case ch.New == nil && ch.Old != nil:
			hasDelete = true
		case ch.New != nil && ch.Old == nil:
			hasCreate = true
		case ch.New != nil && ch.Old != nil:
			hasModify = true
		}
	}
	switch {
	case hasDelete:
		return mpsync.ChangeKindDeleted
	case hasCreate:
		return mpsync.ChangeKindCreated
	case hasModify:
		return mpsync.ChangeKindModified
	default:
		return mpsync.ChangeKindUnknown
	}
}

// latestMTime returns the most recent Modified timestamp across the side's
// changes. Returns the zero Time if no entry has a parseable timestamp.
func latestMTime(changes []listJSONChange) time.Time {
	var latest time.Time
	for _, ch := range changes {
		for _, e := range []*listJSONEntry{ch.New, ch.Old} {
			if e == nil || e.Modified == "" {
				continue
			}
			t, err := time.Parse(time.RFC3339, e.Modified)
			if err != nil {
				continue
			}
			if t.After(latest) {
				latest = t
			}
		}
	}
	return latest
}

// parseMutagenState maps mutagen's status strings to our enum. Mutagen has
// many fine-grained states; we collapse them into our v1 vocabulary.
func parseMutagenState(s string) mpsync.SyncState {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "Connecting"):
		return mpsync.SyncStateConnecting
	case strings.Contains(s, "Scanning") || strings.Contains(s, "Reconciling") || strings.Contains(s, "Staging") || strings.Contains(s, "Transitioning"):
		return mpsync.SyncStateScanning
	case strings.Contains(s, "Watching"):
		return mpsync.SyncStateWatching
	case s == "Paused" || strings.Contains(s, "Disconnected"):
		return mpsync.SyncStatePaused
	case strings.HasPrefix(s, "Halted") || strings.Contains(s, "Error"):
		return mpsync.SyncStateError
	default:
		return mpsync.SyncStateUnknown
	}
}

// OneShot performs a one-direction file copy from src to dst using rsync.
// Mutagen itself doesn't have a clean one-shot mode — its model is ongoing
// sessions. rsync is universally available and is the right tool for the
// "sync session state at handoff/return" path.
func (e *engine) OneShot(ctx context.Context, src, dst mpsync.Endpoint, dir mpsync.Direction) error {
	switch dir {
	case mpsync.DirectionLocalToRemote, mpsync.DirectionRemoteToLocal:
		// ok
	default:
		return mpsync.ErrInvalidDirection
	}
	if src.Path == "" || dst.Path == "" {
		return errors.New("mutagen: OneShot requires non-empty src and dst paths")
	}
	srcURL := rsyncURL(src)
	dstURL := rsyncURL(dst)
	args := []string{"-a", "--delete"}
	// Use ssh as the remote shell. -e is needed only when at least one side
	// is remote; rsync otherwise rejects -e for purely local copies (or
	// rather it accepts it but it's noise).
	if !src.IsLocal() || !dst.IsLocal() {
		args = append(args, "-e", "ssh -o BatchMode=yes -o ConnectTimeout=15")
	}
	args = append(args, srcURL, dstURL)
	_, stderr, code, err := e.exec.Run(ctx, e.rsyncBinary, args, nil)
	if err != nil {
		return fmt.Errorf("rsync: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("rsync exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// rsyncURL renders an Endpoint for rsync. Local: just the path. Remote:
// `host:path`. rsync wants a trailing slash on src to copy contents (vs
// the directory itself); we leave that decision to callers — they pass the
// exact paths they want.
func rsyncURL(ep mpsync.Endpoint) string {
	if ep.IsLocal() {
		return ep.Path
	}
	return ep.SSHHost + ":" + ep.Path
}
