// Owns the single "Moorpost: Claude" terminal that swaps between running
// local `claude` and `moorpost attach` (remote) based on the project's
// active side. The user's mental model: one window in VSCode hosts their
// Claude session — the underlying process happens to be local or remote.
//
// We can't swap a terminal's backing process in place (VSCode API doesn't
// expose that), so each handoff/return disposes the old terminal and
// creates a new one. The terminal NAME (`Moorpost: Claude (local)` vs
// `Moorpost: Claude (remote)`) makes the swap visible.
//
// Disconnect detection: if the active terminal closes without us asking
// (SSH drop, VM stop, etc.) and we're still on the remote side, surface
// a "reconnect?" warning. Same logic for an unexpected local-claude exit
// (e.g., user typed `exit` while side=local) — we just silently let it
// close, since `claude` exiting locally is a normal action.

import * as vscode from 'vscode';
import { cliBinary, getStatus } from './cli';

interface TerminalState {
  terminal: vscode.Terminal;
  side: 'local' | 'remote';
  cwd: string | undefined;
  /** True when we (the extension) closed it programmatically — suppresses
   *  the "disconnected?" warning in that case. */
  closingDeliberately: boolean;
}

let active: TerminalState | undefined;

function dispose(): void {
  if (active && !active.terminal.exitStatus) {
    active.closingDeliberately = true;
    active.terminal.dispose();
  }
  active = undefined;
}

/**
 * Opens (or focuses) the Moorpost Claude terminal in remote mode —
 * runs `moorpost attach` so the integrated terminal becomes the live
 * remote Claude pane. If a remote terminal is already alive, focuses
 * it. If a local terminal is alive, disposes it first and creates the
 * remote one (active side has changed).
 */
export function openRemoteClaude(cwd?: string): vscode.Terminal {
  if (active && active.side === 'remote' && !active.terminal.exitStatus) {
    active.terminal.show(false);
    return active.terminal;
  }
  dispose();
  const terminal = vscode.window.createTerminal({
    name: 'Moorpost: Claude (remote)',
    cwd,
    iconPath: new vscode.ThemeIcon('cloud'),
  });
  active = { terminal, side: 'remote', cwd, closingDeliberately: false };
  terminal.sendText(`${cliBinary()} attach`, true);
  terminal.show(false);
  return terminal;
}

/**
 * Opens (or focuses) the Moorpost Claude terminal in local mode —
 * runs `claude --resume <sessionId>` (or `claude` if no session is
 * known) so the user picks up the session that was just brought back.
 */
export function openLocalClaude(cwd: string, sessionId?: string): vscode.Terminal {
  if (active && active.side === 'local' && !active.terminal.exitStatus) {
    active.terminal.show(false);
    return active.terminal;
  }
  dispose();
  const terminal = vscode.window.createTerminal({
    name: 'Moorpost: Claude (local)',
    cwd,
    iconPath: new vscode.ThemeIcon('home'),
  });
  active = { terminal, side: 'local', cwd, closingDeliberately: false };
  const cmd = sessionId ? `claude --resume ${sessionId}` : 'claude';
  terminal.sendText(cmd, true);
  terminal.show(false);
  return terminal;
}

/** Closes the active Moorpost Claude terminal (any side), suppressing
 *  the disconnect warning. Used by handoff/return to switch sides
 *  cleanly without surfacing a false "disconnected" notification. */
export function closeClaudeTerminalQuietly(): void {
  dispose();
}

/**
 * Wires up the close-terminal watcher. Call once at extension activation.
 */
export function registerClaudeTerminalWatchers(
  context: vscode.ExtensionContext,
): void {
  context.subscriptions.push(
    vscode.window.onDidCloseTerminal(async (t) => {
      if (!active || t !== active.terminal) return;
      const wasDeliberate = active.closingDeliberately;
      const cwd = active.cwd;
      const side = active.side;
      active = undefined;
      if (wasDeliberate) return;

      // Local-claude exit on user `exit` or Ctrl+D is normal — don't
      // nag. Only the remote side gets the disconnect warning, since
      // an unexpected SSH drop is opaque to the user otherwise.
      if (side === 'local') return;

      const status = await getStatus(cwd);
      if (!status || status.active_side !== 'remote') return;
      const choice = await vscode.window.showWarningMessage(
        'Disconnected from remote Claude session. Internet down or VM stopped?',
        'Reconnect',
        'Check status',
        'Dismiss',
      );
      if (choice === 'Reconnect') {
        openRemoteClaude(cwd);
      } else if (choice === 'Check status') {
        await vscode.commands.executeCommand('moorpost.status');
      }
    }),
  );
}

/** True if a Moorpost Claude terminal is alive on either side. */
export function hasActiveClaude(): boolean {
  return Boolean(active && !active.terminal.exitStatus);
}

/** Active side of the Moorpost Claude terminal, or undefined if none. */
export function activeClaudeSide(): 'local' | 'remote' | undefined {
  if (active && !active.terminal.exitStatus) return active.side;
  return undefined;
}

// Backward-compat aliases for the old remoteSession.ts API.
export const openOrFocusAttach = openRemoteClaude;
export const closeAttachQuietly = closeClaudeTerminalQuietly;
export const registerRemoteSessionWatchers = registerClaudeTerminalWatchers;
export const hasActiveAttach = hasActiveClaude;
