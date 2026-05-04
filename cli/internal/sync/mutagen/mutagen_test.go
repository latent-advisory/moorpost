package mutagen

import (
	"context"
	"errors"
	"strings"
	"testing"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
)

// captureExec records every Run call and returns canned outputs in sequence
// (same one for all calls if only one is queued). Lighter than FakeExecutor's
// strict scripting for tests that just want to inspect what was built.
type captureExec struct {
	calls []captureCall
	resp  []captureCall
	pos   int
}

type captureCall struct {
	name     string
	args     []string
	stdin    []byte
	stdout   []byte
	stderr   []byte
	exitCode int
	err      error
}

func (c *captureExec) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	c.calls = append(c.calls, captureCall{name: name, args: args, stdin: stdin})
	if len(c.resp) == 0 {
		return nil, nil, 0, nil
	}
	if c.pos < len(c.resp) {
		r := c.resp[c.pos]
		c.pos++
		return r.stdout, r.stderr, r.exitCode, r.err
	}
	r := c.resp[len(c.resp)-1]
	return r.stdout, r.stderr, r.exitCode, r.err
}

func (c *captureExec) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func newEngine(t *testing.T, c *captureExec) *engine {
	t.Helper()
	if c == nil {
		c = &captureExec{}
	}
	s, err := NewWithOptions(Options{Executor: c, Binary: "mutagen", RsyncBinary: "rsync"})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return s.(*engine)
}

func TestRegisteredInRegistry(t *testing.T) {
	ids := mpsync.List()
	found := false
	for _, id := range ids {
		if id == EngineID {
			found = true
		}
	}
	if !found {
		t.Errorf("mutagen not registered: List() = %v", ids)
	}
}

func TestStartSessionBuildsArgs(t *testing.T) {
	c := &captureExec{}
	e := newEngine(t, c)
	id, err := e.StartSession(context.Background(), mpsync.SyncSpec{
		Label: "argus-sync",
		Alpha: mpsync.Endpoint{Path: "/Users/x/argus"},
		Beta:  mpsync.Endpoint{SSHHost: "argus-vm", Path: "/home/x/argus"},
		ConflictPolicy: "alpha-wins",
		IgnorePatterns: []string{"**/node_modules", "**/.venv"},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if id != "argus-sync" {
		t.Errorf("session id = %q, want argus-sync", id)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(c.calls))
	}
	args := c.calls[0].args
	wantContains := [][2]string{
		{"sync", "subcommand"},
		{"create", "subcommand"},
		{"--name", "name flag"},
		{"argus-sync", "name value"},
		{"--mode", "mode flag"},
		{"two-way-resolved", "mapped mode"},
		{"--ignore", "ignore flag"},
		{"**/node_modules", "ignore pattern"},
		{"/Users/x/argus", "alpha local path"},
		{"argus-vm:/home/x/argus", "beta remote URL"},
	}
	for _, w := range wantContains {
		if !argsContain(args, w[0]) {
			t.Errorf("args missing %s (%s):\n%v", w[1], w[0], args)
		}
	}
}

func TestStartSessionRequiresLabel(t *testing.T) {
	e := newEngine(t, nil)
	_, err := e.StartSession(context.Background(), mpsync.SyncSpec{
		Alpha: mpsync.Endpoint{Path: "/a"},
		Beta:  mpsync.Endpoint{SSHHost: "h", Path: "/b"},
	})
	if err == nil {
		t.Error("StartSession accepted empty label")
	}
}

func TestStartSessionAlreadyExistsIdempotent(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("session with name 'x' already exists")}}}
	e := newEngine(t, c)
	id, err := e.StartSession(context.Background(), mpsync.SyncSpec{
		Label: "x",
		Alpha: mpsync.Endpoint{Path: "/a"},
		Beta:  mpsync.Endpoint{SSHHost: "h", Path: "/b"},
	})
	if err != nil {
		t.Fatalf("StartSession returned err: %v", err)
	}
	if id != "x" {
		t.Errorf("id = %q, want x", id)
	}
}

func TestStartSessionRejectsBadMode(t *testing.T) {
	e := newEngine(t, nil)
	_, err := e.StartSession(context.Background(), mpsync.SyncSpec{
		Label:          "x",
		Alpha:          mpsync.Endpoint{Path: "/a"},
		Beta:           mpsync.Endpoint{Path: "/b"},
		ConflictPolicy: "smashparty",
	})
	if err == nil {
		t.Error("accepted bogus conflict policy")
	}
}

func TestModeMapping(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"alpha-wins", "two-way-resolved"},
		{"two-way-resolved", "two-way-resolved"},
		{"manual", "two-way-safe"},
		{"", "two-way-resolved"}, // default
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := mapMode(tc.in)
			if err != nil {
				t.Fatalf("mapMode(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("mapMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPauseResumeStop(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0}}}
	e := newEngine(t, c)
	if err := e.Pause(context.Background(), "x"); err != nil {
		t.Errorf("Pause: %v", err)
	}
	if err := e.Resume(context.Background(), "x"); err != nil {
		t.Errorf("Resume: %v", err)
	}
	if err := e.Stop(context.Background(), "x"); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if len(c.calls) != 3 {
		t.Errorf("expected 3 calls, got %d", len(c.calls))
	}
}

func TestPauseRequiresID(t *testing.T) {
	e := newEngine(t, nil)
	if err := e.Pause(context.Background(), ""); err == nil {
		t.Error("Pause accepted empty id")
	}
	if err := e.Resume(context.Background(), ""); err == nil {
		t.Error("Resume accepted empty id")
	}
	if err := e.Stop(context.Background(), ""); err == nil {
		t.Error("Stop accepted empty id")
	}
}

func TestPauseUnknownSessionMapsToErr(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("no matching sessions")}}}
	e := newEngine(t, c)
	if err := e.Pause(context.Background(), "x"); !errors.Is(err, mpsync.ErrSessionNotFound) {
		t.Errorf("Pause unknown = %v, want ErrSessionNotFound", err)
	}
}

func TestStopIdempotentOnMissing(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("no matching sessions")}}}
	e := newEngine(t, c)
	if err := e.Stop(context.Background(), "x"); err != nil {
		t.Errorf("Stop on missing returned %v, want nil (idempotent)", err)
	}
}

func TestStatusParsesOutput(t *testing.T) {
	tests := []struct {
		stdout    string
		wantState mpsync.SyncState
		wantConfl int
	}{
		{"Watching for changes|0|/a|/b", mpsync.SyncStateWatching, 0},
		{"Connecting (alpha)|0|/a|/b", mpsync.SyncStateConnecting, 0},
		{"Scanning|0|/a|/b", mpsync.SyncStateScanning, 0},
		{"Paused|0|/a|/b", mpsync.SyncStatePaused, 0},
		{"Halted on root deletion|0|/a|/b", mpsync.SyncStateError, 0},
		{"Watching for changes|3|/a|/b", mpsync.SyncStateConflicted, 3}, // conflicts override state
	}
	for _, tc := range tests {
		t.Run(tc.stdout, func(t *testing.T) {
			c := &captureExec{resp: []captureCall{{exitCode: 0, stdout: []byte(tc.stdout)}}}
			e := newEngine(t, c)
			s, err := e.Status(context.Background(), "x")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if s.State != tc.wantState {
				t.Errorf("State = %q, want %q", s.State, tc.wantState)
			}
			if s.Conflicts != tc.wantConfl {
				t.Errorf("Conflicts = %d, want %d", s.Conflicts, tc.wantConfl)
			}
		})
	}
}

func TestStatusUnknownSession(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("no matching sessions found")}}}
	e := newEngine(t, c)
	s, err := e.Status(context.Background(), "x")
	if !errors.Is(err, mpsync.ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
	if s.State != mpsync.SyncStateUnknown {
		t.Errorf("state = %q, want unknown", s.State)
	}
}

func TestStatusRequiresID(t *testing.T) {
	e := newEngine(t, nil)
	if _, err := e.Status(context.Background(), ""); err == nil {
		t.Error("Status accepted empty id")
	}
}

func TestOneShotInvalidDirection(t *testing.T) {
	e := newEngine(t, nil)
	err := e.OneShot(context.Background(),
		mpsync.Endpoint{Path: "/a"}, mpsync.Endpoint{Path: "/b"},
		mpsync.DirectionBidirectional)
	if !errors.Is(err, mpsync.ErrInvalidDirection) {
		t.Errorf("err = %v, want ErrInvalidDirection", err)
	}
}

func TestOneShotLocalToRemote(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0}}}
	e := newEngine(t, c)
	err := e.OneShot(context.Background(),
		mpsync.Endpoint{Path: "/local/state/"},
		mpsync.Endpoint{SSHHost: "argus-vm", Path: "/remote/state/"},
		mpsync.DirectionLocalToRemote)
	if err != nil {
		t.Fatalf("OneShot: %v", err)
	}
	args := c.calls[0].args
	if !argsContain(args, "-a") {
		t.Errorf("args missing -a: %v", args)
	}
	if !argsContain(args, "--delete") {
		t.Errorf("args missing --delete: %v", args)
	}
	if !argsContain(args, "/local/state/") {
		t.Errorf("args missing src: %v", args)
	}
	if !argsContain(args, "argus-vm:/remote/state/") {
		t.Errorf("args missing remote dst: %v", args)
	}
	// -e flag for SSH should be present when at least one side is remote.
	hasE := false
	for i, a := range args {
		if a == "-e" && i+1 < len(args) && strings.Contains(args[i+1], "ssh") {
			hasE = true
			break
		}
	}
	if !hasE {
		t.Errorf("args missing -e ssh: %v", args)
	}
}

func TestOneShotRemoteToLocal(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0}}}
	e := newEngine(t, c)
	err := e.OneShot(context.Background(),
		mpsync.Endpoint{SSHHost: "argus-vm", Path: "/remote/state/"},
		mpsync.Endpoint{Path: "/local/state/"},
		mpsync.DirectionRemoteToLocal)
	if err != nil {
		t.Fatalf("OneShot: %v", err)
	}
	args := c.calls[0].args
	if !argsContain(args, "argus-vm:/remote/state/") {
		t.Errorf("args missing remote src: %v", args)
	}
	if !argsContain(args, "/local/state/") {
		t.Errorf("args missing local dst: %v", args)
	}
}

func TestOneShotNonZeroExit(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 23, stderr: []byte("rsync error: ...")}}}
	e := newEngine(t, c)
	err := e.OneShot(context.Background(),
		mpsync.Endpoint{Path: "/a/"}, mpsync.Endpoint{Path: "/b/"},
		mpsync.DirectionLocalToRemote)
	if err == nil || !strings.Contains(err.Error(), "exit 23") {
		t.Errorf("err = %v, want exit-code error", err)
	}
}

func TestRenderEndpoint(t *testing.T) {
	tests := []struct {
		name string
		ep   mpsync.Endpoint
		want string
		err  bool
	}{
		{"local", mpsync.Endpoint{Path: "/x"}, "/x", false},
		{"remote", mpsync.Endpoint{SSHHost: "h", Path: "/x"}, "h:/x", false},
		{"empty path", mpsync.Endpoint{}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderEndpoint(tc.ep)
			if (err != nil) != tc.err {
				t.Errorf("err = %v, want err=%v", err, tc.err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecError(t *testing.T) {
	myErr := errors.New("mutagen daemon down")
	c := &captureExec{resp: []captureCall{{err: myErr}}}
	e := newEngine(t, c)
	_, err := e.StartSession(context.Background(), mpsync.SyncSpec{
		Label: "x",
		Alpha: mpsync.Endpoint{Path: "/a"},
		Beta:  mpsync.Endpoint{Path: "/b"},
	})
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrapping %v", err, myErr)
	}
}

// silence unused-import check
var _ = mpexec.New

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
