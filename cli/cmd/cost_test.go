package cmd

import (
	"bytes"
	"context"
	"math"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// TestRescaleEstimate_NoOpWhenNotEstimate ensures we don't touch cb when the
// provider already returned billed (non-estimate) data.
func TestRescaleEstimate_NoOpWhenNotEstimate(t *testing.T) {
	cb := provider.CostBreakdown{
		Compute:    10,
		Total:      10,
		IsEstimate: false,
	}
	period := provider.TimeRange{
		Start: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	hours, periodHours, scaled := rescaleEstimate(&cb, period, period.End)
	if scaled {
		t.Errorf("scaled=true for non-estimate breakdown; want false")
	}
	if cb.Compute != 10 || cb.Total != 10 {
		t.Errorf("cb mutated: %+v", cb)
	}
	if hours != 0 || periodHours != 0 {
		t.Errorf("expected zero outputs for no-op, got hours=%v period=%v", hours, periodHours)
	}
}

// TestRescaleEstimate_FixesCalendarHourBug is the iter 31 motivating case:
// MTD period (May = 31 days = 744h calendar) but the VM ran only 1.5 hours.
// Pre-fix code returned ~$50 (744h × $0.067). The fix returns ~$0.10.
func TestRescaleEstimate_FixesCalendarHourBug(t *testing.T) {
	withInjectedAudit(t, []audit.Entry{
		{Command: "handoff", Timestamp: time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)},
		{Command: "return", Timestamp: time.Date(2026, 5, 10, 11, 30, 0, 0, time.UTC)},
	})

	period := provider.TimeRange{
		Start: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), // May = 744 hours
	}
	const buggyTotal = 49.85 // 744h × $0.067 — what the buggy provider returned
	cb := provider.CostBreakdown{
		Compute:    buggyTotal,
		Total:      buggyTotal,
		IsEstimate: true,
	}
	hours, periodHours, scaled := rescaleEstimate(&cb, period, period.End)
	if !scaled {
		t.Fatalf("scaled=false; want true")
	}
	if periodHours != 744 {
		t.Errorf("periodHours = %v, want 744", periodHours)
	}
	if !floatNear(hours, 1.5, 1e-6) {
		t.Errorf("running hours = %v, want 1.5", hours)
	}
	wantCompute := 1.5 * (buggyTotal / 744)
	if !floatNear(cb.Compute, wantCompute, 1e-6) {
		t.Errorf("cb.Compute = %v, want ~%v", cb.Compute, wantCompute)
	}
	if !floatNear(cb.Total, wantCompute, 1e-6) {
		t.Errorf("cb.Total = %v, want ~%v", cb.Total, wantCompute)
	}
	// Sanity check on the magnitude of the fix: the buggy value is now
	// ~500x smaller, which is the entire point of the iteration.
	if cb.Compute > buggyTotal/100 {
		t.Errorf("scaling didn't shrink the estimate enough: %v vs buggy %v", cb.Compute, buggyTotal)
	}
}

// TestRescaleEstimate_ZeroPeriodHoursIsSafe defends against division-by-zero
// when a caller passes an inverted/empty period.
func TestRescaleEstimate_ZeroPeriodHoursIsSafe(t *testing.T) {
	t1 := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	period := provider.TimeRange{Start: t1, End: t1}
	cb := provider.CostBreakdown{Compute: 1, Total: 1, IsEstimate: true}
	_, _, scaled := rescaleEstimate(&cb, period, t1)
	if scaled {
		t.Errorf("scaled=true for zero-length period; want false")
	}
}

// TestRescaleEstimate_AuditReaderError leaves cb unchanged: failure to read
// the audit log is best-effort, not fatal.
func TestRescaleEstimate_AuditReaderError(t *testing.T) {
	old := auditReaderForCost
	t.Cleanup(func() { auditReaderForCost = old })
	auditReaderForCost = func(int) ([]audit.Entry, error) {
		return nil, errAudit
	}

	period := provider.TimeRange{
		Start: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	cb := provider.CostBreakdown{Compute: 48.24, Total: 48.24, IsEstimate: true}
	_, _, scaled := rescaleEstimate(&cb, period, period.End)
	if scaled {
		t.Errorf("scaled=true on audit reader error; want false")
	}
	if cb.Compute != 48.24 || cb.Total != 48.24 {
		t.Errorf("cb mutated on audit error: %+v", cb)
	}
}

// TestRescaleEstimate_ZeroRateLeavesUnchanged: when the provider returned
// zero compute (unknown machine type), there's nothing to rescale.
func TestRescaleEstimate_ZeroRateLeavesUnchanged(t *testing.T) {
	withInjectedAudit(t, []audit.Entry{
		{Command: "handoff", Timestamp: time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)},
		{Command: "return", Timestamp: time.Date(2026, 5, 10, 11, 30, 0, 0, time.UTC)},
	})

	period := provider.TimeRange{
		Start: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	cb := provider.CostBreakdown{Compute: 0, Total: 0, IsEstimate: true}
	_, _, scaled := rescaleEstimate(&cb, period, period.End)
	if scaled {
		t.Errorf("scaled=true for zero-rate breakdown; want false")
	}
}

// TestRescaleEstimate_PreservesNonComputeComponents: only Compute and Total
// are scaled; Disk/Network/Other are passed through.
func TestRescaleEstimate_PreservesNonComputeComponents(t *testing.T) {
	withInjectedAudit(t, []audit.Entry{
		{Command: "handoff", Timestamp: time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)},
		{Command: "return", Timestamp: time.Date(2026, 5, 10, 11, 0, 0, 0, time.UTC)}, // 1h
	})

	period := provider.TimeRange{
		Start: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), // May = 744h
	}
	const buggyCompute = 7.44 // 744h × $0.01
	cb := provider.CostBreakdown{
		Compute:    buggyCompute,
		Disk:       0.50,
		Network:    0.20,
		Other:      0.10,
		Total:      buggyCompute + 0.50 + 0.20 + 0.10,
		IsEstimate: true,
	}
	originalTotal := cb.Total
	_, _, scaled := rescaleEstimate(&cb, period, period.End)
	if !scaled {
		t.Fatalf("scaled=false; want true")
	}
	wantCompute := 1.0 * (buggyCompute / 744)
	if !floatNear(cb.Compute, wantCompute, 1e-6) {
		t.Errorf("Compute = %v, want %v", cb.Compute, wantCompute)
	}
	if cb.Disk != 0.50 || cb.Network != 0.20 || cb.Other != 0.10 {
		t.Errorf("non-compute components mutated: %+v", cb)
	}
	wantTotal := originalTotal - buggyCompute + wantCompute
	if !floatNear(cb.Total, wantTotal, 1e-6) {
		t.Errorf("Total = %v, want %v", cb.Total, wantTotal)
	}
}

// withInjectedAudit replaces auditReaderForCost with a fixed entry list for
// the duration of the test.
func withInjectedAudit(t *testing.T, entries []audit.Entry) {
	t.Helper()
	old := auditReaderForCost
	t.Cleanup(func() { auditReaderForCost = old })
	auditReaderForCost = func(int) ([]audit.Entry, error) {
		return entries, nil
	}
}

func floatNear(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

// errAudit is a sentinel for the audit-error test.
var errAudit = stringErr("audit read failed (test sentinel)")

type stringErr string

func (e stringErr) Error() string { return string(e) }

// makeCostContextWithProvider builds a Context wired to a Cost-returning
// provider, mirroring makeCapContext but with caller-controlled cost return.
func makeCostContextWithProvider(t *testing.T, costReturn provider.CostBreakdown) *Context {
	t.Helper()
	c, _ := makeLifecycleContext(t, &fakeProvider{}, true)
	c.Provider = &capFakeProvider{
		fakeProvider: &fakeProvider{},
		costReturn:   costReturn,
	}
	c.Stderr = &bytes.Buffer{}
	return c
}

// readVMRecord re-opens state from disk and returns the VM record (or
// fails the test if not present).
func readVMRecord(t *testing.T, c *Context, vmID string) state.VMRecord {
	t.Helper()
	st, err := state.Open(c.StatePath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	rec, ok := st.VMs[vmID]
	if !ok {
		t.Fatalf("VM %q not in state", vmID)
	}
	return rec
}

// TestRunCost_RefreshesMonthToDateCache_ForMTD verifies that running
// `moorpost cost --period mtd` writes the (possibly rescaled) Total back
// to state.VMs[vmID].MonthToDateUSD so the status bar / tree view see
// the same number.
func TestRunCost_RefreshesMonthToDateCache_ForMTD(t *testing.T) {
	withInjectedAudit(t, []audit.Entry{}) // no running hours → rescaled to 0

	c := makeCostContextWithProvider(t, provider.CostBreakdown{
		Compute:    49.85, // calendar-hour buggy estimate from provider
		Total:      49.85,
		IsEstimate: true,
	})

	// Pre-state: cache holds a stale value.
	c.State.VMs["webapp-vm"] = state.VMRecord{Provider: "fake", MonthToDateUSD: 49.85}
	if err := c.State.Save(c.StatePath); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	var out bytes.Buffer
	if err := RunCost(context.Background(), &out, c, "mtd", false); err != nil {
		t.Fatalf("RunCost: %v", err)
	}

	rec := readVMRecord(t, c, "webapp-vm")
	// Empty audit → rescaled compute = 0; Total = 0.
	if rec.MonthToDateUSD != 0 {
		t.Errorf("MonthToDateUSD = %v, want 0 (rescaled)", rec.MonthToDateUSD)
	}
}

// TestRunCost_DoesNotRefreshCache_ForToday: the cache field is named
// MonthToDateUSD; writing today/week totals would clobber it.
func TestRunCost_DoesNotRefreshCache_ForToday(t *testing.T) {
	withInjectedAudit(t, []audit.Entry{})

	c := makeCostContextWithProvider(t, provider.CostBreakdown{
		Compute:    1.61, // 24h × $0.067
		Total:      1.61,
		IsEstimate: true,
	})

	const preserved = 12.34
	c.State.VMs["webapp-vm"] = state.VMRecord{Provider: "fake", MonthToDateUSD: preserved}
	if err := c.State.Save(c.StatePath); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	var out bytes.Buffer
	if err := RunCost(context.Background(), &out, c, "today", false); err != nil {
		t.Fatalf("RunCost: %v", err)
	}

	rec := readVMRecord(t, c, "webapp-vm")
	if rec.MonthToDateUSD != preserved {
		t.Errorf("MonthToDateUSD = %v, want %v (today should not touch cache)", rec.MonthToDateUSD, preserved)
	}
}

// TestRunCost_DoesNotRefreshCache_ForWeek: same gating as today.
func TestRunCost_DoesNotRefreshCache_ForWeek(t *testing.T) {
	withInjectedAudit(t, []audit.Entry{})

	c := makeCostContextWithProvider(t, provider.CostBreakdown{
		Compute:    11.27, // 168h × $0.067
		Total:      11.27,
		IsEstimate: true,
	})

	const preserved = 7.77
	c.State.VMs["webapp-vm"] = state.VMRecord{Provider: "fake", MonthToDateUSD: preserved}
	if err := c.State.Save(c.StatePath); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	var out bytes.Buffer
	if err := RunCost(context.Background(), &out, c, "week", false); err != nil {
		t.Fatalf("RunCost: %v", err)
	}

	rec := readVMRecord(t, c, "webapp-vm")
	if rec.MonthToDateUSD != preserved {
		t.Errorf("MonthToDateUSD = %v, want %v (week should not touch cache)", rec.MonthToDateUSD, preserved)
	}
}

// TestRunCost_RefreshesMonthToDateCache_NonEstimate covers the v1.1 path:
// when the provider returns billed (non-estimate) data, the rescaler
// no-ops and we cache the provider's value verbatim.
func TestRunCost_RefreshesMonthToDateCache_NonEstimate(t *testing.T) {
	withInjectedAudit(t, []audit.Entry{}) // not consulted when IsEstimate=false

	c := makeCostContextWithProvider(t, provider.CostBreakdown{
		Compute:    2.50,
		Total:      2.50,
		IsEstimate: false,
	})

	c.State.VMs["webapp-vm"] = state.VMRecord{Provider: "fake", MonthToDateUSD: 0}
	if err := c.State.Save(c.StatePath); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	var out bytes.Buffer
	if err := RunCost(context.Background(), &out, c, "mtd", false); err != nil {
		t.Fatalf("RunCost: %v", err)
	}

	rec := readVMRecord(t, c, "webapp-vm")
	if !floatNear(rec.MonthToDateUSD, 2.50, 1e-6) {
		t.Errorf("MonthToDateUSD = %v, want 2.50 (non-estimate verbatim)", rec.MonthToDateUSD)
	}
}
