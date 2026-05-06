package cmd

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// TestE2EPerSessionLifecycle exercises the Phase-2 per-session routing
// model end-to-end against the same fakes the unit tests use, but
// chained: handoff(A) → handoff(B) → return(A) → return(B). Asserts the
// state.json fields and routing intent at each step. Caught a real bug
// during development (handoff was unconditionally flipping ActiveSide,
// breaking the legacy contract for unflagged invocations).
func TestE2EPerSessionLifecycle(t *testing.T) {
	stateRoot := t.TempDir()
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	// Seed local session JSONLs so handoff has something to push.
	sessionDir := stateRoot + c.ProjectDir
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	for _, sid := range []string{"sid-A", "sid-B"} {
		if err := os.WriteFile(sessionDir+"/"+sid+".jsonl", []byte(`{}`), 0o600); err != nil {
			t.Fatalf("write JSONL: %v", err)
		}
	}

	step := func(label string, fn func() error) {
		t.Helper()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	refresh := func() state.ProjectState {
		t.Helper()
		s, err := state.Open(c.StatePath)
		if err != nil {
			t.Fatalf("reopen state: %v", err)
		}
		c.State = s
		ps, _ := s.GetProject(c.ProjectDir)
		return ps
	}

	var out bytes.Buffer
	// 1. Initial state: no remote sessions, ActiveSide=local.
	ps := refresh()
	if len(ps.RemoteSIDs) != 0 || ps.ActiveSide != state.SideLocal {
		t.Fatalf("initial state wrong: RemoteSIDs=%v ActiveSide=%q", ps.RemoteSIDs, ps.ActiveSide)
	}

	// 2. handoff --session sid-A: A added, ActiveSide stays local.
	out.Reset()
	step("handoff sid-A", func() error {
		return RunHandoff(context.Background(), &out, strings.NewReader(""), c,
			HandoffOptions{SkipPrompt: true, SkipSSHWait: true, SessionID: "sid-A"})
	})
	ps = refresh()
	if len(ps.RemoteSIDs) != 1 || ps.RemoteSIDs[0] != "sid-A" {
		t.Errorf("after handoff(A): RemoteSIDs=%v want [sid-A]", ps.RemoteSIDs)
	}
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("after handoff(A): ActiveSide=%q want local (per-session shouldn't flip it)", ps.ActiveSide)
	}

	// 3. handoff --session sid-B: B added too, RemoteSIDs has both.
	out.Reset()
	step("handoff sid-B", func() error {
		return RunHandoff(context.Background(), &out, strings.NewReader(""), c,
			HandoffOptions{SkipPrompt: true, SkipSSHWait: true, SessionID: "sid-B"})
	})
	ps = refresh()
	if len(ps.RemoteSIDs) != 2 || !ps.HasRemoteSID("sid-A") || !ps.HasRemoteSID("sid-B") {
		t.Errorf("after handoff(B): RemoteSIDs=%v want [sid-A, sid-B]", ps.RemoteSIDs)
	}

	// 4. handoff --session sid-A again: idempotent, still 2 entries.
	out.Reset()
	step("handoff sid-A again", func() error {
		return RunHandoff(context.Background(), &out, strings.NewReader(""), c,
			HandoffOptions{SkipPrompt: true, SkipSSHWait: true, SessionID: "sid-A"})
	})
	ps = refresh()
	if len(ps.RemoteSIDs) != 2 {
		t.Errorf("after idempotent handoff(A): RemoteSIDs=%v want [sid-A, sid-B] (no dupe)", ps.RemoteSIDs)
	}

	// 5. return --session sid-A: A removed, B remains, VM stays running,
	//    ActiveSide stays local.
	out.Reset()
	step("return sid-A", func() error {
		return RunReturn(context.Background(), &out, c, ReturnOptions{
			Stop: true, SessionID: "sid-A",
		})
	})
	ps = refresh()
	if len(ps.RemoteSIDs) != 1 || ps.RemoteSIDs[0] != "sid-B" {
		t.Errorf("after return(A): RemoteSIDs=%v want [sid-B]", ps.RemoteSIDs)
	}
	if len(fp.stopCalls) != 0 {
		t.Errorf("after return(A) with B still remote: VM stopped %d times, want 0", len(fp.stopCalls))
	}
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("after return(A): ActiveSide=%q want local", ps.ActiveSide)
	}

	// 6. return --session sid-B: B removed, RemoteSIDs empty, VM stops.
	out.Reset()
	step("return sid-B", func() error {
		return RunReturn(context.Background(), &out, c, ReturnOptions{
			Stop: true, SessionID: "sid-B",
		})
	})
	ps = refresh()
	if len(ps.RemoteSIDs) != 0 {
		t.Errorf("after return(B): RemoteSIDs=%v want []", ps.RemoteSIDs)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("after return(B) (last): VM stopped %d times, want 1", len(fp.stopCalls))
	}
}

// TestE2EHandoffReturnAllCycle: handoff two sessions, then `return --all`
// brings them all back and stops the VM in one shot.
func TestE2EHandoffReturnAllCycle(t *testing.T) {
	stateRoot := t.TempDir()
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	for _, sid := range []string{"sid-X", "sid-Y", "sid-Z"} {
		_ = os.WriteFile(sessionDir+"/"+sid+".jsonl", []byte(`{}`), 0o600)
	}

	for _, sid := range []string{"sid-X", "sid-Y", "sid-Z"} {
		var out bytes.Buffer
		if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
			HandoffOptions{SkipPrompt: true, SkipSSHWait: true, SessionID: sid}); err != nil {
			t.Fatalf("handoff %s: %v", sid, err)
		}
		s, _ := state.Open(c.StatePath)
		c.State = s
	}
	ps, _ := c.State.GetProject(c.ProjectDir)
	if len(ps.RemoteSIDs) != 3 {
		t.Fatalf("after 3 handoffs: RemoteSIDs=%v want 3 entries", ps.RemoteSIDs)
	}

	var out bytes.Buffer
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop: true, AllSessions: true,
	}); err != nil {
		t.Fatalf("return --all: %v", err)
	}
	s, _ := state.Open(c.StatePath)
	ps, _ = s.GetProject(c.ProjectDir)
	if len(ps.RemoteSIDs) != 0 {
		t.Errorf("after return --all: RemoteSIDs=%v want []", ps.RemoteSIDs)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("VM stopped %d times, want 1", len(fp.stopCalls))
	}
}

// TestE2ELegacyHandoffReturnCompat: bare `moorpost handoff` (no flags)
// preserves legacy whole-project semantics — flips ActiveSide=remote
// without registering a SID into RemoteSIDs. Bare `moorpost return`
// then walks the legacy path.
func TestE2ELegacyHandoffReturnCompat(t *testing.T) {
	stateRoot := t.TempDir()
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	_ = os.WriteFile(sessionDir+"/legacy-sid.jsonl", []byte(`{}`), 0o600)

	// Bare handoff.
	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true, SkipSSHWait: true}); err != nil {
		t.Fatalf("legacy handoff: %v", err)
	}
	s, _ := state.Open(c.StatePath)
	ps, _ := s.GetProject(c.ProjectDir)
	if ps.ActiveSide != state.SideRemote {
		t.Errorf("after bare handoff: ActiveSide=%q want remote (legacy contract)", ps.ActiveSide)
	}
	if len(ps.RemoteSIDs) != 0 {
		t.Errorf("after bare handoff: RemoteSIDs=%v want [] (legacy doesn't register SIDs)", ps.RemoteSIDs)
	}
	c.State = s

	// Bare return.
	out.Reset()
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{Stop: true}); err != nil {
		t.Fatalf("legacy return: %v", err)
	}
	s, _ = state.Open(c.StatePath)
	ps, _ = s.GetProject(c.ProjectDir)
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("after legacy return: ActiveSide=%q want local", ps.ActiveSide)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("VM stopped %d times, want 1", len(fp.stopCalls))
	}
}

// TestE2EMixedLegacyAndPerSession: legacy handoff + per-session handoff
// produces a hybrid state. Per-session return removes one SID, legacy
// path stays accessible until RemoteSIDs is empty AND ActiveSide is
// flipped back.
func TestE2EMixedLegacyAndPerSession(t *testing.T) {
	stateRoot := t.TempDir()
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", User: "u"}}
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{Value: "sk-ant-test-token-1234567890"},
		sessionStateRoot: stateRoot,
	}
	fs := &cmdFakeSync{}
	c := makeHandoffContext(t, fp, fa, fs, state.SideLocal)

	sessionDir := stateRoot + c.ProjectDir
	_ = os.MkdirAll(sessionDir, 0o755)
	for _, sid := range []string{"legacy-tail", "per-sess"} {
		_ = os.WriteFile(sessionDir+"/"+sid+".jsonl", []byte(`{}`), 0o600)
	}

	// Step 1: legacy handoff. Flips ActiveSide=remote.
	var out bytes.Buffer
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true, SkipSSHWait: true}); err != nil {
		t.Fatalf("legacy handoff: %v", err)
	}
	s, _ := state.Open(c.StatePath)
	c.State = s

	// Step 2: per-session handoff for "per-sess". Adds to RemoteSIDs;
	// ActiveSide stays remote (already was).
	out.Reset()
	if err := RunHandoff(context.Background(), &out, strings.NewReader(""), c,
		HandoffOptions{SkipPrompt: true, SkipSSHWait: true, SessionID: "per-sess"}); err != nil {
		t.Fatalf("per-session handoff: %v", err)
	}
	s, _ = state.Open(c.StatePath)
	ps, _ := s.GetProject(c.ProjectDir)
	if !ps.HasRemoteSID("per-sess") {
		t.Errorf("expected RemoteSIDs to include per-sess, got %v", ps.RemoteSIDs)
	}
	if ps.ActiveSide != state.SideRemote {
		t.Errorf("ActiveSide=%q want remote (was already remote from legacy step)", ps.ActiveSide)
	}
	c.State = s

	// Step 3: per-session return for "per-sess". Removes SID, but
	// RemoteSIDs is now empty — VM should stop and ActiveSide should
	// flip back to local. Legacy assumption: empty RemoteSIDs means
	// "everything's local now".
	out.Reset()
	if err := RunReturn(context.Background(), &out, c, ReturnOptions{
		Stop: true, SessionID: "per-sess",
	}); err != nil {
		t.Fatalf("return per-sess: %v", err)
	}
	s, _ = state.Open(c.StatePath)
	ps, _ = s.GetProject(c.ProjectDir)
	if len(ps.RemoteSIDs) != 0 {
		t.Errorf("after return: RemoteSIDs=%v want []", ps.RemoteSIDs)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("VM stopped %d times, want 1 (last SID returned, drain → stop)", len(fp.stopCalls))
	}
	if ps.ActiveSide != state.SideLocal {
		t.Errorf("after final return: ActiveSide=%q want local", ps.ActiveSide)
	}
}
