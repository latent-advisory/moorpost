//go:build linux
// +build linux

package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// linuxKeychain shells out to secret-tool (libsecret) to manage entries.
type linuxKeychain struct{}

func newOSBackend() (Keychain, error) {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return nil, fmt.Errorf("%w: secret-tool not found (install via 'apt install libsecret-tools' or equivalent)", ErrBackendUnavailable)
	}
	return &linuxKeychain{}, nil
}

func (l *linuxKeychain) Backend() string { return "linux" }

// secret-tool encodes (service, account) as two attribute pairs. We use the
// fixed attribute names "service" and "account" for compatibility with the
// docs and existing tooling.
func (l *linuxKeychain) Store(service, account string, secret []byte) error {
	if err := validateServiceAccount(service, account); err != nil {
		return err
	}
	cmd := exec.Command("secret-tool", "store",
		"--label", "moorpost: "+service,
		"service", service,
		"account", account,
	)
	cmd.Stdin = bytes.NewReader(secret)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: secret-tool store: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (l *linuxKeychain) Retrieve(service, account string) ([]byte, error) {
	if err := validateServiceAccount(service, account); err != nil {
		return nil, err
	}
	cmd := exec.Command("secret-tool", "lookup",
		"service", service,
		"account", account,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// secret-tool exits 1 with empty stdout when not found.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && stdout.Len() == 0 {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keychain: secret-tool lookup: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, ErrNotFound
	}
	out := bytes.TrimRight(stdout.Bytes(), "\n")
	return out, nil
}

func (l *linuxKeychain) Delete(service, account string) error {
	if err := validateServiceAccount(service, account); err != nil {
		return err
	}
	cmd := exec.Command("secret-tool", "clear",
		"service", service,
		"account", account,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// `clear` is idempotent on libsecret — it succeeds even if the entry
		// is absent. If we see a non-zero exit, surface it.
		return fmt.Errorf("keychain: secret-tool clear: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
