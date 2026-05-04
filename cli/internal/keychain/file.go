package keychain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// fileKeychain stores each (service, account) as a separate 0600 file under
// a single directory. The path layout is:
//
//	<dir>/<service>__<account>
//
// Atomic writes are used so partial files never appear.
type fileKeychain struct {
	dir string
}

// NewFile returns a Keychain backed by 0600 files under dir. The dir is
// created on demand.
//
// This backend is used in tests and as the user-opt-in `--unsafe-token-storage`
// fallback. It does NOT encrypt at rest; it relies on filesystem permissions.
func NewFile(dir string) (Keychain, error) {
	if dir == "" {
		return nil, errors.New("keychain: NewFile requires a non-empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("keychain: mkdir %s: %w", dir, err)
	}
	// Lock down the dir even if it pre-existed with looser perms.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("keychain: chmod %s: %w", dir, err)
	}
	return &fileKeychain{dir: dir}, nil
}

func (f *fileKeychain) Backend() string { return "file" }

func (f *fileKeychain) path(service, account string) string {
	return filepath.Join(f.dir, service+"__"+account)
}

func (f *fileKeychain) Store(service, account string, secret []byte) error {
	if err := validateServiceAccount(service, account); err != nil {
		return err
	}
	target := f.path(service, account)

	tmp, err := os.CreateTemp(f.dir, ".kc-*.tmp")
	if err != nil {
		return fmt.Errorf("keychain: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(secret); err != nil {
		tmp.Close()
		return fmt.Errorf("keychain: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("keychain: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("keychain: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("keychain: chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("keychain: rename: %w", err)
	}
	return nil
}

func (f *fileKeychain) Retrieve(service, account string) ([]byte, error) {
	if err := validateServiceAccount(service, account); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(f.path(service, account))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("keychain: read: %w", err)
	}
	return data, nil
}

func (f *fileKeychain) Delete(service, account string) error {
	if err := validateServiceAccount(service, account); err != nil {
		return err
	}
	err := os.Remove(f.path(service, account))
	if errors.Is(err, os.ErrNotExist) {
		return nil // idempotent
	}
	return err
}
