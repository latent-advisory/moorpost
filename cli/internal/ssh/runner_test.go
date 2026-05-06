package ssh

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
)

func TestRunnerRunBuildsArgs(t *testing.T) {
	fx := mpexec.NewFake()
	var capturedArgs []string
	fx.Expect(mpexec.FakeRun{
		Name:   "ssh",
		Stdout: []byte("ok"),
		OnRun: func([]byte) {
			// args validated below by inspecting the FakeExecutor's expected entry
		},
	})
	// We need a way to capture args. Since FakeExecutor.Expect accepts an
	// empty Args slice meaning "any args", we instead capture via a custom
	// runner test below.
	r := NewRunner(fx)
	stdout, _, code, err := r.Run(context.Background(), "webapp-vm", "echo hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d", code)
	}
	if string(stdout) != "ok" {
		t.Errorf("stdout = %q", stdout)
	}
	_ = capturedArgs
}

func TestRunnerArgsContainBatchModeAndHost(t *testing.T) {
	// Capture args via a custom Executor. FakeExecutor doesn't expose them
	// directly, so we use a one-off recorder.
	rec := &recorder{stdout: []byte("ok")}
	r := NewRunner(rec)
	if _, _, _, err := r.Run(context.Background(), "webapp-vm", "uptime"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.name != "ssh" {
		t.Errorf("binary = %q, want ssh", rec.name)
	}
	if !contains(rec.args, "webapp-vm") {
		t.Errorf("args missing host: %v", rec.args)
	}
	if !contains(rec.args, "BatchMode=yes") {
		t.Errorf("args missing BatchMode=yes: %v", rec.args)
	}
	if !contains(rec.args, "ConnectTimeout=15") {
		t.Errorf("args missing ConnectTimeout: %v", rec.args)
	}
	// Command terminator and command itself.
	if !contains(rec.args, "--") || !contains(rec.args, "uptime") {
		t.Errorf("command not appended: %v", rec.args)
	}
}

func TestRunnerExtraArgsHonored(t *testing.T) {
	rec := &recorder{stdout: []byte("ok")}
	r := NewRunnerWithOptions(rec, "/usr/bin/ssh", []string{"-F", "/tmp/x"})
	if _, _, _, err := r.Run(context.Background(), "h", "true"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.name != "/usr/bin/ssh" {
		t.Errorf("custom binary not used: %q", rec.name)
	}
	if !contains(rec.args, "-F") || !contains(rec.args, "/tmp/x") {
		t.Errorf("extra args missing: %v", rec.args)
	}
}

func TestRunnerRejectsEmptyHostAndCmd(t *testing.T) {
	r := NewRunner(mpexec.NewFake())
	if _, _, _, err := r.Run(context.Background(), "", "x"); err == nil {
		t.Error("Run accepted empty host")
	}
	if _, _, _, err := r.Run(context.Background(), "h", ""); err == nil {
		t.Error("Run accepted empty cmd")
	}
}

func TestRunnerWritesRemoteFile(t *testing.T) {
	rec := &recorder{}
	r := NewRunner(rec)
	content := []byte("CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-xyz\n")
	if err := r.WriteRemoteFile(context.Background(), "h", "/etc/moorpost/env", content, 0o600); err != nil {
		t.Fatalf("WriteRemoteFile: %v", err)
	}
	// Inspect the captured remote command.
	cmd := rec.lastCommand()
	for _, want := range []string{
		`mkdir -p`,
		`mktemp`,
		`chmod 600`,
		`mv`,
		`/etc/moorpost/env`,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("remote command missing %q:\n%s", want, cmd)
		}
	}
	if !bytes.Equal(rec.stdin, content) {
		t.Errorf("stdin = %q, want %q", rec.stdin, content)
	}
}

func TestWriteRemoteFileRejectsBadInput(t *testing.T) {
	r := NewRunner(&recorder{})
	if err := r.WriteRemoteFile(context.Background(), "", "/x", []byte{}, 0o600); err == nil {
		t.Error("accepted empty host")
	}
	if err := r.WriteRemoteFile(context.Background(), "h", "", []byte{}, 0o600); err == nil {
		t.Error("accepted empty path")
	}
	if err := r.WriteRemoteFile(context.Background(), "h", "/x'evil", []byte{}, 0o600); err == nil {
		t.Error("accepted path containing single quote")
	}
	if err := r.WriteRemoteFile(context.Background(), "h", "/x", []byte{}, 0); err == nil {
		t.Error("accepted mode 0")
	}
	if err := r.WriteRemoteFile(context.Background(), "h", "/x", []byte{}, 0o1000); err == nil {
		t.Error("accepted out-of-range mode")
	}
}

func TestRunnerPropagatesError(t *testing.T) {
	myErr := errors.New("ssh: connection refused")
	rec := &recorder{err: myErr}
	r := NewRunner(rec)
	_, _, _, err := r.Run(context.Background(), "h", "true")
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrapping %v", err, myErr)
	}
}

func TestWriteRemoteFileSurfacesNonZeroExit(t *testing.T) {
	rec := &recorder{exitCode: 2, stderr: []byte("permission denied")}
	r := NewRunner(rec)
	err := r.WriteRemoteFile(context.Background(), "h", "/x", []byte("c"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "exited 2") || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v, want exit-code error mentioning stderr", err)
	}
}

// recorder is a minimal Executor that captures one Run call.
type recorder struct {
	name     string
	args     []string
	stdin    []byte
	stdout   []byte
	stderr   []byte
	exitCode int
	err      error
}

func (r *recorder) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	r.name = name
	r.args = args
	r.stdin = stdin
	return r.stdout, r.stderr, r.exitCode, r.err
}

func (r *recorder) LookPath(name string) (string, error) {
	return "/usr/bin/" + name, nil
}

func (r *recorder) lastCommand() string {
	// The command is the last arg after "--".
	for i, a := range r.args {
		if a == "--" && i+1 < len(r.args) {
			return strings.Join(r.args[i+1:], " ")
		}
	}
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle || strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
