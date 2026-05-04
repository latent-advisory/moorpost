package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalManifest_MissingRoot_ReturnsEmptyHash(t *testing.T) {
	got, err := LocalManifest("/tmp/definitely-does-not-exist-moorpost-test")
	if err != nil {
		t.Fatalf("missing root should not error: %v", err)
	}
	want := hashEmpty()
	if got != want {
		t.Errorf("missing root: got %s, want %s", got, want)
	}
}

func TestLocalManifest_EmptyRoot_ReturnsEmptyHash(t *testing.T) {
	dir := t.TempDir()
	got, err := LocalManifest(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != hashEmpty() {
		t.Errorf("empty dir hash = %s, want empty hash %s", got, hashEmpty())
	}
}

func TestLocalManifest_Empty_DeterministicAcrossCalls(t *testing.T) {
	a, err := LocalManifest("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LocalManifest("")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("empty manifest non-deterministic: %s vs %s", a, b)
	}
}

func TestLocalManifest_SingleFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "session.json")
	if err := os.WriteFile(f, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LocalManifest(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got == hashEmpty() {
		t.Error("non-empty dir produced empty hash")
	}
}

func TestLocalManifest_NestedFiles_StableOrdering(t *testing.T) {
	// Build the same logical state twice in different orders; expect
	// identical hashes.
	build := func(t *testing.T) string {
		dir := t.TempDir()
		// Create files with explicit identical mtimes so the test is
		// deterministic across runs. Use a fixed past time so we don't
		// depend on filesystem now() resolution.
		mtime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		writeWithMtime(t, filepath.Join(dir, "a.txt"), "alpha", mtime)
		_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
		writeWithMtime(t, filepath.Join(dir, "sub", "b.txt"), "beta", mtime)
		writeWithMtime(t, filepath.Join(dir, "sub", "c.txt"), "gamma", mtime)
		return dir
	}
	d1 := build(t)
	d2 := build(t)
	h1, _ := LocalManifest(d1)
	h2, _ := LocalManifest(d2)
	if h1 != h2 {
		t.Errorf("identical content produced different hashes: %s vs %s", h1, h2)
	}
}

func TestLocalManifest_MtimeSensitivity(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(f, t1, t1); err != nil {
		t.Fatal(err)
	}
	a, err := LocalManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	t2 := t1.Add(time.Second)
	if err := os.Chtimes(f, t2, t2); err != nil {
		t.Fatal(err)
	}
	b, err := LocalManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("manifest insensitive to mtime change: both = %s", a)
	}
}

func TestLocalManifest_SizeSensitivity(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LocalManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := LocalManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("manifest insensitive to size change: both = %s", a)
	}
}

func TestLocalManifest_RelativePath_NotAbsolute(t *testing.T) {
	// Two different directories with the same relative-tree should yield
	// the same hash (paths should be relative to root).
	d1 := t.TempDir()
	d2 := t.TempDir()
	mtime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	writeWithMtime(t, filepath.Join(d1, "a.txt"), "x", mtime)
	writeWithMtime(t, filepath.Join(d2, "a.txt"), "x", mtime)
	h1, _ := LocalManifest(d1)
	h2, _ := LocalManifest(d2)
	if h1 != h2 {
		t.Errorf("hash should be path-relative; %s vs %s", h1, h2)
	}
}

func TestLocalManifest_FollowsTopLevelSymlink(t *testing.T) {
	// Common pattern: ~/.claude/projects/<encoded> is a symlink. The
	// manifest should walk through it and produce the same hash as if
	// called against the target directly.
	target := t.TempDir()
	mtime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	writeWithMtime(t, filepath.Join(target, "session.json"), "x", mtime)

	parent := t.TempDir()
	link := filepath.Join(parent, "session-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported on this filesystem: %v", err)
	}

	hTarget, err := LocalManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	hLink, err := LocalManifest(link)
	if err != nil {
		t.Fatal(err)
	}
	if hTarget != hLink {
		t.Errorf("symlink should walk target: target=%s link=%s", hTarget, hLink)
	}
}

func writeWithMtime(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
