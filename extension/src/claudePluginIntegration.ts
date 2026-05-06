// Drives the Anthropic Claude Code plugin's claudeProcessWrapper setting
// based on moorpost's active_side. When active=remote, the plugin's claude
// invocations are routed through ~/.moorpost/bin/claude-wrapper, which
// SSHes to the VM and runs claude there. When active=local, the wrapper
// is unset so the plugin uses its default claude binary directly.
//
// We deliberately do NOT trigger claude-vscode.newConversation when the
// wrapper changes. An earlier version did, on the theory that a fresh
// conversation would visibly demonstrate the new routing — but it had
// the side effect of wiping the panel's scrollback AND replacing the
// per-conversation CLAUDE_CONFIG_DIR with an empty one, so the wrapper
// rsynced an empty dir to remote and the user saw "no history" after
// every handoff. By leaving the conversation alone, the existing
// CLAUDE_CONFIG_DIR (which contains the resumable session JSONL the
// plugin writes as the conversation progresses) is preserved; the
// next claude invocation through the new wrapper rsyncs that real dir
// to remote, so remote claude resumes the same conversation. Visible
// scrollback is preserved as a bonus.

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

/**
 * True if the plugin's claudeProcessWrapper setting is currently pointing
 * at our wrapper. This is the "we have routed the plugin to remote" signal —
 * used by handoff/return to decide whether the user is in plugin mode (so
 * return should unroute it) without needing to detect plugin-panel focus.
 */
export function pluginCurrentlyRouted(): boolean {
  if (!pluginInstalled()) return false;
  const cfg = vscode.workspace.getConfiguration(SETTING_SECTION);
  return cfg.get<string>(SETTING_KEY) === wrapperPath();
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
 * moorpost shim. The plugin re-reads this on the next claude
 * invocation, so the user's existing conversation is preserved (panel
 * scrollback intact, CLAUDE_CONFIG_DIR carried forward) and the next
 * prompt is routed through the wrapper to remote.
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
}

/**
 * Reset the plugin's wrapper setting so future invocations use the
 * default local claude. Existing conversation is preserved; next
 * prompt runs locally.
 */
export async function routePluginToLocal(): Promise<void> {
  if (!pluginInstalled()) return;
  const cfg = vscode.workspace.getConfiguration(SETTING_SECTION);
  // undefined removes our override; plugin falls back to its built-in
  // claude resolution.
  await cfg.update(SETTING_KEY, undefined, vscode.ConfigurationTarget.Global);
}
