package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
	"github.com/spf13/cobra"
)

// conflictsCmd lists unresolved sync conflicts for the project's active
// session. Closes the v0.3 "Conflict UX for mutagen sync conflicts"
// placeholder bullet from PLUGIN.md §9 line 613.
//
// Exit code 0 = no conflicts; exit code 1 = conflicts present (or any
// non-success error). This makes the command useful as a CI gate / git
// pre-commit hook check.
var conflictsCmd = &cobra.Command{
	Use:   "conflicts",
	Short: "List unresolved sync conflicts for this project",
	Long: `Lists unresolved file conflicts in the project's mutagen sync session.

Returns exit 0 when the session is clean, exit 1 when conflicts are
present (or on error). Run after a handoff/return cycle to verify the
sync converged cleanly.

To see machine-readable output, pass --json.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunConflicts(cmd.Context(), cmd.OutOrStdout(), c, conflictsFlagJSON)
	},
}

var conflictsFlagJSON bool

func init() {
	conflictsCmd.Flags().BoolVar(&conflictsFlagJSON, "json", false, "emit machine-readable JSON")
	rootCmd.AddCommand(conflictsCmd)
}

// ErrConflictsPresent is returned by RunConflicts when one or more
// unresolved conflicts exist. Used to signal a non-zero exit without a
// failure message (the conflicts themselves are the message).
var ErrConflictsPresent = errors.New("sync session has unresolved conflicts")

// RunConflicts is exported so tests can drive it without going through
// cobra. Returns ErrConflictsPresent (which the caller maps to a non-zero
// exit) when conflicts are present; nil when clean.
func RunConflicts(ctx context.Context, out io.Writer, c *Context, asJSON bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Sync == nil {
		return errors.New("conflicts: incomplete context")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.SyncSessionID == "" {
		if asJSON {
			return writeJSON(out, conflictsReport{Session: "", Conflicts: nil})
		}
		fmt.Fprintln(out, "No active sync session for this project. Run `moorpost handoff` to create one.")
		return nil
	}
	conflicts, err := c.Sync.ListConflicts(ctx, mpsync.SyncSessionID(ps.SyncSessionID))
	if errors.Is(err, mpsync.ErrSessionNotFound) {
		if asJSON {
			return writeJSON(out, conflictsReport{Session: ps.SyncSessionID, Conflicts: nil, NotFound: true})
		}
		fmt.Fprintf(out, "Sync session %q not found (engine may have been restarted). Run `moorpost handoff` to recreate.\n", ps.SyncSessionID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("conflicts: %w", err)
	}

	if asJSON {
		if err := writeJSON(out, conflictsReport{
			Session:   ps.SyncSessionID,
			Conflicts: conflicts,
		}); err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return ErrConflictsPresent
		}
		return nil
	}

	if len(conflicts) == 0 {
		fmt.Fprintf(out, "No conflicts in session %q. ✓\n", ps.SyncSessionID)
		return nil
	}
	printConflictTable(out, ps.SyncSessionID, conflicts)
	return ErrConflictsPresent
}

type conflictsReport struct {
	Session   string            `json:"session"`
	Conflicts []mpsync.Conflict `json:"conflicts"`
	// NotFound is true when the engine doesn't know about the session
	// referenced in state (e.g., engine restarted). Distinct from "session
	// is known and has zero conflicts."
	NotFound bool `json:"not_found,omitempty"`
}

func writeJSON(out io.Writer, r conflictsReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// printConflictTable renders a human-friendly summary. Each row shows the
// path plus how each side (alpha=local, beta=remote) modified it.
func printConflictTable(out io.Writer, sessionID string, conflicts []mpsync.Conflict) {
	fmt.Fprintf(out, "Session %q has %d unresolved conflict(s):\n\n", sessionID, len(conflicts))
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tLOCAL\tREMOTE\tLOCAL MTIME\tREMOTE MTIME")
	for _, c := range conflicts {
		path := c.Path
		if path == "" {
			path = "(empty path)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			path,
			fmtKind(c.AlphaKind),
			fmtKind(c.BetaKind),
			fmtTime(c.AlphaModifiedAt),
			fmtTime(c.BetaModifiedAt),
		)
	}
	_ = w.Flush()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Resolve via your editor (the files are present on both sides; mutagen has paused them).")
	fmt.Fprintln(out, "Once resolved, mutagen will re-converge automatically on the next sync tick.")
}

func fmtKind(k mpsync.ChangeKind) string {
	if k == "" || k == mpsync.ChangeKindUnknown {
		return "—"
	}
	return string(k)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}
