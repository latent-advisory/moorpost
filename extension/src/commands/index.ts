// Command registrations. Each command is a thin wrapper that defers to the
// CLI — the extension is intentionally a UI shell, not where the work happens.

import * as vscode from 'vscode';
import { runInTerminal, getStatus, workspaceRoot } from '../cli';
import type { MoorpostTreeProvider } from '../treeView';
import { bootstrapProject, initProject } from './getStarted';
import { editConfig, toggleSide } from './extras';
import { runCliInOutput } from '../output';
import { closeAttachQuietly, openOrFocusAttach } from '../remoteSession';

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

    vscode.commands.registerCommand('moorpost.runSetup', async () => {
      await runCliInOutput(['setup', '--yes'], {
        title: 'Installing prerequisites',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.runDoctor', async () => {
      await runCliInOutput(['doctor'], {
        cwd: workspaceRoot(),
        title: 'Running diagnostics',
        reveal: 'always',
      });
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
      // --wait makes the CLI poll SSH until claude is on PATH on the VM,
      // so the user gets a single "ready to handoff" signal instead of
      // a misleading "VM running" while the 5-7min bootstrap continues
      // silently in the background.
      await runCliInOutput(['provision', '--wait'], {
        cwd,
        title: 'Provisioning VM (waiting for bootstrap)',
        reveal: 'always',
      });
      refreshTreeAfter(2000);
    }),

    vscode.commands.registerCommand('moorpost.handoff', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }

      // Preflight: surface specific, actionable errors instead of letting
      // the CLI fail mid-flight after we've already spun up a VM (or
      // shown the modal confirm).
      const status = await getStatus(cwd);
      if (!status) {
        const pick = await vscode.window.showWarningMessage(
          'No Moorpost project here. Run Bootstrap first.',
          'Run Bootstrap',
          'Dismiss',
        );
        if (pick === 'Run Bootstrap') {
          await vscode.commands.executeCommand('moorpost.bootstrap');
        }
        return;
      }
      if (!status.vm_id) {
        const pick = await vscode.window.showWarningMessage(
          'No VM provisioned yet. Provision one before handoff.',
          'Provision now',
          'Dismiss',
        );
        if (pick === 'Provision now') {
          await vscode.commands.executeCommand('moorpost.provision');
        }
        return;
      }
      if (status.active_side === 'remote') {
        const pick = await vscode.window.showInformationMessage(
          'Session is already on the remote VM — nothing to hand off.',
          'Open live session',
          'Return to local',
          'Dismiss',
        );
        if (pick === 'Open live session') openOrFocusAttach(cwd);
        else if (pick === 'Return to local')
          await vscode.commands.executeCommand('moorpost.return');
        return;
      }

      const choice = await vscode.window.showInformationMessage(
        'Hand off the active Claude session to the remote VM?',
        { modal: true, detail: 'Local Claude pauses. After handoff, a terminal will open with the live remote session — you continue the conversation there.' },
        'Hand off',
      );
      if (choice !== 'Hand off') return;
      const exit = await runCliInOutput(['handoff', '--yes'], {
        cwd,
        title: 'Handing off to remote',
        reveal: 'on-error',
      });
      refreshTreeAfter(2000);
      // After successful handoff, auto-attach so the user lands directly
      // in the remote Claude pane (the "feels local but happens remote"
      // experience). Toggleable via settings.
      if (exit === 0) {
        const auto = vscode.workspace
          .getConfiguration('moorpost')
          .get<boolean>('autoAttachOnHandoff', true);
        if (auto) {
          openOrFocusAttach(cwd);
        }
      }
    }),

    vscode.commands.registerCommand('moorpost.return', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }

      // Preflight: only meaningful if a remote session exists.
      const status = await getStatus(cwd);
      if (!status) {
        vscode.window.showWarningMessage(
          'No Moorpost project here. Nothing to return.',
        );
        return;
      }
      if (!status.vm_id) {
        vscode.window.showWarningMessage(
          'No VM provisioned. There is no remote session to return from.',
        );
        return;
      }
      if ((status.active_side ?? 'local') === 'local') {
        vscode.window.showInformationMessage(
          'Session is already local — nothing to return.',
        );
        return;
      }

      // Close the attached terminal first so the disconnect-warning
      // logic doesn't trip when SSH drops as part of the planned return.
      closeAttachQuietly();
      await runCliInOutput(['return'], {
        cwd,
        title: 'Returning to local',
        reveal: 'on-error',
      });
      refreshTreeAfter(2000);
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
      await runCliInOutput(['conflicts'], {
        cwd,
        title: 'Listing sync conflicts',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.attach', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      // Route through the shared remote-session manager so this terminal
      // is tracked alongside the auto-attached one (single-tracked
      // attach session, disconnect warning, etc.).
      openOrFocusAttach(cwd);
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
      await runCliInOutput(['cost', '--explain'], {
        cwd,
        title: 'Computing cost details',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.editConfig', editConfig),

    vscode.commands.registerCommand('moorpost.toggleSide', toggleSide),
  );
}
