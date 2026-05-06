package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/provider/gcp"
	"github.com/latent-advisory/moorpost/cli/internal/runtime"
	"github.com/latent-advisory/moorpost/cli/internal/state"
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
	// Always emitted (even when 0) so the extension can show "$0.00"
	// to confirm MTD tracking is live, instead of an ambiguous absence.
	MTDCostUSD float64 `json:"month_to_date_usd"`
	// AuthCached reports whether the configured agent has a cached
	// credential in the OS keychain. Always emitted (true or false) so
	// the extension can route the status-bar click to the right next
	// step (auth vs. provision vs. handoff).
	AuthCached bool `json:"auth_cached"`
	// Conflicts is the unresolved-conflict count from the active sync
	// session, if any. -1 means "session known but conflict count is
	// unavailable" (e.g., mutagen daemon down). Omitted when no session.
	Conflicts        int    `json:"conflicts,omitempty"`
	HasSyncSession   bool   `json:"has_sync_session,omitempty"`
	SyncSessionID    string `json:"sync_session_id,omitempty"`
	// AgentSessionID is the Claude Code session ID currently bound to
	// this project (the value used for `claude --resume <id>`). The
	// extension uses it to auto-resume the right session when re-opening
	// the local Claude terminal after a return.
	AgentSessionID string `json:"agent_session_id,omitempty"`
	// PendingResumeSID is the single-use baton set by `moorpost handoff`
	// for the wrapper's --resume injection. Mirrored into status output
	// so the extension knows whether the "Migrate this conversation" UX
	// is meaningful — present means the wrapper has something to consume
	// on the next plugin claude spawn.
	PendingResumeSID string `json:"pending_resume_sid,omitempty"`
	// RemoteSIDs is the per-session routing set: SIDs in this list are
	// routed to the remote VM by the wrapper. Empty/missing on projects
	// that haven't done a per-session handoff yet — extension treats
	// nil and `[]` identically.
	RemoteSIDs []string `json:"remote_sids,omitempty"`
}

// computeMTDCost estimates the month-to-date VM cost from the audit log and
// the machine type in config. No GCP API call — uses list-price table plus
// actual running hours derived from handoff/return events. Returns 0 on any
// error (config missing, unknown machine type, unreadable audit log) so it
// never blocks the status call.
func computeMTDCost(c *Context) float64 {
	if c == nil || c.Config == nil {
		return 0
	}
	gcpCfg := pickSubsection(c.Config.Provider.Raw, c.Config.Provider.Type)
	machineType, _ := gcpCfg["machine_type"].(string)
	if machineType == "" {
		machineType = "e2-standard-2"
	}
	rate, ok := gcp.ListPriceUSDPerHour(machineType)
	if !ok || rate <= 0 {
		return 0
	}
	now := time.Now().UTC()
	y, m, _ := now.Date()
	start := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	tr := provider.TimeRange{Start: start, End: now}
	daysBack := int(now.Sub(start).Hours()/24) + 2
	entries, err := auditReaderForCost(daysBack)
	if err != nil || len(entries) == 0 {
		return 0
	}
	hours := runtime.RunningHours(entries, tr, now)
	return rate * hours
}

// RunStatus prints the project status. If asJSON is true, emits the report
// as a single JSON object; otherwise human-readable text.
func RunStatus(out io.Writer, c *Context, asJSON bool) error {
	if c == nil || c.Config == nil {
		return fmt.Errorf("status: no project context loaded")
	}
	report := statusReport{
		Project:    c.Config.ProjectSlug,
		Provider:   c.Config.Provider.Type,
		Agent:      c.Config.Agent.Type,
		Sync:       c.Config.Sync.Engine,
		Mode:       string(c.Config.Mode),
		AuthCached: authCached(),
	}
	if c.State != nil {
		// Look up project by absolute project dir; fall back to slug match.
		for absPath, ps := range c.State.Projects {
			if ps.Slug == c.Config.ProjectSlug || absPath == c.ProjectDir {
				report.ActiveSide = string(ps.ActiveSide)
				report.VMID = ps.VMID
				report.AgentSessionID = ps.AgentSessionID
				report.PendingResumeSID = ps.PendingResumeSID
				report.RemoteSIDs = ps.RemoteSIDs
				if vm, ok := c.State.VMs[ps.VMID]; ok {
					report.VMState = vm.StateCache
				}
				// Compute cost from audit log + config machine type — no GCP
				// API call. Always fresh so the status bar reflects reality.
				if ps.VMID != "" {
					// Prefer a freshly computed value from the audit log; fall
					// back to the state-cache when the audit log is empty or
					// unavailable (e.g. first run, or no handoffs this month).
					if computed := computeMTDCost(c); computed > 0 {
						report.MTDCostUSD = computed
						_ = withVM(c, ps.VMID, func(rec *state.VMRecord) error {
							rec.MonthToDateUSD = computed
							return nil
						})
					} else if vm, ok := c.State.VMs[ps.VMID]; ok {
						report.MTDCostUSD = vm.MonthToDateUSD
					}
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
