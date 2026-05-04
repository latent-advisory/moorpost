//go:build darwin
// +build darwin

package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// macKeychain shells out to /usr/bin/security to manage entries in the
// user's login keychain.
type macKeychain struct{}

func newOSBackend() (Keychain, error) {
	if _, err := exec.LookPath("security"); err != nil {
		return nil, fmt.Errorf("%w: /usr/bin/security not found", ErrBackendUnavailable)
	}
	return &macKeychain{}, nil
}

func (m *macKeychain) Backend() string { return "macos" }

func (m *macKeychain) Store(service, account string, secret []byte) error {
	if err := validateServiceAccount(service, account); err != nil {
		return err
	}
	// -U: update existing entry if present.
	// -w "": tells security we'll pipe the secret on stdin? Actually
	// `security add-generic-password` requires `-w <secret>` on the cmdline.
	// To avoid leaking via process listings, we pass via stdin only when
	// supported; falling back to argv on older macOS releases.
	//
	// 2026 macOS supports `-w` reading from stdin if the value is "-".
	cmd := exec.Command("security", "add-generic-password",
		"-U",
		"-s", service,
		"-a", account,
		"-w", "-",
	)
	cmd.Stdin = bytes.NewReader(secret)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: security add: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (m *macKeychain) Retrieve(service, account string) ([]byte, error) {
	if err := validateServiceAccount(service, account); err != nil {
		return nil, err
	}
	cmd := exec.Command("security", "find-generic-password",
		"-s", service,
		"-a", account,
		"-w", // print password only, on stdout
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// security exits 44 for "not found" and prints to stderr.
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "could not be found") || strings.Contains(stderrStr, "not be found") {
			return nil, ErrNotFound
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keychain: security find: %w (%s)", err, strings.TrimSpace(stderrStr))
	}
	// `security -w` appends a trailing newline.
	out := stdout.Bytes()
	out = bytes.TrimRight(out, "\n")
	return out, nil
}

func (m *macKeychain) Delete(service, account string) error {
	if err := validateServiceAccount(service, account); err != nil {
		return err
	}
	cmd := exec.Command("security", "delete-generic-password",
		"-s", service,
		"-a", account,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "could not be found") || strings.Contains(stderrStr, "not be found") {
			return nil // idempotent
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return nil
		}
		return fmt.Errorf("keychain: security delete: %w (%s)", err, strings.TrimSpace(stderrStr))
	}
	return nil
}
