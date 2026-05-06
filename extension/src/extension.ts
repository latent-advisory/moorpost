// Moorpost VSCode extension — entrypoint.
//
// This file is intentionally tiny. The extension is a thin UI shell over the
// `moorpost` CLI; all real work happens there. See PLUGIN.md §6.1.

import * as vscode from 'vscode';
import { registerCommands } from './commands';
import { maybeShowFirstRunNudge, startConfiguredContextWatcher } from './commands/extras';
import { setupStatusBar } from './statusBar';
import { MoorpostTreeProvider } from './treeView';
import { IdleMonitor } from './idleMonitor';
import { registerClaudeTerminalWatchers } from './claudeTerminal';
import { SessionTracker } from './sessionTracker';

let sessionTracker: SessionTracker | undefined;

export function getSessionTracker(): SessionTracker | undefined {
  return sessionTracker;
}

export function activate(context: vscode.ExtensionContext): void {
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
