package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	mpssh "github.com/latent-advisory/moorpost/cli/internal/ssh"
)

// recorder captures the args, stdin, and returns canned output so tests can
// inspect what the tmux package built and feed it canned outputs back.
type recorder struct {
	calls []recorderCall
	resp  recorderCall // canned response for the next call
}

type recorderCall struct {
	name     string
	args     []string
	stdin    []byte
	stdout   []byte
	stderr   []byte
	exitCode int
	err      error
}

func (r *recorder) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	r.calls = append(r.calls, recorderCall{name: name, args: args, stdin: stdin})
	return r.resp.stdout, r.resp.stderr, r.resp.exitCode, r.resp.err
}

func (r *recorder) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func (r *recorder) lastArgs() []string {
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1].args
}

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestHasSessionTrue(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 0}}
	tx := NewLocal(r)
	ok, err := tx.HasSession(context.Background(), "webapp")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !ok {
		t.Error("expected ok=true")
	}
	if !argsContain(r.lastArgs(), "has-session") {
		t.Errorf("args missing has-session: %v", r.lastArgs())
	}
	if !argsContain(r.lastArgs(), "webapp") {
		t.Errorf("args missing target: %v", r.lastArgs())
	}
}

func TestHasSessionFalse(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 1, stderr: []byte("can't find session")}}
	tx := NewLocal(r)
	ok, err := tx.HasSession(context.Background(), "webapp")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if ok {
		t.Error("expected ok=false")
	}
}

func TestNewSessionEnvSorted(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 0}}
	tx := NewLocal(r)
	err := tx.NewSession(context.Background(), "webapp", "claude", map[string]string{
		"FOO":                       "1",
		"CLAUDE_CODE_OAUTH_TOKEN":   "tok",
		"BAR":                       "2",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	args := r.lastArgs()
	// Collect the -e VAR=VAL pairs in order.
	var envPairs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-e" && i+1 < len(args) {
			envPairs = append(envPairs, args[i+1])
		}
	}
	want := []string{"BAR=2", "CLAUDE_CODE_OAUTH_TOKEN=tok", "FOO=1"}
	if len(envPairs) != len(want) {
		t.Fatalf("env pairs = %v, want %v", envPairs, want)
	}
	for i := range want {
		if envPairs[i] != want[i] {
			t.Errorf("env[%d] = %q, want %q (sort order)", i, envPairs[i], want[i])
		}
	}
	if !argsContain(args, "-d") {
		t.Errorf("args missing -d (detached): %v", args)
	}
	if !argsContain(args, "webapp") {
		t.Errorf("args missing session name: %v", args)
	}
	if args[len(args)-1] != "claude" {
		t.Errorf("command should be last arg, got %q", args[len(args)-1])
	}
}

func TestNewSessionDuplicateMaps(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 1, stderr: []byte("duplicate session: webapp")}}
	tx := NewLocal(r)
	err := tx.NewSession(context.Background(), "webapp", "claude", nil)
	if !errors.Is(err, ErrSessionExists) {
		t.Errorf("err = %v, want ErrSessionExists", err)
	}
}

func TestNewSessionRequiresCmd(t *testing.T) {
	tx := NewLocal(&recorder{})
	if err := tx.NewSession(context.Background(), "webapp", "", nil); err == nil {
		t.Error("accepted empty cmd")
	}
}

func TestSendKeys(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 0}}
	tx := NewLocal(r)
	if err := tx.SendKeys(context.Background(), "webapp:0", "claude --resume abc", "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	args := r.lastArgs()
	if !argsContain(args, "send-keys") {
		t.Errorf("args missing send-keys: %v", args)
	}
	if !argsContain(args, "webapp:0") {
		t.Errorf("args missing target: %v", args)
	}
	if !argsContain(args, "Enter") {
		t.Errorf("args missing Enter key: %v", args)
	}
	if !argsContain(args, "claude --resume abc") {
		t.Errorf("args missing keys text: %v", args)
	}
}

func TestSendKeysRequiresInputs(t *testing.T) {
	tx := NewLocal(&recorder{})
	if err := tx.SendKeys(context.Background(), "", "x"); err == nil {
		t.Error("accepted empty target")
	}
	if err := tx.SendKeys(context.Background(), "webapp"); err == nil {
		t.Error("accepted no keys")
	}
}

func TestKillSessionIdempotent(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 1, stderr: []byte("can't find session: webapp")}}
	tx := NewLocal(r)
	if err := tx.KillSession(context.Background(), "webapp"); err != nil {
		t.Errorf("KillSession on missing returned %v, want nil", err)
	}
}

func TestKillSessionRealError(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 2, stderr: []byte("permission denied")}}
	tx := NewLocal(r)
	if err := tx.KillSession(context.Background(), "webapp"); err == nil {
		t.Error("KillSession swallowed real error")
	}
}

func TestListSessionsEmpty(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 1, stderr: []byte("no server running on /tmp/tmux-...")}}
	tx := NewLocal(r)
	got, err := tx.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestListSessionsParses(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 0, stdout: []byte("webapp\nworkbench\n")}}
	tx := NewLocal(r)
	got, err := tx.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	want := []string{"webapp", "workbench"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestListSessionsZeroLines(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 0, stdout: []byte{}}}
	tx := NewLocal(r)
	got, err := tx.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if got == nil {
		t.Error("got nil, want empty slice")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestCapturePaneArgs(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 0, stdout: []byte("> ready\n")}}
	tx := NewLocal(r)
	out, err := tx.CapturePane(context.Background(), "webapp:0.0", 50)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if out != "> ready\n" {
		t.Errorf("out = %q", out)
	}
	args := r.lastArgs()
	if !argsContain(args, "capture-pane") || !argsContain(args, "-p") {
		t.Errorf("missing capture-pane/-p: %v", args)
	}
	if !argsContain(args, "-50") {
		t.Errorf("missing -S -50 lines arg: %v", args)
	}
}

func TestCapturePaneNoLines(t *testing.T) {
	r := &recorder{resp: recorderCall{exitCode: 0, stdout: []byte("ok")}}
	tx := NewLocal(r)
	if _, err := tx.CapturePane(context.Background(), "x", 0); err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	args := r.lastArgs()
	for _, a := range args {
		if a == "-S" {
			t.Errorf("unexpected -S in args: %v", args)
		}
	}
}

func TestSessionNameValidation(t *testing.T) {
	tx := NewLocal(&recorder{})
	for _, name := range []string{"", "ar:gus", "ar.gus", "ar gus", "ar\tgus", "ar\ngus"} {
		t.Run("name="+name, func(t *testing.T) {
			if _, err := tx.HasSession(context.Background(), name); err == nil {
				t.Errorf("HasSession accepted invalid name %q", name)
			}
			if err := tx.NewSession(context.Background(), name, "x", nil); err == nil {
				t.Errorf("NewSession accepted invalid name %q", name)
			}
			if err := tx.KillSession(context.Background(), name); err == nil {
				t.Errorf("KillSession accepted invalid name %q", name)
			}
		})
	}
}

func TestRemoteWithFakeSSH(t *testing.T) {
	// We need an ssh.Runner. Use the real-package interface but with a
	// recorder Executor underneath via NewRunner. The remote runner builds a
	// shell command of the form `tmux <quoted args>`.
	captured := &recorder{resp: recorderCall{exitCode: 0, stdout: []byte("webapp\n")}}
	// Use the real ssh.NewRunner with our recorder Executor.
	tx := NewRemote(mpssh.NewRunner(captured), "host")
	got, err := tx.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 1 || got[0] != "webapp" {
		t.Errorf("got %v", got)
	}
	// The recorded call should have name "ssh" and args ending in "tmux 'list-sessions' '-F' '#S'"
	last := captured.calls[len(captured.calls)-1]
	if last.name != "ssh" {
		t.Errorf("expected ssh, got %q", last.name)
	}
	cmdArg := ""
	for i := 0; i < len(last.args); i++ {
		if last.args[i] == "--" && i+1 < len(last.args) {
			cmdArg = last.args[i+1]
			break
		}
	}
	if cmdArg == "" {
		t.Fatalf("could not find command in ssh args: %v", last.args)
	}
	if !strings.HasPrefix(cmdArg, "tmux ") {
		t.Errorf("command does not start with tmux: %q", cmdArg)
	}
	if !strings.Contains(cmdArg, "list-sessions") || !strings.Contains(cmdArg, "#S") {
		t.Errorf("command missing tmux args: %q", cmdArg)
	}
}

func TestShellQuoteHandlesSingleQuote(t *testing.T) {
	got := shellQuote(`it's`)
	want := `'it'\''s'`
	if got != want {
		t.Errorf("shellQuote(it's) = %q, want %q", got, want)
	}
}

func TestNewSessionPropagatesUnderlyingError(t *testing.T) {
	r := &recorder{resp: recorderCall{err: errors.New("exec failure")}}
	tx := NewLocal(r)
	if err := tx.NewSession(context.Background(), "webapp", "claude", nil); err == nil || !strings.Contains(err.Error(), "exec failure") {
		t.Errorf("err = %v, want underlying", err)
	}
}

// Compile-time assertion that *tmuxImpl satisfies Tmux.
var _ Tmux = (*tmuxImpl)(nil)

// satisfy mpexec import in case tests reference it indirectly
var _ = mpexec.New
