package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(data)
}

func TestUpsertOnNonexistentFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	m := NewManager(p)
	err := m.Upsert("argus-vm", HostEntry{
		HostName: "35.1.2.3", User: "landytang",
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	body := read(t, p)
	for _, want := range []string{
		"# >>> moorpost begin: argus-vm >>>",
		"Host argus-vm",
		"HostName 35.1.2.3",
		"User landytang",
		"# <<< moorpost end: argus-vm <<<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("file missing %q\n--- contents ---\n%s", want, body)
		}
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestUpsertIdempotent(t *testing.T) {
	p := writeTemp(t, "Host other\n  HostName x.example\n")
	m := NewManager(p)
	entry := HostEntry{HostName: "1.2.3.4", User: "u", Port: 2222}
	if err := m.Upsert("h", entry); err != nil {
		t.Fatal(err)
	}
	first := read(t, p)
	if err := m.Upsert("h", entry); err != nil {
		t.Fatal(err)
	}
	if read(t, p) != first {
		t.Errorf("Upsert with identical entry mutated the file:\nbefore:\n%s\nafter:\n%s", first, read(t, p))
	}
}

func TestUpsertReplacesExistingBlock(t *testing.T) {
	p := writeTemp(t, "# user comment\nHost other\n  HostName other.example\n")
	m := NewManager(p)
	if err := m.Upsert("h", HostEntry{HostName: "v1.example"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Upsert("h", HostEntry{HostName: "v2.example"}); err != nil {
		t.Fatal(err)
	}
	body := read(t, p)
	if strings.Contains(body, "v1.example") {
		t.Errorf("old block contents persisted:\n%s", body)
	}
	if !strings.Contains(body, "v2.example") {
		t.Errorf("new block contents missing:\n%s", body)
	}
	if !strings.Contains(body, "# user comment") || !strings.Contains(body, "Host other") {
		t.Errorf("user content lost:\n%s", body)
	}
	if strings.Count(body, "# >>> moorpost begin: h >>>") != 1 {
		t.Errorf("found %d begin markers, want 1:\n%s", strings.Count(body, "# >>> moorpost begin: h >>>"), body)
	}
}

func TestUpsertMultipleHostsCoexist(t *testing.T) {
	p := writeTemp(t, "")
	m := NewManager(p)
	if err := m.Upsert("a", HostEntry{HostName: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Upsert("b", HostEntry{HostName: "beta"}); err != nil {
		t.Fatal(err)
	}
	body := read(t, p)
	for _, want := range []string{"alpha", "beta", "begin: a >>>", "begin: b >>>", "end: a <<<", "end: b <<<"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in:\n%s", want, body)
		}
	}
}

func TestUpsertPreservesUserContent(t *testing.T) {
	original := `# my notes
Host bastion
  HostName bastion.corp
  User me
  ProxyJump nope

Host *.internal
  ForwardAgent yes
`
	p := writeTemp(t, original)
	m := NewManager(p)
	if err := m.Upsert("argus-vm", HostEntry{HostName: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	body := read(t, p)
	if !strings.Contains(body, original) {
		// We're permissive: the block may add a leading blank line. Check
		// each user line is still present in order.
		for _, line := range strings.Split(strings.TrimRight(original, "\n"), "\n") {
			if !strings.Contains(body, line) {
				t.Errorf("user line lost: %q\n--- file ---\n%s", line, body)
			}
		}
	}
}

func TestRemoveBlock(t *testing.T) {
	p := writeTemp(t, "")
	m := NewManager(p)
	if err := m.Upsert("h", HostEntry{HostName: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("h"); err != nil {
		t.Fatal(err)
	}
	body := read(t, p)
	if strings.Contains(body, "moorpost begin: h") {
		t.Errorf("block not removed:\n%s", body)
	}
}

func TestRemoveAbsentIsIdempotent(t *testing.T) {
	p := writeTemp(t, "Host other\n  HostName x\n")
	m := NewManager(p)
	if err := m.Remove("h"); err != nil {
		t.Errorf("Remove on absent host returned %v, want nil", err)
	}
}

func TestRemovePreservesUserContent(t *testing.T) {
	user := "Host other\n  HostName other.example\n"
	p := writeTemp(t, user)
	m := NewManager(p)
	if err := m.Upsert("h", HostEntry{HostName: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("h"); err != nil {
		t.Fatal(err)
	}
	body := read(t, p)
	if !strings.Contains(body, "Host other") || !strings.Contains(body, "HostName other.example") {
		t.Errorf("user content lost:\n%s", body)
	}
	if strings.Contains(body, "moorpost") {
		t.Errorf("moorpost markers persisted:\n%s", body)
	}
}

func TestHas(t *testing.T) {
	p := writeTemp(t, "")
	m := NewManager(p)
	if has, err := m.Has("h"); err != nil || has {
		t.Errorf("Has on absent: ok=%v err=%v, want false/nil", has, err)
	}
	if err := m.Upsert("h", HostEntry{HostName: "x"}); err != nil {
		t.Fatal(err)
	}
	if has, err := m.Has("h"); err != nil || !has {
		t.Errorf("Has after Upsert: ok=%v err=%v, want true/nil", has, err)
	}
}

func TestUpsertEmptyHostRejected(t *testing.T) {
	m := NewManager(writeTemp(t, ""))
	if err := m.Upsert("", HostEntry{HostName: "x"}); err == nil {
		t.Error("Upsert accepted empty host")
	}
}

func TestRenderEmitsExpectedDirectives(t *testing.T) {
	e := HostEntry{
		HostName:             "x",
		User:                 "u",
		Port:                 22,
		IdentityFile:         "~/.ssh/k",
		ServerAliveInterval:  30,
		ServerAliveCountMax:  6,
		ControlMaster:        "auto",
		ControlPath:          "~/.moorpost/cm/%C",
		ControlPersist:       "10m",
		StrictHostKeyChecking: "accept-new",
		UserKnownHostsFile:   "~/.moorpost/known_hosts",
	}
	out := e.Render()
	for _, want := range []string{
		"  HostName x",
		"  User u",
		"  Port 22",
		"  IdentityFile ~/.ssh/k",
		"  ServerAliveInterval 30",
		"  ServerAliveCountMax 6",
		"  ControlMaster auto",
		"  ControlPath ~/.moorpost/cm/%C",
		"  ControlPersist 10m",
		"  StrictHostKeyChecking accept-new",
		"  UserKnownHostsFile ~/.moorpost/known_hosts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderOmitsEmptyFields(t *testing.T) {
	e := HostEntry{HostName: "x"}
	out := e.Render()
	if strings.Contains(out, "User") || strings.Contains(out, "Port") {
		t.Errorf("Render emitted blank fields:\n%s", out)
	}
}

func TestUpsertMalformedConfigError(t *testing.T) {
	// Begin marker but no end marker — should error rather than silently
	// truncating the file.
	p := writeTemp(t, "# >>> moorpost begin: h >>>\nHost h\n  HostName x\n# (no end marker)\n")
	m := NewManager(p)
	if err := m.Upsert("h", HostEntry{HostName: "y"}); err == nil {
		t.Error("Upsert accepted file with begin-but-no-end markers")
	}
}

func TestUpsertPermissionsPreserved(t *testing.T) {
	p := writeTemp(t, "Host other\n")
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(p)
	if err := m.Upsert("h", HostEntry{HostName: "x"}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 0644 preserved", info.Mode().Perm())
	}
}
