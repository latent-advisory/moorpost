package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSyncPluginEnvToRemoteMissingDirIsNoOp: when localPluginsDir does
// not exist, the function returns nil without shelling out to rsync/ssh.
// This is the "user has no plugins installed locally" case — handoff
// should silently skip plugin sync rather than failing.
func TestSyncPluginEnvToRemoteMissingDirIsNoOp(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist")
	// Sanity-check: the path really doesn't exist.
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s should not exist; stat err = %v", missing, err)
	}

	// Use a host that would cause an immediate ssh failure if we got that
	// far — proves the function never called out.
	err := syncPluginEnvToRemote(
		context.Background(),
		"nobody@127.0.0.1:0",
		"/dev/null",
		missing,
		"/home/moorpost",
	)
	if err != nil {
		t.Fatalf("syncPluginEnvToRemote on missing dir returned err = %v, want nil (no-op)", err)
	}
}

// TestSyncPluginEnvToRemoteShellsOutToRsync: when the local plugins dir
// exists, the function attempts to invoke rsync. We don't mock exec
// (matches the codebase's existing pattern — see lifecycle_test.go etc.,
// which all shell out and assert on real-process behaviour). Instead we
// point identityFile at /dev/null and use an unroutable RFC5737 test
// host so rsync fails fast with a wrapped "rsync plugins:" error.
//
// Limitation: this test only covers the rsync invocation. The path-
// rewrite ssh step never runs because rsync errors return early. That's
// acceptable — exec mocking would be heavier than the code under test.
func TestSyncPluginEnvToRemoteShellsOutToRsync(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed on this host; skipping shell-out test")
	}

	tmp := t.TempDir()
	pluginsDir := filepath.Join(tmp, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("setup: mkdir plugins: %v", err)
	}
	// Drop a token file so rsync has something to attempt to transfer.
	if err := os.WriteFile(filepath.Join(pluginsDir, "marker.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: write marker: %v", err)
	}

	// 192.0.2.0/24 is RFC5737 TEST-NET-1 (documentation/test). The
	// connection will fail fast (refused or timed out via ConnectTimeout=10).
	// Bound the ctx tightly anyway so the test never hangs in CI.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := syncPluginEnvToRemote(
		ctx,
		"nobody@192.0.2.1",
		"/dev/null",
		pluginsDir,
		"/home/moorpost",
	)
	if err == nil {
		t.Fatal("expected rsync to fail against unroutable host; got nil error")
	}
	// Function wraps rsync errors as "rsync plugins: ...". Confirm we
	// hit that branch (and not e.g. an os.Stat or os.UserHomeDir error).
	if !strings.Contains(err.Error(), "rsync plugins") {
		t.Errorf("err = %v, want wrapped 'rsync plugins:' message", err)
	}
}
