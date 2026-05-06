package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// fakeProvider is a controllable Provider for cmd tests.
type fakeProvider struct {
	provisionCalls []provider.ProvisionSpec
	provisionVM    provider.VM
	provisionErr   error

	startCalls   []string
	startErr     error
	stopCalls    []string
	stopErr      error
	destroyCalls []string
	destroyErr   error

	statusReturn provider.VMState
	statusErr    error

	sshTarget provider.SSHTarget
	sshErr    error

	preflightErr error
}

func (f *fakeProvider) ID() string { return "fake" }
func (f *fakeProvider) Provision(_ context.Context, spec provider.ProvisionSpec) (provider.VM, error) {
	f.provisionCalls = append(f.provisionCalls, spec)
	if f.provisionErr != nil {
		return provider.VM{}, f.provisionErr
	}
	if f.provisionVM.ID == "" {
		return provider.VM{
			ID:        spec.Name,
			Name:      spec.Name,
			Provider:  "fake",
			Zone:      spec.Zone,
			Region:    "fake-region",
			CreatedAt: time.Now(),
		}, nil
	}
	return f.provisionVM, nil
}
func (f *fakeProvider) Start(_ context.Context, vmID string) error {
	f.startCalls = append(f.startCalls, vmID)
	return f.startErr
}
func (f *fakeProvider) Stop(_ context.Context, vmID string) error {
	f.stopCalls = append(f.stopCalls, vmID)
	return f.stopErr
}
func (f *fakeProvider) Destroy(_ context.Context, vmID string) error {
	f.destroyCalls = append(f.destroyCalls, vmID)
	return f.destroyErr
}
func (f *fakeProvider) Status(_ context.Context, vmID string) (provider.VMState, error) {
	if f.statusErr != nil {
		return provider.VMStateUnknown, f.statusErr
	}
	if f.statusReturn != "" {
		return f.statusReturn, nil
	}
	// Default models a "VM doesn't exist yet" GCP state. Tests that want
	// an existing VM (e.g. running, stopped) must set statusReturn
	// explicitly. Was VMStateRunning, which gave provision idempotency
	// the wrong signal — provision would skip its create call thinking
	// the VM already existed.
	return provider.VMStateUnknown, nil
}
func (f *fakeProvider) Snapshot(_ context.Context, _ string, _ string) (provider.SnapshotID, error) {
	return "", nil
}
func (f *fakeProvider) Cost(_ context.Context, _ string, _ provider.TimeRange) (provider.CostBreakdown, error) {
	return provider.CostBreakdown{}, nil
}
func (f *fakeProvider) SSHTarget(_ context.Context, _ string) (provider.SSHTarget, error) {
	if f.sshErr != nil {
		return provider.SSHTarget{}, f.sshErr
	}
	return f.sshTarget, nil
}
func (f *fakeProvider) Preflight(_ context.Context) error {
	return f.preflightErr
}

// makeLifecycleContext builds a Context with a fake provider, a temp state
// file, and a project entry. dir is the project dir (also used as project
// key in state.Projects).
func makeLifecycleContext(t *testing.T, fp *fakeProvider, withProject bool) (*Context, string) {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	cfg := config.Default()
	cfg.ProjectSlug = "webapp"
	cfg.Provider.Type = "gcp"
	cfg.Provider.Raw = map[string]any{
		"gcp": map[string]any{
			"machine_type": "e2-standard-2",
			"disk_size_gb": 100,
			"disk_type":    "pd-standard",
			"zone":         "us-central1-a",
		},
	}

	st := state.New()
	if withProject {
		st.SetProject(dir, state.ProjectState{
			Slug: "webapp", VMID: "webapp-vm", VMZone: "us-central1-a",
			ActiveSide: state.SideLocal,
		})
		st.VMs["webapp-vm"] = state.VMRecord{Provider: "fake", StateCache: "stopped"}
	}
	if err := st.Save(statePath); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	c := &Context{
		ProjectDir: dir,
		StatePath:  statePath,
		Config:     cfg,
		State:      st,
		Provider:   fp,
	}
	return c, dir
}

func writeFakeSSHKey(t *testing.T, dir string) string {
	t.Helper()
	keyPath := filepath.Join(dir, "id.pub")
	if err := os.WriteFile(keyPath, []byte("ssh-ed25519 AAAA fake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return keyPath
}

// --- Provision ---

func TestRunProvisionHappyPath(t *testing.T) {
	fp := &fakeProvider{}
	c, dir := makeLifecycleContext(t, fp, false)
	keyPath := writeFakeSSHKey(t, dir)
	var out bytes.Buffer
	err := RunProvision(context.Background(), &out, c, ProvisionOptions{SSHKeyPath: keyPath})
	if err != nil {
		t.Fatalf("RunProvision: %v", err)
	}
	if len(fp.provisionCalls) != 1 {
		t.Fatalf("expected 1 Provision call, got %d", len(fp.provisionCalls))
	}
	spec := fp.provisionCalls[0]
	if spec.Name != "webapp-vm" {
		t.Errorf("vm name = %q", spec.Name)
	}
	if spec.MachineType != "e2-standard-2" {
		t.Errorf("machine type = %q", spec.MachineType)
	}
	if spec.DiskGB != 100 {
		t.Errorf("disk gb = %d", spec.DiskGB)
	}
	if spec.StartImmediately {
		t.Error("StartImmediately should default to false (local-first)")
	}
	if !strings.Contains(string(spec.SSHKeyPub), "ssh-ed25519") {
		t.Errorf("SSHKeyPub looks wrong: %q", spec.SSHKeyPub)
	}
	// State updated.
	st, err := state.Open(c.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	ps, ok := st.GetProject(dir)
	if !ok || ps.VMID != "webapp-vm" {
		t.Errorf("project state not updated: ok=%v ps=%+v", ok, ps)
	}
	rec := st.VMs["webapp-vm"]
	if rec.StateCache != "stopped" {
		t.Errorf("VM state cache = %q, want stopped", rec.StateCache)
	}
}

func TestRunProvisionWithStartFlag(t *testing.T) {
	fp := &fakeProvider{}
	c, dir := makeLifecycleContext(t, fp, false)
	keyPath := writeFakeSSHKey(t, dir)
	var out bytes.Buffer
	if err := RunProvision(context.Background(), &out, c, ProvisionOptions{
		SSHKeyPath: keyPath, Start: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !fp.provisionCalls[0].StartImmediately {
		t.Error("--start should set StartImmediately=true")
	}
	st, _ := state.Open(c.StatePath)
	if st.VMs["webapp-vm"].StateCache != "running" {
		t.Errorf("VM state cache = %q, want running", st.VMs["webapp-vm"].StateCache)
	}
}

func TestRunProvisionMissingSSHKey(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, false)
	var out bytes.Buffer
	err := RunProvision(context.Background(), &out, c, ProvisionOptions{
		SSHKeyPath: "/no/such/key.pub",
	})
	if err == nil {
		t.Error("RunProvision accepted missing SSH key")
	}
}

func TestRunProvisionPropagatesProviderError(t *testing.T) {
	myErr := errors.New("quota exceeded")
	fp := &fakeProvider{provisionErr: myErr}
	c, dir := makeLifecycleContext(t, fp, false)
	keyPath := writeFakeSSHKey(t, dir)
	var out bytes.Buffer
	err := RunProvision(context.Background(), &out, c, ProvisionOptions{SSHKeyPath: keyPath})
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrap %v", err, myErr)
	}
}

// --- Up ---

func TestRunUpHappyPath(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "35.1.2.3", Port: 22, User: "u"}}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	if err := RunUp(context.Background(), &out, c, UpOptions{}); err != nil {
		t.Fatalf("RunUp: %v", err)
	}
	if len(fp.startCalls) != 1 || fp.startCalls[0] != "webapp-vm" {
		t.Errorf("Start calls = %v", fp.startCalls)
	}
	st, _ := state.Open(c.StatePath)
	if st.VMs["webapp-vm"].StateCache != "running" {
		t.Error("state cache should be running")
	}
	if st.VMs["webapp-vm"].ExternalIP != "35.1.2.3" {
		t.Errorf("ExternalIP = %q, want 35.1.2.3", st.VMs["webapp-vm"].ExternalIP)
	}
}

func TestRunUpNoProject(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, false)
	var out bytes.Buffer
	if err := RunUp(context.Background(), &out, c, UpOptions{}); err == nil {
		t.Error("RunUp without provisioned project should error")
	}
}

// --- Down ---

func TestRunDownHappyPath(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	if err := RunDown(context.Background(), &out, c); err != nil {
		t.Fatalf("RunDown: %v", err)
	}
	if len(fp.stopCalls) != 1 {
		t.Errorf("Stop calls = %v", fp.stopCalls)
	}
	st, _ := state.Open(c.StatePath)
	if st.VMs["webapp-vm"].StateCache != "stopped" {
		t.Error("state cache should be stopped")
	}
}

// --- Destroy ---

func TestRunDestroyWithSkipPrompt(t *testing.T) {
	fp := &fakeProvider{}
	c, dir := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	if err := RunDestroy(context.Background(), &out, strings.NewReader(""), c, true); err != nil {
		t.Fatalf("RunDestroy: %v", err)
	}
	if len(fp.destroyCalls) != 1 {
		t.Errorf("Destroy calls = %v", fp.destroyCalls)
	}
	st, _ := state.Open(c.StatePath)
	if _, ok := st.GetProject(dir); ok {
		t.Error("project should be removed from state")
	}
	if _, ok := st.VMs["webapp-vm"]; ok {
		t.Error("VM record should be removed from state")
	}
}

func TestRunDestroyConfirmYes(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	in := strings.NewReader("yes\n")
	if err := RunDestroy(context.Background(), &out, in, c, false); err != nil {
		t.Fatalf("RunDestroy: %v", err)
	}
	if len(fp.destroyCalls) != 1 {
		t.Error("Destroy not called after yes confirmation")
	}
}

func TestRunDestroyConfirmNo(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	in := strings.NewReader("n\n")
	err := RunDestroy(context.Background(), &out, in, c, false)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected aborted error, got %v", err)
	}
	if len(fp.destroyCalls) != 0 {
		t.Errorf("Destroy should not have been called on 'no'")
	}
}

// --- Attach ---

func TestPlanAttach(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "35.1.2.3", Port: 22, User: "landy"}}
	c, _ := makeLifecycleContext(t, fp, true)
	plan, err := planAttach(context.Background(), c)
	if err != nil {
		t.Fatalf("planAttach: %v", err)
	}
	if plan.SSHBin != "ssh" {
		t.Errorf("SSHBin = %q", plan.SSHBin)
	}
	args := plan.Args
	want := []string{"-t", "landy@35.1.2.3", "tmux", "attach-session", "-t", "webapp"}
	for _, w := range want {
		if !sliceContains(args, w) {
			t.Errorf("args missing %q: %v", w, args)
		}
	}
}

func TestPlanAttachCustomPort(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", Port: 2222, User: "u"}}
	c, _ := makeLifecycleContext(t, fp, true)
	plan, err := planAttach(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !sliceContains(plan.Args, "2222") {
		t.Errorf("custom port not in args: %v", plan.Args)
	}
}

func TestPlanAttachNoUser(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{Host: "h", Port: 22}}
	c, _ := makeLifecycleContext(t, fp, true)
	plan, err := planAttach(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	// No user@: just the host.
	if !sliceContains(plan.Args, "h") {
		t.Errorf("host not in args: %v", plan.Args)
	}
	for _, a := range plan.Args {
		if strings.Contains(a, "@") {
			t.Errorf("unexpected user@ in args: %q", a)
		}
	}
}

func TestPlanAttachNoVM(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, false)
	if _, err := planAttach(context.Background(), c); err == nil {
		t.Error("planAttach accepted unprovisioned project")
	}
}

func TestPlanAttachNoIP(t *testing.T) {
	fp := &fakeProvider{sshTarget: provider.SSHTarget{}}
	c, _ := makeLifecycleContext(t, fp, true)
	if _, err := planAttach(context.Background(), c); err == nil {
		t.Error("planAttach accepted empty IP")
	}
}

func sliceContains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
