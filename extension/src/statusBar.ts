// Status bar item — single bar item that summarizes the project's current
// state and updates periodically via `moorpost status --json`.

import * as vscode from 'vscode';
import { getStatus, workspaceRoot } from './cli';

let item: vscode.StatusBarItem | undefined;
let refreshTimer: NodeJS.Timeout | undefined;

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
  // Configured and provisioned — show normal active-side summary.
  item.backgroundColor = undefined;
  const side = status.active_side ?? 'local';
  const icon = side === 'remote' ? '$(cloud)' : '$(home)';
  const vmState = status.vm_state ? ` · ${status.vm_state}` : '';
  // Show MTD cost whenever the field is present in the JSON, even when
  // it's $0.00 — the user gets visible confirmation that cost tracking
  // is wired up rather than an ambiguous absence.
  const cost =
    typeof status.month_to_date_usd === 'number'
      ? ` · $${status.month_to_date_usd.toFixed(2)}`
      : '';
  item.text = `${icon} Moorpost · ${side}${vmState}${cost}`;
  item.tooltip = renderTooltip(status);
}

function renderTooltip(s: ReturnType<typeof renderTooltipShape>): string {
  const lines = [
    `Project:       ${s.project}`,
    `Provider:      ${s.provider}`,
    `Agent:         ${s.agent}`,
    `Sync engine:   ${s.sync}`,
    `Mode:          ${s.mode}`,
  ];
  if (s.active_side) lines.push(`Active side:   ${s.active_side}`);
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
