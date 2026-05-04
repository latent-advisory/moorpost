package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/spf13/cobra"
)

var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "Show estimated cost for the project's VM",
	Long: `Reports the cost incurred by this project's VM over the chosen
period. v0.1 prints a list-price estimate (IsEstimate=true); real billing
API integration ships in v0.3.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunCost(cmd.Context(), cmd.OutOrStdout(), c, costFlagPeriod)
	},
}

var costFlagPeriod string

func init() {
	costCmd.Flags().StringVar(&costFlagPeriod, "period", "mtd", "period: mtd (month-to-date), today, week")
	rootCmd.AddCommand(costCmd)
}

// RunCost prints the cost for the requested period.
func RunCost(ctx context.Context, out io.Writer, c *Context, period string) error {
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
	tr, err := timeRangeForPeriod(period, time.Now())
	if err != nil {
		return err
	}
	cb, err := c.Provider.Cost(ctx, ps.VMID, tr)
	if err != nil {
		// Some providers return a partial CostBreakdown alongside an error
		// (e.g. ErrCostUnavailable). If there's nothing useful, surface.
		if cb.Total == 0 {
			return fmt.Errorf("cost: %w", err)
		}
		fmt.Fprintf(out, "(partial cost data: %v)\n", err)
	}
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
	return nil
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
