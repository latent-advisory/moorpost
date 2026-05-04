//go:build !darwin && !linux
// +build !darwin,!linux

package keychain

import (
	"fmt"
	"os"
)

// On unsupported platforms (Windows in v1, BSDs, etc.) the OS-native backend
// is not implemented. New() falls through to the file backend with a clear
// directory.
func newOSBackend() (Keychain, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("keychain: cannot resolve home dir: %w", err)
	}
	return NewFile(home + "/.moorpost/credentials")
}
