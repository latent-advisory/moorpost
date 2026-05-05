package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
)

// fakeAgent is the cmd-test fakeAgent (separate from the one in
// claudecode_test.go).
type cmdFakeAgent struct {
	authResult       agent.Credential
	authErr          error
	injectCalls      []agent.Credential
	resumeCalls      int
	pauseCalls       int
	isActiveResult   bool
	isActiveErr      error
	sessionStateRoot string
}

func (f *cmdFakeAgent) ID() string                         { return "fake-agent" }
func (f *cmdFakeAgent) InstallScript(agent.OSFamily) string { return "" }
func (f *cmdFakeAgent) AuthenticateLocal(context.Context) (agent.Credential, error) {
	return f.authResult, f.authErr
}
func (f *cmdFakeAgent) LoadCachedCredential() (agent.Credential, error) {
	return f.authResult, f.authErr
}
func (f *cmdFakeAgent) InjectCredential(_ context.Context, _ agent.SSHTarget, c agent.Credential) error {
	f.injectCalls = append(f.injectCalls, c)
	return nil
}
func (f *cmdFakeAgent) SessionStatePath(projectAbsDir string) string {
	if f.sessionStateRoot == "" {
		return ""
	}
	return f.sessionStateRoot + "/" + projectAbsDir
}
func (f *cmdFakeAgent) Pause(context.Context, agent.SSHTarget, agent.SessionRef) error {
	f.pauseCalls++
	return nil
}
func (f *cmdFakeAgent) Resume(context.Context, agent.SSHTarget, agent.SessionRef) error {
	f.resumeCalls++
	return nil
}
func (f *cmdFakeAgent) IsActive(context.Context, agent.SSHTarget, agent.SessionRef) (bool, error) {
	return f.isActiveResult, f.isActiveErr
}

// cmdFakeSync records OneShot, StartSession, Stop calls.
type cmdFakeSync struct {
	oneShotCalls     []oneShotRecord
	oneShotErr       error
	startCalls       []mpsync.SyncSpec
	startReturnID    mpsync.SyncSessionID // configurable; default "fake-sync-id"
	startErr         error
	stopCalls        []mpsync.SyncSessionID
	stopErr          error

	conflicts        []mpsync.Conflict
	listConflictsErr error
}

type oneShotRecord struct {
	src, dst mpsync.Endpoint
	dir      mpsync.Direction
}

func (f *cmdFakeSync) ID() string { return "fake-sync" }
func (f *cmdFakeSync) StartSession(_ context.Context, spec mpsync.SyncSpec) (mpsync.SyncSessionID, error) {
	f.startCalls = append(f.startCalls, spec)
	if f.startErr != nil {
		return "", f.startErr
	}
	if f.startReturnID == "" {
		return mpsync.SyncSessionID("fake-sync-id"), nil
	}
	return f.startReturnID, nil
}
func (f *cmdFakeSync) Pause(context.Context, mpsync.SyncSessionID) error  { return nil }
func (f *cmdFakeSync) Resume(context.Context, mpsync.SyncSessionID) error { return nil }
func (f *cmdFakeSync) OneShot(_ context.Context, src, dst mpsync.Endpoint, dir mpsync.Direction) error {
	f.oneShotCalls = append(f.oneShotCalls, oneShotRecord{src: src, dst: dst, dir: dir})
	return f.oneShotErr
}
func (f *cmdFakeSync) Status(context.Context, mpsync.SyncSessionID) (mpsync.SyncStatus, error) {
	return mpsync.SyncStatus{}, nil
}
func (f *cmdFakeSync) Stop(_ context.Context, id mpsync.SyncSessionID) error {
	f.stopCalls = append(f.stopCalls, id)
	return f.stopErr
}
func (f *cmdFakeSync) ListConflicts(_ context.Context, _ mpsync.SyncSessionID) ([]mpsync.Conflict, error) {
	if f.listConflictsErr != nil {
		return nil, f.listConflictsErr
	}
	return f.conflicts, nil
}

func makeHandoffContext(t *testing.T, fp *fakeProvider, fa *cmdFakeAgent, fs *cmdFakeSync, activeSide state.Side) *Context {
	t.Helper()
	c, _ := makeLifecycleContext(t, fp, true)
	if activeSide != "" {
		_ = state.WithLock(c.StatePath, func(s *state.State) error {
			ps := s.Projects[c.ProjectDir]
			ps.ActiveSide = activeSide
			s.SetProject(c.ProjectDir, ps)
			c.State = s
			return nil
		})
	}
	c.Agent = fa
	c.Sync = fs
	return c
}

func TestRunHandoffHappyPath(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "35.1.2.3", Port: 22, User: "u"}}
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{EnvVar: "TOKEN", Value: "x", Kind: "k"},
		sessionStateRoot: t.TempDir(),
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)
	var out bytes.Buffer
	fixedNow := time.Date(2026, 5, 4, 21, 0, 0, 0, time.UTC)
	err := RunHandoff(context.Background(), &out, strings.NewReader("y\n"), c, HandoffOptions{
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("RunHandoff: %v", err)
	}
	if len(fp.startCalls) != 1 {
		t.Errorf("Provider.Start calls = %d, want 1", len(fp.startCalls))
	}
	if len(fa.injectCalls) != 1 {
		t.Errorf("Agent.InjectCredential calls = %d, want 1", len(fa.injectCalls))
	}
	if fa.resumeCalls != 1 {
		t.Errorf("Agent.Resume calls = %d, want 1", fa.resumeCalls)
	}
	if len(fs.oneShotCalls) < 1 || len(fs.oneShotCalls) > 2 {
		t.Errorf("Sync.OneShot calls = %d, want 1 or 2", len(fs.oneShotCalls))
	}
	for _, call := range fs.oneShotCalls {
		if call.dir != mpsync.DirectionLocalToRemote {
			t.Errorf("OneShot dir = %q, want local-to-remote", call.dir)
		}
	}
	// State updated.
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if ps.ActiveSide != state.SideRemote {
		t.Errorf("ActiveSide = %q, want remote", ps.ActiveSide)
	}
	if !ps.LastHandoff.Equal(fixedNow) {
		t.Errorf("LastHandoff = %v, want %v", ps.LastHandoff, fixedNow)
	}
}

func TestRunHandoffRejectsAlreadyRemote(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	var out bytes.Buffer
	err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{SkipPrompt: true})
	if err == nil || !strings.Contains(err.Error(), "already remote") {
		t.Errorf("err = %v, want 'already remote'", err)
	}
	if len(fp.startCalls) != 0 {
		t.Error("Start should not be called when already remote")
	}
}

func TestRunHandoffPromptAbort(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)
	var out bytes.Buffer
	err := RunHandoff(context.Background(), &out, strings.NewReader("n\n"), c, HandoffOptions{})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("err = %v, want aborted", err)
	}
}

func TestRunHandoffStartFailure(t *testing.T) {
	myErr := errors.New("permission denied")
	fp := &fakeProvider{startErr: myErr, sshTarget: provider.SSHTarget{Host: "h"}}
	c := makeHandoffContext(t, fp, &cmdFakeAgent{}, &cmdFakeSync{}, state.SideLocal)
	var out bytes.Buffer
	err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{SkipPrompt: true})
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrap %v", err, myErr)
	}
}

func TestRunHandoffNoProject(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, false)
	c.Agent = &cmdFakeAgent{}
	c.Sync = &cmdFakeSync{}
	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{SkipPrompt: true}); err == nil {
		t.Error("RunHandoff accepted unprovisioned project")
	}
}

// --- Return ---

func TestRunReturnHappyPath(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "35.1.2.3", User: "u"}}
	fa := &cmdFakeAgent{sessionStateRoot: t.TempDir()}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)

	// Pre-populate a SyncSessionID (would normally be set by an earlier
	// handoff). Return must call Sync.Stop with this ID.
	_ = state.WithLock(c.StatePath, func(s *state.State) error {
		ps := s.Projects[c.ProjectDir]
		ps.SyncSessionID = "session-from-prior-handoff"
		s.SetProject(c.ProjectDir, ps)
		return nil
	})
	c.State, _ = state.Open(c.StatePath)

	var out bytes.Buffer
	fixedNow := time.Date(2026, 5, 4, 22, 0, 0, 0, time.UTC)
	err := RunReturn(context.Background(), &out, c, ReturnOptions{Stop: true, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("RunReturn: %v", err)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("Provider.Stop calls = %d, want 1 (Stop=true)", len(fp.stopCalls))
	}
	// v0.2.1+: return uses one-shot only for session state (RemoteToLocal);
	// project files come back via the continuous sync that ran since handoff.
	if len(fs.oneShotCalls) != 1 {
		t.Errorf("OneShot calls = %d, want 1 (session state only)", len(fs.oneShotCalls))
	}
	for _, call := range fs.oneShotCalls {
		if call.dir != mpsync.DirectionRemoteToLocal {
			t.Errorf("OneShot dir = %q, want remote-to-local", call.dir)
		}
	}
	// Continuous sync stopped with the persisted ID.
	if len(fs.stopCalls) != 1 || fs.stopCalls[0] != "session-from-prior-handoff" {
		t.Errorf("Sync.Stop calls = %v, want exactly [session-from-prior-handoff]", fs.stopCalls)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("ActiveSide = %q, want local", ps.ActiveSide)
	}
	if !ps.LastReturn.Equal(fixedNow) {
		t.Errorf("LastReturn = %v, want %v", ps.LastReturn, fixedNow)
	}
	if ps.SyncSessionID != "" {
		t.Errorf("SyncSessionID should be cleared after return; got %q", ps.SyncSessionID)
	}
}

func TestRunHandoffStartSessionFailure(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{authResult: agent.Credential{Value: "x"}}
	fs := &cmdFakeSync{startErr: errors.New("mutagen daemon not running")}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)
	var out bytes.Buffer
	err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{SkipPrompt: true})
	if err == nil || !strings.Contains(err.Error(), "start sync session") {
		t.Errorf("err = %v, want 'start sync session'", err)
	}
	// Resume should NOT have been called when sync session creation failed.
	if fa.resumeCalls != 0 {
		t.Errorf("Resume should not be called when sync session start fails; got %d", fa.resumeCalls)
	}
	// State should NOT have been updated to remote.
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if ps.ActiveSide == state.SideRemote {
		t.Error("ActiveSide should not have flipped to remote on sync failure")
	}
}

func TestRunReturnPreV0_2_1HandoffNoSyncID(t *testing.T) {
	// Compatibility: a project handed off pre-v0.2.1 won't have a
	// SyncSessionID. Return should still work (skip the sync stop).
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	// Don't populate SyncSessionID.
	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{}); err != nil {
		t.Fatalf("RunReturn: %v", err)
	}
	if len(fs.stopCalls) != 0 {
		t.Errorf("Sync.Stop should not be called when no SyncSessionID recorded; got %d", len(fs.stopCalls))
	}
	if !strings.Contains(out.String(), "predates v0.2.1") {
		t.Errorf("output should note pre-v0.2.1 compat: %q", out.String())
	}
}

func TestRunReturnNoStopFlag(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{Stop: false}); err != nil {
		t.Fatal(err)
	}
	if len(fp.stopCalls) != 0 {
		t.Errorf("Stop should not be called when --stop=false")
	}
}

func TestRunReturnRejectsAlreadyLocal(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h"}}
	c := makeHandoffContext(t, fp, &cmdFakeAgent{}, &cmdFakeSync{}, state.SideLocal)
	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{}); err == nil {
		t.Error("RunReturn accepted active=local")
	}
}

// --- Snapshot ---

func TestRunSnapshot(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	if err := RunSnapshot(context.Background(), &out, c, "pre-handoff"); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}
	// fakeProvider's Snapshot returns "" empty SnapshotID by default; just
	// verify it didn't error and we printed something.
	if out.Len() == 0 {
		t.Error("RunSnapshot produced no output")
	}
}

func TestRunSnapshotNoProject(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, false)
	var out bytes.Buffer
	if err := RunSnapshot(context.Background(), &out, c, "x"); err == nil {
		t.Error("RunSnapshot accepted unprovisioned project")
	}
}

// --- Cost ---

func TestTimeRangeForPeriod(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		period string
		ok     bool
	}{
		{"mtd", true},
		{"", true},
		{"today", true},
		{"week", true},
		{"yearly", false},
	}
	for _, tc := range tests {
		t.Run(tc.period, func(t *testing.T) {
			tr, err := timeRangeForPeriod(tc.period, now)
			if tc.ok && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected error for unknown period")
			}
			if tc.ok && !tr.End.Equal(now.UTC()) {
				t.Errorf("End = %v, want %v", tr.End, now.UTC())
			}
		})
	}
}

func TestRunCostHappyPath(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	if err := RunCost(context.Background(), &out, c, "mtd", false); err != nil {
		t.Fatalf("RunCost: %v", err)
	}
	for _, want := range []string{"Compute:", "Disk:", "Total:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunCostUnknownPeriod(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	if err := RunCost(context.Background(), &out, c, "yearly", false); err == nil {
		t.Error("RunCost accepted unknown period")
	}
}

// --- iter 35: --prefer-local / --prefer-remote conflict detection ---

func TestRunHandoffRejectsBothPreferFlags(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)
	var out bytes.Buffer
	err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{
		SkipPrompt:   true,
		PreferLocal:  true,
		PreferRemote: true,
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v, want mutually-exclusive error", err)
	}
	// VM should not have been started.
	if len(fp.startCalls) != 0 {
		t.Error("Start should not be called when both flags conflict")
	}
}

// TestRunHandoffWritesWatermarkOnFirstHandoff: with no prior watermark
// and an existing session-state directory, handoff proceeds and writes
// the manifest hash to state.LastSessionSyncHash.
func TestRunHandoffWritesWatermarkOnFirstHandoff(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	stateRoot := t.TempDir()
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "x"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	// Populate the session-state dir so the manifest is non-empty.
	sessionDir := stateRoot + c.ProjectDir
	if err := osMkdirAllForTest(sessionDir); err != nil {
		t.Fatal(err)
	}
	if err := osWriteFileForTest(sessionDir+"/state.json", []byte(`{"k":1}`)); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{SkipPrompt: true})
	if err != nil {
		t.Fatalf("first handoff: %v", err)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if ps.LastSessionSyncHash == "" {
		t.Error("LastSessionSyncHash not set after successful handoff")
	}
}

// TestRunHandoffPreferRemoteAbortsOnDivergence: after a successful
// handoff sets the watermark, a subsequent local edit (changing the
// manifest) plus --prefer-remote should abort.
func TestRunHandoffPreferRemoteAbortsOnDivergence(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	stateRoot := t.TempDir()
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "x"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = osMkdirAllForTest(sessionDir)
	_ = osWriteFileForTest(sessionDir+"/v1.json", []byte(`{"v":1}`))

	// First handoff: succeeds, watermark gets set.
	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{SkipPrompt: true}); err != nil {
		t.Fatalf("first handoff: %v", err)
	}
	// Reset for second handoff: state shows ActiveSide=local again.
	_ = state.WithLock(c.StatePath, func(s *state.State) error {
		ps := s.Projects[c.ProjectDir]
		ps.ActiveSide = state.SideLocal
		s.SetProject(c.ProjectDir, ps)
		return nil
	})
	c.State, _ = state.Open(c.StatePath)

	// Modify session state — local now diverges from the watermark.
	_ = osWriteFileForTest(sessionDir+"/v2.json", []byte(`{"v":2}`))

	// Second handoff with --prefer-remote should refuse.
	out.Reset()
	err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{
		SkipPrompt:   true,
		PreferRemote: true,
	})
	if err == nil {
		t.Fatal("expected --prefer-remote to abort on local divergence")
	}
	if !strings.Contains(err.Error(), "local session state has changed") {
		t.Errorf("err message doesn't explain divergence: %v", err)
	}
}

// TestRunHandoffPreferLocalProceedsOnDivergence: same setup as the
// abort test, but --prefer-local should proceed and update the
// watermark to the new manifest.
func TestRunHandoffPreferLocalProceedsOnDivergence(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	stateRoot := t.TempDir()
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "x"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = osMkdirAllForTest(sessionDir)
	_ = osWriteFileForTest(sessionDir+"/v1.json", []byte(`{"v":1}`))

	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{SkipPrompt: true}); err != nil {
		t.Fatalf("first handoff: %v", err)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	firstWatermark := ps.LastSessionSyncHash

	_ = state.WithLock(c.StatePath, func(s *state.State) error {
		p := s.Projects[c.ProjectDir]
		p.ActiveSide = state.SideLocal
		s.SetProject(c.ProjectDir, p)
		return nil
	})
	c.State, _ = state.Open(c.StatePath)

	_ = osWriteFileForTest(sessionDir+"/v2.json", []byte(`{"v":2}`))

	out.Reset()
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c, HandoffOptions{
		SkipPrompt:  true,
		PreferLocal: true,
	}); err != nil {
		t.Fatalf("--prefer-local handoff: %v", err)
	}
	st, _ = state.Open(c.StatePath)
	ps, _ = st.GetProject(c.ProjectDir)
	if ps.LastSessionSyncHash == "" || ps.LastSessionSyncHash == firstWatermark {
		t.Errorf("watermark not updated after --prefer-local: first=%q now=%q", firstWatermark, ps.LastSessionSyncHash)
	}
}

// Helpers — kept in this file (not lifecycle_test) so other suites don't
// pick them up as test funcs.
func osMkdirAllForTest(p string) error {
	return os.MkdirAll(p, 0o755)
}

func osWriteFileForTest(p string, b []byte) error {
	return os.WriteFile(p, b, 0o600)
}
