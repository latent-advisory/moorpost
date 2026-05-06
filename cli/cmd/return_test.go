package cmd

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
)

// --- iter 36: --prefer-local / --prefer-remote on return ---

func TestRunReturnRejectsBothPreferFlags(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	var out bytes.Buffer
	err := RunReturn(context.Background(), &out, c, ReturnOptions{
		PreferLocal:  true,
		PreferRemote: true,
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("err = %v, want mutually-exclusive error", err)
	}
}

// TestRunReturnFreshSession_NoWatermark_Proceeds: a return with no
// recorded watermark proceeds without abort regardless of flag.
func TestRunReturnFreshSession_NoWatermark_Proceeds(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	stateRoot := t.TempDir()
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)

	// Populate local session state but NO watermark. PreferLocal should
	// not abort because we have no history to compare against.
	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	_ = os.WriteFile(sessionDir+"/state.json", []byte(`{"v":1}`), 0o600)

	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c,
		ReturnOptions{Stop: false, PreferLocal: true}); err != nil {
		t.Fatalf("fresh session with --prefer-local should proceed: %v", err)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("ActiveSide = %q, want local after return", ps.ActiveSide)
	}
}

// TestRunReturnPreferLocalAbortsOnDivergence: with a watermark recorded
// (via earlier handoff) and local subsequently modified, --prefer-local
// must refuse to overwrite local with remote.
func TestRunReturnPreferLocalAbortsOnDivergence(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	stateRoot := t.TempDir()
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	_ = os.WriteFile(sessionDir+"/v1.json", []byte(`{"v":1}`), 0o600)

	// Run handoff to set the watermark.
	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true, SkipSSHWait: true}); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}

	// State now has watermark + ActiveSide=remote. Modify local AFTER
	// handoff (simulating a desktop app touching session state while
	// remote was active).
	_ = os.WriteFile(sessionDir+"/v2-after-handoff.json", []byte(`{"v":2}`), 0o600)

	// Refresh c.State so RunReturn sees the post-handoff state.
	c.State, _ = state.Open(c.StatePath)

	// Return with --prefer-local should abort.
	out.Reset()
	err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop:        false,
		PreferLocal: true,
	})
	if err == nil {
		t.Fatal("expected --prefer-local to abort on local divergence")
	}
	if !strings.Contains(err.Error(), "local session state has changed") {
		t.Errorf("err message doesn't explain divergence: %v", err)
	}
}

// TestRunReturnPreferRemoteProceedsOnDivergence: same setup, but
// --prefer-remote forces the pull (overwrites local with remote) and
// updates the watermark.
func TestRunReturnPreferRemoteProceedsOnDivergence(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	stateRoot := t.TempDir()
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	_ = os.WriteFile(sessionDir+"/v1.json", []byte(`{"v":1}`), 0o600)

	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true, SkipSSHWait: true}); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	firstWatermark := ps.LastSessionSyncHash

	_ = os.WriteFile(sessionDir+"/v2-after-handoff.json", []byte(`{"v":2}`), 0o600)
	c.State, _ = state.Open(c.StatePath)

	out.Reset()
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop:         false,
		PreferRemote: true,
	}); err != nil {
		t.Fatalf("--prefer-remote return: %v", err)
	}
	st, _ = state.Open(c.StatePath)
	ps, _ = st.GetProject(c.ProjectDir)
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("ActiveSide = %q, want local", ps.ActiveSide)
	}
	if ps.LastSessionSyncHash == "" || ps.LastSessionSyncHash == firstWatermark {
		t.Errorf("watermark not updated after --prefer-remote return: first=%q now=%q",
			firstWatermark, ps.LastSessionSyncHash)
	}
}

// TestRunReturnNoFlagsNoDivergence_UpdatesWatermark: the typical case —
// active was remote, local hasn't changed, return pulls and updates
// watermark to match the (post-pull) local content.
func TestRunReturnNoFlagsNoDivergence_UpdatesWatermark(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	stateRoot := t.TempDir()
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	_ = os.WriteFile(sessionDir+"/v1.json", []byte(`{"v":1}`), 0o600)

	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true, SkipSSHWait: true}); err != nil {
		t.Fatalf("seed handoff: %v", err)
	}

	// No additional local writes (no divergence). Refresh state and call
	// return — should succeed and update the watermark to the same hash
	// (since local is unchanged).
	c.State, _ = state.Open(c.StatePath)
	stBefore, _ := state.Open(c.StatePath)
	psBefore, _ := stBefore.GetProject(c.ProjectDir)
	hashBefore := psBefore.LastSessionSyncHash

	fixedNow := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	out.Reset()
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop: false,
		Now:  func() time.Time { return fixedNow },
	}); err != nil {
		t.Fatalf("return: %v", err)
	}

	stAfter, _ := state.Open(c.StatePath)
	psAfter, _ := stAfter.GetProject(c.ProjectDir)
	if !psAfter.LastReturn.Equal(fixedNow) {
		t.Errorf("LastReturn = %v, want %v", psAfter.LastReturn, fixedNow)
	}
	// Watermark should still be set (no manifest divergence means
	// hashAfter == hashBefore is acceptable).
	if psAfter.LastSessionSyncHash == "" {
		t.Error("watermark cleared after return; should still be set")
	}
	_ = hashBefore
}

// --- Per-session return ---
//
// These tests cover the per-session routing introduced when handoff/return
// became per-SID. Setup pattern: stash some SIDs into ps.RemoteSIDs via a
// state.WithLock, then drive RunReturn with --session/--all and assert
// RemoteSIDs and Provider.Stop behavior.

// seedRemoteSIDs is a small helper to populate RemoteSIDs (and ActiveSide=
// remote, since per-session return is only meaningful when remote is
// holding work) under the state lock.
func seedRemoteSIDs(t *testing.T, c *Context, sids ...string) {
	t.Helper()
	if err := state.WithLock(c.StatePath, func(s *state.State) error {
		ps := s.Projects[c.ProjectDir]
		ps.RemoteSIDs = append([]string(nil), sids...)
		ps.ActiveSide = state.SideRemote
		s.SetProject(c.ProjectDir, ps)
		return nil
	}); err != nil {
		t.Fatalf("seedRemoteSIDs: %v", err)
	}
	c.State, _ = state.Open(c.StatePath)
}

// TestRunReturnSession_OneOfTwo: returning one of two RemoteSIDs leaves
// the other in place AND keeps the VM running (no Provider.Stop call).
func TestRunReturnSession_OneOfTwo(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{sessionStateRoot: t.TempDir()}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	seedRemoteSIDs(t, c, "sid-alpha", "sid-beta")

	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop:      true, // even with Stop=true, VM must stay up while sids remain
		SessionID: "sid-alpha",
	}); err != nil {
		t.Fatalf("RunReturn: %v", err)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if got := ps.RemoteSIDs; len(got) != 1 || got[0] != "sid-beta" {
		t.Errorf("RemoteSIDs after return = %v, want [sid-beta]", got)
	}
	if ps.ActiveSide != state.SideRemote {
		t.Errorf("ActiveSide = %q, want still remote (sid-beta still routed to remote)", ps.ActiveSide)
	}
	if len(fp.stopCalls) != 0 {
		t.Errorf("Provider.Stop should NOT be called while RemoteSIDs is non-empty; got %d calls", len(fp.stopCalls))
	}
	if !strings.Contains(out.String(), "still on remote") {
		t.Errorf("output should mention sessions still on remote: %q", out.String())
	}
}

// TestRunReturnSession_LastSession: returning the only RemoteSID drains
// the set, stops the VM, and flips ActiveSide=local with LastReturn set.
func TestRunReturnSession_LastSession(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{sessionStateRoot: t.TempDir()}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	seedRemoteSIDs(t, c, "sid-only")

	fixedNow := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop:      true,
		SessionID: "sid-only",
		Now:       func() time.Time { return fixedNow },
	}); err != nil {
		t.Fatalf("RunReturn: %v", err)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if len(ps.RemoteSIDs) != 0 {
		t.Errorf("RemoteSIDs after final return = %v, want []", ps.RemoteSIDs)
	}
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("ActiveSide = %q, want local", ps.ActiveSide)
	}
	if !ps.LastReturn.Equal(fixedNow) {
		t.Errorf("LastReturn = %v, want %v", ps.LastReturn, fixedNow)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("Provider.Stop calls = %d, want 1 when RemoteSIDs drains", len(fp.stopCalls))
	}
}

// TestRunReturnAll_TwoSessions: --all loops through both SIDs and stops
// the VM after the set drains.
func TestRunReturnAll_TwoSessions(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{sessionStateRoot: t.TempDir()}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	seedRemoteSIDs(t, c, "sid-a", "sid-b")

	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop:        true,
		AllSessions: true,
	}); err != nil {
		t.Fatalf("RunReturn --all: %v", err)
	}
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if len(ps.RemoteSIDs) != 0 {
		t.Errorf("RemoteSIDs after --all = %v, want []", ps.RemoteSIDs)
	}
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("ActiveSide = %q, want local", ps.ActiveSide)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("Provider.Stop calls = %d, want 1 (VM stopped after final SID)", len(fp.stopCalls))
	}
	// Sync should have been called twice (once per SID), each pulling a
	// per-SID file path (not the directory).
	if len(fs.oneShotCalls) != 2 {
		t.Errorf("OneShot calls = %d, want 2 (one per session)", len(fs.oneShotCalls))
	}
	for _, call := range fs.oneShotCalls {
		if call.dir != mpsync.DirectionRemoteToLocal {
			t.Errorf("OneShot dir = %q, want remote-to-local", call.dir)
		}
		if !strings.HasSuffix(call.src.Path, ".jsonl") {
			t.Errorf("expected per-SID JSONL pull, got src path %q", call.src.Path)
		}
	}
}

// TestRunReturnSession_NotInRemoteSIDs: error out when caller passes a
// SID that isn't currently routed to remote.
func TestRunReturnSession_NotInRemoteSIDs(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	seedRemoteSIDs(t, c, "sid-real")

	var out bytes.Buffer
	err := RunReturn(context.Background(), &out, c, ReturnOptions{
		SessionID: "sid-bogus",
	})
	if err == nil || !strings.Contains(err.Error(), "session not on remote") {
		t.Errorf("err = %v, want 'session not on remote'", err)
	}
	// Defensive: nothing was mutated.
	st, _ := state.Open(c.StatePath)
	ps, _ := st.GetProject(c.ProjectDir)
	if len(ps.RemoteSIDs) != 1 || ps.RemoteSIDs[0] != "sid-real" {
		t.Errorf("RemoteSIDs mutated despite error: %v", ps.RemoteSIDs)
	}
	if len(fp.stopCalls) != 0 {
		t.Error("Provider.Stop should not be called on validation error")
	}
}

// TestRunReturnNoFlag_NonEmptyRemoteSIDs: with sessions routed to remote
// and neither --session nor --all, error and list the sids in the message.
func TestRunReturnNoFlag_NonEmptyRemoteSIDs(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideRemote)
	seedRemoteSIDs(t, c, "sid-x", "sid-y")

	var out bytes.Buffer
	err := RunReturn(context.Background(), &out, c, ReturnOptions{})
	if err == nil {
		t.Fatal("expected error when RemoteSIDs is non-empty and no flag passed")
	}
	if !strings.Contains(err.Error(), "--session") || !strings.Contains(err.Error(), "--all") {
		t.Errorf("err should mention --session/--all: %v", err)
	}
	if !strings.Contains(err.Error(), "sid-x") || !strings.Contains(err.Error(), "sid-y") {
		t.Errorf("err should list current SIDs: %v", err)
	}
	if len(fp.stopCalls) != 0 {
		t.Error("Provider.Stop should not be called on ambiguity error")
	}
}
