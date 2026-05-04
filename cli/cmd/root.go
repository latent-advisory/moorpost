package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
	"github.com/latent-advisory/moorpost/cli/internal/version"
	"github.com/spf13/cobra"
)

// auditStart records the start time for the duration calculation in
// PersistentPostRunE. Set in PersistentPreRunE.
var auditStart time.Time

// commandsToSkipAuditLogging are commands we don't want to log (avoids
// self-recursion or simply isn't useful to track).
//
// "moorpost" is the root command's name; it appears when the user runs
// `moorpost`, `moorpost --version`, or `moorpost --help` (no subcommand).
// Cobra bypasses PersistentPreRunE for those, so auditStart stays zero and
// the recorded duration would be nonsensical. Skip logging.
var commandsToSkipAuditLogging = map[string]bool{
	"moorpost":   true, // root (--version, --help, no subcommand)
	"audit":      true,
	"help":       true,
	"completion": true,
	"version":    true,
}

var rootCmd = &cobra.Command{
	Use:           "moorpost",
	Short:         "Tether your laptop to a remote forward base where Claude Code keeps working.",
	Long:          "Moorpost lets you work locally by default and hand off to a remote VM when stepping away.\nSee https://github.com/latent-advisory/moorpost for documentation.",
	Version:       version.Info(),
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		auditStart = time.Now()
		return nil
	},
}

// auditLoggerForRun returns a Logger rooted at ~/.moorpost/logs/. Returns
// nil + error to indicate "skip logging" rather than fail the user's command.
func auditLoggerForRun() (*audit.Logger, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	logger := audit.NewLogger(filepath.Join(home, ".moorpost", "logs"))
	logger.RetentionDays = 30 // per PLUGIN.md §10 #13
	return logger, nil
}

// logCommandResult is called by Execute after the command finishes. It
// is best-effort: failure to log never bubbles up (logging shouldn't break
// the user's command).
func logCommandResult(cmd *cobra.Command, args []string, err error) {
	if cmd == nil || commandsToSkipAuditLogging[cmd.Name()] {
		return
	}
	// Defensive: if PersistentPreRunE didn't fire (e.g., the cmd has its
	// own RunE that bypasses Persistent hooks, or cobra's --help intercepted),
	// auditStart is zero and `time.Since` would be nonsensical. Skip logging
	// rather than emit garbage.
	if auditStart.IsZero() {
		return
	}
	logger, lerr := auditLoggerForRun()
	if lerr != nil || logger == nil {
		return
	}
	entry := audit.Entry{
		Timestamp:  auditStart,
		Command:    cmd.Name(),
		Args:       args,
		ExitCode:   0,
		DurationMS: time.Since(auditStart).Milliseconds(),
	}
	if err != nil {
		entry.ExitCode = 1
		entry.Error = err.Error()
	}
	_ = logger.Append(entry)
}

// Execute runs the root command and is called by main. Wraps cobra.Execute
// to log the result via the audit logger.
func Execute() error {
	cmd, err := rootCmd.ExecuteC()
	logCommandResult(cmd, os.Args[1:], err)
	return err
}
