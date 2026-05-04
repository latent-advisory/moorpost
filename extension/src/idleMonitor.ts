// Smart handoff prompt — non-modal "Hand off to remote?" notification when
// the user steps away from the laptop.
//
// VSCode exposes no direct lid-close or OS-sleep event, so we combine three
// signals to approximate "stepped away":
//   1. Window focus loss        (onDidChangeWindowState)
//   2. Editor inactivity         (text-document + selection events reset
//                                 lastActivityAt)
//   3. Wall-clock drift          (heartbeat setInterval; if Date.now()-lastTick
//                                 exceeds expected by >60s, the OS slept)
//
// Trigger: any of (focus-lost > N min) | (idle > M min) | (wall-clock gap)
// AND active_side === 'local' AND vm_id is set. Cooldown 15min between prompts.
//
// Settings:
//   moorpost.promptOnFocusLossMinutes  (default 30, 0 disables)
//   moorpost.promptOnIdleMinutes        (default 15, 0 disables)
// If both are 0, no listeners or timers are installed.

import * as vscode from 'vscode';
import { getStatus, workspaceRoot } from './cli';

const HEARTBEAT_MS = 30 * 1000;
const SLEEP_DRIFT_MS = 60 * 1000; // a 60s+ gap implies OS sleep, not GC pause
const COOLDOWN_MS = 15 * 60 * 1000;

interface Settings {
  focusLossMs: number; // 0 = disabled
  idleMs: number; // 0 = disabled
}

function readSettings(): Settings {
  const cfg = vscode.workspace.getConfiguration('moorpost');
  const focusMin = cfg.get<number>('promptOnFocusLossMinutes') ?? 30;
  const idleMin = cfg.get<number>('promptOnIdleMinutes') ?? 15;
  return {
    focusLossMs: Math.max(0, focusMin) * 60 * 1000,
    idleMs: Math.max(0, idleMin) * 60 * 1000,
  };
}

export interface TriggerInputs {
  now: number;
  lastTick: number;
  lastActivityAt: number;
  focusLostAt: number | null;
  cooldownUntil: number;
  settings: Settings;
}

/**
 * Pure trigger decision: should the heartbeat fire a "you've been away" prompt
 * now? Extracted from IdleMonitor.tick so it's testable without VSCode and so
 * the rules (cooldown, sleep-drift, focus-loss, idle) live in one inspectable
 * place.
 */
export function shouldPrompt(in_: TriggerInputs): boolean {
  if (in_.now < in_.cooldownUntil) return false;
  const drift = in_.now - in_.lastTick - HEARTBEAT_MS;
  if (drift > SLEEP_DRIFT_MS) return true;
  const focusLostFor = in_.focusLostAt !== null ? in_.now - in_.focusLostAt : 0;
  if (in_.settings.focusLossMs > 0 && focusLostFor >= in_.settings.focusLossMs) {
    return true;
  }
  const idleFor = in_.now - in_.lastActivityAt;
  if (in_.settings.idleMs > 0 && idleFor >= in_.settings.idleMs) return true;
  return false;
}

export class IdleMonitor {
  private lastActivityAt = Date.now();
  private focusLostAt: number | null = null;
  private lastTick = Date.now();
  private cooldownUntil = 0;
  private heartbeat: NodeJS.Timeout | undefined;
  private subscriptions: vscode.Disposable[] = [];

  start(context: vscode.ExtensionContext): void {
    this.applyCurrentSettings();

    // React to settings changes: rebuild listeners when thresholds toggle
    // to/from 0. The settings-change listener itself lives for the
    // extension's lifetime; only the inner heartbeat + activity listeners
    // are torn down/recreated.
    context.subscriptions.push(
      vscode.workspace.onDidChangeConfiguration((e) => {
        if (
          e.affectsConfiguration('moorpost.promptOnFocusLossMinutes') ||
          e.affectsConfiguration('moorpost.promptOnIdleMinutes')
        ) {
          this.tearDownInner();
          this.applyCurrentSettings();
        }
      }),
      new vscode.Disposable(() => this.tearDownInner()),
    );
  }

  private applyCurrentSettings(): void {
    const settings = readSettings();
    if (settings.focusLossMs === 0 && settings.idleMs === 0) return;
    this.installListeners();
    this.heartbeat = setInterval(() => this.tick(), HEARTBEAT_MS);
    // Reset baselines so the first tick after re-enable doesn't immediately
    // fire on stale state.
    this.lastActivityAt = Date.now();
    this.lastTick = Date.now();
    this.focusLostAt = null;
  }

  private installListeners(): void {
    this.subscriptions.push(
      vscode.window.onDidChangeWindowState((s) => {
        if (s.focused) {
          this.focusLostAt = null;
          this.lastActivityAt = Date.now();
        } else if (this.focusLostAt === null) {
          this.focusLostAt = Date.now();
        }
      }),
      vscode.workspace.onDidChangeTextDocument(() => {
        this.lastActivityAt = Date.now();
      }),
      vscode.window.onDidChangeTextEditorSelection((e) => {
        // Filter to keyboard/mouse to avoid programmatic-edit false positives
        // (formatters, LSP rename, etc.).
        const k = e.kind;
        if (
          k === vscode.TextEditorSelectionChangeKind.Keyboard ||
          k === vscode.TextEditorSelectionChangeKind.Mouse
        ) {
          this.lastActivityAt = Date.now();
        }
      }),
    );
  }

  private tick(): void {
    const now = Date.now();
    const fire = shouldPrompt({
      now,
      lastTick: this.lastTick,
      lastActivityAt: this.lastActivityAt,
      focusLostAt: this.focusLostAt,
      cooldownUntil: this.cooldownUntil,
      settings: readSettings(),
    });
    this.lastTick = now;
    if (fire) void this.maybePrompt();
  }

  private async maybePrompt(): Promise<void> {
    const cwd = workspaceRoot();
    if (!cwd) return;
    const status = await getStatus(cwd);
    if (!status) return;
    if ((status.active_side ?? 'local') !== 'local') return;
    if (!status.vm_id) return;

    // Set cooldown BEFORE awaiting the message — even if the user dismisses,
    // we don't want to re-prompt for COOLDOWN_MS.
    this.cooldownUntil = Date.now() + COOLDOWN_MS;
    // Reset activity window so we don't immediately re-fire when cooldown ends.
    this.lastActivityAt = Date.now();
    this.focusLostAt = null;

    const choice = await vscode.window.showInformationMessage(
      'You’ve been away — hand off to the remote VM?',
      'Hand off',
      'Not now',
    );
    if (choice === 'Hand off') {
      await vscode.commands.executeCommand('moorpost.handoff');
    }
  }

  private tearDownInner(): void {
    if (this.heartbeat) {
      clearInterval(this.heartbeat);
      this.heartbeat = undefined;
    }
    for (const d of this.subscriptions) d.dispose();
    this.subscriptions = [];
  }
}
