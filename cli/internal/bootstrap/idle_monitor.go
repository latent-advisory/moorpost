// VM-side idle-detection units installed at bootstrap time when
// `mode: persistent` and `persistent.auto_stop_minutes > 0`.
//
// Why VM-side and not laptop-side: persistent mode's whole point is that
// the VM keeps running while the laptop is offline / asleep / closed.
// A laptop-driven check defeats that — exactly when the cost-protection
// is most needed (laptop closed, user forgot to return), the laptop
// can't run the check. So the VM polls itself via systemd timer.
//
// Per PLUGIN.md §10 #6, idle is defined as the AND of three signals:
//   - no SSH session     (`who | wc -l` == 0)
//   - no tmux input      (`pgrep -c tmux` == 0)  // crude proxy; refine later
//   - no mutagen sync    (`pgrep -c mutagen-agent` == 0)
//
// When all three are 0 for `auto_stop_minutes` consecutive minutes, the VM
// runs `sudo shutdown -h now`. On GCE that transitions the instance to
// TERMINATED — no compute billing.

package bootstrap

import (
	"fmt"
	"strings"
)

// DefaultCheckIntervalMinutes is how often the systemd timer wakes the
// script when the caller doesn't override. 5 minutes is a reasonable
// balance: short enough that "stop after 60min" has 60/5 = 12 sample
// points (the threshold check is robust to one or two transient spikes),
// long enough that the timer's load is negligible.
const DefaultCheckIntervalMinutes = 5

// CheckIntervalMinutes preserves the v0.x-era exported name used by tests
// that assert "INTERVAL=5" against the default-rendered script. New code
// should pass an explicit interval via BuildIdleMonitorUnitsWithInterval
// or BootstrapVars.CheckIntervalMinutes instead of relying on this.
const CheckIntervalMinutes = DefaultCheckIntervalMinutes

// IdleMonitorUnits is the bundle of files installed for VM-side auto-stop.
type IdleMonitorUnits struct {
	// Script is the bash content of /usr/local/bin/moorpost-idle-check.sh
	Script string
	// Service is the systemd unit content of /etc/systemd/system/moorpost-idle.service
	Service string
	// Timer is the systemd unit content of /etc/systemd/system/moorpost-idle.timer
	Timer string
}

// BuildIdleMonitorUnits returns the script + service + timer for an idle
// threshold of thresholdMinutes, using DefaultCheckIntervalMinutes for the
// systemd timer cadence. Threshold of 0 returns empty units (caller
// should not install when 0).
func BuildIdleMonitorUnits(thresholdMinutes int) IdleMonitorUnits {
	return BuildIdleMonitorUnitsWithInterval(thresholdMinutes, DefaultCheckIntervalMinutes)
}

// BuildIdleMonitorUnitsWithInterval is BuildIdleMonitorUnits with an
// explicit systemd timer cadence in minutes. Used by the e2e test to lower
// the wait time for the auto-stop transition; production callers should
// generally use DefaultCheckIntervalMinutes.
func BuildIdleMonitorUnitsWithInterval(thresholdMinutes, intervalMinutes int) IdleMonitorUnits {
	if thresholdMinutes <= 0 {
		return IdleMonitorUnits{}
	}
	if intervalMinutes <= 0 {
		intervalMinutes = DefaultCheckIntervalMinutes
	}
	return IdleMonitorUnits{
		Script:  buildIdleScript(thresholdMinutes, intervalMinutes),
		Service: buildIdleService(),
		Timer:   buildIdleTimer(intervalMinutes),
	}
}

func buildIdleScript(thresholdMinutes, intervalMinutes int) string {
	const tmpl = `#!/usr/bin/env bash
# Moorpost VM-side idle monitor. Installed when mode=persistent.
# Stops the VM via 'sudo shutdown -h now' after {{THRESHOLD}} consecutive
# idle minutes. Idle = no SSH AND no tmux AND no mutagen-agent.

set -euo pipefail

STATE_DIR=/var/lib/moorpost
STATE_FILE="$STATE_DIR/idle_minutes"
LOG_FILE=/var/log/moorpost-idle.log
THRESHOLD={{THRESHOLD}}
INTERVAL={{INTERVAL}}

mkdir -p "$STATE_DIR"
touch "$LOG_FILE"

ssh_count=$(who 2>/dev/null | wc -l | tr -d ' ')
tmux_count=$(pgrep -c tmux 2>/dev/null || echo 0)
mutagen_count=$(pgrep -c mutagen-agent 2>/dev/null || echo 0)

if [ "$ssh_count" -gt 0 ] || [ "$tmux_count" -gt 0 ] || [ "$mutagen_count" -gt 0 ]; then
    # Active: reset the idle counter.
    echo 0 > "$STATE_FILE"
    echo "$(date -u +%FT%TZ) active ssh=$ssh_count tmux=$tmux_count mutagen=$mutagen_count" >> "$LOG_FILE"
    exit 0
fi

# All three signals are zero. Increment counter by INTERVAL minutes.
prev=$(cat "$STATE_FILE" 2>/dev/null || echo 0)
case "$prev" in ''|*[!0-9]*) prev=0 ;; esac
new=$((prev + INTERVAL))
echo "$new" > "$STATE_FILE"
echo "$(date -u +%FT%TZ) idle minutes=$new threshold=$THRESHOLD" >> "$LOG_FILE"

if [ "$new" -ge "$THRESHOLD" ]; then
    echo "$(date -u +%FT%TZ) auto-stop: idle for $new minutes (threshold $THRESHOLD); shutting down" >> "$LOG_FILE"
    /usr/bin/sudo /sbin/shutdown -h now "moorpost auto-stop after $new minutes idle"
fi
`
	out := strings.ReplaceAll(tmpl, "{{THRESHOLD}}", fmt.Sprintf("%d", thresholdMinutes))
	out = strings.ReplaceAll(out, "{{INTERVAL}}", fmt.Sprintf("%d", intervalMinutes))
	return out
}

func buildIdleService() string {
	return `[Unit]
Description=Moorpost VM-side idle check (auto-stop in persistent mode)

[Service]
Type=oneshot
ExecStart=/usr/local/bin/moorpost-idle-check.sh
User=root
`
}

func buildIdleTimer(intervalMinutes int) string {
	return fmt.Sprintf(`[Unit]
Description=Run moorpost idle check every %d minutes

[Timer]
OnBootSec=%dmin
OnUnitActiveSec=%dmin
AccuracySec=30s
Unit=moorpost-idle.service

[Install]
WantedBy=timers.target
`, intervalMinutes, intervalMinutes, intervalMinutes)
}

// renderIdleInstall returns the inline shell snippet that the bootstrap
// template uses to install the units. It encodes the file contents inline
// via heredocs so the bootstrap script remains a single self-contained
// file (no fetching, no external assets).
func renderIdleInstall(units IdleMonitorUnits) string {
	if units.Script == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n# --------------------------------------------------------------------------\n")
	b.WriteString("# 7. Idle monitor (mode=persistent + auto_stop_minutes>0)\n")
	b.WriteString("# --------------------------------------------------------------------------\n")
	b.WriteString(`echo "[moorpost] step 7: idle monitor"` + "\n")
	b.WriteString("install -d -m 0755 /var/lib/moorpost\n")
	b.WriteString("install -d -m 0755 /usr/local/bin\n")
	b.WriteString(heredoc("/usr/local/bin/moorpost-idle-check.sh", units.Script, "0755"))
	b.WriteString(heredoc("/etc/systemd/system/moorpost-idle.service", units.Service, "0644"))
	b.WriteString(heredoc("/etc/systemd/system/moorpost-idle.timer", units.Timer, "0644"))
	b.WriteString("systemctl daemon-reload\n")
	b.WriteString("systemctl enable --now moorpost-idle.timer\n")
	return b.String()
}

// heredoc emits a shell heredoc that writes content to path then chmods
// it to mode. The "EOF" sentinel is quoted so $-expansion is disabled,
// making the bash content opaque to the outer shell.
func heredoc(path, content, mode string) string {
	return fmt.Sprintf(`cat > %s <<'MOORPOST_EOF'
%sMOORPOST_EOF
chmod %s %s
`, path, content, mode, path)
}
