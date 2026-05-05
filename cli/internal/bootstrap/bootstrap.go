// Package bootstrap renders the VM bootstrap script that runs on first boot.
//
// The script template is embedded at build time so the CLI binary carries
// it self-contained — no fetching from GitHub at provision time.
package bootstrap

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"text/template"
)

//go:embed bootstrap.sh.tmpl
var rawTemplate string

// BootstrapVars are the values substituted into the script template.
type BootstrapVars struct {
	// ProjectSlug is the project's lower-cased identifier (e.g. "argus").
	// Required.
	ProjectSlug string

	// LocalAbsPath is the absolute path of the project on the user's local
	// machine (e.g. "/Users/x/argus"). Used to install a symlink on the
	// remote so the encoded session-state path matches local. Required.
	LocalAbsPath string

	// RemoteUser is the OS user on the VM that will own the project tree.
	// Defaults to "moorpost" when empty.
	RemoteUser string

	// NodeVersion is the Node.js major version to install (e.g. "20").
	// Defaults to "20".
	NodeVersion string

	// ClaudeCodeVersion optionally pins @anthropic-ai/claude-code to a
	// specific version (e.g. "2.0.0"). Empty = latest.
	ClaudeCodeVersion string

	// IdleAutoStopMinutes installs the VM-side idle monitor when > 0.
	// Should only be set when the project's mode is `persistent`. The
	// installed systemd timer polls every CheckIntervalMinutes minutes
	// (default 5) and stops the VM after this many consecutive idle
	// minutes.
	IdleAutoStopMinutes int

	// CheckIntervalMinutes overrides how often the VM-side systemd timer
	// wakes the idle-check script. 0 (default) uses
	// DefaultCheckIntervalMinutes. Lowered by the e2e test so the
	// auto-stop transition fires within minutes instead of tens of
	// minutes; production callers should leave it at 0.
	CheckIntervalMinutes int

	// IdleMonitorInstall is the rendered shell snippet that the template
	// includes inline. Computed by Render from IdleAutoStopMinutes.
	IdleMonitorInstall string
}

// applyDefaults fills in zero-valued fields with v0.1 defaults.
func (v *BootstrapVars) applyDefaults() {
	if v.RemoteUser == "" {
		v.RemoteUser = "moorpost"
	}
	if v.NodeVersion == "" {
		v.NodeVersion = "20"
	}
}

// validate enforces required fields.
func (v BootstrapVars) validate() error {
	if v.ProjectSlug == "" {
		return errors.New("bootstrap: ProjectSlug is required")
	}
	if v.LocalAbsPath == "" {
		return errors.New("bootstrap: LocalAbsPath is required")
	}
	return nil
}

// Render produces the final shell script for vars.
func Render(v BootstrapVars) (string, error) {
	if err := v.validate(); err != nil {
		return "", err
	}
	v.applyDefaults()
	if v.IdleAutoStopMinutes > 0 {
		interval := v.CheckIntervalMinutes
		if interval <= 0 {
			interval = DefaultCheckIntervalMinutes
		}
		v.IdleMonitorInstall = renderIdleInstall(
			BuildIdleMonitorUnitsWithInterval(v.IdleAutoStopMinutes, interval),
		)
	}
	tmpl, err := template.New("bootstrap").Parse(rawTemplate)
	if err != nil {
		return "", fmt.Errorf("bootstrap: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, v); err != nil {
		return "", fmt.Errorf("bootstrap: execute: %w", err)
	}
	return buf.String(), nil
}
