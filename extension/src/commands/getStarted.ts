// Top-level flows used by the Bootstrap command and the walkthrough buttons.
// Onboarding has two paths: the native VSCode walkthrough (declared in
// package.json with explanatory media) and the one-shot Bootstrap command
// here. The earlier QuickPick "Get started" wizard was removed in v1.0 —
// it duplicated both paths without adding value.

import * as vscode from 'vscode';
import { runInTerminal } from '../cli';

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

  const args = ['bootstrap', '--yes'];
  if (provisionChoice.provision) args.push('--provision');
  runInTerminal(args, target.uri.fsPath);
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
