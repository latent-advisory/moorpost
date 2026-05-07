// Status bar item — the project-state bar (vm_state / cost
// / remote-session count). Updates periodically via `moorpost status --json`.

import * as vscode from 'vscode';
import * as cp from 'child_process';
import { cliBinary, getStatus, StatusReport, workspaceRoot } from './cli';
import { isBootstrapping, onRunStateChanged } from './runState';
import { logToChannel } from './output';

let item: vscode.StatusBarItem | undefined;
let refreshTimer: NodeJS.Timeout | undefined;
// Throttle the auto-stop watcher so a transient running-but-no-sessions
// observation doesn't fire `moorpost down` repeatedly while the VM is
// actually mid-stop. 90s gives a generous window for the previous stop
// request to complete (gcloud --async returns ~1-2s but VM transitions
// can take 60s+).
let lastAutoStopAttemptMs = 0;

/**
 * Force an immediate status-bar refresh. Use after commands that change
 * state (auth, handoff, return, provision) so the user sees the new
 * state right away instead of waiting for the next 30s tick.
 */
export function refreshStatusBarNow(): void {
  void refresh();
}

export function setupStatusBar(context: vscode.ExtensionContext): void {
  item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  // Click does the right thing for the current state (bootstrap / handoff /
  // return). Use the palette command "Moorpost: Show status" if you want
  // status details instead.
  item.command = 'moorpost.toggleSide';
  item.text = '$(moorpost-loading) Moorpost…';
  item.tooltip = 'Click to switch local ↔ remote';
  item.show();
  context.subscriptions.push(item);

  scheduleRefresh(context);
  // Refresh immediately whenever a long-running command (currently just
  // bootstrap) starts or ends, so users see the "setting up…" state flip
  // in / out without waiting for the next polling tick.
  onRunStateChanged(() => void refresh());
  void refresh();
}

function scheduleRefresh(context: vscode.ExtensionContext): void {
  const seconds =
    vscode.workspace.getConfiguration('moorpost').get<number>('statusBarRefreshSeconds') ?? 10;
  refreshTimer = setInterval(() => void refresh(), Math.max(5, seconds) * 1000);
  context.subscriptions.push(new vscode.Disposable(() => {
    if (refreshTimer) clearInterval(refreshTimer);
  }));
}

async function refresh(): Promise<void> {
  if (!item) return;
  // Bootstrap-in-progress takes precedence over any other state. While
  // bootstrap is walking the project through "no config → no auth → no VM
  // → ready", clicking the bar must NOT route to whatever toggleSide would
  // pick from the partial status; toggleSide handles this same case by
  // focusing the terminal so the user sees what's happening.
  if (isBootstrapping()) {
    item.text = '$(sync~spin) Moorpost: setting up…';
    item.tooltip = 'Bootstrap is running in the terminal. Click to focus it.';
    item.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
    return;
  }
  const cwd = workspaceRoot();
  const status = await getStatus(cwd);
  if (!status) {
    // Empty-state: make it visually distinct so the user knows setup is
    // needed, and align the tooltip with what clicking actually does
    // (toggleSide runs Bootstrap when no config is found).
    item.text = '$(warning) Moorpost: not set up · click to start';
    item.tooltip = 'No Moorpost config found in this workspace. Click to run Bootstrap (one-shot setup).';
    item.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
    return;
  }
  // Auth not cached — surface the "sign in" gap before "no VM" since
  // provisioning a VM you can't hand off to is a dead-end.
  if (status.auth_cached === false) {
    item.text = '$(key) Moorpost · sign in needed · click';
    item.tooltip = `Project: ${status.project}\nClick to run \`moorpost auth\` (Claude OAuth flow).`;
    item.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
    return;
  }
  // Configured + auth'd but unprovisioned — "in-between" state.
  if (!status.vm_id) {
    item.text = '$(server-environment) Moorpost · no VM · click to provision';
    item.tooltip = `Project: ${status.project}\nClick to provision the GCP VM (one-time, ~30s).`;
    item.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
    return;
  }
  // Configured and provisioned — show summary that reflects per-session
  // routing. The per-session truth lives in remote_sids.length.
  item.backgroundColor = undefined;
  const remoteCount = status.remote_sids?.length ?? 0;
  let sideLabel: string;
  if (remoteCount > 0) {
    sideLabel = remoteCount === 1 ? '1 on remote' : `${remoteCount} on remote`;
  } else {
    sideLabel = 'local';
  }
  const icon = '$(cloud)';
  const vmState = status.vm_state ? ` · ${status.vm_state}` : '';
  // Show MTD cost whenever the field is present in the JSON, even when
  // it's $0.00 — the user gets visible confirmation that cost tracking
  // is wired up rather than an ambiguous absence.
  const cost =
    typeof status.month_to_date_usd === 'number'
      ? ` · $${status.month_to_date_usd.toFixed(2)}`
      : '';
  item.text = `${icon} Moorpost · ${sideLabel}${vmState}${cost}`;
  item.tooltip = renderTooltip(status);

  // Auto-stop watcher: when no sessions are routed to remote AND the
  // VM is still running, fire `moorpost down`. Catches the rare case
  // where the previous stop was rejected (network blip, GCE
  // fingerprint race) or the user reached this state via a path
  // that bypassed the return CLI's auto-stop hook (e.g., direct
  // state.json edit, crash recovery).
  //
  // Throttled to once per 90s so we don't fire repeated stop requests
  // while the previous async stop is still mid-transition.
  maybeAutoStop(status);
}

function maybeAutoStop(status: StatusReport): void {
  const remoteCount = status.remote_sids?.length ?? 0;
  const isRunning = status.vm_state === 'running';
  if (remoteCount > 0 || !isRunning) {
    return;
  }
  const now = Date.now();
  if (now - lastAutoStopAttemptMs < 90 * 1000) {
    return;
  }
  // Respect a user opt-out for power users who want to keep the VM
  // warm for quick handoffs.
  const enabled =
    vscode.workspace
      .getConfiguration('moorpost')
      .get<boolean>('autoStopWhenNoRemoteSessions', true);
  if (!enabled) return;

  lastAutoStopAttemptMs = now;
  const cwd = workspaceRoot();
  if (!cwd) return;
  logToChannel(
    `auto-stop: remote_sids=[] but vm_state=running — invoking \`moorpost down\``,
  );
  // Fire-and-forget. `moorpost down` calls Provider.Stop with --async,
  // so it returns in 1-2s. We don't await — the next refresh tick will
  // pick up the new vm_state.
  cp.execFile(
    cliBinary(),
    ['down'],
    { cwd, timeout: 30_000 },
    (err, stdout, stderr) => {
      if (err) {
        logToChannel(`auto-stop: \`moorpost down\` failed: ${String(err)} (stderr: ${stderr.trim()})`);
      } else {
        logToChannel(`auto-stop: \`moorpost down\` ok — ${stdout.trim()}`);
      }
    },
  );
}

function renderTooltip(s: ReturnType<typeof renderTooltipShape>): string {
  const lines = [
    `Project:       ${s.project}`,
    `Provider:      ${s.provider}`,
    `Agent:         ${s.agent}`,
    `Sync engine:   ${s.sync}`,
    `Mode:          ${s.mode}`,
  ];
  const remoteCount = s.remote_sids?.length ?? 0;
  if (remoteCount > 0) {
    lines.push(`Remote sessions: ${remoteCount}`);
  }
  if (s.vm_id) lines.push(`VM:            ${s.vm_id}`);
  if (s.vm_state) lines.push(`VM state:      ${s.vm_state}`);
  if (s.month_to_date_usd) lines.push(`Month-to-date: $${s.month_to_date_usd.toFixed(2)}`);
  return lines.join('\n');
}

// Type witness so the TypeScript compiler infers ReturnType<...> correctly
// without exporting another symbol.
function renderTooltipShape(s: import('./cli').StatusReport) {
  return s;
}
