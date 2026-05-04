// Package mutagen implements the Sync interface using the Mutagen CLI.
//
// Each Moorpost project's sync session uses spec.Label as both the
// `--name` and as the SyncSessionID — so callers can drive the session
// using the name they chose rather than tracking opaque mutagen IDs.
package mutagen

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
