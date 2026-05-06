package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
)

// emptyAuditReader makes computeMTDCost return 0 so tests exercise the
// cached-value fallback path (MonthToDateUSD from state).
func injectEmptyAudit(t *testing.T) {
	t.Helper()
	old := auditReaderForCost
	t.Cleanup(func() { auditReaderForCost = old })
	auditReaderForCost = func(int) ([]audit.Entry, error) { return nil, nil }
}

func makeContext(t *testing.T) *Context {
	t.Helper()
	cfg := config.Default()
	cfg.ProjectSlug = "webapp"
	cfg.Provider.Type = "gcp"
	cfg.Agent.Type = "claude-code"
	cfg.Sync.Engine = "mutagen"
	st := state.New()
	st.SetProject("/abs/webapp", state.ProjectState{
		Slug:       "webapp",
		VMID:       "webapp-vm",
		ActiveSide: state.SideLocal,
	})
	st.VMs["webapp-vm"] = state.VMRecord{
		Provider:       "gcp",
		ExternalIP:     "35.1.2.3",
		StateCache:     "stopped",
		MonthToDateUSD: 1.42,
	}
	return &Context{
		Config:     cfg,
		State:      st,
		ProjectDir: "/abs/webapp",
	}
}

func TestRunStatusText(t *testing.T) {
	injectEmptyAudit(t)
	c := makeContext(t)
	var out bytes.Buffer
	if err := RunStatus(&out, c, false); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"webapp", "gcp", "claude-code", "mutagen",
		"local", // active side
		"webapp-vm", "stopped",
		"$1.42",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
}

func TestRunStatusJSON(t *testing.T) {
	injectEmptyAudit(t)
	c := makeContext(t)
	var out bytes.Buffer
	if err := RunStatus(&out, c, true); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("JSON unmarshal: %v\noutput: %s", err, out.String())
	}
	if report.Project != "webapp" || report.Provider != "gcp" || report.VMID != "webapp-vm" {
		t.Errorf("report = %+v", report)
	}
	if report.MTDCostUSD != 1.42 {
		t.Errorf("MTDCostUSD = %v, want 1.42", report.MTDCostUSD)
	}
}

func TestRunStatusRejectsMissingConfig(t *testing.T) {
	c := &Context{}
	var out bytes.Buffer
	if err := RunStatus(&out, c, false); err == nil {
		t.Error("RunStatus accepted empty context")
	}
}

// statusFakeSync returns a configurable Sync.Status; everything else is no-op.
type statusFakeSync struct {
	statusReturn mpsync.SyncStatus
	statusErr    error
}

func (s *statusFakeSync) ID() string { return "fake-sync" }
func (s *statusFakeSync) StartSession(context.Context, mpsync.SyncSpec) (mpsync.SyncSessionID, error) {
	return "", nil
}
func (s *statusFakeSync) Pause(context.Context, mpsync.SyncSessionID) error  { return nil }
func (s *statusFakeSync) Resume(context.Context, mpsync.SyncSessionID) error { return nil }
func (s *statusFakeSync) OneShot(context.Context, mpsync.Endpoint, mpsync.Endpoint, mpsync.Direction) error {
	return nil
}
func (s *statusFakeSync) Status(context.Context, mpsync.SyncSessionID) (mpsync.SyncStatus, error) {
	return s.statusReturn, s.statusErr
}
func (s *statusFakeSync) Stop(context.Context, mpsync.SyncSessionID) error { return nil }
func (s *statusFakeSync) ListConflicts(context.Context, mpsync.SyncSessionID) ([]mpsync.Conflict, error) {
	return nil, nil
}

// TestRunStatusJSON_Conflicts verifies that when the project's
// SyncSessionID is populated, the JSON includes has_sync_session +
// sync_session_id + conflicts (from Sync.Status).
func TestRunStatusJSON_Conflicts(t *testing.T) {
	c := makeContext(t)
	// Add a sync session id and wire a fake sync that reports 3 conflicts.
	ps, _ := c.State.GetProject(c.ProjectDir)
	ps.SyncSessionID = "webapp-handoff"
	c.State.SetProject(c.ProjectDir, ps)
	c.Sync = &statusFakeSync{statusReturn: mpsync.SyncStatus{Conflicts: 3}}

	var out bytes.Buffer
	if err := RunStatus(&out, c, true); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, out.String())
	}
	if !report.HasSyncSession {
		t.Error("HasSyncSession=false; want true")
	}
	if report.SyncSessionID != "webapp-handoff" {
		t.Errorf("SyncSessionID = %q", report.SyncSessionID)
	}
	if report.Conflicts != 3 {
		t.Errorf("Conflicts = %d, want 3", report.Conflicts)
	}
}

// TestRunStatusJSON_NoSyncSession_OmitsConflicts: the absence of a
// SyncSessionID means HasSyncSession=false and Conflicts is omitted from
// JSON (omitempty zero-value).
func TestRunStatusJSON_NoSyncSession_OmitsConflicts(t *testing.T) {
	c := makeContext(t)
	c.Sync = &statusFakeSync{statusReturn: mpsync.SyncStatus{Conflicts: 99}}

	var out bytes.Buffer
	if err := RunStatus(&out, c, true); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if strings.Contains(out.String(), "conflicts") || strings.Contains(out.String(), "has_sync_session") {
		t.Errorf("expected no conflict fields in JSON; got: %s", out.String())
	}
}

// TestRunStatusJSON_SyncStatusError_PreservesZero: when the sync engine's
// Status fails, conflicts stays 0 (best-effort) — status output still
// succeeds.
func TestRunStatusJSON_SyncStatusError_PreservesZero(t *testing.T) {
	c := makeContext(t)
	ps, _ := c.State.GetProject(c.ProjectDir)
	ps.SyncSessionID = "webapp-handoff"
	c.State.SetProject(c.ProjectDir, ps)
	c.Sync = &statusFakeSync{statusErr: errSyncDown}

	var out bytes.Buffer
	if err := RunStatus(&out, c, true); err != nil {
		t.Fatalf("RunStatus should not fail when Sync.Status errors: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !report.HasSyncSession {
		t.Error("HasSyncSession should still be true even when Sync.Status fails")
	}
	if report.Conflicts != 0 {
		t.Errorf("Conflicts = %d, want 0 (best-effort fallback)", report.Conflicts)
	}
}

var errSyncDown = newSimpleError("mutagen daemon not running")

func newSimpleError(s string) error { return &simpleErr{msg: s} }

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
