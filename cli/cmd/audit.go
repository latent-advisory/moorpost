package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Show recent moorpost CLI invocations from the local audit log",
	Long: `Reads ~/.moorpost/logs/<date>.jsonl files and prints recent
invocations. Useful for security review (what commands ran when?) and
debugging (when did the last provision fail?).

Default: last 20 entries within the past 7 days. Use --last to
change the count and --days to widen the window.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("audit: home: %w", err)
		}
		dir := filepath.Join(home, ".moorpost", "logs")
		return RunAudit(cmd.OutOrStdout(), audit.NewLogger(dir), AuditOptions{
			Days:     auditFlagDays,
			Last:     auditFlagLast,
			AsJSON:   auditFlagJSON,
		})
	},
}

var (
	auditFlagDays int
	auditFlagLast int
	auditFlagJSON bool
)

func init() {
	auditCmd.Flags().IntVar(&auditFlagDays, "days", 7, "look back this many days")
	auditCmd.Flags().IntVar(&auditFlagLast, "last", 20, "print only the last N entries (0 = all)")
	auditCmd.Flags().BoolVar(&auditFlagJSON, "json", false, "emit the raw JSONL")
	rootCmd.AddCommand(auditCmd)
}

// AuditOptions controls RunAudit.
type AuditOptions struct {
	Days   int
	Last   int
	AsJSON bool
}

// RunAudit reads the audit log and prints entries.
func RunAudit(out io.Writer, logger *audit.Logger, opts AuditOptions) error {
	if logger == nil {
		return errors.New("audit: logger is nil")
	}
	days := opts.Days
	if days <= 0 {
		days = 7
	}
	entries, err := logger.Read(days)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "No audit entries in the last", days, "days.")
		return nil
	}
	// Trim to the most recent N if --last is set.
	if opts.Last > 0 && len(entries) > opts.Last {
		entries = entries[len(entries)-opts.Last:]
	}
	if opts.AsJSON {
		enc := json.NewEncoder(out)
		for _, e := range entries {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	// Human-readable:
	//   2026-05-05 12:34:56 │ provision   │ exit=0 │  14.3s
	for _, e := range entries {
		ts := e.Timestamp.Local().Format("2006-01-02 15:04:05")
		dur := time.Duration(e.DurationMS) * time.Millisecond
		fmt.Fprintf(out, "%s │ %-12s │ exit=%d │ %6s\n",
			ts, e.Command, e.ExitCode, dur.Round(100*time.Millisecond))
		if e.Error != "" {
			fmt.Fprintf(out, "  └─ error: %s\n", e.Error)
		}
	}
	return nil
}
