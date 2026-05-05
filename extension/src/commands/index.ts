// Command registrations. Each command is a thin wrapper that defers to the
// CLI — the extension is intentionally a UI shell, not where the work happens.

import * as vscode from 'vscode';
import { runInTerminal, getStatus, workspaceRoot } from '../cli';
import type { MoorpostTreeProvider } from '../treeView';
import { bootstrapProject, initProject } from './getStarted';
import { editConfig, toggleSide } from './extras';
import { runCliInOutput } from '../output';
import {
  closeClaudeTerminalQuietly,
  openLocalClaude,
  openRemoteClaude,
} from '../claudeTerminal';
import { refreshStatusBarNow } from '../statusBar';

export function registerCommands(
  context: vscode.ExtensionContext,
  treeProvider?: MoorpostTreeProvider,
): void {
  // Tree + status bar refresh after state-changing commands. The CLI runs
  // out-of-process; refresh on a short delay so the new state.json is
  // visible to `moorpost status`.
  const refreshTreeAfter = (ms: number) => {
    setTimeout(() => {
      if (treeProvider) treeProvider.refresh();
      refreshStatusBarNow();
    }, ms);
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
      // Soft-warn on missing auth so the user doesn't end up with a
      // provisioned-but-unhandoffable VM. Not blocking — provisioning
      // itself doesn't need auth.
      const status = await getStatus(cwd);
      if (status && status.auth_cached === false) {
        const pick = await vscode.window.showWarningMessage(
          'No Claude credential cached. The VM will provision fine, but you won\'t be able to hand off until you sign in.',
          'Sign in first',
          'Provision anyway',
          'Cancel',
        );
        if (pick === 'Cancel' || !pick) return;
        if (pick === 'Sign in first') {
          await vscode.commands.executeCommand('moorpost.signIn');
          return;
        }
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
      if (status.auth_cached === false) {
        const pick = await vscode.window.showWarningMessage(
          'Not signed in to Claude. Sign in before handoff.',
          'Sign in',
          'Dismiss',
        );
        if (pick === 'Sign in') {
          await vscode.commands.executeCommand('moorpost.signIn');
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
        if (pick === 'Open live session') openRemoteClaude(cwd);
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
          openRemoteClaude(cwd);
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

      // Close the remote attach terminal first so the disconnect-warning
      // logic doesn't trip when SSH drops as part of the planned return.
      closeClaudeTerminalQuietly();
      const exit = await runCliInOutput(['return'], {
        cwd,
        title: 'Returning to local',
        reveal: 'on-error',
      });
      refreshTreeAfter(2000);
      // After successful return, auto-open the local Claude side in the
      // same Moorpost: Claude terminal slot. So the user's "single Claude
      // window" experience continues in local mode without context-switching.
      if (exit === 0) {
        const auto = vscode.workspace
          .getConfiguration('moorpost')
          .get<boolean>('autoAttachOnHandoff', true);
        if (auto) {
          // Pull the agent session id from the latest status so we can
          // `claude --resume <id>`. Best effort — if missing, just open
          // a fresh `claude` invocation; the user can pick from the
          // session picker.
          const refreshed = await getStatus(cwd);
          openLocalClaude(cwd, refreshed?.agent_session_id);
        }
      }
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
      openRemoteClaude(cwd);
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
