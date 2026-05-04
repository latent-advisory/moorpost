package gcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
)

// captureExec records calls and returns a sequence of canned responses
// (sticks on the last one). Lighter than FakeExecutor's strict scripting.
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

func newProvider(t *testing.T, c *captureExec) *engine {
	t.Helper()
	if c == nil {
		c = &captureExec{}
	}
	p, err := NewWithOptions(Options{
		Executor: c,
		Binary:   "gcloud",
		Project:  "latent-advisory",
		Region:   "us-central1",
		Zone:     "us-central1-a",
		SSHUser:  "landytang",
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return p.(*engine)
}

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func argsHaveFlag(args []string, flag, want string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == want {
			return true
		}
	}
	return false
}

func TestRegisteredInRegistry(t *testing.T) {
	ids := provider.List()
	found := false
	for _, id := range ids {
		if id == ProviderID {
			found = true
		}
	}
	if !found {
		t.Errorf("gcp not registered: List() = %v", ids)
	}
}

func TestNewRequiresProject(t *testing.T) {
	if _, err := NewWithOptions(Options{}); err == nil {
		t.Error("NewWithOptions accepted empty project")
	}
}

func TestNewDefaultsZoneFromRegion(t *testing.T) {
	p, err := NewWithOptions(Options{Project: "x", Region: "us-west1"})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if e := p.(*engine).zone; e != "us-west1-a" {
		t.Errorf("zone = %q, want us-west1-a", e)
	}
}

func TestProvisionArgvAssembly(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0, stdout: []byte("NAME ZONE ... 10.0.0.1 35.1.2.3 RUNNING\n")}}}
	p := newProvider(t, c)
	vm, err := p.Provision(context.Background(), provider.ProvisionSpec{
		Name:             "argus-vm",
		Zone:             "us-central1-a",
		MachineType:      "e2-standard-2",
		DiskGB:           100,
		DiskType:         "pd-standard",
		Image:            "ubuntu-2404-lts-amd64",
		SSHKeyPub:        "ssh-ed25519 AAAA...",
		Tags:             []string{"moorpost", "argus"},
		StartImmediately: true,
		BootstrapScript:  "#!/bin/bash\necho hi\n",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if vm.ID != "argus-vm" {
		t.Errorf("vm.ID = %q", vm.ID)
	}
	if vm.Provider != "gcp" {
		t.Errorf("vm.Provider = %q", vm.Provider)
	}
	if vm.Zone != "us-central1-a" {
		t.Errorf("vm.Zone = %q", vm.Zone)
	}
	if vm.Region != "us-central1" {
		t.Errorf("vm.Region = %q (deriveRegion expected us-central1)", vm.Region)
	}
	if vm.ExternalIP != "35.1.2.3" {
		t.Errorf("vm.ExternalIP = %q, want 35.1.2.3", vm.ExternalIP)
	}

	args := c.calls[0].args
	for _, want := range []string{
		"compute", "instances", "create", "argus-vm",
		"e2-standard-2", "100GB", "pd-standard",
		"ubuntu-2404-lts-amd64", "ubuntu-os-cloud",
		"latent-advisory", "us-central1-a",
		"moorpost,argus",
	} {
		if !argsContain(args, want) {
			t.Errorf("args missing %q:\n%v", want, args)
		}
	}
	// stdin should carry the bootstrap script.
	if string(c.calls[0].stdin) != "#!/bin/bash\necho hi\n" {
		t.Errorf("bootstrap stdin = %q", c.calls[0].stdin)
	}
}

func TestProvisionRequiresSSHKey(t *testing.T) {
	p := newProvider(t, nil)
	_, err := p.Provision(context.Background(), provider.ProvisionSpec{
		Name: "x",
	})
	if err == nil {
		t.Error("Provision accepted empty SSHKeyPub")
	}
}

func TestProvisionRequiresName(t *testing.T) {
	p := newProvider(t, nil)
	_, err := p.Provision(context.Background(), provider.ProvisionSpec{
		SSHKeyPub: "ssh-ed25519 X",
	})
	if err == nil {
		t.Error("Provision accepted empty Name")
	}
}

func TestProvisionDefaults(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0}}}
	p := newProvider(t, c)
	_, err := p.Provision(context.Background(), provider.ProvisionSpec{
		Name:             "x",
		SSHKeyPub:        "ssh-ed25519 X",
		StartImmediately: true,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	args := c.calls[0].args
	// Defaults should kick in.
	for _, want := range []string{"e2-standard-2", "100GB", "pd-standard", defaultImageFamily} {
		if !argsContain(args, want) {
			t.Errorf("default missing in args: %q\n%v", want, args)
		}
	}
}

func TestProvisionStopAfterCreate(t *testing.T) {
	// StartImmediately=false should issue a stop after the create succeeds.
	c := &captureExec{resp: []captureCall{
		{exitCode: 0, stdout: []byte("ok")},
		{exitCode: 0},
	}}
	p := newProvider(t, c)
	_, err := p.Provision(context.Background(), provider.ProvisionSpec{
		Name:      "x",
		SSHKeyPub: "ssh-ed25519 X",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(c.calls) != 2 {
		t.Fatalf("expected 2 calls (create + stop), got %d", len(c.calls))
	}
	if !argsContain(c.calls[1].args, "stop") {
		t.Errorf("second call should be 'stop': %v", c.calls[1].args)
	}
}

func TestStartIdempotentOnAlreadyRunning(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("Instance is already running")}}}
	p := newProvider(t, c)
	if err := p.Start(context.Background(), "x"); err != nil {
		t.Errorf("Start on running = %v, want nil", err)
	}
}

func TestStartNotFound(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("Instance 'x' was not found")}}}
	p := newProvider(t, c)
	if err := p.Start(context.Background(), "x"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("Start not-found = %v, want ErrNotFound", err)
	}
}

func TestStopIdempotentOnAlreadyStopped(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("Instance is already TERMINATED")}}}
	p := newProvider(t, c)
	if err := p.Stop(context.Background(), "x"); err != nil {
		t.Errorf("Stop on stopped = %v, want nil", err)
	}
}

func TestDestroyIdempotent(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("Instance 'x' was not found")}}}
	p := newProvider(t, c)
	if err := p.Destroy(context.Background(), "x"); err != nil {
		t.Errorf("Destroy on missing = %v, want nil (idempotent)", err)
	}
}

func TestStatusParsing(t *testing.T) {
	tests := []struct {
		stdout string
		want   provider.VMState
	}{
		{"PROVISIONING\n", provider.VMStateProvisioning},
		{"STAGING", provider.VMStateProvisioning},
		{"RUNNING\n", provider.VMStateRunning},
		{"STOPPING", provider.VMStateStopping},
		{"SUSPENDING", provider.VMStateStopping},
		{"TERMINATED\n", provider.VMStateStopped},
		{"SUSPENDED", provider.VMStateStopped},
		{"REPAIRING", provider.VMStateError},
		{"", provider.VMStateUnknown},
		{"UNKNOWN-FUTURE-STATE", provider.VMStateUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.stdout, func(t *testing.T) {
			c := &captureExec{resp: []captureCall{{exitCode: 0, stdout: []byte(tc.stdout)}}}
			p := newProvider(t, c)
			got, err := p.Status(context.Background(), "x")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusNotFound(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 1, stderr: []byte("Instance 'x' was not found")}}}
	p := newProvider(t, c)
	_, err := p.Status(context.Background(), "x")
	if !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSSHTarget(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0, stdout: []byte("35.1.2.3\n")}}}
	p := newProvider(t, c)
	tgt, err := p.SSHTarget(context.Background(), "x")
	if err != nil {
		t.Fatalf("SSHTarget: %v", err)
	}
	if tgt.Host != "35.1.2.3" {
		t.Errorf("Host = %q", tgt.Host)
	}
	if tgt.Port != 22 {
		t.Errorf("Port = %d", tgt.Port)
	}
	if tgt.User != "landytang" {
		t.Errorf("User = %q", tgt.User)
	}
}

func TestSSHTargetEmptyIPErrors(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0, stdout: []byte("\n")}}}
	p := newProvider(t, c)
	if _, err := p.SSHTarget(context.Background(), "x"); err == nil {
		t.Error("SSHTarget accepted empty IP")
	}
}

func TestSnapshotLabelSanitization(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"My_Pre Handoff!", "my-pre-handoff"},
		{"argus", "argus"},
		{"--leading", "leading"},
		{"trailing--", "trailing"},
		{"UPPERCASE", "uppercase"},
		{"with$special#chars", "with-special-chars"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := sanitizeSnapshotLabel(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSnapshotIssuesCommand(t *testing.T) {
	c := &captureExec{resp: []captureCall{{exitCode: 0}}}
	p := newProvider(t, c)
	id, err := p.Snapshot(context.Background(), "argus-vm", "Pre Handoff")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.HasPrefix(string(id), "argus-vm-pre-handoff-") {
		t.Errorf("snapshot id = %q, want argus-vm-pre-handoff-<stamp>", id)
	}
	args := c.calls[0].args
	for _, want := range []string{"compute", "disks", "snapshot", "argus-vm", "--snapshot-names"} {
		if !argsContain(args, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
}

func TestCostKnownMachineType(t *testing.T) {
	c := &captureExec{resp: []captureCall{
		{exitCode: 0, stdout: []byte("https://www.googleapis.com/compute/v1/projects/x/zones/us-central1-a/machineTypes/e2-standard-2\n")},
	}}
	p := newProvider(t, c)
	period := provider.TimeRange{Start: time.Now().Add(-1 * time.Hour), End: time.Now()}
	cost, err := p.Cost(context.Background(), "x", period)
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if !cost.IsEstimate {
		t.Error("IsEstimate should be true for v0.1 list-price cost")
	}
	if cost.Compute == 0 || cost.Total == 0 {
		t.Errorf("expected non-zero cost, got %+v", cost)
	}
	// e2-standard-2 ≈ $0.067/hr; 1 hour ≈ $0.067.
	if cost.Compute < 0.05 || cost.Compute > 0.10 {
		t.Errorf("Compute cost %.4f looks wrong for 1hr e2-standard-2", cost.Compute)
	}
}

func TestCostUnknownMachineType(t *testing.T) {
	c := &captureExec{resp: []captureCall{
		{exitCode: 0, stdout: []byte("custom-99-99999\n")},
	}}
	p := newProvider(t, c)
	_, err := p.Cost(context.Background(), "x", provider.TimeRange{End: time.Now()})
	if !errors.Is(err, provider.ErrCostUnavailable) {
		t.Errorf("err = %v, want ErrCostUnavailable", err)
	}
}

func TestDeriveRegion(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"us-central1-a", "us-central1"},
		{"us-west1-c", "us-west1"},
		{"europe-north1-b", "europe-north1"},
		{"", ""},
		{"weirdformat", "weirdformat"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := deriveRegion(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewFromConfigMap(t *testing.T) {
	cfg := map[string]any{
		"project":  "p",
		"region":   "us-east1",
		"zone":     "us-east1-b",
		"ssh_user": "alice",
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.ID() != ProviderID {
		t.Errorf("ID = %q", p.ID())
	}
}

func TestExecError(t *testing.T) {
	myErr := errors.New("gcloud not found")
	c := &captureExec{resp: []captureCall{{err: myErr}}}
	p := newProvider(t, c)
	if err := p.Start(context.Background(), "x"); !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrapping %v", err, myErr)
	}
}

// silence unused import
var _ = mpexec.New
