// CLI wrapper — every interaction with the moorpost binary goes through here.
// Returns parsed JSON for commands invoked with `--json`; surfaces stderr on
// non-zero exits as a thrown Error.

import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import * as vscode from 'vscode';

const execFileAsync = promisify(execFile);

/**
 * Resolves the moorpost CLI binary path from settings (with `moorpost` as
 * the default — relies on PATH lookup).
 */
export function cliBinary(): string {
  return vscode.workspace.getConfiguration('moorpost').get<string>('cliPath') || 'moorpost';
}

/**
 * Project status as returned by `moorpost status --json`.
 */
export interface StatusReport {
  project: string;
  provider: string;
  agent: string;
  sync: string;
  mode: string;
  active_side?: 'local' | 'remote';
  vm_id?: string;
  vm_state?: string;
  month_to_date_usd?: number;
  // True if the configured agent has a cached credential (OS keychain).
  // Always emitted by the CLI; defaults to false on older binaries.
  auth_cached?: boolean;
  // Conflict surface — populated only when a sync session is active.
  has_sync_session?: boolean;
  sync_session_id?: string;
  conflicts?: number;
  // The Claude Code session ID bound to this project (optional —
  // present after the first handoff records it). Used by the
  // extension to `claude --resume <id>` when reopening the local
  // Moorpost: Claude terminal.
  agent_session_id?: string;
  // Single-use baton set by `moorpost handoff`. When non-empty, the
  // wrapper will inject `--resume <sid>` into the next plugin-spawned
  // claude — meaning the post-handoff "Migrate this conversation"
  // prompt has something live to consume.
  pending_resume_sid?: string;
  // Per-session routing (Phase 2). Sessions whose SID is in this list
  // are routed to the remote VM by the wrapper, even if active_side is
  // "local". Empty/missing on older builds — handle as `[]`.
  remote_sids?: string[];
}

/**
 * Run a moorpost subcommand from the workspace root and return parsed JSON.
 * Throws on non-zero exit; the error message includes stderr.
 */
export async function runJSON(args: string[], cwd?: string): Promise<unknown> {
  const bin = cliBinary();
  try {
    const { stdout } = await execFileAsync(bin, args, {
      cwd,
      maxBuffer: 1024 * 1024,
      env: process.env,
    });
    return JSON.parse(stdout);
  } catch (err) {
    if (err instanceof Error && 'stderr' in err) {
      const stderr = (err as unknown as { stderr: string }).stderr;
      throw new Error(`moorpost ${args.join(' ')}: ${stderr || err.message}`);
    }
    throw err;
  }
}

/**
 * One entry in the gcloud configurations list, as emitted by
 * `moorpost gcloud configs --json`. Field names are stable per the
 * struct tags on cli/cmd/gcloud_picker.go's gcloudConfig.
 */
export interface GCloudConfig {
  name: string;
  is_active: boolean;
  account: string;
  project: string;
}

/**
 * Fetch the local gcloud configurations the user has on this machine.
 * Returns an empty array if gcloud isn't installed or has no configs;
 * the caller is responsible for surfacing that case.
 */
export async function listGCloudConfigs(): Promise<GCloudConfig[]> {
  try {
    return (await runJSON(['gcloud', 'configs', '--json'])) as GCloudConfig[];
  } catch {
    // gcloud missing, moorpost binary too old, or command failed: degrade
    // to "no configs" so the caller falls back to the terminal picker.
    return [];
  }
}

/**
 * Fetch the project's current status. Returns null if no .moorpost/config.yaml
 * exists in the workspace (status will fail).
 */
export async function getStatus(cwd?: string): Promise<StatusReport | null> {
  try {
    const out = (await runJSON(['status', '--json'], cwd)) as StatusReport;
    return out;
  } catch {
    return null;
  }
}

/**
 * Run a moorpost subcommand interactively in a new VSCode terminal.
 * Used for commands that prompt the user (auth, handoff confirmation, etc.).
 */
export function runInTerminal(args: string[], cwd?: string): vscode.Terminal {
  const bin = cliBinary();
  const term = vscode.window.createTerminal({
    name: `Moorpost: ${args.join(' ')}`,
    cwd,
  });
  term.sendText(`${bin} ${args.join(' ')}`);
  term.show();
  return term;
}

/**
 * Returns the absolute path of the first workspace folder, or undefined.
 * Most moorpost commands operate against the workspace's project config.
 */
export function workspaceRoot(): string | undefined {
  const folders = vscode.workspace.workspaceFolders;
  return folders && folders.length > 0 ? folders[0].uri.fsPath : undefined;
}
