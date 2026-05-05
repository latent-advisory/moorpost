package claudecode

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	"github.com/latent-advisory/moorpost/cli/internal/keychain"
	"github.com/latent-advisory/moorpost/cli/internal/tmux"
)

// newAgent returns a claudeCode with a FakeExecutor + file-backed keychain
// rooted in t.TempDir(). Used by all tests.
func newAgent(t *testing.T) (*claudeCode, *mpexec.FakeExecutor) {
	t.Helper()
	kc, err := keychain.NewFile(t.TempDir())
	if err != nil {
		t.Fatalf("keychain: %v", err)
	}
	fx := mpexec.NewFake()
	fx.OnUnexpectedCall = func(name string, args []string) {
		t.Errorf("FakeExecutor: unexpected call to %s %v", name, args)
	}
	a, err := NewWithOptions(Options{
		Executor: fx,
		Keychain: kc,
		HomeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return a.(*claudeCode), fx
}

func TestAgentRegistered(t *testing.T) {
	// init() should have registered the constructor before any test runs.
	regs := agent.List()
	found := false
	for _, id := range regs {
		if id == AgentID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("agent not registered: agent.List() = %v", regs)
	}
}

func TestNewFromRegistry(t *testing.T) {
	// The default New() goes through OS-native keychain, which we can't safely
	// poke in tests. Verify it's at least callable; if it errors that's
	// acceptable in environments without a keychain.
	a, err := agent.Get(AgentID, nil)
	if err != nil {
		t.Skipf("agent.Get(claude-code) = %v (acceptable on CI without keychain)", err)
	}
	if a.ID() != AgentID {
		t.Errorf("ID() = %q, want %q", a.ID(), AgentID)
	}
}

func TestInstallScriptKnownOS(t *testing.T) {
	a, _ := newAgent(t)
	for _, os := range []agent.OSFamily{agent.OSUbuntu, agent.OSDebian, agent.OSAmazonLinux} {
		t.Run(string(os), func(t *testing.T) {
			s := a.InstallScript(os)
			if s == "" {
				t.Errorf("InstallScript(%s) returned empty", os)
			}
			if !strings.Contains(s, "@anthropic-ai/claude-code") {
				t.Errorf("InstallScript(%s) missing package name", os)
			}
			if !strings.Contains(s, "command -v claude") {
				t.Errorf("InstallScript(%s) missing idempotency check", os)
			}
		})
	}
}

func TestInstallScriptUnknownOS(t *testing.T) {
	a, _ := newAgent(t)
	s := a.InstallScript(agent.OSFamily("freebsd"))
	if !strings.Contains(s, "exit 1") {
		t.Errorf("InstallScript(unknown) should fail-fast; got %q", s)
	}
}

func TestSessionStatePath(t *testing.T) {
	tests := []struct {
		name string
		abs  string
		want string // tail after <home>/.claude/projects/
	}{
		{"empty", "", ""},
		{"argus", "/Users/landytang/argus", "-Users-landytang-argus"},
		{"AI M&A", "/Users/landytang/Documents/Claude/Projects/AI M&A", "-Users-landytang-Documents-Claude-Projects-AI-M-A"},
		{"hidden subdir", "/path/.claude/x", "-path--claude-x"},
		{"existing dashes preserved", "/claw-playground/groovy", "-claw-playground-groovy"},
		{"unicode replaced", "/path/résumé", "-path-r-sum-"},
		{"trailing slash", "/foo/bar/", "-foo-bar-"},
		{"spaces and dots", "/My Project v1.2", "-My-Project-v1-2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAgent(t)
			got := a.SessionStatePath(tc.abs)
			if tc.abs == "" {
				if got != "" {
					t.Errorf("SessionStatePath('') = %q, want ''", got)
				}
				return
			}
			wantFull := filepath.Join(a.home, ".claude", "projects", tc.want)
			if got != wantFull {
				t.Errorf("SessionStatePath(%q) = %q, want %q", tc.abs, got, wantFull)
			}
		})
	}
}

func TestAuthenticateLocalHappyPath(t *testing.T) {
	a, fx := newAgent(t)
	fx.AllowLookPath("claude", "/usr/local/bin/claude")
	fx.Expect(mpexec.FakeRun{
		Name:   "claude",
		Args:   []string{"setup-token"},
		Stdout: []byte("Generating token...\nsk-ant-oat01-AbCdEfGhIjKlMnOpQrStUv0123456789-_xyz\nDone.\n"),
	})

	cred, err := a.AuthenticateLocal(context.Background())
	if err != nil {
		t.Fatalf("AuthenticateLocal: %v", err)
	}
	if cred.EnvVar != CredentialEnvVar {
		t.Errorf("EnvVar = %q, want %q", cred.EnvVar, CredentialEnvVar)
	}
	if !strings.HasPrefix(cred.Value, "sk-ant-oat01-") {
		t.Errorf("Value = %q, want sk-ant-oat01- prefix", cred.Value)
	}
	if cred.Kind != "oauth-subscription" {
		t.Errorf("Kind = %q", cred.Kind)
	}
	// Token should now be cached.
	cached, err := a.kc.Retrieve(KeychainService, KeychainAccount)
	if err != nil {
		t.Errorf("token not cached in keychain: %v", err)
	}
	if string(cached) != cred.Value {
		t.Errorf("cached token mismatch: %q vs %q", cached, cred.Value)
	}
}

func TestAuthenticateLocalCachedTokenSkipsExec(t *testing.T) {
	a, fx := newAgent(t)
	// Pre-populate the keychain.
	if err := a.kc.Store(KeychainService, KeychainAccount, []byte("sk-ant-oat01-cached")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Don't allow LookPath or scripted runs — if AuthenticateLocal shells out,
	// the FakeExecutor's OnUnexpectedCall will fail the test.
	cred, err := a.AuthenticateLocal(context.Background())
	if err != nil {
		t.Fatalf("AuthenticateLocal: %v", err)
	}
	if cred.Value != "sk-ant-oat01-cached" {
		t.Errorf("Value = %q, want sk-ant-oat01-cached", cred.Value)
	}
	if fx.Remaining() != 0 {
		t.Errorf("scripted runs left: %d (cached path should not consume any)", fx.Remaining())
	}
}

func TestAuthenticateLocalClaudeNotInstalled(t *testing.T) {
	a, fx := newAgent(t)
	// LookPath will fail (no entry registered).
	_, err := a.AuthenticateLocal(context.Background())
	if !errors.Is(err, agent.ErrAgentNotInstalled) {
		t.Errorf("err = %v, want ErrAgentNotInstalled", err)
	}
	_ = fx
}

func TestAuthenticateLocalSetupTokenNonZero(t *testing.T) {
	a, fx := newAgent(t)
	fx.AllowLookPath("claude", "/usr/local/bin/claude")
	fx.Expect(mpexec.FakeRun{
		Name:     "claude",
		Args:     []string{"setup-token"},
		Stderr:   []byte("Error: not logged in"),
		ExitCode: 1,
	})
	_, err := a.AuthenticateLocal(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited 1") {
		t.Errorf("err = %v, want exit-code error", err)
	}
}

func TestAuthenticateLocalNoTokenInOutput(t *testing.T) {
	a, fx := newAgent(t)
	fx.AllowLookPath("claude", "/usr/local/bin/claude")
	fx.Expect(mpexec.FakeRun{
		Name:   "claude",
		Args:   []string{"setup-token"},
		Stdout: []byte("Setting up...\nDone.\n"),
	})
	_, err := a.AuthenticateLocal(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no sk-ant-oat01") {
		t.Errorf("err = %v, want no-token error", err)
	}
}

func TestParseOAuthTokenStripsWhitespace(t *testing.T) {
	out := "  prefix \nsk-ant-oat01-cleanvalue123  \n suffix"
	tok, err := parseOAuthToken(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tok != "sk-ant-oat01-cleanvalue123" {
		t.Errorf("got %q", tok)
	}
}

func TestParseOAuthTokenPicksFirstValid(t *testing.T) {
	// The regex matches any sk-ant-<subprefix>-<body> token (oat01, api03,
	// future variants). Multiple tokens in one output: parser returns the
	// first match in stream order. Real `claude setup-token` only ever
	// prints one; this case is purely defensive.
	out := "noise\nsk-ant-oat01-real-token\nsk-ant-api03-also-valid\nmore-noise"
	tok, err := parseOAuthToken(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tok != "sk-ant-oat01-real-token" {
		t.Errorf("got %q, want sk-ant-oat01-real-token", tok)
	}
}

// fakeTmux is a controllable Tmux for testing the four new methods.
type fakeTmux struct {
	mu             sync.Mutex
	exists         bool
	newSessionErr  error
	newSessionLog  []newSessionCall
	captureSeq     []string // returned in order; last value sticks
	capturePos     int
	captureErr     error
	hasSessionErr  error
	captureCalls   int
}

type newSessionCall struct {
	name, cmd string
	env       map[string]string
}

func (f *fakeTmux) HasSession(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hasSessionErr != nil {
		return false, f.hasSessionErr
	}
	return f.exists, nil
}

func (f *fakeTmux) NewSession(_ context.Context, name, cmd string, env map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newSessionLog = append(f.newSessionLog, newSessionCall{name: name, cmd: cmd, env: env})
	if f.newSessionErr != nil {
		return f.newSessionErr
	}
	f.exists = true
	return nil
}

func (f *fakeTmux) SendKeys(context.Context, string, ...string) error    { return nil }
func (f *fakeTmux) KillSession(context.Context, string) error             { return nil }
func (f *fakeTmux) ListSessions(context.Context) ([]string, error)        { return nil, nil }

func (f *fakeTmux) CapturePane(_ context.Context, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureCalls++
	if f.captureErr != nil {
		return "", f.captureErr
	}
	if len(f.captureSeq) == 0 {
		return "", nil
	}
	if f.capturePos < len(f.captureSeq) {
		v := f.captureSeq[f.capturePos]
		f.capturePos++
		return v, nil
	}
	return f.captureSeq[len(f.captureSeq)-1], nil
}

// fakeSSHRunner records WriteRemoteFile invocations for InjectCredential tests.
type fakeSSHRunner struct {
	mu    sync.Mutex
	wrote []writeCall
	err   error
}

type writeCall struct {
	host, path string
	content    []byte
	mode       int
}

func (r *fakeSSHRunner) Run(_ context.Context, _, _ string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}
func (r *fakeSSHRunner) RunWithStdin(_ context.Context, _, _ string, _ []byte) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}
func (r *fakeSSHRunner) WriteRemoteFile(_ context.Context, host, path string, content []byte, mode int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wrote = append(r.wrote, writeCall{host: host, path: path, content: append([]byte(nil), content...), mode: mode})
	return r.err
}

func newAgentWithTmux(t *testing.T, tx tmux.Tmux, ssh *fakeSSHRunner) *claudeCode {
	t.Helper()
	kc, err := keychain.NewFile(t.TempDir())
	if err != nil {
		t.Fatalf("keychain: %v", err)
	}
	fx := mpexec.NewFake()
	a, err := NewWithOptions(Options{
		Executor:  fx,
		Keychain:  kc,
		SSHRunner: ssh,
		HomeDir:   t.TempDir(),
		TmuxFactory: func(string) tmux.Tmux {
			return tx
		},
		PausePollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return a.(*claudeCode)
}

func TestInjectCredentialHappyPath(t *testing.T) {
	r := &fakeSSHRunner{}
	a := newAgentWithTmux(t, &fakeTmux{}, r)
	cred := agent.Credential{EnvVar: CredentialEnvVar, Value: "sk-ant-oat01-xyz"}
	if err := a.InjectCredential(context.Background(), agent.SSHTarget{Host: "vm"}, cred); err != nil {
		t.Fatalf("InjectCredential: %v", err)
	}
	if len(r.wrote) != 1 {
		t.Fatalf("expected 1 write, got %d", len(r.wrote))
	}
	w := r.wrote[0]
	if w.host != "vm" {
		t.Errorf("host = %q", w.host)
	}
	if w.path != DefaultRemoteEnvPath {
		t.Errorf("path = %q, want %s", w.path, DefaultRemoteEnvPath)
	}
	if w.mode != 0o600 {
		t.Errorf("mode = %o, want 0600", w.mode)
	}
	want := "CLAUDE_CODE_OAUTH_TOKEN='sk-ant-oat01-xyz'\n"
	if string(w.content) != want {
		t.Errorf("content = %q, want %q", w.content, want)
	}
}

func TestInjectCredentialRejectsBadInput(t *testing.T) {
	r := &fakeSSHRunner{}
	a := newAgentWithTmux(t, &fakeTmux{}, r)
	cases := []struct {
		name   string
		target agent.SSHTarget
		cred   agent.Credential
	}{
		{"empty host", agent.SSHTarget{}, agent.Credential{Value: "x"}},
		{"empty value", agent.SSHTarget{Host: "vm"}, agent.Credential{Value: ""}},
		{"single quote", agent.SSHTarget{Host: "vm"}, agent.Credential{Value: "abc'def"}},
		{"newline", agent.SSHTarget{Host: "vm"}, agent.Credential{Value: "abc\ndef"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.InjectCredential(context.Background(), tc.target, tc.cred); err == nil {
				t.Errorf("InjectCredential accepted bad input: %v / %v", tc.target, tc.cred)
			}
		})
	}
	if len(r.wrote) != 0 {
		t.Errorf("expected no writes for bad inputs, got %d", len(r.wrote))
	}
}

func TestResumeStartsNewSession(t *testing.T) {
	tx := &fakeTmux{exists: false}
	r := &fakeSSHRunner{}
	a := newAgentWithTmux(t, tx, r)
	// Pre-cache token so Resume's env map carries it.
	if err := a.kc.Store(KeychainService, KeychainAccount, []byte("sk-ant-oat01-cached")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	ref := agent.SessionRef{ProjectSlug: "argus", SessionID: "session-abc"}
	if err := a.Resume(context.Background(), agent.SSHTarget{Host: "vm"}, ref); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(tx.newSessionLog) != 1 {
		t.Fatalf("expected 1 NewSession, got %d", len(tx.newSessionLog))
	}
	c := tx.newSessionLog[0]
	if c.name != "argus" {
		t.Errorf("session name = %q", c.name)
	}
	if c.cmd != "claude --resume session-abc" {
		t.Errorf("cmd = %q", c.cmd)
	}
	if c.env[CredentialEnvVar] != "sk-ant-oat01-cached" {
		t.Errorf("env missing token: %v", c.env)
	}
}

func TestResumeNoSessionIDStartsFreshClaude(t *testing.T) {
	tx := &fakeTmux{exists: false}
	a := newAgentWithTmux(t, tx, &fakeSSHRunner{})
	ref := agent.SessionRef{ProjectSlug: "argus"}
	if err := a.Resume(context.Background(), agent.SSHTarget{Host: "vm"}, ref); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if tx.newSessionLog[0].cmd != "claude" {
		t.Errorf("cmd = %q, want plain claude", tx.newSessionLog[0].cmd)
	}
}

func TestResumeExistingSessionIsNoOp(t *testing.T) {
	tx := &fakeTmux{exists: true}
	a := newAgentWithTmux(t, tx, &fakeSSHRunner{})
	ref := agent.SessionRef{ProjectSlug: "argus", SessionID: "x"}
	if err := a.Resume(context.Background(), agent.SSHTarget{Host: "vm"}, ref); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(tx.newSessionLog) != 0 {
		t.Errorf("Resume on existing session called NewSession %d times (want 0)", len(tx.newSessionLog))
	}
}

func TestResumeRejectsBadInput(t *testing.T) {
	a := newAgentWithTmux(t, &fakeTmux{}, &fakeSSHRunner{})
	if err := a.Resume(context.Background(), agent.SSHTarget{}, agent.SessionRef{ProjectSlug: "x"}); err == nil {
		t.Error("accepted empty host")
	}
	if err := a.Resume(context.Background(), agent.SSHTarget{Host: "vm"}, agent.SessionRef{}); err == nil {
		t.Error("accepted empty slug")
	}
}

func TestIsActive(t *testing.T) {
	tx := &fakeTmux{exists: true}
	a := newAgentWithTmux(t, tx, &fakeSSHRunner{})
	ref := agent.SessionRef{ProjectSlug: "argus"}
	got, err := a.IsActive(context.Background(), agent.SSHTarget{Host: "vm"}, ref)
	if err != nil || !got {
		t.Errorf("IsActive(true) = (%v, %v)", got, err)
	}
	tx.exists = false
	got, err = a.IsActive(context.Background(), agent.SSHTarget{Host: "vm"}, ref)
	if err != nil || got {
		t.Errorf("IsActive(false) = (%v, %v)", got, err)
	}
}

func TestPauseNoSession(t *testing.T) {
	tx := &fakeTmux{exists: false}
	a := newAgentWithTmux(t, tx, &fakeSSHRunner{})
	ref := agent.SessionRef{ProjectSlug: "argus"}
	if err := a.Pause(context.Background(), agent.SSHTarget{Host: "vm"}, ref); err != nil {
		t.Errorf("Pause on missing session = %v, want nil", err)
	}
}

func TestPauseWaitsForReady(t *testing.T) {
	// First two captures are mid-turn; third shows the ready prompt.
	tx := &fakeTmux{
		exists:     true,
		captureSeq: []string{"running tools...\n", "writing file abc.go\n", "> "},
	}
	a := newAgentWithTmux(t, tx, &fakeSSHRunner{})
	ref := agent.SessionRef{ProjectSlug: "argus"}
	if err := a.Pause(context.Background(), agent.SSHTarget{Host: "vm"}, ref); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if tx.captureCalls < 3 {
		t.Errorf("expected at least 3 capture calls, got %d", tx.captureCalls)
	}
}

func TestPauseRespectsContextCancellation(t *testing.T) {
	// Captures never show ready; ensure Pause exits when ctx is canceled.
	tx := &fakeTmux{exists: true, captureSeq: []string{"running...\n"}}
	a := newAgentWithTmux(t, tx, &fakeSSHRunner{})
	ref := agent.SessionRef{ProjectSlug: "argus"}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := a.Pause(ctx, agent.SSHTarget{Host: "vm"}, ref)
	if err == nil {
		t.Fatal("Pause returned nil after timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}

func TestPauseRejectsBadInput(t *testing.T) {
	a := newAgentWithTmux(t, &fakeTmux{}, &fakeSSHRunner{})
	if err := a.Pause(context.Background(), agent.SSHTarget{}, agent.SessionRef{ProjectSlug: "x"}); err == nil {
		t.Error("accepted empty host")
	}
	if err := a.Pause(context.Background(), agent.SSHTarget{Host: "vm"}, agent.SessionRef{}); err == nil {
		t.Error("accepted empty slug")
	}
}

func TestPauseReadyRegexCustomizable(t *testing.T) {
	customRE := regexp.MustCompile(`MY-CUSTOM-PROMPT`)
	tx := &fakeTmux{exists: true, captureSeq: []string{"running\n", "MY-CUSTOM-PROMPT"}}
	kc, err := keychain.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewWithOptions(Options{
		Keychain:         kc,
		SSHRunner:        &fakeSSHRunner{},
		HomeDir:          t.TempDir(),
		TmuxFactory:      func(string) tmux.Tmux { return tx },
		ReadyPromptRegex: customRE,
		PausePollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := agent.SessionRef{ProjectSlug: "argus"}
	if err := a.Pause(context.Background(), agent.SSHTarget{Host: "vm"}, ref); err != nil {
		t.Errorf("Pause: %v", err)
	}
}

func TestSessionStatePathIsSurprisingButCorrect(t *testing.T) {
	// Document the gotcha: encoding is one-way (lossy). Different paths can
	// collapse to the same encoded directory.
	a, _ := newAgent(t)
	p1 := a.SessionStatePath("/foo bar")
	p2 := a.SessionStatePath("/foo-bar")
	if p1 != p2 {
		t.Errorf("different paths %q vs %q encoded differently: %q vs %q (encoding *should* collapse them)", "/foo bar", "/foo-bar", p1, p2)
	}
	// This is the v1 trade-off: matches Claude Code's actual encoding so
	// `claude --resume` works across machines. Document this in the agent's
	// godoc.
}
