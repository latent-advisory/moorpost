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
	// `security add-generic-password -w <password>` takes the secret as a
	// literal argv value. There is no stdin-redirection mode (a previous
	// version of this code passed `-w "-"` and piped via stdin, but
	// security(1) does not honor `-` as a stdin sentinel — it stored the
	// literal `-` instead, silently corrupting every saved credential).
	//
	// Tradeoff: the secret is briefly visible to other processes via
	// `ps`/`/proc/<pid>/cmdline` on macOS for the few milliseconds the
	// security process is alive. Acceptable for a single-user dev
	// machine; matches what git-credential-osxkeychain and similar tools
	// do. If we ever need stronger isolation, the path is the Security
	// framework via Cgo (e.g. SecKeychainAddGenericPassword), not stdin.
	//
	// -U: update existing entry if present (otherwise security errors
	// with "already exists" instead of overwriting).
	cmd := exec.Command("security", "add-generic-password",
		"-U",
		"-s", service,
		"-a", account,
		"-w", string(secret),
	)
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
