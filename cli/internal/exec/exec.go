// Package exec abstracts os/exec so callers can be tested without real
// subprocess execution.
//
// Real callers use New() to get a thin wrapper around os/exec. Tests use
// FakeExecutor (NewFake) to script command outputs deterministically.
package exec

import (
	"bytes"
	"context"
	"errors"
	stdexec "os/exec"
	"strings"
	"sync"
)

// Executor runs subprocess commands. Implementations must be safe for
// concurrent use by multiple goroutines.
type Executor interface {
	// Run executes name with args, optionally piping stdin, and returns the
	// captured stdout, stderr, exit code, and any execution error. A non-zero
	// exit code is NOT an error — the caller decides whether to treat it as one.
	Run(ctx context.Context, name string, args []string, stdin []byte) (stdout, stderr []byte, exitCode int, err error)

	// LookPath returns the resolved path of name on PATH, or os/exec's error.
	LookPath(name string) (string, error)
}

// New returns an Executor backed by os/exec.
func New() Executor { return &realExecutor{} }

type realExecutor struct{}

func (r *realExecutor) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := stdexec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *stdexec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
			err = nil // treat exit-code errors as non-error returns; caller decides
		}
	}
	return stdout.Bytes(), stderr.Bytes(), exitCode, err
}

func (r *realExecutor) LookPath(name string) (string, error) {
	return stdexec.LookPath(name)
}

// FakeRun is one entry in a FakeExecutor's scripted call sequence.
type FakeRun struct {
	// Match is checked against the actual command. If Name matches and (Args
	// are empty OR equal the actual args), this entry is consumed.
	Name string
	Args []string

	// Outputs to return.
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error

	// Optional callback for inspecting stdin / asserting per-call invariants.
	OnRun func(stdin []byte)
}

// FakeExecutor is a scripted Executor for tests. It expects calls in order;
// any unexpected call fails via the test hook configured on construction.
type FakeExecutor struct {
	mu        sync.Mutex
	runs      []FakeRun
	runsIndex int

	// LookPathOK lists the names that LookPath should succeed for.
	LookPathOK map[string]string

	// OnUnexpectedCall is invoked when no scripted run matches; tests typically
	// bind this to t.Fatalf so unmocked calls fail loudly.
	OnUnexpectedCall func(name string, args []string)
}

// NewFake returns a fresh FakeExecutor with no scripted calls.
func NewFake() *FakeExecutor {
	return &FakeExecutor{
		LookPathOK: map[string]string{},
	}
}

// Expect adds a scripted run to the queue. Calls are matched in order.
func (f *FakeExecutor) Expect(r FakeRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, r)
}

// AllowLookPath registers name as findable, returning the given path.
func (f *FakeExecutor) AllowLookPath(name, resolvedPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LookPathOK[name] = resolvedPath
}

// Remaining returns the number of scripted runs not yet consumed.
func (f *FakeExecutor) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs) - f.runsIndex
}

func (f *FakeExecutor) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runsIndex >= len(f.runs) {
		f.unexpected(name, args)
		return nil, nil, 0, errors.New("FakeExecutor: unexpected call to " + name)
	}
	want := f.runs[f.runsIndex]
	if want.Name != name {
		f.unexpected(name, args)
		return nil, nil, 0, errors.New("FakeExecutor: expected " + want.Name + " got " + name)
	}
	if len(want.Args) > 0 && !equalArgs(want.Args, args) {
		f.unexpected(name, args)
		return nil, nil, 0, errors.New("FakeExecutor: args mismatch for " + name + ": got " + strings.Join(args, " "))
	}
	f.runsIndex++
	if want.OnRun != nil {
		want.OnRun(stdin)
	}
	return want.Stdout, want.Stderr, want.ExitCode, want.Err
}

func (f *FakeExecutor) LookPath(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.LookPathOK[name]; ok {
		return p, nil
	}
	return "", &stdexec.Error{Name: name, Err: stdexec.ErrNotFound}
}

func (f *FakeExecutor) unexpected(name string, args []string) {
	if f.OnUnexpectedCall != nil {
		f.OnUnexpectedCall(name, args)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
