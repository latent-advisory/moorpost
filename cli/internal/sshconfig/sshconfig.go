// Package sshconfig manages a moorpost-owned ssh config file that
// downstream tools (rsync, mutagen, plain ssh) read by default.
//
// Why we need it: mutagen has no per-create option to pass `-i <key>` or
// `-o <opt>` to the spawned ssh process; it just runs `ssh user@host`.
// rsync's -e flag works but has the same identity-file pain. The
// universal solution is the user's ~/.ssh/config — but we don't want
// to scribble in the user's main file. So we maintain our own file at
// ~/.moorpost/ssh_config and add a single idempotent `Include` line at
// the top of ~/.ssh/config, fenced by marker comments so we can find
// and update or remove it later.
package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MarkerStart and MarkerEnd fence the Include line we add to
	// ~/.ssh/config. Hex-style markers so they're recognizable in a
	// config dump and can't appear naturally.
	markerStart = "# BEGIN moorpost-managed (do not edit between markers)"
	markerEnd   = "# END moorpost-managed"
)

// HostBlock describes one ssh Host stanza we want present in our
// managed config.
type HostBlock struct {
	Host         string // matches `Host` directive value (typically the VM IP)
	User         string // ssh user (e.g. "moorpost")
	IdentityFile string // absolute path to the private key
	Port         int    // 0 → 22 (omitted from output when default)
}

// EnsureHost writes the moorpost ssh config so that any future ssh
// invocation against block.Host uses block.User + block.IdentityFile.
// Idempotent. Replaces an existing block for the same Host.
//
// Also ensures the user's ~/.ssh/config Includes the moorpost-managed
// file (writing the marker-fenced Include line on first use).
func EnsureHost(block HostBlock) error {
	if block.Host == "" || block.User == "" || block.IdentityFile == "" {
		return errors.New("sshconfig: Host, User, and IdentityFile required")
	}
	moorpostCfgPath, err := moorpostConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(moorpostCfgPath), 0o700); err != nil {
		return fmt.Errorf("sshconfig: mkdir: %w", err)
	}

	// Read current moorpost-managed file (if any), strip old block for
	// the same host, append the new one.
	current, _ := os.ReadFile(moorpostCfgPath) // ignore not-exist
	updated := stripHostBlock(string(current), block.Host) + "\n" + renderHostBlock(block)
	updated = strings.TrimSpace(updated) + "\n"
	if err := os.WriteFile(moorpostCfgPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("sshconfig: write %s: %w", moorpostCfgPath, err)
	}

	return ensureUserConfigInclude(moorpostCfgPath)
}

// RemoveHost strips the block for host from the moorpost-managed file.
// Best-effort: returns nil if the file or block is already absent.
func RemoveHost(host string) error {
	moorpostCfgPath, err := moorpostConfigPath()
	if err != nil {
		return err
	}
	current, err := os.ReadFile(moorpostCfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	updated := strings.TrimSpace(stripHostBlock(string(current), host)) + "\n"
	return os.WriteFile(moorpostCfgPath, []byte(updated), 0o600)
}

// renderHostBlock returns the ssh-config text for a single Host stanza.
func renderHostBlock(b HostBlock) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# moorpost host:%s\n", b.Host)
	fmt.Fprintf(&sb, "Host %s\n", b.Host)
	fmt.Fprintf(&sb, "    User %s\n", b.User)
	fmt.Fprintf(&sb, "    IdentityFile %s\n", b.IdentityFile)
	fmt.Fprintf(&sb, "    StrictHostKeyChecking accept-new\n")
	if b.Port != 0 && b.Port != 22 {
		fmt.Fprintf(&sb, "    Port %d\n", b.Port)
	}
	return sb.String()
}

// stripHostBlock removes a `# moorpost host:<host>` ... block (terminated
// by the next `# moorpost host:` line, the next blank line, or EOF) from
// content. No-op if not present.
func stripHostBlock(content, host string) string {
	marker := "# moorpost host:" + host
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		if strings.TrimSpace(line) == marker {
			inBlock = true
			continue
		}
		if inBlock {
			t := strings.TrimSpace(line)
			// End of block: another moorpost-host marker, or a non-
			// indented non-empty line that's not part of the stanza.
			if strings.HasPrefix(t, "# moorpost host:") {
				inBlock = false
				out = append(out, line)
				continue
			}
			if t == "" {
				inBlock = false
				continue
			}
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") &&
				!strings.HasPrefix(t, "Host ") && !strings.HasPrefix(t, "#") {
				inBlock = false
				out = append(out, line)
				continue
			}
			// still inside the block; skip
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func moorpostConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sshconfig: home dir: %w", err)
	}
	return filepath.Join(home, ".moorpost", "ssh_config"), nil
}

// ensureUserConfigInclude prepends a marker-fenced `Include` line to
// ~/.ssh/config if not already present. ssh requires Include directives
// to come BEFORE the Host blocks they reference, so we always insert at
// the top.
func ensureUserConfigInclude(includePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("sshconfig: home dir: %w", err)
	}
	cfgPath := filepath.Join(home, ".ssh", "config")

	current, _ := os.ReadFile(cfgPath) // ignore not-exist
	if strings.Contains(string(current), markerStart) {
		// Already managed; verify the Include line points at the right
		// path. If not, rewrite the block.
		if strings.Contains(string(current), "Include "+includePath) {
			return nil
		}
		// Strip the old block before re-prepending below.
		current = []byte(stripManagedBlock(string(current)))
	}

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return fmt.Errorf("sshconfig: mkdir ~/.ssh: %w", err)
	}

	header := markerStart + "\n" +
		"Include " + includePath + "\n" +
		markerEnd + "\n\n"
	out := header + string(current)
	return os.WriteFile(cfgPath, []byte(out), 0o600)
}

func stripManagedBlock(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	in := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == markerStart {
			in = true
			continue
		}
		if in {
			if t == markerEnd {
				in = false
			}
			continue
		}
		out = append(out, line)
	}
	// Trim leading blank lines we may have left behind.
	return strings.TrimLeft(strings.Join(out, "\n"), "\n")
}
