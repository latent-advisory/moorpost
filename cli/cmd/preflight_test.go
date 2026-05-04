package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionRunsPreflight(t *testing.T) {
	// fakeProvider with preflightErr set should reject before Provision.
	preflightErr := errors.New("compute API disabled\n  fix: gcloud services enable compute.googleapis.com")
	fp := &fakeProvider{preflightErr: preflightErr}
	c, dir := makeLifecycleContext(t, fp, false)
	keyPath := filepath.Join(dir, "id.pub")
	if err := os.WriteFile(keyPath, []byte("ssh-ed25519 fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := RunProvision(context.Background(), &out, c, ProvisionOptions{SSHKeyPath: keyPath})
	if err == nil {
		t.Fatal("RunProvision should have failed at preflight")
	}
	if !errors.Is(err, preflightErr) {
		t.Errorf("err = %v, want wrap of preflightErr", err)
	}
	if len(fp.provisionCalls) != 0 {
		t.Errorf("Provision should NOT be called when Preflight fails; calls = %d", len(fp.provisionCalls))
	}
}

func TestProvisionPreflightOKContinues(t *testing.T) {
	fp := &fakeProvider{}
	c, dir := makeLifecycleContext(t, fp, false)
	keyPath := filepath.Join(dir, "id.pub")
	if err := os.WriteFile(keyPath, []byte("ssh-ed25519 fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunProvision(context.Background(), &out, c, ProvisionOptions{SSHKeyPath: keyPath}); err != nil {
		t.Fatalf("RunProvision: %v", err)
	}
	if len(fp.provisionCalls) != 1 {
		t.Errorf("Provision should have been called; calls = %d", len(fp.provisionCalls))
	}
}

func TestCheckProviderPreflightOK(t *testing.T) {
	fp := &fakeProvider{}
	check := checkProviderPreflight(fp, "gcp")
	res := check(context.Background())
	if res.Severity != "ok" {
		t.Errorf("severity = %q, want ok", res.Severity)
	}
	if !strings.Contains(res.Name, "gcp") {
		t.Errorf("name should mention gcp: %q", res.Name)
	}
}

func TestCheckProviderPreflightFail(t *testing.T) {
	preflightErr := errors.New("Compute Engine API not enabled")
	fp := &fakeProvider{preflightErr: preflightErr}
	check := checkProviderPreflight(fp, "gcp")
	res := check(context.Background())
	if res.Severity != "fail" {
		t.Errorf("severity = %q, want fail", res.Severity)
	}
	if !strings.Contains(res.Hint, "Compute Engine API") {
		t.Errorf("hint should expose preflight error message: %q", res.Hint)
	}
}
