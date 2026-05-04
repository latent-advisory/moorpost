//go:build gcp_e2e
// +build gcp_e2e

// E2E test against real GCP. Run with: `go test -tags=gcp_e2e -timeout=10m ./internal/provider/gcp/...`
//
// Cost guardrails (per memory/gcp_setup.md):
//   - Single VM at a time (pre-flight asserts zero existing moorpost-test instances)
//   - e2-small machine
//   - Tagged moorpost-test for orphan-sweep
//   - Cleanup runs even on test failure (t.Cleanup)
//
// Required env (or defaults):
//   MOORPOST_E2E_PROJECT (default: latent-advisory)
//   MOORPOST_E2E_ZONE    (default: us-central1-a)

package gcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/bootstrap"
	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// generateEd25519PubKey returns a fresh OpenSSH-format public key with no
// private-key persistence — we only need the public for VM provisioning.
func generateEd25519PubKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 keygen: %v", err)
	}
	// Wire format per RFC 4253:
	//   string "ssh-ed25519"
	//   string <pubkey-bytes>
	keyBytes := []byte("ssh-ed25519")
	wire := encodeString(keyBytes)
	wire = append(wire, encodeString(pub)...)
	encoded := base64.StdEncoding.EncodeToString(wire)
	return "ssh-ed25519 " + encoded + " moorpost-e2e@local"
}

func encodeString(b []byte) []byte {
	out := make([]byte, 4+len(b))
	out[0] = byte(len(b) >> 24)
	out[1] = byte(len(b) >> 16)
	out[2] = byte(len(b) >> 8)
	out[3] = byte(len(b))
	copy(out[4:], b)
	return out
}

// runGcloud is a thin helper for pre/post-flight assertions that don't go
// through the Provider abstraction.
func runGcloud(ctx context.Context, args ...string) (string, string, int, error) {
	e := mpexec.New()
	stdout, stderr, code, err := e.Run(ctx, "gcloud", args, nil)
	return string(stdout), string(stderr), code, err
}

func TestGCPProvision_E2E(t *testing.T) {
	project := envOr("MOORPOST_E2E_PROJECT", "latent-advisory")
	zone := envOr("MOORPOST_E2E_ZONE", "us-central1-a")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// --- Pre-flight ---
	t.Logf("pre-flight: project=%s zone=%s", project, zone)
	stdout, _, code, err := runGcloud(ctx,
		"compute", "instances", "list",
		"--project="+project,
		"--filter=tags.items:moorpost-test",
		"--format=value(name)")
	if err != nil || code != 0 {
		t.Fatalf("pre-flight gcloud list: err=%v code=%d", err, code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("pre-flight: orphan moorpost-test instances found:\n%s\nPlease destroy manually before running this test.", stdout)
	}
	t.Log("pre-flight ok: zero moorpost-test instances")

	// --- Construct provider ---
	p, err := NewWithOptions(Options{
		Project: project,
		Zone:    zone,
		SSHUser: "moorpost",
	})
	if err != nil {
		t.Fatalf("New gcp provider: %v", err)
	}

	pubKey := generateEd25519PubKey(t)
	bootScript, err := bootstrap.Render(bootstrap.BootstrapVars{
		ProjectSlug:  "e2e",
		LocalAbsPath: "/Users/landytang/Documents/Claude/Projects/AI M&A/code/argus",
		RemoteUser:   "moorpost",
	})
	if err != nil {
		t.Fatalf("bootstrap.Render: %v", err)
	}

	vmName := fmt.Sprintf("moorpost-test-%d", time.Now().Unix())
	t.Logf("provisioning VM: %s (e2-small, %s)", vmName, zone)
	spec := provider.ProvisionSpec{
		Name:             vmName,
		Zone:             zone,
		MachineType:      "e2-small",
		DiskGB:           20,
		DiskType:         "pd-standard",
		SSHKeyPub:        pubKey,
		Tags:             []string{"moorpost-test"},
		StartImmediately: true, // we want it RUNNING for the status check
		BootstrapScript:  bootScript,
	}

	// --- Cleanup is mandatory ---
	t.Cleanup(func() {
		// Use a fresh context with its own timeout so cleanup runs even if
		// the parent ctx is canceled by the timeout.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancelCleanup()
		t.Logf("CLEANUP: destroying %s", vmName)
		if err := p.Destroy(cleanupCtx, vmName); err != nil {
			t.Errorf("CLEANUP FAILED: %v — please run: gcloud compute instances delete %s --zone=%s --project=%s --quiet", err, vmName, zone, project)
		}
		// Verify the VM is gone.
		stdout, _, _, _ := runGcloud(cleanupCtx,
			"compute", "instances", "list",
			"--project="+project,
			"--filter=tags.items:moorpost-test",
			"--format=value(name)")
		if strings.TrimSpace(stdout) != "" {
			t.Errorf("CLEANUP VERIFY FAILED: orphan instances still present:\n%s", stdout)
		} else {
			t.Log("cleanup verified: zero moorpost-test instances")
		}
	})

	// --- Provision ---
	provisionStart := time.Now()
	vm, err := p.Provision(ctx, spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("provisioned in %s: vmID=%s ip=%s", time.Since(provisionStart).Round(time.Second), vm.ID, vm.ExternalIP)
	if vm.ID == "" {
		t.Fatal("Provision returned empty VM ID")
	}
	if vm.Provider != "gcp" {
		t.Errorf("vm.Provider = %q, want gcp", vm.Provider)
	}

	// --- Wait for RUNNING ---
	deadline := time.Now().Add(90 * time.Second)
	var lastState provider.VMState
	for time.Now().Before(deadline) {
		st, err := p.Status(ctx, vm.ID)
		if err != nil {
			t.Logf("Status (transient): %v", err)
		} else {
			lastState = st
			t.Logf("status: %s", st)
			if st == provider.VMStateRunning {
				break
			}
		}
		time.Sleep(5 * time.Second)
	}
	if lastState != provider.VMStateRunning {
		t.Errorf("VM did not reach RUNNING within 90s; last seen: %s", lastState)
	}

	// --- SSH target resolution ---
	tgt, err := p.SSHTarget(ctx, vm.ID)
	if err != nil {
		t.Errorf("SSHTarget: %v", err)
	}
	if tgt.Host == "" {
		t.Errorf("SSHTarget returned empty Host")
	} else {
		t.Logf("ssh target: %s@%s:%d", tgt.User, tgt.Host, tgt.Port)
	}

	t.Logf("✓ E2E provision happy path passed (cost guardrails honored; cleanup deferred)")
}
