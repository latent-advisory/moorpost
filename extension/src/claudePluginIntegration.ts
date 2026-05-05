// Drives the Anthropic Claude Code plugin's claudeProcessWrapper setting
// based on moorpost's active_side. When active=remote, the plugin's claude
// invocations are routed through ~/.moorpost/bin/claude-wrapper, which
// SSHes to the VM and runs claude there. When active=local, the wrapper
// is unset so the plugin uses its default claude binary directly.
//
// The plugin re-reads the setting on the next claude invocation, so the
// effect is immediate for the next "new conversation"; an in-flight
// conversation continues with whatever wrapper was in effect when it
// started. We trigger claude-vscode.newConversation on side-flip so the
// switch is visible to the user without manual action.

import * as vscode from 'vscode';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs/promises';

const PLUGIN_ID = 'anthropic.claude-code';
const SETTING_SECTION = 'claudeCode';
const SETTING_KEY = 'claudeProcessWrapper';

function wrapperPath(): string {
  return path.join(os.homedir(), '.moorpost', 'bin', 'claude-wrapper');
}

/** True if the Anthropic Claude Code plugin appears to be installed. */
export function pluginInstalled(): boolean {
  return vscode.extensions.getExtension(PLUGIN_ID) !== undefined;
}

async function wrapperExists(): Promise<boolean> {
  try {
    const stat = await fs.stat(wrapperPath());
    return stat.isFile();
  } catch {
    return false;
  }
}

/**
 * Switch the plugin's claudeProcessWrapper setting to point at the
 * moorpost shim, AND ask the plugin to start a new conversation so the
 * user's panel immediately reflects the new (remote) routing.
 *
 * No-op if the plugin isn't installed or the wrapper script isn't on
 * disk yet (`moorpost bootstrap` writes it; `moorpost install-claude-wrapper`
 * is the manual path).
 */
export async function routePluginToRemote(): Promise<void> {
  if (!pluginInstalled()) return;
  if (!(await wrapperExists())) {
    void vscode.window.showWarningMessage(
      `Moorpost wrapper not installed at ${wrapperPath()}. ` +
        'Run `moorpost install-claude-wrapper` so the Anthropic Claude Code panel can route to remote.',
    );
    return;
  }
  const cfg = vscode.workspace.getConfiguration(SETTING_SECTION);
  await cfg.update(SETTING_KEY, wrapperPath(), vscode.ConfigurationTarget.Global);
  // New conversation forces the panel to start a fresh claude through
  // the wrapper. The previous conversation (if any) keeps running with
  // its old wrapper but the user's next prompt goes through the new path.
  await tryNewConversation();
}

/**
 * Reset the plugin's wrapper setting so future invocations use the
 * default local claude. Triggers a new conversation so the panel
 * visibly transitions back to local.
 */
export async function routePluginToLocal(): Promise<void> {
  if (!pluginInstalled()) return;
  const cfg = vscode.workspace.getConfiguration(SETTING_SECTION);
  // undefined removes our override; plugin falls back to its built-in
  // claude resolution.
  await cfg.update(SETTING_KEY, undefined, vscode.ConfigurationTarget.Global);
  await tryNewConversation();
}

async function tryNewConversation(): Promise<void> {
  // The plugin's command id has been stable across recent versions.
  // Best-effort: silently ignore if the command isn't registered (older
  // plugin version, or plugin disabled).
  try {
    await vscode.commands.executeCommand('claude-vscode.newConversation');
  } catch {
    // ignore
  }
}
