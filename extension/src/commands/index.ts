// Command registrations. Each command is a thin wrapper that defers to the
// CLI — the extension is intentionally a UI shell, not where the work happens.

import * as vscode from 'vscode';
import { runInTerminal, getStatus, workspaceRoot } from '../cli';
import type { MoorpostTreeProvider } from '../treeView';
import { bootstrapProject, getStarted, initProject } from './getStarted';
import { editConfig, toggleSide } from './extras';

export function registerCommands(
  context: vscode.ExtensionContext,
  treeProvider?: MoorpostTreeProvider,
): void {
  // Tree refreshes after handoff/return because the active side flips. The CLI
  // commands run in a terminal so we can't await their exit; refresh on a
  // short delay to give the CLI time to update state.
  const refreshTreeAfter = (ms: number) => {
    if (!treeProvider) return;
    setTimeout(() => treeProvider.refresh(), ms);
  };

  context.subscriptions.push(
    vscode.commands.registerCommand('moorpost.bootstrap', bootstrapProject),

    vscode.commands.registerCommand('moorpost.getStarted', getStarted),

    vscode.commands.registerCommand('moorpost.runSetup', async () => {
      runInTerminal(['setup']);
    }),

    vscode.commands.registerCommand('moorpost.runDoctor', async () => {
      runInTerminal(['doctor'], workspaceRoot());
    }),

    vscode.commands.registerCommand('moorpost.initProject', initProject),

    vscode.commands.registerCommand('moorpost.signIn', async () => {
      runInTerminal(['auth']);
    }),

    vscode.commands.registerCommand('moorpost.provision', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      runInTerminal(['provision'], cwd);
      refreshTreeAfter(5000);
    }),

    vscode.commands.registerCommand('moorpost.handoff', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      const choice = await vscode.window.showInformationMessage(
        'Hand off the active Claude session to the remote VM?',
        { modal: true },
        'Hand off',
      );
      if (choice !== 'Hand off') return;
      runInTerminal(['handoff', '--yes'], cwd);
      refreshTreeAfter(8000);
    }),

    vscode.commands.registerCommand('moorpost.return', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      runInTerminal(['return'], cwd);
      refreshTreeAfter(8000);
    }),

    vscode.commands.registerCommand('moorpost.status', async () => {
      const cwd = workspaceRoot();
      const status = await getStatus(cwd);
      if (!status) {
        vscode.window.showWarningMessage(
          'No Moorpost project found here. Run `moorpost init` in a project directory.',
        );
        return;
      }
      const lines = [
        `Project: ${status.project}`,
        `Provider: ${status.provider}`,
        `Active side: ${status.active_side ?? 'local'}`,
      ];
      if (status.vm_id) lines.push(`VM: ${status.vm_id} (${status.vm_state ?? '?'})`);
      if (status.month_to_date_usd) lines.push(`MTD cost: $${status.month_to_date_usd.toFixed(2)}`);
      void vscode.window.showInformationMessage(lines.join(' · '));
    }),

    vscode.commands.registerCommand('moorpost.showConflicts', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      runInTerminal(['conflicts'], cwd);
    }),

    vscode.commands.registerCommand('moorpost.attach', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      runInTerminal(['attach'], cwd);
    }),

    vscode.commands.registerCommand('moorpost.destroy', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      const choice = await vscode.window.showWarningMessage(
        'Permanently destroy this VM and its boot disk? This cannot be undone.',
        { modal: true },
        'Destroy',
      );
      if (choice !== 'Destroy') return;
      runInTerminal(['destroy', '--yes'], cwd);
      refreshTreeAfter(8000);
    }),

    vscode.commands.registerCommand('moorpost.showCost', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      runInTerminal(['cost', '--explain'], cwd);
    }),

    vscode.commands.registerCommand('moorpost.editConfig', editConfig),

    vscode.commands.registerCommand('moorpost.toggleSide', toggleSide),
  );
}
