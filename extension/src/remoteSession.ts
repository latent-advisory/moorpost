// Owns the lifecycle of the integrated terminal attached to the remote
// Claude session. The user's mental model is "after handoff, my Claude is
// in this VSCode panel"; this module makes that feel real:
//
//   - On handoff success: spawn an integrated terminal running
//     `moorpost attach`. The user types into it as if Claude were local;
//     keystrokes flow to the remote tmux pane.
//
//   - On the terminal closing unexpectedly while still on the remote side
//     (e.g. SSH dropped because internet died): show a "disconnected"
//     warning with a Reconnect action.
//
//   - On return: close the terminal.
//
// We keep at most one attach terminal at a time per workspace; re-running
// the open call focuses the existing one.

import * as vscode from 'vscode';
import { cliBinary, getStatus } from './cli';

const TERMINAL_NAME = 'Moorpost: Live Claude (remote)';

interface TerminalState {
  terminal: vscode.Terminal;
  cwd: string | undefined;
  /** True when we (the extension) closed it programmatically — suppresses
   *  the "disconnected?" warning in that case. */
  closingDeliberately: boolean;
}

let active: TerminalState | undefined;

/**
 * Opens (or focuses) the attached Claude terminal. Runs `moorpost attach`
 * in a fresh integrated terminal; if one already exists, focuses it.
 *
 * Returns the terminal so callers can chain dispose etc.
 */
export function openOrFocusAttach(cwd?: string): vscode.Terminal {
  if (active && !active.terminal.exitStatus) {
    active.terminal.show(false);
    return active.terminal;
  }
  const terminal = vscode.window.createTerminal({
    name: TERMINAL_NAME,
    cwd,
    iconPath: new vscode.ThemeIcon('cloud'),
  });
  active = { terminal, cwd, closingDeliberately: false };
  terminal.sendText(`${cliBinary()} attach`, true);
  terminal.show(false);
  return terminal;
}

/** Closes the attach terminal if open, suppressing the disconnect warning. */
export function closeAttachQuietly(): void {
  if (!active) return;
  active.closingDeliberately = true;
  active.terminal.dispose();
  active = undefined;
}

/**
 * Wires up the disconnect-warning behavior. Call once at extension activation.
 */
export function registerRemoteSessionWatchers(
  context: vscode.ExtensionContext,
): void {
  context.subscriptions.push(
    vscode.window.onDidCloseTerminal(async (t) => {
      if (!active || t !== active.terminal) return;
      const wasDeliberate = active.closingDeliberately;
      const cwd = active.cwd;
      active = undefined;
      if (wasDeliberate) return;

      // Terminal closed without us asking. Could be: user typed `exit` or
      // detached cleanly, OR SSH dropped (internet died, VM stopped, etc.).
      // Check current state — if still on the remote side, warn.
      const status = await getStatus(cwd);
      if (!status || status.active_side !== 'remote') return;
      const choice = await vscode.window.showWarningMessage(
        'Disconnected from remote Claude session. Internet down or VM stopped?',
        'Reconnect',
        'Check status',
        'Dismiss',
      );
      if (choice === 'Reconnect') {
        openOrFocusAttach(cwd);
      } else if (choice === 'Check status') {
        await vscode.commands.executeCommand('moorpost.status');
      }
    }),
  );
}

/** True if an attach terminal is currently alive. */
export function hasActiveAttach(): boolean {
  return Boolean(active && !active.terminal.exitStatus);
}
