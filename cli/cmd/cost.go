package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/runtime"
	"github.com/spf13/cobra"
)

var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "Show estimated cost for the project's VM",
	Long: `Reports the cost incurred by this project's VM over the chosen
period.

The estimate uses the provider's list-price for the VM's machine type,
multiplied by the actual hours the VM was running during the period
(derived from local handoff/return events in ~/.moorpost/logs/). Real
billing-API integration ships in v1.1 behind a --actual opt-in flag.

Use --explain to see the methodology and which audit entries were used.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunCost(cmd.Context(), cmd.OutOrStdout(), c, costFlagPeriod, costFlagExplain)
	},
}

var (
	costFlagPeriod  string
	costFlagExplain bool
)

func init() {
	costCmd.Flags().StringVar(&costFlagPeriod, "period", "mtd", "period: mtd (month-to-date), today, week")
	costCmd.Flags().BoolVar(&costFlagExplain, "explain", false, "print methodology and audit entries used")
	rootCmd.AddCommand(costCmd)
}

// auditReader is a hook so tests can inject synthetic audit entries instead
// of touching the real ~/.moorpost/logs/ tree.
var auditReaderForCost = defaultAuditReader

func defaultAuditReader(daysBack int) ([]audit.Entry, error) {
	logger, err := auditLoggerForRun()
	if err != nil || logger == nil {
		return nil, err
	}
	return logger.Read(daysBack)
}

// RunCost prints the cost for the requested period.
func RunCost(ctx context.Context, out io.Writer, c *Context, period string, explain bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil {
		return errors.New("cost: incomplete context")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("cost: project not provisioned")
	}
	now := time.Now().UTC()
	tr, err := timeRangeForPeriod(period, now)
	if err != nil {
		return err
	}
	cb, providerErr := c.Provider.Cost(ctx, ps.VMID, tr)
	if providerErr != nil && cb.Total == 0 {
		return fmt.Errorf("cost: %w", providerErr)
	}
	// Provider.Cost (v0.x) computes Compute = rate × calendar_hours which
	// massively overestimates for handoff workflows. Rescale by the actual
	// running hours from the audit log when the result is an estimate.
	scaledHours, periodHours, scaled := rescaleEstimate(&cb, tr, now)

	fmt.Fprintf(out, "Period:    %s (%s → %s)\n", period, tr.Start.Format("2006-01-02"), tr.End.Format("2006-01-02 15:04"))
	fmt.Fprintf(out, "Compute:   $%.2f\n", cb.Compute)
	fmt.Fprintf(out, "Disk:      $%.2f\n", cb.Disk)
	fmt.Fprintf(out, "Network:   $%.2f\n", cb.Network)
	fmt.Fprintf(out, "Other:     $%.2f\n", cb.Other)
	fmt.Fprintf(out, "Total:     $%.2f", cb.Total)
	if cb.IsEstimate {
		fmt.Fprint(out, " (estimate)")
	}
	fmt.Fprintln(out)

	if providerErr != nil {
		fmt.Fprintf(out, "(partial cost data: %v)\n", providerErr)
	}

	if explain {
		printExplain(out, periodHours, scaledHours, scaled, cb)
	}
	return nil
}

// rescaleEstimate replaces cb.Compute and cb.Total with values based on the
// actual VM running hours (per the audit log) instead of calendar hours.
// Only runs when cb.IsEstimate is true. Returns (actual_hours,
// period_calendar_hours, scaled_bool).
func rescaleEstimate(cb *provider.CostBreakdown, period provider.TimeRange, now time.Time) (float64, float64, bool) {
	if !cb.IsEstimate {
		return 0, 0, false
	}
	periodHours := period.End.Sub(period.Start).Hours()
	if periodHours <= 0 {
		return 0, 0, false
	}
	rate := cb.Compute / periodHours
	if rate <= 0 {
		// Provider returned zero compute (e.g. unknown machine type), nothing
		// to rescale. Leave cb as-is; caller will see 0.
		return 0, periodHours, false
	}
	// Look up actual running hours from the audit log. The audit log can have
	// entries up to 30 days old (default retention). For periods longer than
	// 30 days we'd need a larger Read window or persisted state — out of
	// scope for v1.
	daysBack := int(periodHours/24) + 1
	if daysBack < 1 {
		daysBack = 1
	}
	if daysBack > 60 {
		daysBack = 60
	}
	entries, err := auditReaderForCost(daysBack)
	if err != nil {
		// Audit log unavailable — best-effort. Leave cb as-is.
		return 0, periodHours, false
	}
	actualHours := runtime.RunningHours(entries, period, now)
	newCompute := rate * actualHours
	delta := newCompute - cb.Compute
	cb.Compute = newCompute
	cb.Total += delta
	return actualHours, periodHours, true
}

func printExplain(out io.Writer, periodHours, runHours float64, scaled bool, cb provider.CostBreakdown) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Methodology:")
	fmt.Fprintf(out, "  Period spans:      %.1f calendar hours\n", periodHours)
	if scaled {
		fmt.Fprintf(out, "  VM running hours:  %.2f (from handoff/return events in audit log)\n", runHours)
		if periodHours > 0 {
			fmt.Fprintf(out, "  Implied $/hour:    $%.4f (list price for this machine type)\n", cb.Compute/maxFloat(runHours, 1e-9))
		}
		fmt.Fprintln(out, "  Compute = rate × running hours.")
	} else {
		fmt.Fprintln(out, "  No running-hour scaling (provider returned non-estimate cost).")
	}
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Real billing-API integration (actual billed amounts) ships in v1.1.")
	fmt.Fprintln(out, "Until then, treat (estimate) as a list-price approximation.")
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// timeRangeForPeriod converts a period string to a TimeRange ending at now.
func timeRangeForPeriod(period string, now time.Time) (provider.TimeRange, error) {
	switch period {
	case "", "mtd":
		// Start of current calendar month, in UTC.
		y, m, _ := now.UTC().Date()
		start := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		return provider.TimeRange{Start: start, End: now.UTC()}, nil
	case "today":
		y, m, d := now.UTC().Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return provider.TimeRange{Start: start, End: now.UTC()}, nil
	case "week":
		// Last 7 days.
		return provider.TimeRange{Start: now.UTC().Add(-7 * 24 * time.Hour), End: now.UTC()}, nil
	default:
		return provider.TimeRange{}, fmt.Errorf("cost: unknown period %q (want mtd|today|week)", period)
	}
}
