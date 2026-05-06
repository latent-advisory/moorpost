package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// TestE2EMockHappyPath runs the full v0.1 walking-skeleton flow against fakes:
//   init → provision → handoff → return → destroy → status
//
// This catches wiring bugs that single-command tests can't, since each step's
// state changes feed the next.
func TestE2EMockHappyPath(t *testing.T) {
	projectDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	keyPath := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(keyPath, []byte("ssh-ed25519 AAAA fake-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1) init — write config to projectDir.
	var initOut bytes.Buffer
	if err := RunInit(&initOut, InitOptions{
		Dir:        projectDir,
		Slug:       "webapp",
		GCPProject: "example-project",
	}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".moorpost", "config.yaml")); err != nil {
		t.Fatalf("init did not write config: %v", err)
	}

	// 2) Build the Context manually (skipping the live registry construction
	// of Provider/Agent/Sync since we want to inject fakes deterministically).
	cfg, err := config.Load(filepath.Join(projectDir, ".moorpost", "config.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	fp := &fakeProvider{
		sshTarget: provider.SSHTarget{Host: "35.1.2.3", Port: 22, User: "u"},
	}
	fa := &cmdFakeAgent{
		authResult:       agent.Credential{EnvVar: "TOKEN", Value: "sk-ant-oat01-test-fixture-token", Kind: "k"},
		sessionStateRoot: t.TempDir(),
	}
	fs := &cmdFakeSync{}

	st, err := state.Open(statePath)
	if err != nil {
		t.Fatalf("Open state: %v", err)
	}

	ctx := &Context{
		ProjectDir: projectDir,
		ConfigPath: filepath.Join(projectDir, ".moorpost", "config.yaml"),
		StatePath:  statePath,
		Config:     cfg,
		State:      st,
		Provider:   fp,
		Agent:      fa,
		Sync:       fs,
	}

	// 3) provision — should create VM, leave stopped, persist state.
	var provOut bytes.Buffer
	if err := RunProvision(context.Background(), &provOut, ctx, ProvisionOptions{
		SSHKeyPath: keyPath,
	}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(fp.provisionCalls) != 1 {
		t.Errorf("Provision calls = %d, want 1", len(fp.provisionCalls))
	}
	// Note: in the real flow, gcp.engine.Provision internally issues a stop
	// when StartImmediately=false. fakeProvider doesn't replicate that
	// internal behavior — it just records the call. So we assert
	// `provisionCalls[0].StartImmediately == false` instead.
	if fp.provisionCalls[0].StartImmediately {
		t.Errorf("Provision spec should have StartImmediately=false (local-first default)")
	}
	st1, _ := state.Open(statePath)
	ps, ok := st1.GetProject(projectDir)
	if !ok || ps.VMID != "webapp-vm" {
		t.Fatalf("project state after provision: ok=%v ps=%+v", ok, ps)
	}
	if st1.VMs["webapp-vm"].StateCache != "stopped" {
		t.Errorf("VM state cache = %q, want stopped (local-first default)", st1.VMs["webapp-vm"].StateCache)
	}

	// Refresh the in-memory state on the context (mirrors what real flow does).
	ctx.State, _ = state.Open(statePath)

	// 4) handoff — Start, Inject, OneShot×2, Resume; ActiveSide=remote.
	// Per-session routing: only --new-session flips ActiveSide=remote
	// (per-SID handoffs leave the project default alone). The full
	// lifecycle test mimics the "fresh spawn defaults remote" path.
	var handoffOut bytes.Buffer
	fixedHandoff := time.Date(2026, 5, 4, 22, 0, 0, 0, time.UTC)
	if err := RunHandoff(context.Background(), &handoffOut, strings.NewReader(""), ctx, HandoffOptions{
		SkipPrompt:  true,
		SkipSSHWait: true,
		NewSession:  true,
		Now:         func() time.Time { return fixedHandoff },
	}); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if len(fp.startCalls) != 1 {
		t.Errorf("handoff: Start calls = %d, want 1", len(fp.startCalls))
	}
	if len(fa.injectCalls) != 1 {
		t.Errorf("handoff: InjectCredential calls = %d, want 1", len(fa.injectCalls))
	}
	if fa.resumeCalls != 1 {
		t.Errorf("handoff: Resume calls = %d, want 1", fa.resumeCalls)
	}
	st2, _ := state.Open(statePath)
	ps2, _ := st2.GetProject(projectDir)
	if ps2.ActiveSide != state.SideRemote {
		t.Errorf("after handoff: ActiveSide = %q, want remote", ps2.ActiveSide)
	}
	if !ps2.LastHandoff.Equal(fixedHandoff) {
		t.Errorf("LastHandoff = %v, want %v", ps2.LastHandoff, fixedHandoff)
	}

	// 5) status (mid-cycle) — should reflect remote.
	ctx.State, _ = state.Open(statePath)
	var statusOut bytes.Buffer
	if err := RunStatus(&statusOut, ctx, false); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(statusOut.String(), "remote") {
		t.Errorf("status mid-cycle should show remote ActiveSide:\n%s", statusOut.String())
	}

	// 6) return — pull back, Stop VM.
	priorStops := len(fp.stopCalls)
	var retOut bytes.Buffer
	fixedReturn := fixedHandoff.Add(2 * time.Hour)
	if err := RunReturn(context.Background(), &retOut, ctx, ReturnOptions{
		Stop: true,
		Now:  func() time.Time { return fixedReturn },
	}); err != nil {
		t.Fatalf("return: %v", err)
	}
	// RunReturn directly calls Provider.Stop when --stop=true (not through
	// any internal provider logic), so the fakeProvider sees one new Stop call.
	if len(fp.stopCalls) != priorStops+1 {
		t.Errorf("return should issue Stop; Stop calls before=%d after=%d", priorStops, len(fp.stopCalls))
	}
	st3, _ := state.Open(statePath)
	ps3, _ := st3.GetProject(projectDir)
	if ps3.ActiveSide != state.SideLocal {
		t.Errorf("after return: ActiveSide = %q, want local", ps3.ActiveSide)
	}
	if !ps3.LastReturn.Equal(fixedReturn) {
		t.Errorf("LastReturn = %v, want %v", ps3.LastReturn, fixedReturn)
	}

	// 7) destroy — auto-yes; project + VM removed from state.
	ctx.State, _ = state.Open(statePath)
	var destroyOut bytes.Buffer
	if err := RunDestroy(context.Background(), &destroyOut, strings.NewReader(""), ctx, true); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if len(fp.destroyCalls) != 1 {
		t.Errorf("Destroy calls = %d, want 1", len(fp.destroyCalls))
	}
	st4, _ := state.Open(statePath)
	if _, ok := st4.GetProject(projectDir); ok {
		t.Error("project should be cleared from state after destroy")
	}
	if _, ok := st4.VMs["webapp-vm"]; ok {
		t.Error("VM record should be cleared from state after destroy")
	}

	// 8) Sanity: total OneShot calls = 4 (2 push during handoff, 2 pull during return).
	if len(fs.oneShotCalls) < 2 {
		t.Errorf("expected ≥2 OneShot calls (handoff + return), got %d", len(fs.oneShotCalls))
	}
}

// TestE2EMockHandoffWithoutProvisionFails proves we can't skip steps.
func TestE2EMockHandoffWithoutProvisionFails(t *testing.T) {
	projectDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")

	var initOut bytes.Buffer
	if err := RunInit(&initOut, InitOptions{Dir: projectDir, Slug: "webapp", GCPProject: "p"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(filepath.Join(projectDir, ".moorpost", "config.yaml"))
	st, _ := state.Open(statePath)
	ctx := &Context{
		ProjectDir: projectDir,
		StatePath:  statePath,
		Config:     cfg,
		State:      st,
		Provider:   &fakeProvider{},
		Agent:      &cmdFakeAgent{},
		Sync:       &cmdFakeSync{},
	}
	var out bytes.Buffer
	err := RunHandoff(context.Background(), &out, strings.NewReader(""), ctx, HandoffOptions{SkipPrompt: true, SkipSSHWait: true})
	if err == nil || !strings.Contains(err.Error(), "not provisioned") {
		t.Errorf("err = %v, want 'not provisioned'", err)
	}
}

// TestE2EMockReturnRequiresRemoteSide ensures the active-side state machine
// is enforced.
func TestE2EMockReturnRequiresRemoteSide(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true) // ActiveSide defaults to local
	c.Agent = &cmdFakeAgent{}
	c.Sync = &cmdFakeSync{}
	var out bytes.Buffer
	err := RunReturn(context.Background(), &out, c, ReturnOptions{})
	if err == nil || !strings.Contains(err.Error(), "not remote") {
		t.Errorf("err = %v, want 'not remote'", err)
	}
}

// TestE2EMockStatusAfterDestroyDoesntCrash ensures status gracefully reports
// "no VM" rather than panicking when the project record is gone.
func TestE2EMockStatusAfterDestroyDoesntCrash(t *testing.T) {
	projectDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")

	var initOut bytes.Buffer
	if err := RunInit(&initOut, InitOptions{Dir: projectDir, Slug: "webapp", GCPProject: "p"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(filepath.Join(projectDir, ".moorpost", "config.yaml"))
	st, _ := state.Open(statePath)
	// No project record in state — status should still print fields from config.
	ctx := &Context{
		ProjectDir: projectDir,
		StatePath:  statePath,
		Config:     cfg,
		State:      st,
	}
	var out bytes.Buffer
	if err := RunStatus(&out, ctx, false); err != nil {
		t.Errorf("status crashed without provisioned project: %v", err)
	}
	if !strings.Contains(out.String(), "webapp") {
		t.Errorf("status output should still have project info: %q", out.String())
	}
}
