package exec

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestRealExecutorEcho(t *testing.T) {
	e := New()
	stdout, _, code, err := e.Run(context.Background(), "echo", []string{"hello"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !bytes.Contains(stdout, []byte("hello")) {
		t.Errorf("stdout = %q, want to contain 'hello'", stdout)
	}
}

func TestRealExecutorNonZeroExit(t *testing.T) {
	e := New()
	_, _, code, err := e.Run(context.Background(), "false", nil, nil)
	if err != nil {
		t.Errorf("Run returned err for non-zero exit: %v (should be nil; exit code is %d)", err, code)
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestRealExecutorMissingBinary(t *testing.T) {
	e := New()
	_, _, _, err := e.Run(context.Background(), "definitely-not-a-real-binary-xyz", nil, nil)
	if err == nil {
		t.Error("Run did not return error for missing binary")
	}
}

func TestRealExecutorContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := New()
	_, _, _, err := e.Run(ctx, "sleep", []string{"5"}, nil)
	if err == nil {
		t.Error("Run did not honor canceled context")
	}
}

func TestRealExecutorStdin(t *testing.T) {
	e := New()
	stdout, _, code, err := e.Run(context.Background(), "cat", nil, []byte("piped-input"))
	if err != nil || code != 0 {
		t.Fatalf("Run cat: err=%v code=%d", err, code)
	}
	if string(stdout) != "piped-input" {
		t.Errorf("stdout = %q, want piped-input", stdout)
	}
}

func TestRealExecutorLookPath(t *testing.T) {
	e := New()
	if _, err := e.LookPath("sh"); err != nil {
		t.Errorf("LookPath(sh) = %v, want non-nil resolution", err)
	}
	if _, err := e.LookPath("definitely-not-real-xyz"); err == nil {
		t.Error("LookPath did not error on missing binary")
	}
}

func TestFakeExecutorScriptedHappyPath(t *testing.T) {
	f := NewFake()
	f.Expect(FakeRun{Name: "claude", Args: []string{"--version"}, Stdout: []byte("2.0.0\n")})
	f.AllowLookPath("claude", "/usr/local/bin/claude")

	if _, err := f.LookPath("claude"); err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	stdout, _, code, err := f.Run(context.Background(), "claude", []string{"--version"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d", code)
	}
	if string(stdout) != "2.0.0\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if f.Remaining() != 0 {
		t.Errorf("Remaining() = %d, want 0", f.Remaining())
	}
}

func TestFakeExecutorUnexpectedCallRejected(t *testing.T) {
	f := NewFake()
	called := false
	f.OnUnexpectedCall = func(name string, args []string) { called = true }
	_, _, _, err := f.Run(context.Background(), "claude", nil, nil)
	if err == nil {
		t.Error("expected error for unexpected call")
	}
	if !called {
		t.Error("OnUnexpectedCall was not invoked")
	}
}

func TestFakeExecutorArgsMismatchFails(t *testing.T) {
	f := NewFake()
	f.Expect(FakeRun{Name: "claude", Args: []string{"setup-token"}, Stdout: []byte("ok")})
	failed := false
	f.OnUnexpectedCall = func(string, []string) { failed = true }
	_, _, _, err := f.Run(context.Background(), "claude", []string{"different-arg"}, nil)
	if err == nil {
		t.Error("expected error on args mismatch")
	}
	if !failed {
		t.Error("OnUnexpectedCall was not invoked on args mismatch")
	}
}

func TestFakeExecutorIgnoresArgsWhenWantArgsEmpty(t *testing.T) {
	f := NewFake()
	f.Expect(FakeRun{Name: "tail", Stdout: []byte("ok")})
	stdout, _, _, err := f.Run(context.Background(), "tail", []string{"-f", "anything"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(stdout) != "ok" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestFakeExecutorOnRunCallback(t *testing.T) {
	f := NewFake()
	var captured []byte
	f.Expect(FakeRun{
		Name:   "tee",
		Stdout: []byte("done"),
		OnRun:  func(stdin []byte) { captured = stdin },
	})
	if _, _, _, err := f.Run(context.Background(), "tee", nil, []byte("piped")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(captured) != "piped" {
		t.Errorf("captured stdin = %q", captured)
	}
}

func TestFakeExecutorPropagatesErr(t *testing.T) {
	myErr := errors.New("boom")
	f := NewFake()
	f.Expect(FakeRun{Name: "x", Err: myErr})
	_, _, _, err := f.Run(context.Background(), "x", nil, nil)
	if !errors.Is(err, myErr) {
		t.Errorf("Run err = %v, want %v", err, myErr)
	}
}

func TestFakeExecutorPropagatesExitCode(t *testing.T) {
	f := NewFake()
	f.Expect(FakeRun{Name: "x", ExitCode: 42, Stderr: []byte("oops")})
	stdout, stderr, code, err := f.Run(context.Background(), "x", nil, nil)
	if err != nil {
		t.Errorf("err = %v", err)
	}
	if code != 42 {
		t.Errorf("code = %d", code)
	}
	if string(stderr) != "oops" {
		t.Errorf("stderr = %q", stderr)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestFakeExecutorLookPathMissingReturnsExecError(t *testing.T) {
	f := NewFake()
	_, err := f.LookPath("missing")
	if err == nil {
		t.Fatal("LookPath did not error on missing")
	}
	var ee *exec.Error
	if !errors.As(err, &ee) {
		t.Errorf("LookPath err type = %T, want *exec.Error", err)
	}
}
