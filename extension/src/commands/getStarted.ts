// Top-level flows used by the Bootstrap command and the walkthrough buttons.
// Onboarding has two paths: the native VSCode walkthrough (declared in
// package.json with explanatory media) and the one-shot Bootstrap command
// here. The earlier QuickPick "Get started" wizard was removed in v1.0 —
// it duplicated both paths without adding value.

import * as vscode from 'vscode';
import { listGCloudConfigs, runInTerminal, type GCloudConfig } from '../cli';
import { setBootstrapTerminal } from '../runState';

/**
 * One-shot bootstrap. Asks which folder to initialize (multi-root only),
 * confirms whether to also provision a VM, and runs `moorpost bootstrap`
 * in a terminal — `--yes` is implied because we just confirmed in the UI.
 */
export async function bootstrapProject(): Promise<void> {
  const folders = vscode.workspace.workspaceFolders ?? [];
  if (folders.length === 0) {
    void vscode.window.showWarningMessage(
      'Open a folder in VSCode first — bootstrap initializes the workspace folder you choose.',
    );
    return;
  }

  let target: vscode.WorkspaceFolder | undefined;
  if (folders.length === 1) {
    target = folders[0];
  } else {
    target = await vscode.window.showWorkspaceFolderPick({
      placeHolder: 'Which folder should Moorpost manage?',
    });
    if (!target) return;
  }

  const provisionChoice = await vscode.window.showQuickPick(
    [
      {
        label: 'Skip provisioning (recommended for first run)',
        detail: 'Sets up everything except the VM. Run `moorpost provision` later when you\'re ready.',
        provision: false,
      },
      {
        label: 'Also provision the VM',
        detail: 'Creates a stopped GCP VM (~$4/mo disk fee, ~$0.067/hr while running).',
        provision: true,
      },
    ],
    {
      title: `Bootstrap "${target.name}"`,
      placeHolder: 'Should bootstrap also create the VM at the end?',
    },
  );
  if (!provisionChoice) return;

  // Pre-select the gcloud configuration via a native QuickPick so the user
  // doesn't have to spot the prompt scrolling past in the terminal during
  // `moorpost init`. If the user picks an existing config we hand its name
  // and project to bootstrap as flags, fully bypassing the terminal picker.
  // If they pick "add new" — or we can't reach gcloud at all — we fall back
  // to the in-terminal flow (which still shows a banner).
  const gcloudPick = await pickGCloudConfig();
  if (gcloudPick === undefined) return; // user dismissed

  const args = ['bootstrap', '--yes'];
  if (provisionChoice.provision) args.push('--provision');
  if (gcloudPick !== 'fallback-to-terminal') {
    args.push(`--gcp-config=${gcloudPick.name}`);
    args.push(`--gcp-project=${gcloudPick.project}`);
  }
  const term = runInTerminal(args, target.uri.fsPath);
  // Tell the status bar that bootstrap is in flight. While this terminal
  // is alive, status-bar clicks focus this terminal instead of routing
  // through toggleSide (which would fire signIn/provision/handoff on top
  // of the still-running bootstrap as its intermediate states tick by).
  setBootstrapTerminal(term);
}

/**
 * Native VSCode picker for the gcloud configuration moorpost should use.
 *
 * Returns:
 *   - a GCloudConfig: user picked an existing config (skip terminal picker)
 *   - "fallback-to-terminal": user wants to add a new account, OR no
 *     configs were found (gcloud not installed / never logged in) — let
 *     `moorpost init` handle the OAuth flow in the terminal
 *   - undefined: user dismissed the picker (caller should abort the action)
 */
async function pickGCloudConfig(): Promise<GCloudConfig | 'fallback-to-terminal' | undefined> {
  const configs = await listGCloudConfigs();
  if (configs.length === 0) {
    // No configs (or gcloud missing). Fall through to the terminal so init's
    // own picker can run `gcloud auth login` and walk the user through.
    return 'fallback-to-terminal';
  }

  type Item = vscode.QuickPickItem & { kind?: 'config' | 'new'; config?: GCloudConfig };
  const items: Item[] = configs.map((c) => ({
    label: c.name,
    description: c.is_active ? '(active)' : undefined,
    detail: `account: ${c.account || '(none)'}   project: ${c.project || '(unset)'}`,
    kind: 'config',
    config: c,
  }));
  items.push({
    label: '$(add) Add a new gcloud account',
    detail: 'Opens a browser OAuth flow in the terminal — needed only the first time.',
    kind: 'new',
  });

  const choice = await vscode.window.showQuickPick(items, {
    title: 'Pick a gcloud configuration for Moorpost',
    placeHolder: 'Moorpost will pin this configuration in .moorpost/config.yaml',
    ignoreFocusOut: true,
  });
  if (!choice) return undefined;
  if (choice.kind === 'new' || !choice.config) return 'fallback-to-terminal';

  // A configuration without a project set would force `moorpost init` to
  // re-trigger its own picker (it treats empty project as "ask the user").
  // Surface that here instead of silently falling back, since the user
  // *just* picked this config thinking it was set up.
  if (!choice.config.project) {
    void vscode.window.showWarningMessage(
      `Configuration "${choice.config.name}" has no GCP project set. ` +
        `Run \`gcloud --configuration=${choice.config.name} config set project YOUR_PROJECT\` first, then retry bootstrap.`,
    );
    return undefined;
  }
  return choice.config;
}

/**
 * Folder-aware `moorpost init`. If the workspace has multiple roots, asks
 * which one to initialize. Single-root workspaces skip the picker.
 */
export async function initProject(): Promise<void> {
  const folders = vscode.workspace.workspaceFolders ?? [];
  if (folders.length === 0) {
    void vscode.window.showWarningMessage(
      'Open a folder in VSCode first — Moorpost initializes the workspace folder you choose.',
    );
    return;
  }

  let target: vscode.WorkspaceFolder | undefined;
  if (folders.length === 1) {
    target = folders[0];
  } else {
    target = await vscode.window.showWorkspaceFolderPick({
      placeHolder: 'Which folder should Moorpost manage? (this is what gets synced to the VM)',
    });
    if (!target) return;
  }

  const confirm = await vscode.window.showInformationMessage(
    `Initialize Moorpost in "${target.name}"?\n\nThis writes .moorpost/config.yaml with sensible defaults. Sync will mirror this folder (minus standard excludes) to the remote VM.`,
    { modal: true },
    'Initialize',
  );
  if (confirm !== 'Initialize') return;

  runInTerminal(['init'], target.uri.fsPath);
}
