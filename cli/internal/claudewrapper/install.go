// Package claudewrapper writes the moorpost shim that the Anthropic
// Claude Code plugin's `claudeCode.claudeProcessWrapper` setting can
// point at. The shim's job: route claude invocations to local or remote
// based on the project's active_side in ~/.moorpost/state.json.
//
// We embed the bash script in the binary so `moorpost` ships
// self-contained — no separate install step or version drift between
// the script and the CLI it talks to.
package claudewrapper

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed wrapper.sh
var script []byte

// Path returns the absolute path where the wrapper script lives once
// installed (~/.moorpost/bin/claude-wrapper). Stable so the extension
// can write the same value into VSCode's claudeCode.claudeProcessWrapper
// setting without re-querying the binary.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claudewrapper: home dir: %w", err)
	}
	return filepath.Join(home, ".moorpost", "bin", "claude-wrapper"), nil
}

// Install writes the embedded script to Path() with mode 0755. Idempotent
// — overwrites any existing copy so users always get the version that
// shipped with this moorpost build (no stale-script footgun).
func Install() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("claudewrapper: mkdir: %w", err)
	}
	if err := os.WriteFile(p, script, 0o755); err != nil {
		return "", fmt.Errorf("claudewrapper: write %s: %w", p, err)
	}
	return p, nil
}

// IsInstalled reports whether the wrapper exists on disk at Path().
// Used by the extension to decide whether to call Install on first
// handoff vs. just point the plugin setting at the existing path.
func IsInstalled() bool {
	p, err := Path()
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if info.Mode()&0o111 == 0 {
		return false // file exists but isn't executable
	}
	return true
}

// ErrUnsupportedOS indicates the wrapper bash script can't run on this
// platform (currently Windows). Callers should skip the plugin
// integration on these platforms.
var ErrUnsupportedOS = errors.New("claudewrapper: bash wrapper not supported on this platform")
