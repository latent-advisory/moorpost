// Package keychain stores small secrets (Claude OAuth tokens, API keys) in
// the OS keychain when available, with a file-based fallback.
//
// v1 backends:
//   - macOS: /usr/bin/security (built-in)
//   - Linux: secret-tool (libsecret)
//   - File:  0600 file under a Moorpost-controlled dir (used as test
//            backend and as the `--unsafe-token-storage` fallback)
//
// Service-name convention: "moorpost.<agent-id>.<purpose>"
//   e.g. "moorpost.claude-code.token"
package keychain

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Keychain stores secrets keyed by (service, account). All secrets are
// stored as opaque byte slices; callers may marshal/unmarshal as needed.
//
// Implementations must be safe for concurrent use across multiple Moorpost
// invocations on the same machine (the OS keychains already are; the file
// backend uses atomic writes).
type Keychain interface {
	// Backend returns a stable identifier ("macos", "linux", "file") for
	// diagnostics.
	Backend() string

	// Store writes secret under (service, account). Overwrites if present.
	Store(service, account string, secret []byte) error

	// Retrieve reads the secret under (service, account). Returns ErrNotFound
	// if absent.
	Retrieve(service, account string) ([]byte, error)

	// Delete removes the secret under (service, account). Returns nil if
	// already absent (idempotent).
	Delete(service, account string) error
}

// ErrNotFound indicates a (service, account) is not in the keychain.
var ErrNotFound = errors.New("keychain: not found")

// ErrBackendUnavailable indicates the OS-native keychain backend is not
// usable (e.g. secret-tool not installed on Linux). Callers may fall back to
// NewFile if the user has opted into --unsafe-token-storage.
var ErrBackendUnavailable = errors.New("keychain: native backend unavailable")

// envForceFileBackend, when set to "1", makes New() return the file backend
// regardless of OS. Used by tests; documented for advanced users only.
const envForceFileBackend = "MOORPOST_FORCE_FILE_KEYCHAIN"

// envFileBackendDir overrides the default file-backend directory.
const envFileBackendDir = "MOORPOST_FILE_KEYCHAIN_DIR"

// New returns the default backend for the current OS, falling back to the
// file backend if explicitly requested via MOORPOST_FORCE_FILE_KEYCHAIN=1.
//
// On unsupported platforms, returns the file backend.
func New() (Keychain, error) {
	if os.Getenv(envForceFileBackend) == "1" {
		dir := os.Getenv(envFileBackendDir)
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("keychain: cannot resolve home dir: %w", err)
			}
			dir = home + "/.moorpost/credentials"
		}
		return NewFile(dir)
	}
	return newOSBackend()
}

// validateServiceAccount enforces conservative rules on (service, account):
// no path separators (so file backend is safe), no ".." (defense in depth),
// max 255 chars each. Empty values are rejected.
func validateServiceAccount(service, account string) error {
	if service == "" {
		return errors.New("keychain: service must not be empty")
	}
	if account == "" {
		return errors.New("keychain: account must not be empty")
	}
	for _, name := range []string{service, account} {
		if len(name) > 255 {
			return fmt.Errorf("keychain: name %q exceeds 255 chars", truncate(name, 32))
		}
		if strings.ContainsAny(name, "/\\\x00") {
			return fmt.Errorf("keychain: name contains forbidden characters: %q", truncate(name, 32))
		}
		if name == "." || name == ".." || strings.Contains(name, "..") {
			return fmt.Errorf("keychain: name contains forbidden traversal sequence: %q", truncate(name, 32))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
