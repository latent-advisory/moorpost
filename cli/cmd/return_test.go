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
		authResult:       agent.Credential{Value: "x"},
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
		authResult:       agent.Credential{Value: "x"},
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
		HandoffOptions{SkipPrompt: true}); err != nil {
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
		authResult:       agent.Credential{Value: "x"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	_ = os.WriteFile(sessionDir+"/v1.json", []byte(`{"v":1}`), 0o600)

	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true}); err != nil {
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
		authResult:       agent.Credential{Value: "x"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	_ = os.WriteFile(sessionDir+"/v1.json", []byte(`{"v":1}`), 0o600)

	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true}); err != nil {
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
