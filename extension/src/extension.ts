// Moorpost VSCode extension — entrypoint.
//
// This file is intentionally tiny. The extension is a thin UI shell over the
// `moorpost` CLI; all real work happens there. See PLUGIN.md §6.1.

import * as vscode from 'vscode';
import * as fsPromises from 'node:fs/promises';
import * as os from 'node:os';
import { spawnSync } from 'node:child_process';
import * as https from 'node:https';
import { registerCommands } from './commands';
import { maybeShowFirstRunNudge, startConfiguredContextWatcher } from './commands/extras';
import { setupStatusBar } from './statusBar';
import { MoorpostTreeProvider } from './treeView';
import { IdleMonitor } from './idleMonitor';
import { registerClaudeTerminalWatchers } from './claudeTerminal';
import { SessionTracker } from './sessionTracker';
import { ensureCliInstalled, type InstallerDeps } from './cliInstaller';

let sessionTracker: SessionTracker | undefined;

export function getSessionTracker(): SessionTracker | undefined {
  return sessionTracker;
}

/**
 * Builds the production InstallerDeps from Node built-ins. Kept here
 * (not in cliInstaller.ts) so the installer module stays free of
 * top-level Node imports we'd otherwise have to mock in unit tests.
 */
function buildInstallerDeps(): InstallerDeps {
  return {
    spawnSync: (cmd, args, opts) => {
      const result = spawnSync(cmd, args, { encoding: 'utf8', ...(opts ?? {}) });
      return {
        status: result.status,
        stdout: typeof result.stdout === 'string' ? result.stdout : result.stdout?.toString('utf8') ?? '',
        stderr: typeof result.stderr === 'string' ? result.stderr : result.stderr?.toString('utf8') ?? '',
      };
    },
    httpsGet: (url) =>
      new Promise((resolve, reject) => {
        const get = (target: string, hopsLeft: number) => {
          if (hopsLeft <= 0) {
            reject(new Error(`too many redirects fetching ${url}`));
            return;
          }
          https
            .get(target, (res) => {
              const status = res.statusCode ?? 0;
              if (status >= 300 && status < 400 && res.headers.location) {
                res.resume();
                get(res.headers.location, hopsLeft - 1);
                return;
              }
              const chunks: Buffer[] = [];
              res.on('data', (c) => chunks.push(Buffer.from(c)));
              res.on('end', () => resolve({ status, body: Buffer.concat(chunks) }));
              res.on('error', reject);
            })
            .on('error', reject);
        };
        get(url, 5);
      }),
    fs: fsPromises,
    os,
    platform: process.platform,
    arch: process.arch,
    pathEnv: process.env.PATH ?? '',
    cliPathSetting: vscode.workspace.getConfiguration('moorpost').get<string>('cliPath') || undefined,
  };
}

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  // Run the CLI auto-installer first so every subsequent command finds a
  // working binary. Failures surface as toasts but don't block activation.
  await ensureCliInstalled(buildInstallerDeps());

  sessionTracker = new SessionTracker();
  context.subscriptions.push({ dispose: () => sessionTracker?.dispose() });

  const treeProvider = new MoorpostTreeProvider();
  context.subscriptions.push(
    vscode.window.registerTreeDataProvider('moorpost.projectTree', treeProvider),
    vscode.commands.registerCommand('moorpost.refreshTree', () => treeProvider.refresh()),
    vscode.commands.registerCommand('moorpost.debugSessionTracker', () => {
      const text = sessionTracker?.describe() ?? 'SessionTracker not initialized';
      vscode.window.showInformationMessage(text, { modal: true });
    }),
  );

  registerCommands(context, treeProvider);
  setupStatusBar(context);
  startConfiguredContextWatcher(context);
  registerClaudeTerminalWatchers(context);
  void maybeShowFirstRunNudge(context);

  const idle = new IdleMonitor();
  idle.start(context);

  // Smoke-log so the user can confirm the extension activated. Visible in
  // Output → "Moorpost".
  const out = vscode.window.createOutputChannel('Moorpost');
  out.appendLine(`Moorpost extension activated at ${new Date().toISOString()}`);
  context.subscriptions.push(out);
}

export function deactivate(): void {
  // No-op; subscriptions registered above are cleaned up by VSCode automatically.
}
