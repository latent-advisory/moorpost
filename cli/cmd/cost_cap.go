package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/provider"
)

// enforceCostCap checks that the project's month-to-date spend hasn't
// crossed `cost.monthly_cap_usd`. Returns nil if:
//
//   - the cap is 0 (disabled by default for projects that don't set it)
//   - the user passed `--override-cap` (override == true)
//   - the project has no provisioned VM yet (nothing to spend on)
//   - the MTD spend (per Provider.Cost) is strictly less than the cap
//
// Returns a descriptive error otherwise. The error includes the actual
// numbers + the override-flag hint so the user can decide whether to
// bypass.
func enforceCostCap(ctx context.Context, c *Context, override bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Config == nil {
		return nil // can't check; let the caller decide
	}
	if override {
		return nil
	}
	cap := c.Config.Cost.MonthlyCapUSD
	if cap <= 0 {
		return nil // disabled
	}
	if c.Provider == nil || c.State == nil {
		return nil
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		// Nothing provisioned yet; can't have spent anything.
		return nil
	}

	now := time.Now().UTC()
	y, m, _ := now.Date()
	period := provider.TimeRange{
		Start: time.Date(y, m, 1, 0, 0, 0, 0, time.UTC),
		End:   now,
	}
	cb, err := c.Provider.Cost(ctx, ps.VMID, period)
	if err != nil {
		// Cost lookup failed — be conservative: if a partial estimate is
		// present and over cap, still enforce. Otherwise let the caller proceed
		// (we don't want a flaky billing API to lock users out).
		if cb.Total >= cap {
			return capExceededError(cb.Total, cap, ps.VMID)
		}
		return nil
	}
	if cb.Total >= cap {
		return capExceededError(cb.Total, cap, ps.VMID)
	}
	return nil
}

// ErrCostCapExceeded is the sentinel for cap-exceeded errors. Wrapped so
// callers can match via errors.Is.
var ErrCostCapExceeded = errors.New("cost cap exceeded")

func capExceededError(mtd, cap float64, vmID string) error {
	return fmt.Errorf("%w: VM %s month-to-date spend $%.2f ≥ cap $%.2f. "+
		"Bypass with --override-cap, or raise cost.monthly_cap_usd in .moorpost/config.yaml",
		ErrCostCapExceeded, vmID, mtd, cap)
}
