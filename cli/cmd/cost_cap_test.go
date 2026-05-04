package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/provider"
)

// capFakeProvider returns a fixed Cost. Used to test enforceCostCap
// without dragging in the full lifecycle harness.
type capFakeProvider struct {
	*fakeProvider
	costReturn provider.CostBreakdown
	costErr    error
}

func (p *capFakeProvider) Cost(_ context.Context, _ string, _ provider.TimeRange) (provider.CostBreakdown, error) {
	return p.costReturn, p.costErr
}

func makeCapContext(t *testing.T, capUSD float64, mtdUSD float64) *Context {
	t.Helper()
	c, _ := makeLifecycleContext(t, &fakeProvider{}, true)
	c.Config.Cost.MonthlyCapUSD = capUSD
	// Swap to a Cost-aware fake.
	c.Provider = &capFakeProvider{
		fakeProvider: &fakeProvider{},
		costReturn: provider.CostBreakdown{
			Total:      mtdUSD,
			IsEstimate: true,
		},
	}
	return c
}

func TestEnforceCostCapDisabledWhenZero(t *testing.T) {
	c := makeCapContext(t, 0, 999.99)
	if err := enforceCostCap(context.Background(), c, false); err != nil {
		t.Errorf("cap=0 should be disabled; got %v", err)
	}
}

func TestEnforceCostCapBelowCap(t *testing.T) {
	c := makeCapContext(t, 50, 12.34)
	if err := enforceCostCap(context.Background(), c, false); err != nil {
		t.Errorf("MTD < cap should pass; got %v", err)
	}
}

func TestEnforceCostCapAtOrAboveCap(t *testing.T) {
	tests := []struct{ mtd, cap float64 }{
		{50.00, 50.00}, // equal counts as exceeded (defensive)
		{75.50, 50.00},
	}
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			c := makeCapContext(t, tc.cap, tc.mtd)
			err := enforceCostCap(context.Background(), c, false)
			if err == nil {
				t.Fatalf("MTD=%.2f cap=%.2f should be rejected", tc.mtd, tc.cap)
			}
			if !errors.Is(err, ErrCostCapExceeded) {
				t.Errorf("err = %v, want ErrCostCapExceeded", err)
			}
			if !strings.Contains(err.Error(), "--override-cap") {
				t.Errorf("error should mention --override-cap: %q", err.Error())
			}
		})
	}
}

func TestEnforceCostCapOverrideBypasses(t *testing.T) {
	c := makeCapContext(t, 10, 999.99)
	if err := enforceCostCap(context.Background(), c, true); err != nil {
		t.Errorf("override=true should bypass; got %v", err)
	}
}

func TestEnforceCostCapNoVMNoCheck(t *testing.T) {
	// Project not provisioned yet — nothing to be over budget for.
	c, _ := makeLifecycleContext(t, &fakeProvider{}, false) // false = no project state
	c.Config.Cost.MonthlyCapUSD = 0.01
	if err := enforceCostCap(context.Background(), c, false); err != nil {
		t.Errorf("no VM should pass without check; got %v", err)
	}
}

func TestEnforceCostCapCostErrorWithLowEstimateProceeds(t *testing.T) {
	// Provider.Cost errored, but the partial estimate is below cap. Don't
	// lock the user out on a flaky billing API.
	c := makeCapContext(t, 50, 0)
	c.Provider.(*capFakeProvider).costErr = errors.New("billing API unavailable")
	if err := enforceCostCap(context.Background(), c, false); err != nil {
		t.Errorf("cost error with low partial should not block; got %v", err)
	}
}

func TestEnforceCostCapCostErrorWithHighEstimateBlocks(t *testing.T) {
	// Provider.Cost errored AND the partial estimate is over cap.
	c := makeCapContext(t, 50, 100)
	c.Provider.(*capFakeProvider).costErr = errors.New("billing API timeout")
	err := enforceCostCap(context.Background(), c, false)
	if err == nil {
		t.Fatal("partial estimate over cap should still block")
	}
	if !errors.Is(err, ErrCostCapExceeded) {
		t.Errorf("err = %v, want ErrCostCapExceeded", err)
	}
}

func TestEnforceCostCapNilContextSafe(t *testing.T) {
	if err := enforceCostCap(context.Background(), nil, false); err != nil {
		t.Errorf("nil context should be no-op; got %v", err)
	}
}
