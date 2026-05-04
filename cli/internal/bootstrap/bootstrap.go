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
