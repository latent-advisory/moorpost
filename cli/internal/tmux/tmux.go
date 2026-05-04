// Package tmux orchestrates tmux sessions on either the local host (via
// internal/exec) or a remote host (via internal/ssh). The interface is the
// same for both so callers don't care where tmux is running.
package tmux

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	mpssh "github.com/latent-advisory/moorpost/cli/internal/ssh"
)

// Tmux abstracts a tmux server.
type Tmux interface {
	// HasSession reports whether a session named name exists.
	HasSession(ctx context.Context, name string) (bool, error)

	// NewSession starts a detached session named name running cmd. env is
	// exported in the spawned shell (so the agent inherits the values).
	// Returns an error if the session already exists.
	NewSession(ctx context.Context, name, cmd string, env map[string]string) error

	// SendKeys sends keys to target (a session name or session:window.pane).
	// Each call is one tmux send-keys invocation; callers append "Enter" as a
	// final key to submit input.
	SendKeys(ctx context.Context, target string, keys ...string) error

	// KillSession terminates a session. Idempotent on missing session.
	KillSession(ctx context.Context, name string) error

	// ListSessions returns the existing session names. Returns an empty slice
	// (not nil, no error) if there are no sessions.
	ListSessions(ctx context.Context) ([]string, error)

	// CapturePane returns the last `lines` lines of the pane buffer for target.
	// If lines <= 0, captures the whole visible buffer.
	CapturePane(ctx context.Context, target string, lines int) (string, error)
}

// ErrSessionExists is returned by NewSession if a session with the requested
// name is already running.
var ErrSessionExists = errors.New("tmux: session already exists")

// validateSessionName rejects names tmux can't unambiguously address. tmux
// uses `:` and `.` as window/pane separators, and spaces would break our
// argv splitting; so we keep names ASCII-conservative for v0.1.
func validateSessionName(name string) error {
	if name == "" {
		return errors.New("tmux: session name must not be empty")
	}
	if strings.ContainsAny(name, ":. \t\r\n") {
		return fmt.Errorf("tmux: session name %q contains forbidden characters (: . whitespace)", name)
	}
	return nil
}

// runner abstracts how we run a tmux command on the target host. Local and
// remote implementations differ only in how they execute the command line.
type runner interface {
	// Run executes args[0] with args[1:] (i.e. `tmux <args...>`).
	Run(ctx context.Context, args []string, stdin []byte) (stdout []byte, stderr []byte, exitCode int, err error)
}

// localRunner runs tmux on the local host via mpexec.Executor.
type localRunner struct {
	exec mpexec.Executor
	bin  string // tmux binary; defaults to "tmux"
}

func (l *localRunner) Run(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, int, error) {
	bin := l.bin
	if bin == "" {
		bin = "tmux"
	}
	return l.exec.Run(ctx, bin, args, stdin)
}

// remoteRunner runs tmux on a remote host via ssh.Runner.
type remoteRunner struct {
	ssh  mpssh.Runner
	host string
}

func (r *remoteRunner) Run(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := "tmux"
	for _, a := range args {
		cmd += " " + shellQuote(a)
	}
	return r.ssh.RunWithStdin(ctx, r.host, cmd, stdin)
}

// shellQuote returns s in single quotes, with embedded single quotes escaped
// as the standard shell pattern `'\''`. Always quotes (even if the string
// "looks safe") so ergonomics are predictable.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tmuxImpl wires the runner-agnostic tmux logic.
type tmuxImpl struct{ r runner }

// NewLocal returns a Tmux that runs against the local tmux server.
func NewLocal(exec mpexec.Executor) Tmux {
	return &tmuxImpl{r: &localRunner{exec: exec}}
}

// NewLocalWithBinary lets tests override the tmux binary path.
func NewLocalWithBinary(exec mpexec.Executor, bin string) Tmux {
	return &tmuxImpl{r: &localRunner{exec: exec, bin: bin}}
}

// NewRemote returns a Tmux that runs against a remote host's tmux server,
// using the provided ssh.Runner.
func NewRemote(runner mpssh.Runner, host string) Tmux {
	return &tmuxImpl{r: &remoteRunner{ssh: runner, host: host}}
}

func (t *tmuxImpl) HasSession(ctx context.Context, name string) (bool, error) {
	if err := validateSessionName(name); err != nil {
		return false, err
	}
	_, _, code, err := t.r.Run(ctx, []string{"has-session", "-t", name}, nil)
	if err != nil {
		return false, fmt.Errorf("tmux has-session: %w", err)
	}
	// tmux returns 0 if the session exists, 1 if not (writing to stderr).
	if code == 0 {
		return true, nil
	}
	return false, nil
}

func (t *tmuxImpl) NewSession(ctx context.Context, name, cmd string, env map[string]string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}
	if cmd == "" {
		return errors.New("tmux: NewSession requires a non-empty command")
	}
	args := []string{"new-session", "-d", "-s", name}
	// Stable env-var order so command lines are reproducible (helps debugging
	// + makes tests deterministic).
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(args, cmd)
	_, stderr, code, err := t.r.Run(ctx, args, nil)
	if err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}
	if code != 0 {
		// "duplicate session" is tmux's error for re-creating an existing one.
		if strings.Contains(string(stderr), "duplicate session") {
			return ErrSessionExists
		}
		return fmt.Errorf("tmux new-session exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (t *tmuxImpl) SendKeys(ctx context.Context, target string, keys ...string) error {
	if target == "" {
		return errors.New("tmux: SendKeys requires a non-empty target")
	}
	if len(keys) == 0 {
		return errors.New("tmux: SendKeys requires at least one key")
	}
	args := []string{"send-keys", "-t", target}
	args = append(args, keys...)
	_, stderr, code, err := t.r.Run(ctx, args, nil)
	if err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("tmux send-keys exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (t *tmuxImpl) KillSession(ctx context.Context, name string) error {
	if err := validateSessionName(name); err != nil {
		return err
	}
	_, stderr, code, err := t.r.Run(ctx, []string{"kill-session", "-t", name}, nil)
	if err != nil {
		return fmt.Errorf("tmux kill-session: %w", err)
	}
	if code == 0 {
		return nil
	}
	stderrStr := string(stderr)
	if strings.Contains(stderrStr, "can't find session") || strings.Contains(stderrStr, "no such session") {
		return nil // idempotent
	}
	return fmt.Errorf("tmux kill-session exit %d: %s", code, strings.TrimSpace(stderrStr))
}

func (t *tmuxImpl) ListSessions(ctx context.Context) ([]string, error) {
	stdout, stderr, code, err := t.r.Run(ctx, []string{"list-sessions", "-F", "#S"}, nil)
	if err != nil {
		return nil, fmt.Errorf("tmux list-sessions: %w", err)
	}
	if code != 0 {
		// tmux exits non-zero with "no server running" when no sessions exist.
		// Treat that as the empty case rather than an error.
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "no server running") || strings.Contains(stderrStr, "no sessions") {
			return []string{}, nil
		}
		return nil, fmt.Errorf("tmux list-sessions exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	out := strings.TrimRight(string(stdout), "\n")
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(out, "\n"), nil
}

func (t *tmuxImpl) CapturePane(ctx context.Context, target string, lines int) (string, error) {
	if target == "" {
		return "", errors.New("tmux: CapturePane requires a non-empty target")
	}
	args := []string{"capture-pane", "-p", "-t", target}
	if lines > 0 {
		// `-S -N` starts the capture N lines back from the bottom.
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	stdout, stderr, code, err := t.r.Run(ctx, args, nil)
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("tmux capture-pane exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	return string(stdout), nil
}
