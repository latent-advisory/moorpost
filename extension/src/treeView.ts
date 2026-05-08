// Tree view — a persistent UI panel showing the project's Moorpost state.
//
// Top-level nodes:
//   • Project              webapp
//   • Provider             gcp
//   • Active side          local | remote
//   • VM                   webapp-vm (running) — only present once provisioned
//   • Sync engine          mutagen
//   • Cost (MTD)           $0.42 — only present when VM exists
//   • Remote sessions (N)  expandable, lists each SID currently routed to remote
//
// Click "refresh" (in the view title) to re-fetch via `moorpost status --json`.

import * as vscode from 'vscode';
import { getStatus, workspaceRoot } from './cli';
import type { StatusReport } from './cli';
import { listLocalSessions, type SessionInfo } from './sessionList';

/** Single row in the tree. Most are leaves; "Remote sessions" parent expands. */
export class MoorpostTreeItem extends vscode.TreeItem {
  constructor(
    public readonly label: string,
    public readonly value: string,
    public readonly icon?: vscode.ThemeIcon,
    command?: vscode.Command,
    contextValue?: string,
    collapsibleState: vscode.TreeItemCollapsibleState = vscode.TreeItemCollapsibleState.None,
    /** Children for expandable nodes. Empty for leaves. */
    public readonly children: MoorpostTreeItem[] = [],
  ) {
    super(value ? `${label}: ${value}` : label, collapsibleState);
    this.tooltip = value ? `${label}: ${value}` : label;
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

  async getChildren(parent?: MoorpostTreeItem): Promise<MoorpostTreeItem[]> {
    // Re-fetch remote sessions live when the node is expanded. VSCode
    // caches expanded nodes and may call getChildren(oldItem) after a
    // refresh — returning oldItem.children would show stale data.
    if (parent?.contextValue === 'moorpost.remoteSessionsRoot') {
      const cwd = workspaceRoot();
      const status = cwd ? await getStatus(cwd) : null;
      const remoteSids = status?.remote_sids ?? [];
      const sessions = cwd ? await listLocalSessions(cwd) : [];
      return buildRemoteSessionChildren(remoteSids, sessions);
    }
    if (parent) return parent.children;
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
    // Top-level project rows (cheap — derived from status).
    const items = buildItems(status);
    // Remote-sessions section (slightly more expensive — reads JSONLs to
    // compute first-message labels). Skip when there's nothing to show.
    const remoteSids = status.remote_sids ?? [];
    if (remoteSids.length > 0) {
      const sessions = await listLocalSessions(cwd);
      items.push(buildRemoteSessionsItem(remoteSids, sessions));
    }
    return items;
  }
}

/**
 * Build the expandable "Remote sessions" parent node. Children:
 *   - "Return all" action (only when N > 1).
 *   - One row per remote SID, with first-message label, click-to-return
 *     command, and a contextValue that lets per-row context menus show
 *     a Return action.
 *
 * Sessions whose JSONL is missing from the local enumeration (rare —
 * could happen if the JSONL hasn't been synced back yet) still appear
 * with the SID as the only label.
 */
function buildRemoteSessionChildren(
  remoteSids: string[],
  localSessions: SessionInfo[],
): MoorpostTreeItem[] {
  const byId = new Map(localSessions.map((s) => [s.sessionId, s] as const));
  const children: MoorpostTreeItem[] = [];
  if (remoteSids.length > 1) {
    children.push(
      new MoorpostTreeItem(
        'Return all',
        '',
        new vscode.ThemeIcon('arrow-down'),
        { command: 'moorpost.return', title: 'Return all sessions' },
        'moorpost.remoteSessions.all',
      ),
    );
  }
  for (const sid of remoteSids) {
    const info = byId.get(sid);
    const label = info?.firstUserText || '(no preview)';
    const value = sid.slice(0, 8);
    children.push(
      new MoorpostTreeItem(
        label,
        value,
        new vscode.ThemeIcon('cloud'),
        {
          command: 'moorpost.openRemoteSession',
          title: 'Open remote session',
          arguments: [sid],
        },
        'moorpost.remoteSession',
      ),
    );
  }
  return children;
}

function buildRemoteSessionsItem(
  remoteSids: string[],
  localSessions: SessionInfo[],
): MoorpostTreeItem {
  return new MoorpostTreeItem(
    'Remote sessions',
    String(remoteSids.length),
    new vscode.ThemeIcon('cloud'),
    undefined,
    'moorpost.remoteSessionsRoot',
    vscode.TreeItemCollapsibleState.Expanded,
    buildRemoteSessionChildren(remoteSids, localSessions),
  );
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
  {
    const remoteCount = s.remote_sids?.length ?? 0;
    let label: string;
    let effectiveSide: 'local' | 'remote';
    if (remoteCount > 0) {
      label = remoteCount === 1 ? '1 on remote' : `${remoteCount} on remote`;
      effectiveSide = 'remote';
    } else {
      label = 'local';
      effectiveSide = 'local';
    }
    const ctx = `moorpost.activeSide.${effectiveSide}`;
    const toggle: vscode.Command = {
      command: 'moorpost.toggleSide',
      title: 'Switch local ↔ remote',
    };
    items.push(
      new MoorpostTreeItem('Active side', label, new vscode.ThemeIcon('cloud'), toggle, ctx),
    );
  }
  if (s.vm_id) {
    const vmDetail = s.vm_state ? `${s.vm_id} (${s.vm_state})` : s.vm_id;
    // State-suffixed contextValue lets package.json's view/item/context
    // menu show the right inline action (Stop button when running, Start
    // button when stopped). 'unknown' renders no inline button.
    const stateSuffix =
      s.vm_state === 'running' ? '.running' :
      s.vm_state === 'stopped' || s.vm_state === 'terminated' ? '.stopped' :
      '';
    items.push(
      new MoorpostTreeItem('VM', vmDetail, new vscode.ThemeIcon('vm'), showStatus, `moorpost.vm${stateSuffix}`),
    );
  }
  if (typeof s.month_to_date_usd === 'number') {
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
