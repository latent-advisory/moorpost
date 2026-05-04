package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/state"
)

func makeContext(t *testing.T) *Context {
	t.Helper()
	cfg := config.Default()
	cfg.ProjectSlug = "argus"
	cfg.Provider.Type = "gcp"
	cfg.Agent.Type = "claude-code"
	cfg.Sync.Engine = "mutagen"
	st := state.New()
	st.SetProject("/abs/argus", state.ProjectState{
		Slug:       "argus",
		VMID:       "argus-vm",
		ActiveSide: state.SideLocal,
	})
	st.VMs["argus-vm"] = state.VMRecord{
		Provider:       "gcp",
		ExternalIP:     "35.1.2.3",
		StateCache:     "stopped",
		MonthToDateUSD: 1.42,
	}
	return &Context{
		Config:     cfg,
		State:      st,
		ProjectDir: "/abs/argus",
	}
}

func TestRunStatusText(t *testing.T) {
	c := makeContext(t)
	var out bytes.Buffer
	if err := RunStatus(&out, c, false); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"argus", "gcp", "claude-code", "mutagen",
		"local", // active side
		"argus-vm", "stopped",
		"$1.42",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
}

func TestRunStatusJSON(t *testing.T) {
	c := makeContext(t)
	var out bytes.Buffer
	if err := RunStatus(&out, c, true); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("JSON unmarshal: %v\noutput: %s", err, out.String())
	}
	if report.Project != "argus" || report.Provider != "gcp" || report.VMID != "argus-vm" {
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
