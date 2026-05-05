// Get-started orchestration. The walkthrough (declared in package.json)
// covers each step independently; this command runs an inline QuickPick wizard
// for users who'd rather drive from the command palette.
//
// Each branch shells the existing CLI; we never duplicate setup logic in TS.

import * as vscode from 'vscode';
import { runInTerminal, workspaceRoot } from '../cli';

interface Step {
  label: string;
  description: string;
  detail: string;
  run: () => Promise<void>;
}

export async function getStarted(): Promise<void> {
  const steps: Step[] = [
    {
      label: '$(rocket) 0. Bootstrap (one-shot — recommended)',
      description: 'moorpost bootstrap',
      detail: 'Runs setup → auth → init in sequence, skipping work that\'s already done. Add --provision to also create the VM.',
      run: bootstrapProject,
    },
    {
      label: '$(tools) 1. Install prerequisites',
      description: 'moorpost setup',
      detail: 'Detects gcloud / mutagen / tmux / claude on PATH and prompts to install missing tools.',
      run: async () => {
        runInTerminal(['setup']);
      },
    },
    {
      label: '$(stethoscope) 2. Run diagnostics',
      description: 'moorpost doctor',
      detail: 'Verifies prereqs, keychain access, and (in a project) the GCP preflight.',
      run: async () => {
        runInTerminal(['doctor']);
      },
    },
    {
      label: '$(key) 3. Sign in to Claude',
      description: 'moorpost auth',
      detail: 'OAuth flow to claude.ai; token cached in your OS keychain.',
      run: async () => {
        runInTerminal(['auth']);
      },
    },
    {
      label: '$(folder-opened) 4. Initialize a project',
      description: 'moorpost init (you pick the folder)',
      detail: 'Writes .moorpost/config.yaml in the workspace folder you choose.',
      run: initProject,
    },
    {
      label: '$(cloud-upload) 5. Provision the VM',
      description: 'moorpost provision',
      detail: 'Creates the GCP VM (left stopped). One-time per project.',
      run: async () => {
        const cwd = workspaceRoot();
        if (!cwd) {
          void vscode.window.showWarningMessage('Open a workspace folder first.');
          return;
        }
        runInTerminal(['provision'], cwd);
      },
    },
    {
      label: '$(book) Open the full walkthrough',
      description: 'A guided checklist in the editor',
      detail: 'Same steps with explanatory media; persists progress.',
      run: async () => {
        await vscode.commands.executeCommand(
          'workbench.action.openWalkthrough',
          'latent-advisory.moorpost#moorpost.gettingStarted',
          true,
        );
      },
    },
  ];

  const pick = await vscode.window.showQuickPick(steps, {
    title: 'Moorpost — Get started',
    placeHolder: 'Pick a step. You can re-run this command at any time.',
    matchOnDescription: true,
    matchOnDetail: true,
  });
  if (!pick) return;
  await pick.run();
}

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
