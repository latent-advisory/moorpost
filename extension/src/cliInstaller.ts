// Auto-installer for the moorpost CLI.
//
// On extension activation, ensureCliInstalled() probes the configured
// CLI binary for its version. If absent or older than MIN_CLI_VERSION,
// it silently downloads the matching release asset from GitHub,
// verifies SHA-256 against the release's SHA256SUMS, installs it to
// ~/.local/bin/moorpost, and writes the moorpost.cliPath setting if
// ~/.local/bin isn't on the inherited PATH.
//
// All Node I/O is dependency-injected so unit tests can drive the
// orchestrator without touching the network or the real filesystem.

import * as vscode from 'vscode';

export const MIN_CLI_VERSION = '1.1.5';

export interface SpawnResult {
  status: number | null;
  stdout: string;
  stderr: string;
}

// The subset of `node:fs/promises` the installer uses. Splitting it out
// lets tests provide a hand-rolled mock without faking the whole module.
export interface InstallerFs {
  mkdir(path: string, opts: { recursive: boolean }): Promise<void>;
  writeFile(path: string, data: Buffer): Promise<void>;
  rename(from: string, to: string): Promise<void>;
  copyFile(from: string, to: string): Promise<void>;
  unlink(path: string): Promise<void>;
  chmod(path: string, mode: number): Promise<void>;
}

export interface InstallerDeps {
  spawnSync: (cmd: string, args: string[], opts?: object) => SpawnResult;
  httpsGet: (url: string) => Promise<{ status: number; body: Buffer }>;
  fs: InstallerFs;
  os: { tmpdir: () => string; homedir: () => string };
  platform: NodeJS.Platform;
  arch: string;
  pathEnv: string;
  cliPathSetting: string | undefined;
}

/**
 * Probe the CLI for `--version` and parse the leading vX.Y.Z token.
 * Returns null if the binary isn't on PATH (ENOENT) or output is unparseable.
 */
export function readInstalledVersion(deps: InstallerDeps): string | null {
  const bin = deps.cliPathSetting || 'moorpost';
  let result: SpawnResult;
  try {
    result = deps.spawnSync(bin, ['--version'], { encoding: 'utf8' });
  } catch {
    return null;
  }
  if (result.status !== 0) return null;
  const match = result.stdout.match(/v?(\d+\.\d+\.\d+)/);
  return match ? match[1] : null;
}

/** Compare semver-shaped strings. Returns -1, 0, 1. */
export function compareVersions(a: string, b: string): number {
  const pa = a.split('.').map((n) => parseInt(n, 10));
  const pb = b.split('.').map((n) => parseInt(n, 10));
  for (let i = 0; i < 3; i++) {
    if ((pa[i] ?? 0) < (pb[i] ?? 0)) return -1;
    if ((pa[i] ?? 0) > (pb[i] ?? 0)) return 1;
  }
  return 0;
}

export async function ensureCliInstalled(deps: InstallerDeps): Promise<void> {
  const installed = readInstalledVersion(deps);
  if (installed && compareVersions(installed, MIN_CLI_VERSION) >= 0) {
    return; // happy path — nothing to do.
  }
  // Subsequent tasks fill in the install logic.
  void vscode; // keep import for later steps.
}
