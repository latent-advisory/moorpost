package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/audit"
)

func newTestAuditLogger(t *testing.T, fixedNow time.Time) *audit.Logger {
	t.Helper()
	return &audit.Logger{
		Dir: t.TempDir(),
		Now: func() time.Time { return fixedNow },
	}
}

func TestRunAuditEmpty(t *testing.T) {
	logger := newTestAuditLogger(t, time.Now())
	var out bytes.Buffer
	if err := RunAudit(&out, logger, AuditOptions{Days: 7}); err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	if !strings.Contains(out.String(), "No audit entries") {
		t.Errorf("expected 'No audit entries' message; got %q", out.String())
	}
}

func TestRunAuditPrintsHumanReadable(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 30, 45, 0, time.UTC)
	logger := newTestAuditLogger(t, now)
	for _, e := range []audit.Entry{
		{Timestamp: now.Add(-2 * time.Hour), Command: "init", DurationMS: 100},
		{Timestamp: now.Add(-1 * time.Hour), Command: "provision", DurationMS: 14300, ExitCode: 0},
	} {
		if err := logger.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := RunAudit(&out, logger, AuditOptions{Days: 1}); err != nil {
		t.Fatalf("RunAudit: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "init") || !strings.Contains(s, "provision") {
		t.Errorf("expected init+provision in output: %q", s)
	}
	if !strings.Contains(s, "exit=0") {
		t.Errorf("expected exit=0 marker: %q", s)
	}
}

func TestRunAuditJSONOutput(t *testing.T) {
	logger := newTestAuditLogger(t, time.Now())
	if err := logger.Append(audit.Entry{Command: "init", DurationMS: 100}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunAudit(&out, logger, AuditOptions{Days: 1, AsJSON: true}); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	var e audit.Entry
	if err := dec.Decode(&e); err != nil {
		t.Fatalf("JSON decode: %v\noutput: %q", err, out.String())
	}
	if e.Command != "init" {
		t.Errorf("decoded command = %q, want init", e.Command)
	}
}

func TestRunAuditTrimsToLast(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	logger := newTestAuditLogger(t, now)
	for i := 0; i < 5; i++ {
		if err := logger.Append(audit.Entry{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Command:   "x",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := RunAudit(&out, logger, AuditOptions{Days: 1, Last: 2}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "exit=") != 2 {
		t.Errorf("expected 2 entries, got output:\n%s", out.String())
	}
}

func TestRunAuditWithError(t *testing.T) {
	logger := newTestAuditLogger(t, time.Now())
	if err := logger.Append(audit.Entry{
		Command:  "provision",
		ExitCode: 1,
		Error:    "Compute Engine API not enabled",
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunAudit(&out, logger, AuditOptions{Days: 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "exit=1") {
		t.Errorf("expected exit=1: %q", out.String())
	}
	if !strings.Contains(out.String(), "Compute Engine API not enabled") {
		t.Errorf("expected error line: %q", out.String())
	}
}

func TestRunAuditNilLoggerErrors(t *testing.T) {
	var out bytes.Buffer
	if err := RunAudit(&out, nil, AuditOptions{}); err == nil {
		t.Error("RunAudit accepted nil logger")
	}
}

func TestRunAuditNegativeDaysDefaultsTo7(t *testing.T) {
	logger := newTestAuditLogger(t, time.Now())
	var out bytes.Buffer
	if err := RunAudit(&out, logger, AuditOptions{Days: -3}); err != nil {
		t.Errorf("Days=-3 should default not error: %v", err)
	}
}
