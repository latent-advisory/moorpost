// Tree view — a persistent UI panel showing the project's Moorpost state.
//
// Top-level nodes:
//   • Project        argus
//   • Provider       gcp
//   • Active side    local | remote
//   • VM             argus-vm (running) — only present once provisioned
//   • Sync engine    mutagen
//   • Cost (MTD)     $0.42 — only present when VM exists
//
// Click "refresh" (in the view title) to re-fetch via `moorpost status --json`.

import * as vscode from 'vscode';
import { getStatus, workspaceRoot } from './cli';
import type { StatusReport } from './cli';

/** Single row in the tree. Always a leaf in v0.2 (no nested groups yet). */
export class MoorpostTreeItem extends vscode.TreeItem {
  constructor(
    public readonly label: string,
    public readonly value: string,
    public readonly icon?: vscode.ThemeIcon,
    command?: vscode.Command,
    contextValue?: string,
  ) {
    super(`${label}: ${value}`, vscode.TreeItemCollapsibleState.None);
    this.tooltip = `${label}: ${value}`;
    if (icon) this.iconPath = icon;
    if (command) this.command = command;
    if (contextValue) this.contextValue = contextValue;
  }
}

export class MoorpostTreeProvider
  implements vscode.TreeDataProvider<MoorpostTreeItem>
{
  private readonly _onDidChange = new vscode.EventEmitter<MoorpostTreeItem | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChange.event;

  /** Fire to re-fetch and re-render. */
  refresh(): void {
    this._onDidChange.fire();
  }

  getTreeItem(item: MoorpostTreeItem): vscode.TreeItem {
    return item;
  }

  async getChildren(): Promise<MoorpostTreeItem[]> {
    const cwd = workspaceRoot();
    if (!cwd) {
      return [
        new MoorpostTreeItem('Status', 'no workspace open', new vscode.ThemeIcon('warning')),
      ];
    }
    const status = await getStatus(cwd);
    if (!status) {
      return [
        new MoorpostTreeItem(
          'Status',
          'not configured (run `moorpost init`)',
          new vscode.ThemeIcon('warning'),
        ),
      ];
    }
    return buildItems(status);
  }
}

/**
 * Pure function so it's trivially unit-testable later. Returns the rows the
 * tree should display for a given status report. Each row has a default
 * left-click action — most lead to "edit config" (opens .moorpost/config.yaml)
 * since the rows are derived from config; rows with a more direct action
 * (Active side → toggle, VM → status, Cost → cost details, Conflicts →
 * conflicts list) override that default.
 */
export function buildItems(s: StatusReport): MoorpostTreeItem[] {
  const editConfig: vscode.Command = {
    command: 'moorpost.editConfig',
    title: 'Edit project config',
  };
  const showStatus: vscode.Command = {
    command: 'moorpost.status',
    title: 'Show status',
  };
  const items: MoorpostTreeItem[] = [
    new MoorpostTreeItem('Project', s.project, new vscode.ThemeIcon('folder'), editConfig),
    new MoorpostTreeItem('Provider', s.provider, new vscode.ThemeIcon('cloud'), editConfig),
    new MoorpostTreeItem('Agent', s.agent, new vscode.ThemeIcon('robot'), editConfig),
    new MoorpostTreeItem('Sync engine', s.sync, new vscode.ThemeIcon('sync'), editConfig),
    new MoorpostTreeItem('Mode', s.mode, new vscode.ThemeIcon('gear'), editConfig),
  ];
  if (s.active_side) {
    const icon = s.active_side === 'remote' ? 'cloud' : 'home';
    const ctx = `moorpost.activeSide.${s.active_side}`;
    const toggle: vscode.Command = {
      command: 'moorpost.toggleSide',
      title: 'Switch local ↔ remote',
    };
    items.push(
      new MoorpostTreeItem('Active side', s.active_side, new vscode.ThemeIcon(icon), toggle, ctx),
    );
  }
  if (s.vm_id) {
    const vmDetail = s.vm_state ? `${s.vm_id} (${s.vm_state})` : s.vm_id;
    items.push(
      new MoorpostTreeItem('VM', vmDetail, new vscode.ThemeIcon('vm'), showStatus, 'moorpost.vm'),
    );
  }
  if (typeof s.month_to_date_usd === 'number' && s.month_to_date_usd > 0) {
    items.push(
      new MoorpostTreeItem(
        'Cost (MTD)',
        `$${s.month_to_date_usd.toFixed(2)} (estimate)`,
        new vscode.ThemeIcon('credit-card'),
        { command: 'moorpost.showCost', title: 'Show cost details' },
        'moorpost.cost',
      ),
    );
  }
  if (s.has_sync_session) {
    const count = typeof s.conflicts === 'number' ? s.conflicts : 0;
    const value = count === 0 ? '0 (clean)' : `${count} (click to view)`;
    const icon = count === 0 ? 'check' : 'warning';
    items.push(
      new MoorpostTreeItem(
        'Conflicts',
        value,
        new vscode.ThemeIcon(icon),
        { command: 'moorpost.showConflicts', title: 'Show conflicts' },
        'moorpost.conflicts',
      ),
    );
  }
  return items;
}
