//go:build darwin
// +build darwin

package keychain

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// TestMacOSBackendRoundTripExercisesRealSecurity is the regression test
// for the silently-corrupted-writes bug: an earlier version of Store
// passed `-w "-"` to security(1) intending to read the secret from
// stdin, but security ignored stdin and stored the literal `-` as the
// secret. The unit tests against the file backend round-tripped fine,
// so the bug only surfaced at runtime when handoff tried to read back
// the cached Claude token and got `-` instead.
//
// This test stores a random non-trivial secret through the real macOS
// security backend and verifies Retrieve returns it unchanged. With the
// old code, this would have failed immediately: written 32 hex chars,
// retrieved `-`.
func TestMacOSBackendRoundTripExercisesRealSecurity(t *testing.T) {
	kc, err := newOSBackend()
	if err != nil {
		t.Skipf("macOS backend unavailable: %v", err)
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("rand: %v", err)
	}
	service := "moorpost.test." + hex.EncodeToString(suffix)
	account := "default"

	secretBytes := make([]byte, 24)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	secret := []byte("sk-ant-test-" + hex.EncodeToString(secretBytes))

	t.Cleanup(func() {
		_ = kc.Delete(service, account)
	})

	if err := kc.Store(service, account, secret); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := kc.Retrieve(service, account)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if string(got) != string(secret) {
		t.Fatalf("round-trip mismatch:\n  wrote %q (%d bytes)\n  read  %q (%d bytes)",
			secret, len(secret), got, len(got))
	}
}
