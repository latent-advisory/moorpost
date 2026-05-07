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
import { createHash } from 'node:crypto';

export const MIN_CLI_VERSION = '1.1.5';

const RELEASE_BASE = 'https://github.com/latent-advisory/moorpost/releases/download';

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

/**
 * Map (platform, arch) to the release asset name. Returns null for
 * unsupported combinations (Windows, ia32, etc.) so callers can surface
 * a manual-install message instead of trying to download.
 */
export function resolveAssetName(platform: NodeJS.Platform, arch: string): string | null {
  if (platform !== 'darwin' && platform !== 'linux') return null;
  let normalized: 'amd64' | 'arm64';
  if (arch === 'x64') normalized = 'amd64';
  else if (arch === 'arm64') normalized = 'arm64';
  else return null;
  return `moorpost-${platform}-${normalized}`;
}

/** Pull the SHA-256 hash for a given asset name from a SHA256SUMS body. */
export function parseShaLine(sumsBody: string, asset: string): string | null {
  for (const line of sumsBody.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    // Format: "<64-hex>  <filename>" — `shasum -a 256` output.
    const match = trimmed.match(/^([0-9a-fA-F]{64})\s+(\S+)$/);
    if (match && match[2] === asset) return match[1].toLowerCase();
  }
  return null;
}

export interface DownloadAndVerifyArgs {
  version: string;
  asset: string;
  httpsGet: InstallerDeps['httpsGet'];
}

/**
 * Download the asset binary, fetch SHA256SUMS, verify, and return the
 * binary buffer. Throws on any failure (network non-200, missing SHA
 * entry, hash mismatch).
 */
export async function downloadAndVerify(args: DownloadAndVerifyArgs): Promise<Buffer> {
  const binUrl = `${RELEASE_BASE}/v${args.version}/${args.asset}`;
  const sumsUrl = `${RELEASE_BASE}/v${args.version}/SHA256SUMS`;

  const binResp = await args.httpsGet(binUrl);
  if (binResp.status !== 200) {
    throw new Error(`download ${args.asset}: HTTP ${binResp.status}`);
  }
  const sumsResp = await args.httpsGet(sumsUrl);
  if (sumsResp.status !== 200) {
    throw new Error(`download SHA256SUMS: HTTP ${sumsResp.status}`);
  }

  const expected = parseShaLine(sumsResp.body.toString('utf8'), args.asset);
  if (!expected) {
    throw new Error(`no SHA256SUMS entry for ${args.asset}`);
  }
  const actual = createHash('sha256').update(binResp.body).digest('hex');
  if (actual !== expected) {
    throw new Error(`checksum mismatch for ${args.asset}: expected ${expected}, got ${actual}`);
  }
  return binResp.body;
}

export async function ensureCliInstalled(deps: InstallerDeps): Promise<void> {
  const installed = readInstalledVersion(deps);
  if (installed && compareVersions(installed, MIN_CLI_VERSION) >= 0) {
    return; // happy path — nothing to do.
  }
  // Subsequent tasks fill in the install logic.
  void vscode; // keep import for later steps.
}
