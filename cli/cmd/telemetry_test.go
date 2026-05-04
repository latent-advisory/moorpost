package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/state"
)

func TestTelemetryStatusDefaultsOff(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	if err := RunTelemetry(&out, statePath, "status"); err != nil {
		t.Fatalf("RunTelemetry: %v", err)
	}
	if !strings.Contains(out.String(), "OFF (default)") {
		t.Errorf("expected OFF default; got %q", out.String())
	}
	if !strings.Contains(out.String(), "telemetry on") {
		t.Errorf("expected hint to opt-in: %q", out.String())
	}
}

func TestTelemetryEmptyOpDefaultsToStatus(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	if err := RunTelemetry(&out, statePath, ""); err != nil {
		t.Fatalf("RunTelemetry: %v", err)
	}
	if !strings.Contains(out.String(), "OFF") {
		t.Errorf("empty op should print status; got %q", out.String())
	}
}

func TestTelemetryOnPersists(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	if err := RunTelemetry(&out, statePath, "on"); err != nil {
		t.Fatalf("RunTelemetry on: %v", err)
	}
	if !strings.Contains(out.String(), "Telemetry opt-in: ON") {
		t.Errorf("expected ON message: %q", out.String())
	}
	// Re-read state and confirm.
	st, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !st.TelemetryOptIn {
		t.Error("TelemetryOptIn was not persisted")
	}
}

func TestTelemetryOnThenOffRoundTrip(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	if err := RunTelemetry(&out, statePath, "on"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := RunTelemetry(&out, statePath, "off"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Telemetry opt-in: OFF") {
		t.Errorf("expected OFF message: %q", out.String())
	}
	st, _ := state.Open(statePath)
	if st.TelemetryOptIn {
		t.Error("TelemetryOptIn should be false after `off`")
	}
}

func TestTelemetryStatusReflectsOn(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	if err := RunTelemetry(&out, statePath, "on"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := RunTelemetry(&out, statePath, "status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Telemetry: ON") {
		t.Errorf("status after on should report ON: %q", out.String())
	}
	if !strings.Contains(out.String(), "consent gate") {
		t.Errorf("status should explain no sender ships yet: %q", out.String())
	}
}

func TestTelemetryUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	statePath := filepath.Join(t.TempDir(), "state.json")
	err := RunTelemetry(&out, statePath, "maybe")
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("err = %v, want unknown-subcommand error", err)
	}
}

func TestTelemetryOptInDataDescriptionVisible(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	if err := RunTelemetry(&out, statePath, "on"); err != nil {
		t.Fatal(err)
	}
	// Spec from PLUGIN.md §10 #12: must surface what gets collected and
	// what does not, before any data could be sent.
	for _, want := range []string{
		"command name", "exit code", "duration", "OS", "CLI version",
		"Never sent", "project names", "file paths",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("on-message missing %q:\n%s", want, out.String())
		}
	}
}
