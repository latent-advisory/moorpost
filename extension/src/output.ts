// OutputChannel-based runner for non-interactive CLI invocations.
//
// Used for read-only or auto-approved CLI calls (doctor, cost, conflicts,
// `setup --yes`) so the extension doesn't open a fresh terminal for every
// click. The runner streams stdout/stderr to a single shared "Moorpost"
// OutputChannel and shows a progress notification with a Cancel button.
//
// For genuinely-interactive commands (auth, handoff, attach, bootstrap),
// continue using `runInTerminal` from cli.ts — those need a real TTY.

import * as vscode from 'vscode';
import { spawn } from 'node:child_process';
import { cliBinary } from './cli';

let channel: vscode.OutputChannel | undefined;

function getChannel(): vscode.OutputChannel {
  if (!channel) channel = vscode.window.createOutputChannel('Moorpost');
  return channel;
}

/** Public accessor: lets command handlers write diagnostic lines to the
 * shared "Moorpost" OutputChannel. Useful when a flow has multiple gates
 * (surface detection, baton presence, popup choice) and we need to show
 * the user *why* a popup did or didn't fire — DevTools console requires
 * Help → Toggle Developer Tools, but Output → "Moorpost" is one click. */
export function logToChannel(line: string): void {
  getChannel().appendLine(`[${new Date().toISOString()}] ${line}`);
}

export interface RunOptions {
  cwd?: string;
  /** Title shown in the progress notification. */
  title: string;
  /** When to reveal the OutputChannel: always, on-error (default), or never. */
  reveal?: 'always' | 'on-error' | 'never';
}

/**
 * Run `moorpost <args...>` non-interactively, streaming output to the shared
 * OutputChannel and showing a progress notification with a cancel button.
 * Resolves with the child's exit code; -1 on spawn error or cancellation.
 */
export async function runCliInOutput(args: string[], opts: RunOptions): Promise<number> {
  const ch = getChannel();
  ch.appendLine('');
  ch.appendLine(`$ moorpost ${args.join(' ')}`);

  return await vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: `Moorpost: ${opts.title}`,
      cancellable: true,
    },
    (_progress, token) =>
      new Promise<number>((resolve) => {
        const child = spawn(cliBinary(), args, {
          cwd: opts.cwd,
          // NO_COLOR keeps the OutputChannel readable; many CLIs respect it.
          env: { ...process.env, NO_COLOR: '1' },
        });

        const cancelSub = token.onCancellationRequested(() => {
          ch.appendLine('[cancelled]');
          child.kill('SIGTERM');
        });

        child.stdout?.on('data', (chunk: Buffer) => ch.append(chunk.toString()));
        child.stderr?.on('data', (chunk: Buffer) => ch.append(chunk.toString()));

        child.on('exit', (code) => {
          cancelSub.dispose();
          ch.appendLine(`[exit ${code ?? '?'}]`);
          const reveal = opts.reveal ?? 'on-error';
          if (reveal === 'always' || (reveal === 'on-error' && code !== 0)) {
            ch.show(true);
          }
          resolve(code ?? -1);
        });

        child.on('error', (err) => {
          cancelSub.dispose();
          ch.appendLine(`[spawn error: ${err.message}]`);
          ch.show(true);
          resolve(-1);
        });
      }),
  );
}
