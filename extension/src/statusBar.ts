// Status bar item — single bar item that summarizes the project's current
// state and updates periodically via `moorpost status --json`.

import * as vscode from 'vscode';
import { getStatus, workspaceRoot } from './cli';

let item: vscode.StatusBarItem | undefined;
let refreshTimer: NodeJS.Timeout | undefined;

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
    vscode.workspace.getConfiguration('moorpost').get<number>('statusBarRefreshSeconds') ?? 30;
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
  // Clear any prior warning style.
  item.backgroundColor = undefined;
  const side = status.active_side ?? 'local';
  const icon = side === 'remote' ? '$(cloud)' : '$(home)';
  const vmState = status.vm_state ? ` · ${status.vm_state}` : '';
  const cost = status.month_to_date_usd ? ` · $${status.month_to_date_usd.toFixed(2)}` : '';
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
