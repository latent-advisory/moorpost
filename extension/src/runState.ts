// Run-state tracker for long-running, terminal-hosted moorpost commands.
//
// The status bar's click action routes to whatever the *current* status
// implies — "no config" → bootstrap, "no auth" → sign in, "no VM" →
// provision, etc. Bootstrap walks the project through several of those
// states in sequence; without coordination, a status-bar click during
// bootstrap could fire a duplicate auth/provision/handoff on top of the
// running command.
//
// This module records the active bootstrap terminal so the status bar
// (and toggleSide) can detect "we're in the middle of setup, route the
// click somewhere safe" instead.

import * as vscode from 'vscode';

let activeBootstrapTerminal: vscode.Terminal | undefined;
let onChangeCallback: (() => void) | undefined;
let listenerRegistered = false;

/**
 * Register the bootstrap terminal as the in-progress one. The status bar
 * is refreshed immediately, and again when this terminal closes (success
 * or abort) so the bar transitions back to its normal state.
 */
export function setBootstrapTerminal(term: vscode.Terminal): void {
  activeBootstrapTerminal = term;
  ensureCloseListener();
  onChangeCallback?.();
}

/** Returns the active bootstrap terminal, or undefined if none. */
export function getBootstrapTerminal(): vscode.Terminal | undefined {
  return activeBootstrapTerminal;
}

/** True while a bootstrap is running in a terminal we tracked. */
export function isBootstrapping(): boolean {
  return activeBootstrapTerminal !== undefined;
}

/**
 * Register a callback fired whenever the in-progress state changes
 * (terminal tracked, or terminal closed). The status bar uses this to
 * refresh immediately rather than waiting for its 10s tick.
 */
export function onRunStateChanged(cb: () => void): void {
  onChangeCallback = cb;
}

function ensureCloseListener(): void {
  if (listenerRegistered) return;
  listenerRegistered = true;
  vscode.window.onDidCloseTerminal((closed) => {
    if (closed === activeBootstrapTerminal) {
      activeBootstrapTerminal = undefined;
      onChangeCallback?.();
    }
  });
}
