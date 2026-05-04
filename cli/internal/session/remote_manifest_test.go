package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureSSH records exec.Run calls and returns canned outputs.
type captureSSH struct {
	calls    []captureSSHCall
	stdout   []byte
	stderr   []byte
	exitCode int
	runErr   error
}

type captureSSHCall struct {
	name  string
	args  []string
	stdin []byte
}

func (c *captureSSH) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, []byte, int, error) {
	c.calls = append(c.calls, captureSSHCall{name: name, args: args, stdin: stdin})
	return c.stdout, c.stderr, c.exitCode, c.runErr
}

func (c *captureSSH) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func TestRemoteManifest_ValidatesArgs(t *testing.T) {
	c := &captureSSH{}
	if _, err := RemoteManifest(context.Background(), c, "", "/path"); err == nil {
		t.Error("empty sshHost should error")
	}
	if _, err := RemoteManifest(context.Background(), c, "host", ""); err == nil {
		t.Error("empty remoteRoot should error")
	}
	if _, err := RemoteManifest(context.Background(), nil, "host", "/path"); err == nil {
		t.Error("nil exec should error")
	}
}

func TestRemoteManifest_PassesExpectedSSHArgs(t *testing.T) {
	c := &captureSSH{
		stdout: []byte("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n"),
	}
	_, err := RemoteManifest(context.Background(), c, "user@host", "/home/user/state")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(c.calls))
	}
	args := c.calls[0].args
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout=10", "user@host", "bash", "-s"} {
		if !argsContainSSH(args, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	stdin := string(c.calls[0].stdin)
	for _, want := range []string{"DIR=", "/home/user/state", "find", "sort", "sha256sum", "cut -d' ' -f1"} {
		if !strings.Contains(stdin, want) {
			t.Errorf("stdin missing %q\nstdin:\n%s", want, stdin)
		}
	}
}

func TestRemoteManifest_ParsesHexHash(t *testing.T) {
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	c := &captureSSH{stdout: []byte(want + "\n")}
	got, err := RemoteManifest(context.Background(), c, "host", "/path")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRemoteManifest_StripsWhitespace(t *testing.T) {
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	c := &captureSSH{stdout: []byte("  " + want + "  \n\n")}
	got, err := RemoteManifest(context.Background(), c, "host", "/path")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRemoteManifest_RejectsMalformedOutput(t *testing.T) {
	cases := [][]byte{
		[]byte(""),                                 // empty
		[]byte("not-a-hash"),                       // gibberish
		[]byte("e3b0c44298fc1c1"),                  // too short
		[]byte("XXX0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"), // bad chars
	}
	for _, out := range cases {
		c := &captureSSH{stdout: out}
		_, err := RemoteManifest(context.Background(), c, "host", "/path")
		if err == nil {
			t.Errorf("expected error for output %q", out)
		}
	}
}

func TestRemoteManifest_PropagatesNonZeroExit(t *testing.T) {
	c := &captureSSH{
		stderr:   []byte("ssh: connection refused"),
		exitCode: 255,
	}
	_, err := RemoteManifest(context.Background(), c, "host", "/path")
	if err == nil {
		t.Fatal("expected error on non-zero ssh exit")
	}
	if !strings.Contains(err.Error(), "ssh exit 255") {
		t.Errorf("err = %v, want exit code in message", err)
	}
}

func TestRemoteManifest_PropagatesRunError(t *testing.T) {
	c := &captureSSH{runErr: errors.New("network unreachable")}
	_, err := RemoteManifest(context.Background(), c, "host", "/path")
	if err == nil {
		t.Fatal("expected error when exec returns error")
	}
	if !strings.Contains(err.Error(), "ssh exec") {
		t.Errorf("err = %v, want 'ssh exec' wrapper", err)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":          "'plain'",
		"with space":     "'with space'",
		"can't":          `'can'\''t'`,
		"a'b'c":          `'a'\''b'\''c'`,
		"":               "''",
		"/home/user/x":   "'/home/user/x'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLocalAndRemoteHashesMatch_RealBash is an integration test that runs
// the actual remote shell pipeline locally (against a tempdir). Verifies
// that LocalManifest and the bash pipeline produce byte-identical hashes
// for the same filesystem snapshot. Skipped if bash, find, awk, sort, or
// sha256sum aren't on PATH.
//
// This is the load-bearing test: it's the parity check between the two
// manifest paths. If this passes, we know iter 36's design works end-to-end.
func TestLocalAndRemoteHashesMatch_RealBash(t *testing.T) {
	for _, tool := range []string{"bash", "find", "awk", "sort", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("missing tool %q on PATH; skipping parity check", tool)
		}
	}
	if _, err := exec.LookPath("gfind"); err == nil {
		// macOS GNU find via Homebrew. Detected but not used — bash on
		// macOS finds /usr/bin/find (BSD) which lacks -printf. Skip.
		t.Skip("test requires GNU find without macOS BSD-find shadow; skipping on this host")
	}
	// Test on macOS: BSD find doesn't support -printf. Skip in that case.
	out, _ := exec.Command("find", "--version").Output()
	if !strings.Contains(string(out), "GNU") {
		t.Skip("non-GNU find on PATH; this test requires GNU find")
	}

	dir := t.TempDir()
	mtime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	// Build a non-trivial tree.
	for _, sub := range []string{"a.txt", "sub/b.txt", "sub/deep/c.txt"} {
		full := filepath.Join(dir, sub)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte("payload-"+sub), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	localHash, err := LocalManifest(dir)
	if err != nil {
		t.Fatalf("LocalManifest: %v", err)
	}

	// Run the bash pipeline directly (not via ssh) by feeding it to bash.
	script := buildRemoteScript(dir)
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	bashOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash pipeline failed: %v\noutput: %s", err, bashOut)
	}
	remoteHash := strings.TrimSpace(string(bashOut))

	if localHash != remoteHash {
		t.Errorf("hash mismatch:\n  local:  %s\n  remote: %s", localHash, remoteHash)
	}
}

func argsContainSSH(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
