package keychain

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFileBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	kc, err := NewFile(dir)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	const service = "moorpost.claude-code.token"
	const account = "default"
	want := []byte("sk-ant-oat01-redacted")

	if err := kc.Store(service, account, want); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := kc.Retrieve(service, account)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Retrieve = %q, want %q", got, want)
	}
}

func TestFileBackendRetrieveMissing(t *testing.T) {
	kc, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	_, err = kc.Retrieve("svc", "acct")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Retrieve missing returned %v, want ErrNotFound", err)
	}
}

func TestFileBackendDeleteIdempotent(t *testing.T) {
	kc, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	// Delete a non-existent entry.
	if err := kc.Delete("svc", "acct"); err != nil {
		t.Errorf("Delete on missing key returned %v, want nil", err)
	}
	// Store, delete, retrieve.
	if err := kc.Store("svc", "acct", []byte("x")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := kc.Delete("svc", "acct"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kc.Retrieve("svc", "acct"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, Retrieve = %v, want ErrNotFound", err)
	}
}

func TestFileBackendOverwrite(t *testing.T) {
	kc, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	if err := kc.Store("s", "a", []byte("v1")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := kc.Store("s", "a", []byte("v2-longer")); err != nil {
		t.Fatalf("Store overwrite: %v", err)
	}
	got, err := kc.Retrieve("s", "a")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if string(got) != "v2-longer" {
		t.Errorf("Retrieve = %q, want v2-longer", got)
	}
}

func TestFileBackendPermissions(t *testing.T) {
	dir := t.TempDir()
	kc, err := NewFile(dir)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	if err := kc.Store("svc", "acct", []byte("secret")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Check the dir is 0700.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("dir perms = %o, want 0700", info.Mode().Perm())
	}
	// Check the secret file is 0600.
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var secretFiles int
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasPrefix(f.Name(), ".kc-") {
			secretFiles++
			info, err := f.Info()
			if err != nil {
				t.Fatalf("f.Info: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("file %s perms = %o, want 0600", f.Name(), info.Mode().Perm())
			}
		}
	}
	if secretFiles != 1 {
		t.Errorf("expected 1 secret file, found %d", secretFiles)
	}
}

func TestServiceAccountValidation(t *testing.T) {
	dir := t.TempDir()
	kc, err := NewFile(dir)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	bad := []struct {
		name, service, account string
	}{
		{"empty service", "", "a"},
		{"empty account", "s", ""},
		{"slash in service", "a/b", "x"},
		{"slash in account", "s", "a/b"},
		{"backslash in service", `a\b`, "x"},
		{"null byte", "a\x00b", "x"},
		{"dotdot in service", "..", "a"},
		{"dotdot embedded", "x..y", "a"},
		{"dot only", ".", "a"},
		{"too long service", strings.Repeat("a", 256), "x"},
		{"too long account", "s", strings.Repeat("a", 256)},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := kc.Store(tc.service, tc.account, []byte("v")); err == nil {
				t.Errorf("Store(%q, %q) accepted invalid input", tc.service, tc.account)
			}
			if _, err := kc.Retrieve(tc.service, tc.account); err == nil {
				t.Errorf("Retrieve(%q, %q) accepted invalid input", tc.service, tc.account)
			}
			if err := kc.Delete(tc.service, tc.account); err == nil {
				t.Errorf("Delete(%q, %q) accepted invalid input", tc.service, tc.account)
			}
		})
	}
}

func TestPathTraversalCannotEscapeDir(t *testing.T) {
	root := t.TempDir()
	kcDir := filepath.Join(root, "kc")
	otherDir := filepath.Join(root, "other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	kc, err := NewFile(kcDir)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	// Even if validation were bypassed, the path-traversal sequence should
	// be rejected by validateServiceAccount.
	if err := kc.Store("..", "x", []byte("evil")); err == nil {
		t.Fatal("Store accepted path-traversal service")
	}
	// Confirm no file landed outside kcDir.
	files, _ := os.ReadDir(otherDir)
	if len(files) != 0 {
		t.Errorf("path traversal escaped: found %d files in %s", len(files), otherDir)
	}
}

func TestNewFileRejectsEmptyDir(t *testing.T) {
	if _, err := NewFile(""); err == nil {
		t.Error("NewFile('') accepted empty dir")
	}
}

func TestNewWithForceFileBackend(t *testing.T) {
	t.Setenv(envForceFileBackend, "1")
	t.Setenv(envFileBackendDir, t.TempDir())
	kc, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if kc.Backend() != "file" {
		t.Errorf("Backend = %q, want file", kc.Backend())
	}
}

func TestNewDefaultBackendBuilds(t *testing.T) {
	// Without the force-file env, New() should return the OS-native backend
	// on supported platforms. We don't poke any real keychain; just verify
	// New() succeeds and the backend identifier matches expectations on the
	// build platform. On macOS we expect "macos"; on Linux we expect either
	// "linux" or ErrBackendUnavailable (if secret-tool isn't installed in CI).
	t.Setenv(envForceFileBackend, "")
	kc, err := New()
	if err != nil {
		// On Linux without secret-tool, this is an acceptable failure mode.
		if errors.Is(err, ErrBackendUnavailable) {
			t.Skipf("OS keychain unavailable in this environment: %v", err)
		}
		t.Fatalf("New: %v", err)
	}
	switch kc.Backend() {
	case "macos", "linux", "file":
		// ok
	default:
		t.Errorf("Backend = %q, want one of macos/linux/file", kc.Backend())
	}
}

func TestFileBackendConcurrentStores(t *testing.T) {
	// File backend uses atomic rename per write; concurrent stores to
	// different keys must not corrupt one another.
	kc, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	const N = 25
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			account := "a" + intToStr(i)
			val := []byte("v" + intToStr(i))
			if err := kc.Store("svc", account, val); err != nil {
				t.Errorf("Store(%s): %v", account, err)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < N; i++ {
		account := "a" + intToStr(i)
		got, err := kc.Retrieve("svc", account)
		if err != nil {
			t.Errorf("Retrieve(%s): %v", account, err)
			continue
		}
		want := []byte("v" + intToStr(i))
		if !bytes.Equal(got, want) {
			t.Errorf("Retrieve(%s) = %q, want %q", account, got, want)
		}
	}
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
