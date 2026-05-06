// Small standalone commands that didn't fit elsewhere: edit config, toggle
// side, and the context-key updater used to gate palette visibility on
// project initialization.

import * as vscode from 'vscode';
import * as path from 'node:path';
import { getStatus, workspaceRoot } from '../cli';
import { getBootstrapTerminal } from '../runState';

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
 * Status-bar click handler. For the setup lifecycle (no config / no auth /
 * no VM) it routes directly to the next-needed step. Once configured and
 * provisioned, it shows a quick-pick offering handoff AND return so the
 * user can move any session in any direction at any time — required for
 * per-session routing where a project may have BOTH local and remote
 * sessions simultaneously.
 *
 *   no config              → bootstrap
 *   no auth credential     → sign in
 *   configured but no VM   → provision
 *   configured + VM        → quick-pick { Handoff, Return (if any remote), Status }
 */
export async function toggleSide(): Promise<void> {
  const cwd = workspaceRoot();
  if (!cwd) {
    void vscode.window.showWarningMessage('Open a workspace folder first.');
    return;
  }
  const bootstrapTerm = getBootstrapTerminal();
  if (bootstrapTerm) {
    bootstrapTerm.show();
    return;
  }
  const status = await getStatus(cwd);
  if (!status) {
    await vscode.commands.executeCommand('moorpost.bootstrap');
    return;
  }
  if (status.auth_cached === false) {
    await vscode.commands.executeCommand('moorpost.signIn');
    return;
  }
  if (!status.vm_id) {
    await vscode.commands.executeCommand('moorpost.provision');
    return;
  }

  const remoteCount = status.remote_sids?.length ?? 0;
  const legacyRemote = remoteCount === 0 && status.active_side === 'remote';
  const hasRemote = remoteCount > 0 || legacyRemote;

  type Action = 'handoff' | 'return' | 'status';
  interface ActionItem extends vscode.QuickPickItem { action: Action }
  const items: ActionItem[] = [
    {
      label: '$(arrow-up) Handoff a session to remote',
      description: 'Move a local Claude Code session to the VM',
      action: 'handoff',
    },
  ];
  if (hasRemote) {
    items.push({
      label: '$(arrow-down) Return a session to local',
      description:
        remoteCount > 0
          ? `${remoteCount} session(s) currently on remote`
          : 'Bring the remote-routed session back to this machine',
      action: 'return',
    });
  }
  items.push({
    label: '$(info) Show status details',
    description: 'Open the Moorpost status report',
    action: 'status',
  });

  const placeHolder = hasRemote
    ? `Moorpost — ${remoteCount} on remote, VM ${status.vm_state ?? '?'}`
    : `Moorpost — all sessions local, VM ${status.vm_state ?? '?'}`;
  const picked = await vscode.window.showQuickPick(items, { placeHolder });
  if (!picked) return;
  switch (picked.action) {
    case 'handoff':
      await vscode.commands.executeCommand('moorpost.handoff');
      break;
    case 'return':
      await vscode.commands.executeCommand('moorpost.return');
      break;
    case 'status':
      await vscode.commands.executeCommand('moorpost.status');
      break;
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
 *
 * Logs every decision to the "Moorpost" OutputChannel so failed-to-fire
 * cases can be diagnosed without a debug build.
 */
export async function maybeShowFirstRunNudge(
  context: vscode.ExtensionContext,
): Promise<void> {
  // Versioned key — bump suffix to break cached "already shown" state
  // from older builds without touching state.vscdb directly.
  const KEY = 'moorpost.firstRunNudgeShownAt.v2';
  const log = vscode.window.createOutputChannel('Moorpost');
  const seen = context.globalState.get<number>(KEY);
  if (seen) {
    log.appendLine(`[nudge] already shown at ${new Date(seen).toISOString()}; skipping`);
    return;
  }

  const cwd = workspaceRoot();
  const status = cwd ? await getStatus(cwd) : null;
  if (status) {
    log.appendLine(`[nudge] workspace already configured (project=${status.project}); marking seen`);
    await context.globalState.update(KEY, Date.now());
    return;
  }

  log.appendLine(`[nudge] firing first-run notification (cwd=${cwd ?? '<none>'})`);
  const choice = await vscode.window.showInformationMessage(
    'Moorpost installed. Run Bootstrap to set up your laptop ↔ remote VM workflow.',
    'Get Started',
    'Open walkthrough',
    'Not now',
  );
  log.appendLine(`[nudge] user chose: ${choice ?? '<dismissed>'}`);
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
