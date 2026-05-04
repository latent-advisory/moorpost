package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// snapshotFakeProvider extends the lifecycle fake to record snapshot calls.
type snapshotFakeProvider struct {
	*fakeProvider
	snapshotCalls []snapshotCall
	snapshotErr   error
}

type snapshotCall struct {
	vmID, label string
}

func newSnapshotFake() *snapshotFakeProvider {
	return &snapshotFakeProvider{fakeProvider: &fakeProvider{}}
}

func (s *snapshotFakeProvider) Snapshot(_ context.Context, vmID string, label string) (snapshotID, error) { return "", nil }

// We need to override the embedded Snapshot to record. Go doesn't let us
// shadow method via embedding because both have the same signature; instead
// we provide our own and use a different runner helper for tests.

// Actually Go DOES allow this via embedding — the outer type's method shadows.
// But Snapshot's return type is provider.SnapshotID; we need to import that.

// Simpler: just record on the embedded fakeProvider by adding fields there.
// That's already the pattern lifecycle_test uses. Let's do that.

func TestRunResetHappyPath(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)

	// Write a fake SSH key in the project dir so re-provision can read it.
	keyPath := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(keyPath, []byte("ssh-ed25519 fake"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := RunReset(context.Background(), &out, strings.NewReader(""), c, ResetOptions{
		SkipPrompt: true,
		SkipSnapshot: false,
		SSHKeyPath:   keyPath,
	})
	if err != nil {
		t.Fatalf("RunReset: %v", err)
	}
	if len(fp.destroyCalls) != 1 {
		t.Errorf("Destroy calls = %d, want 1", len(fp.destroyCalls))
	}
	if len(fp.provisionCalls) != 1 {
		t.Errorf("Provision calls = %d, want 1 (re-provision after destroy)", len(fp.provisionCalls))
	}
	// Project state should still exist (slug preserved across reset).
	st, _ := state.Open(c.StatePath)
	ps, ok := st.GetProject(c.ProjectDir)
	if !ok || ps.Slug != "argus" {
		t.Errorf("project record lost across reset: ok=%v slug=%q", ok, ps.Slug)
	}
}

func TestRunResetSkipSnapshot(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	keyPath := filepath.Join(t.TempDir(), "id.pub")
	_ = os.WriteFile(keyPath, []byte("ssh-ed25519 fake"), 0o600)

	var out bytes.Buffer
	err := RunReset(context.Background(), &out, strings.NewReader(""), c, ResetOptions{
		SkipPrompt:   true,
		SkipSnapshot: true,
		SSHKeyPath:   keyPath,
	})
	if err != nil {
		t.Fatalf("RunReset: %v", err)
	}
	// Snapshot should NOT have been mentioned in output.
	if strings.Contains(out.String(), "Snapshotting") {
		t.Errorf("--skip-snapshot was set but Snapshot was attempted:\n%s", out.String())
	}
}

func TestRunResetPromptAbort(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, true)
	var out bytes.Buffer
	err := RunReset(context.Background(), &out, strings.NewReader("n\n"), c, ResetOptions{})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("err = %v, want aborted", err)
	}
	if len(fp.destroyCalls) != 0 {
		t.Error("Destroy should not be called after abort")
	}
}

func TestRunResetNoProject(t *testing.T) {
	fp := &fakeProvider{}
	c, _ := makeLifecycleContext(t, fp, false)
	var out bytes.Buffer
	if err := RunReset(context.Background(), &out, strings.NewReader(""), c, ResetOptions{SkipPrompt: true}); err == nil {
		t.Error("RunReset accepted unprovisioned project")
	}
}

func TestRunResetDestroyError(t *testing.T) {
	myErr := errors.New("api 500")
	fp := &fakeProvider{destroyErr: myErr}
	c, _ := makeLifecycleContext(t, fp, true)
	keyPath := filepath.Join(t.TempDir(), "id.pub")
	_ = os.WriteFile(keyPath, []byte("k"), 0o600)
	var out bytes.Buffer
	err := RunReset(context.Background(), &out, strings.NewReader(""), c, ResetOptions{
		SkipPrompt:   true,
		SkipSnapshot: true,
		SSHKeyPath:   keyPath,
	})
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrap %v", err, myErr)
	}
}

// snapshotID is exported via provider.SnapshotID; alias here for the embed.
type snapshotID = string

func TestReadYesNo(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y", true},
		{"Yes\r\n", true},
		{"n", false},
		{"\n", false},
		{"  ", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := readYesNo(strings.NewReader(tc.in)); got != tc.want {
				t.Errorf("readYesNo(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
