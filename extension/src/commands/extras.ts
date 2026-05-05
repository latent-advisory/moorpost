// Small standalone commands that didn't fit elsewhere: edit config, toggle
// side, and the context-key updater used to gate palette visibility on
// project initialization.

import * as vscode from 'vscode';
import * as path from 'node:path';
import { getStatus, workspaceRoot } from '../cli';

const CONFIG_RELATIVE = path.join('.moorpost', 'config.yaml');

/** Opens the workspace's `.moorpost/config.yaml` in the editor. */
export async function editConfig(): Promise<void> {
  const cwd = workspaceRoot();
  if (!cwd) {
    void vscode.window.showWarningMessage('Open a workspace folder first.');
    return;
  }
  const uri = vscode.Uri.file(path.join(cwd, CONFIG_RELATIVE));
  try {
    await vscode.workspace.fs.stat(uri);
  } catch {
    void vscode.window.showWarningMessage(
      `${CONFIG_RELATIVE} not found. Run "Moorpost: Bootstrap" first to create it.`,
    );
    return;
  }
  const doc = await vscode.workspace.openTextDocument(uri);
  await vscode.window.showTextDocument(doc);
}

/**
 * Single-action toggle that routes to the next-needed step in the setup
 * lifecycle:
 *   no config              → bootstrap
 *   no auth credential     → sign in
 *   configured but no VM   → provision
 *   configured + VM, local → handoff
 *   configured + VM, remote → return
 *
 * Wired to the status bar click and exposed as a palette command.
 */
export async function toggleSide(): Promise<void> {
  const cwd = workspaceRoot();
  if (!cwd) {
    void vscode.window.showWarningMessage('Open a workspace folder first.');
    return;
  }
  const status = await getStatus(cwd);
  if (!status) {
    await vscode.commands.executeCommand('moorpost.bootstrap');
    return;
  }
  if (status.auth_cached === false) {
    // Config exists but the keychain has no Claude credential — usually
    // means a partial bootstrap (auth was skipped/cancelled). Drive
    // straight to sign-in instead of letting them provision a VM they
    // can't hand off to.
    await vscode.commands.executeCommand('moorpost.signIn');
    return;
  }
  if (!status.vm_id) {
    await vscode.commands.executeCommand('moorpost.provision');
    return;
  }
  const side = status.active_side ?? 'local';
  if (side === 'local') {
    await vscode.commands.executeCommand('moorpost.handoff');
  } else {
    await vscode.commands.executeCommand('moorpost.return');
  }
}

/**
 * Background watcher that flips the `moorpost.configured` context key based
 * on whether `.moorpost/config.yaml` resolves in the workspace. The key
 * gates command-palette visibility (see package.json's `commandPalette`).
 *
 * Polls every `intervalMs`; cheap because `getStatus` short-circuits when no
 * config is present (CLI returns non-zero, getStatus returns null).
 */
export function startConfiguredContextWatcher(
  context: vscode.ExtensionContext,
  intervalMs = 10_000,
): void {
  const update = async () => {
    const cwd = workspaceRoot();
    const status = cwd ? await getStatus(cwd) : null;
    await vscode.commands.executeCommand(
      'setContext',
      'moorpost.configured',
      status !== null,
    );
  };
  void update();
  const timer = setInterval(() => void update(), intervalMs);
  context.subscriptions.push(
    new vscode.Disposable(() => clearInterval(timer)),
    vscode.workspace.onDidChangeWorkspaceFolders(() => void update()),
  );
}

/**
 * On first activation in a workspace without a Moorpost config, prompt the
 * user with a non-modal "Get started" notification. Stores a flag in
 * globalState so we don't nag — once dismissed (or accepted), never shown
 * again on this machine. Skipped silently when the workspace is already
 * configured (the user clearly knows what Moorpost is).
 */
export async function maybeShowFirstRunNudge(
  context: vscode.ExtensionContext,
): Promise<void> {
  const KEY = 'moorpost.firstRunNudgeShownAt';
  if (context.globalState.get<number>(KEY)) return;

  const cwd = workspaceRoot();
  const status = cwd ? await getStatus(cwd) : null;
  if (status) {
    // Already configured — silently mark as seen and move on.
    await context.globalState.update(KEY, Date.now());
    return;
  }

  const choice = await vscode.window.showInformationMessage(
    'Moorpost installed. Run Bootstrap to set up your laptop ↔ remote VM workflow.',
    'Get Started',
    'Open walkthrough',
    'Not now',
  );
  await context.globalState.update(KEY, Date.now());
  if (choice === 'Get Started') {
    await vscode.commands.executeCommand('moorpost.bootstrap');
  } else if (choice === 'Open walkthrough') {
    await vscode.commands.executeCommand(
      'workbench.action.openWalkthrough',
      'latent-advisory.moorpost#moorpost.gettingStarted',
      true,
    );
  }
}
