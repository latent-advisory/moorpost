// Tracks (Claude Code tab → session id) so handoff can migrate the
// FOCUSED conversation rather than the most-recently-modified JSONL.
//
// The plugin doesn't expose its per-panel sessionId via context keys,
// tab labels, or storage. The only externally observable signal is the
// `claude --resume <sid>` spawn that our wrapper sees on each panel
// creation. The wrapper appends a structured record to
// ~/.moorpost/log/spawns.jsonl per spawn; here we correlate each new
// Claude Code tab event with the most recent spawn record to learn
// the tab's session id.
//
// Tab→SID mapping persists in memory only. Tabs created before this
// extension activated are bootstrapped heuristically (most-recent
// spawns ↔ tab order); imperfect but better than the most-recently-
// modified-JSONL fallback the CLI uses today.
//
// Active-tab tracking goes through onDidChangeTabs.changed (which fires
// when a tab's `isActive` flips) — that's how we know which Claude tab
// the user is currently focused on, vs. which one happened to be
// spawned last.

import * as vscode from 'vscode';
import * as fs from 'fs';
import * as path from 'path';
import * as os from 'os';
import { logToChannel } from './output';

const SPAWNS_LOG = path.join(os.homedir(), '.moorpost', 'log', 'spawns.jsonl');
const CLAUDE_VIEW_TYPE = 'claudeVSCodePanel';
const SPAWN_CORRELATION_DELAY_MS = 1500;
const SPAWN_RECENT_WINDOW_MS = 8000;

interface SpawnRecord {
  ts: string;
  sid: string;
  pid: number;
  ppid: number;
  cwd: string;
}

export class SessionTracker {
  private tabSidMap = new WeakMap<vscode.Tab, string>();
  private lastFocusedClaudeTab: vscode.Tab | undefined;
  private disposables: vscode.Disposable[] = [];

  constructor() {
    this.bootstrapExistingTabs();
    this.updateLastFocusedFromActive();

    this.disposables.push(
      vscode.window.tabGroups.onDidChangeTabs((event) => {
        for (const tab of event.opened) {
          if (this.isClaudeTab(tab)) void this.associateNewTab(tab);
        }
        for (const tab of event.changed) {
          if (this.isClaudeTab(tab) && tab.isActive) {
            this.lastFocusedClaudeTab = tab;
            const sid = this.tabSidMap.get(tab);
            logToChannel(
              `SessionTracker: focus → Claude tab (sid=${sid ?? 'unknown'})`,
            );
          }
        }
      }),
      vscode.window.tabGroups.onDidChangeTabGroups(() => {
        this.updateLastFocusedFromActive();
      }),
    );
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
  }

  /**
   * Find all open Claude Code tabs whose tracked SID matches `sid`.
   * Used by handoff/return to close ONLY the panel(s) for the
   * session being moved — leaves other Claude Code tabs (different
   * SIDs) alone.
   *
   * Returns an empty array if no tab is associated with the SID, or
   * if the tab→SID map hasn't captured this tab yet (e.g., panel
   * opened before extension activation and bootstrap-mapping was
   * unable to correlate via the spawns log).
   */
  getTabsForSid(sid: string): vscode.Tab[] {
    const matches: vscode.Tab[] = [];
    for (const group of vscode.window.tabGroups.all) {
      for (const tab of group.tabs) {
        if (!this.isClaudeTab(tab)) continue;
        if (this.tabSidMap.get(tab) === sid) matches.push(tab);
      }
    }
    return matches;
  }

  /**
   * Best-guess the Claude Code tab the user was working in just
   * before invoking handoff/return. Falls back through:
   *   1. The "lastFocusedClaudeTab" we recorded via tab change events.
   *   2. The currently active tab if it's a Claude Code tab.
   *   3. The most recently used Claude Code tab in the open list.
   *
   * Used as the SID-specific close fallback when the tab→SID map
   * doesn't have a binding for the session being moved (common for
   * tabs that pre-existed extension activation). Reasonable
   * assumption: if the user is interactively triggering handoff/
   * return, they're most likely doing it from the Claude tab they
   * want to migrate.
   */
  getLastFocusedClaudeTab(): vscode.Tab | undefined {
    if (this.lastFocusedClaudeTab && this.isClaudeTab(this.lastFocusedClaudeTab)) {
      // Verify it's still in the open tab list (close + reopen would
      // invalidate the reference but `isClaudeTab` doesn't catch that).
      for (const group of vscode.window.tabGroups.all) {
        if (group.tabs.includes(this.lastFocusedClaudeTab)) {
          return this.lastFocusedClaudeTab;
        }
      }
    }
    const active = vscode.window.tabGroups.activeTabGroup.activeTab;
    if (active && this.isClaudeTab(active)) return active;
    for (const group of vscode.window.tabGroups.all) {
      for (const tab of group.tabs) {
        if (this.isClaudeTab(tab)) return tab;
      }
    }
    return undefined;
  }

  getFocusedClaudeSid(): string | undefined {
    const focused =
      this.lastFocusedClaudeTab ??
      vscode.window.tabGroups.activeTabGroup.activeTab;
    if (focused && this.isClaudeTab(focused)) {
      const sid = this.tabSidMap.get(focused);
      if (sid) return sid;
    }
    // Fallback: any open Claude tab with a known SID.
    for (const group of vscode.window.tabGroups.all) {
      for (const tab of group.tabs) {
        if (this.isClaudeTab(tab)) {
          const sid = this.tabSidMap.get(tab);
          if (sid) return sid;
        }
      }
    }
    return undefined;
  }

  // For diagnostic command: dump current tab→SID map.
  describe(): string {
    const lines: string[] = [];
    let i = 0;
    for (const group of vscode.window.tabGroups.all) {
      for (const tab of group.tabs) {
        if (this.isClaudeTab(tab)) {
          const sid = this.tabSidMap.get(tab) ?? '(unmapped)';
          const active = tab.isActive ? ' [active]' : '';
          lines.push(`  tab[${i++}] sid=${sid}${active} label=${tab.label}`);
        }
      }
    }
    const focused = this.getFocusedClaudeSid() ?? '(none)';
    return `SessionTracker state:\n  focused sid=${focused}\n${lines.join('\n')}`;
  }

  private isClaudeTab(tab: vscode.Tab): boolean {
    const input = tab.input as { viewType?: string } | undefined;
    return input?.viewType === CLAUDE_VIEW_TYPE;
  }

  private updateLastFocusedFromActive(): void {
    const active = vscode.window.tabGroups.activeTabGroup.activeTab;
    if (active && this.isClaudeTab(active)) {
      this.lastFocusedClaudeTab = active;
    }
  }

  private async associateNewTab(tab: vscode.Tab): Promise<void> {
    // The wrapper writes its spawn record near the start of its
    // execution — within a few hundred ms of the plugin spawning it.
    // 1.5s gives a generous margin without making the UI feel laggy.
    await new Promise((r) => setTimeout(r, SPAWN_CORRELATION_DELAY_MS));
    const recent = this.readRecentSpawns(SPAWN_RECENT_WINDOW_MS);
    if (recent.length === 0) {
      logToChannel(
        `SessionTracker: no recent spawns to associate with new Claude tab`,
      );
      return;
    }
    const sid = recent[recent.length - 1].sid;
    if (!sid) {
      logToChannel(`SessionTracker: latest spawn has empty sid, skipping`);
      return;
    }
    this.tabSidMap.set(tab, sid);
    logToChannel(`SessionTracker: new Claude tab → sid=${sid}`);
  }

  private bootstrapExistingTabs(): void {
    const claudeTabs: vscode.Tab[] = [];
    for (const group of vscode.window.tabGroups.all) {
      for (const tab of group.tabs) {
        if (this.isClaudeTab(tab)) claudeTabs.push(tab);
      }
    }
    if (claudeTabs.length === 0) return;

    const allSpawns = this.readAllSpawns();
    // Most-recent unique SIDs first. We use unique SIDs because the
    // wrapper can be invoked more than once per panel (e.g., on reload)
    // — only distinct sessions get a panel.
    const uniqueSids: string[] = [];
    const seen = new Set<string>();
    for (let i = allSpawns.length - 1; i >= 0; i--) {
      const sid = allSpawns[i].sid;
      if (sid && !seen.has(sid)) {
        seen.add(sid);
        uniqueSids.push(sid);
      }
    }
    const n = Math.min(claudeTabs.length, uniqueSids.length);
    for (let i = 0; i < n; i++) {
      this.tabSidMap.set(claudeTabs[i], uniqueSids[i]);
    }
    logToChannel(
      `SessionTracker: bootstrap associated ${n}/${claudeTabs.length} existing Claude tabs`,
    );
  }

  private readRecentSpawns(maxAgeMs: number): SpawnRecord[] {
    const all = this.readAllSpawns();
    const cutoff = Date.now() - maxAgeMs;
    return all.filter((s) => Date.parse(s.ts) >= cutoff);
  }

  private readAllSpawns(): SpawnRecord[] {
    try {
      if (!fs.existsSync(SPAWNS_LOG)) return [];
      const data = fs.readFileSync(SPAWNS_LOG, 'utf8');
      const records: SpawnRecord[] = [];
      for (const line of data.split('\n')) {
        const t = line.trim();
        if (!t) continue;
        try {
          records.push(JSON.parse(t));
        } catch {
          // skip malformed lines
        }
      }
      return records;
    } catch (e) {
      logToChannel(`SessionTracker: failed to read spawns log: ${String(e)}`);
      return [];
    }
  }
}
