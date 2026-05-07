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
import * as childProcess from 'child_process';
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

  /**
   * Authoritative per-SID surface detection via `ps` parent inspection.
   *
   * For each live process matching `claude --resume <sid>` (local) OR
   * `ssh ... claude --resume <sid>` (remote-routed), look at its parent
   * process. A shell parent (bash/zsh/etc.) means the session is being
   * driven by a terminal — Moorpost: Claude terminal or external shell.
   * A non-shell parent (typically the VSCode plugin host node process)
   * means the session is in a Claude Code panel.
   *
   * Returns:
   *   'plugin'   — at least one match has a non-shell parent
   *   'terminal' — all matches have shell parents
   *   'unknown'  — ps failed, no live processes for this SID, or
   *                indeterminate parents (will fall back to heuristic)
   *
   * Plugin wins if both surfaces have processes for the same SID — the
   * caller (preflight popup) errs on the side of prompting.
   */
  /**
   * If `claude --resume <sid>` is currently running locally, return the
   * PID of its parent shell process (the VSCode terminal's shell). Used
   * to close the original terminal before opening a new remote one.
   * Returns undefined when the process can't be found via ps.
   */
  async getShellPidForSid(sid: string): Promise<number | undefined> {
    return new Promise((resolve) => {
      const child = childProcess.execFile(
        'ps',
        ['-eo', 'pid=,ppid=,args='],
        { maxBuffer: 4 * 1024 * 1024 },
        (err, stdout) => {
          if (err) { resolve(undefined); return; }
          const rows = new Map<number, { ppid: number; args: string }>();
          for (const raw of stdout.split('\n')) {
            const line = raw.trimStart();
            if (!line) continue;
            const m = line.match(/^(\d+)\s+(\d+)\s+(.+)$/);
            if (!m) continue;
            rows.set(parseInt(m[1], 10), { ppid: parseInt(m[2], 10), args: m[3] });
          }
          const sidRe = new RegExp(`--resume[ =]${escapeRe(sid)}\\b`);
          for (const [pid, row] of rows) {
            if (!sidRe.test(row.args)) continue;
            if (!/\b(claude|ssh)\b/.test(row.args)) continue;
            const parent = rows.get(row.ppid);
            if (parent && looksLikeShell(parent.args)) {
              resolve(row.ppid);
              return;
            }
          }
          resolve(undefined);
        },
      );
      setTimeout(() => { try { child.kill(); } catch { /* ignore */ } }, 5000);
    });
  }

  async getSessionSurfaceForSid(
    sid: string,
  ): Promise<'plugin' | 'terminal' | 'unknown'> {
    return new Promise((resolve) => {
      const child = childProcess.execFile(
        'ps',
        ['-eo', 'pid=,ppid=,args='],
        { maxBuffer: 4 * 1024 * 1024 },
        (err, stdout) => {
          if (err) {
            logToChannel(`getSessionSurfaceForSid(${sid}): ps failed: ${String(err)}`);
            resolve('unknown');
            return;
          }
          const rows = new Map<number, { ppid: number; args: string }>();
          for (const raw of stdout.split('\n')) {
            const line = raw.trimStart();
            if (!line) continue;
            const m = line.match(/^(\d+)\s+(\d+)\s+(.+)$/);
            if (!m) continue;
            rows.set(parseInt(m[1], 10), {
              ppid: parseInt(m[2], 10),
              args: m[3],
            });
          }
          const sidRe = new RegExp(`--resume[ =]${escapeRe(sid)}\\b`);
          const matches: number[] = [];
          for (const [pid, row] of rows) {
            if (!sidRe.test(row.args)) continue;
            // Filter to actual claude/ssh processes (not unrelated commands
            // that happen to mention the SID in an arg).
            if (!/\b(claude|ssh)\b/.test(row.args)) continue;
            matches.push(pid);
          }
          if (matches.length === 0) {
            resolve('unknown');
            return;
          }
          let sawPlugin = false;
          let sawTerminal = false;
          for (const pid of matches) {
            const row = rows.get(pid);
            if (!row) continue;
            // SSH process routing --resume to a remote VM = terminal.
            // SSH wins unconditionally — if there's also an orphaned plugin
            // process for the same SID (from a previous interrupted handoff),
            // the SSH process is the authoritative surface.
            if (/^ssh\b/.test(row.args.trimStart())) {
              sawTerminal = true;
              continue;
            }
            const parent = rows.get(row.ppid);
            if (!parent) continue;
            if (looksLikeShell(parent.args)) sawTerminal = true;
            else sawPlugin = true;
          }
          // Terminal (especially SSH) takes priority over plugin — an orphaned
          // plugin panel from a previous failed handoff should not override an
          // active SSH-routed terminal session.
          if (sawTerminal) resolve('terminal');
          else if (sawPlugin) resolve('plugin');
          else resolve('unknown');
        },
      );
      setTimeout(() => {
        try { child.kill(); } catch { /* ignore */ }
      }, 5000);
    });
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

const SHELL_BINS = new Set([
  'bash', '-bash', 'zsh', '-zsh', 'sh', 'fish', '-fish', 'dash', 'ksh',
  'login',
]);

function looksLikeShell(args: string): boolean {
  const first = args.trimStart().split(/\s+/)[0];
  if (!first) return false;
  const base = first.split('/').pop() ?? '';
  return SHELL_BINS.has(base);
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
