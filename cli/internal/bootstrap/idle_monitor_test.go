package bootstrap

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBuildIdleMonitorUnits_Empty_WhenThresholdZero(t *testing.T) {
	u := BuildIdleMonitorUnits(0)
	if u.Script != "" || u.Service != "" || u.Timer != "" {
		t.Errorf("threshold=0 should return empty units; got %+v", u)
	}
}

func TestBuildIdleMonitorUnits_Empty_WhenThresholdNegative(t *testing.T) {
	u := BuildIdleMonitorUnits(-5)
	if u.Script != "" {
		t.Errorf("negative threshold should return empty; got %+v", u)
	}
}

func TestBuildIdleMonitorUnits_ScriptContent(t *testing.T) {
	u := BuildIdleMonitorUnits(60)
	for _, want := range []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"THRESHOLD=60",
		"INTERVAL=5", // CheckIntervalMinutes
		`who 2>/dev/null | wc -l`,
		`pgrep -c tmux`,
		`pgrep -c mutagen-agent`,
		`/sbin/shutdown -h now`,
		"/var/lib/moorpost",
	} {
		if !strings.Contains(u.Script, want) {
			t.Errorf("script missing %q\nfull script:\n%s", want, u.Script)
		}
	}
}

func TestBuildIdleMonitorUnits_ThresholdSubstitution(t *testing.T) {
	cases := []int{1, 30, 60, 120, 1440}
	for _, n := range cases {
		u := BuildIdleMonitorUnits(n)
		want := "THRESHOLD=" + itoa(n)
		if !strings.Contains(u.Script, want) {
			t.Errorf("threshold=%d: script missing %q", n, want)
		}
	}
}

func TestBuildIdleMonitorUnits_ServiceContent(t *testing.T) {
	u := BuildIdleMonitorUnits(60)
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"Type=oneshot",
		"ExecStart=/usr/local/bin/moorpost-idle-check.sh",
		"User=root",
	} {
		if !strings.Contains(u.Service, want) {
			t.Errorf("service missing %q\nfull service:\n%s", want, u.Service)
		}
	}
}

func TestBuildIdleMonitorUnits_TimerContent(t *testing.T) {
	u := BuildIdleMonitorUnits(60)
	for _, want := range []string{
		"[Unit]",
		"[Timer]",
		"OnUnitActiveSec=5min",
		"OnBootSec=5min",
		"Unit=moorpost-idle.service",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(u.Timer, want) {
			t.Errorf("timer missing %q\nfull timer:\n%s", want, u.Timer)
		}
	}
}

// TestBuildIdleMonitorUnits_BashSyntax runs `bash -n` against the generated
// script to catch syntax errors. Skips if bash isn't on PATH (e.g. CI image
// without bash).
func TestBuildIdleMonitorUnits_BashSyntax(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH; skipping syntax check")
	}
	u := BuildIdleMonitorUnits(60)
	cmd := exec.Command(bashPath, "-n")
	cmd.Stdin = strings.NewReader(u.Script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("bash -n: %v\nstderr:\n%s\nscript:\n%s", err, out, u.Script)
	}
}

func TestRender_OmitsIdleInstall_WhenZero(t *testing.T) {
	out, err := Render(BootstrapVars{
		ProjectSlug:         "webapp",
		LocalAbsPath:        "/Users/x/webapp",
		IdleAutoStopMinutes: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"moorpost-idle-check.sh",
		"moorpost-idle.timer",
		"moorpost-idle.service",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("rendered script has %q despite IdleAutoStopMinutes=0", forbidden)
		}
	}
}

func TestRender_IncludesIdleInstall_WhenNonZero(t *testing.T) {
	out, err := Render(BootstrapVars{
		ProjectSlug:         "webapp",
		LocalAbsPath:        "/Users/x/webapp",
		IdleAutoStopMinutes: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"step 7: idle monitor",
		"/usr/local/bin/moorpost-idle-check.sh",
		"/etc/systemd/system/moorpost-idle.service",
		"/etc/systemd/system/moorpost-idle.timer",
		"systemctl daemon-reload",
		"systemctl enable --now moorpost-idle.timer",
		"THRESHOLD=60",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered script missing %q", want)
		}
	}
}

// TestRender_BashSyntax_WithIdleMonitor: the rendered top-level bootstrap
// script (with idle monitor installed via heredoc) must parse cleanly.
func TestRender_BashSyntax_WithIdleMonitor(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH; skipping syntax check")
	}
	out, err := Render(BootstrapVars{
		ProjectSlug:         "webapp",
		LocalAbsPath:        "/Users/x/webapp",
		IdleAutoStopMinutes: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bashPath, "-n")
	cmd.Stdin = strings.NewReader(out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("bash -n: %v\nstderr:\n%s\nrendered script:\n%s", err, msg, out)
	}
}

// itoa avoids importing strconv just for the substring tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
