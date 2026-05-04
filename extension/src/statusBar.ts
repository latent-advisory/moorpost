// Status bar item — single bar item that summarizes the project's current
// state and updates periodically via `moorpost status --json`.

import * as vscode from 'vscode';
import { getStatus, workspaceRoot } from './cli';

let item: vscode.StatusBarItem | undefined;
let refreshTimer: NodeJS.Timeout | undefined;

export function setupStatusBar(context: vscode.ExtensionContext): void {
  item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  item.command = 'moorpost.status';
  item.text = '$(moorpost-loading) Moorpost…';
  item.tooltip = 'Click for full status';
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
    item.text = 'Moorpost: not configured';
    item.tooltip = 'No .moorpost/config.yaml in this workspace. Run `moorpost init`.';
    return;
  }
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
