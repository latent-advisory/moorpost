package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current Moorpost state for this project",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunStatus(cmd.OutOrStdout(), c, statusFlagJSON)
	},
}

var statusFlagJSON bool

func init() {
	statusCmd.Flags().BoolVar(&statusFlagJSON, "json", false, "emit machine-readable JSON")
	rootCmd.AddCommand(statusCmd)
}

// statusReport is the shape of `--json` output.
type statusReport struct {
	Project    string  `json:"project"`
	Provider   string  `json:"provider"`
	Agent      string  `json:"agent"`
	Sync       string  `json:"sync"`
	Mode       string  `json:"mode"`
	ActiveSide string  `json:"active_side,omitempty"`
	VMID       string  `json:"vm_id,omitempty"`
	VMState    string  `json:"vm_state,omitempty"`
	MTDCostUSD float64 `json:"month_to_date_usd,omitempty"`
	// Conflicts is the unresolved-conflict count from the active sync
	// session, if any. -1 means "session known but conflict count is
	// unavailable" (e.g., mutagen daemon down). Omitted when no session.
	Conflicts        int    `json:"conflicts,omitempty"`
	HasSyncSession   bool   `json:"has_sync_session,omitempty"`
	SyncSessionID    string `json:"sync_session_id,omitempty"`
}

// RunStatus prints the project status. If asJSON is true, emits the report
// as a single JSON object; otherwise human-readable text.
func RunStatus(out io.Writer, c *Context, asJSON bool) error {
	if c == nil || c.Config == nil {
		return fmt.Errorf("status: no project context loaded")
	}
	report := statusReport{
		Project:  c.Config.ProjectSlug,
		Provider: c.Config.Provider.Type,
		Agent:    c.Config.Agent.Type,
		Sync:     c.Config.Sync.Engine,
		Mode:     string(c.Config.Mode),
	}
	if c.State != nil {
		// Look up project by absolute project dir; fall back to slug match.
		for absPath, ps := range c.State.Projects {
			if ps.Slug == c.Config.ProjectSlug || absPath == c.ProjectDir {
				report.ActiveSide = string(ps.ActiveSide)
				report.VMID = ps.VMID
				if vm, ok := c.State.VMs[ps.VMID]; ok {
					report.VMState = vm.StateCache
					report.MTDCostUSD = vm.MonthToDateUSD
				}
				if ps.SyncSessionID != "" && c.Sync != nil {
					report.HasSyncSession = true
					report.SyncSessionID = ps.SyncSessionID
					// Best-effort: a Sync.Status failure here shouldn't
					// fail the user's `moorpost status` call. Conflicts
					// stays 0 (pessimistically optimistic — clean).
					ss, err := c.Sync.Status(context.Background(), mpsync.SyncSessionID(ps.SyncSessionID))
					if err == nil {
						report.Conflicts = ss.Conflicts
					}
				}
				break
			}
		}
	}

	if asJSON {
		return json.NewEncoder(out).Encode(report)
	}

	fmt.Fprintf(out, "Project:       %s\n", report.Project)
	fmt.Fprintf(out, "Provider:      %s\n", report.Provider)
	fmt.Fprintf(out, "Agent:         %s\n", report.Agent)
	fmt.Fprintf(out, "Sync engine:   %s\n", report.Sync)
	fmt.Fprintf(out, "Mode:          %s\n", report.Mode)
	if report.ActiveSide != "" {
		fmt.Fprintf(out, "Active side:   %s\n", report.ActiveSide)
	}
	if report.VMID != "" {
		fmt.Fprintf(out, "VM:            %s\n", report.VMID)
		if report.VMState != "" {
			fmt.Fprintf(out, "VM state:      %s\n", report.VMState)
		}
	}
	if report.MTDCostUSD > 0 {
		fmt.Fprintf(out, "Month-to-date: $%.2f (estimate)\n", report.MTDCostUSD)
	}
	if report.HasSyncSession {
		if report.Conflicts == 0 {
			fmt.Fprintln(out, "Conflicts:     0 (clean)")
		} else {
			fmt.Fprintf(out, "Conflicts:     %d (run `moorpost conflicts` for details)\n", report.Conflicts)
		}
	}
	return nil
}
