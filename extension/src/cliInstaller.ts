// Auto-installer for the moorpost CLI.
//
// On extension activation, ensureCliInstalled() probes the configured
// CLI binary for its version. If absent or older than MIN_CLI_VERSION,
// it silently downloads the matching release asset from GitHub,
// verifies SHA-256 against the release's SHA256SUMS, installs it to
// ~/.local/bin/moorpost, and writes the moorpost.cliPath setting if
// ~/.local/bin isn't on the inherited PATH.
//
// Node I/O (os/fs/https/child_process) is dependency-injected via the
// `deps` argument so unit tests can drive the orchestrator without
// touching the network or the real filesystem. The `vscode` API surface
// (workspace.getConfiguration, window.withProgress, showInformationMessage,
// showErrorMessage, env.openExternal) is intentionally NOT in `deps`;
// it is mocked at module-load time via extension/test/vscode-shim.mjs,
// which the test loader registers before this file is imported. Both
// boundaries are intentional design choices.

import * as path from 'node:path';
import { createHash } from 'node:crypto';
import * as vscode from 'vscode';

export const MIN_CLI_VERSION = '1.1.12';

const RELEASE_BASE = 'https://github.com/latent-advisory/moorpost/releases/download';

export interface SpawnResult {
  status: number | null;
  stdout: string;
  stderr: string;
}

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
  // Optional logger; in production points at the "Moorpost" Output channel
  // so users can diagnose install hangs. Tests pass a no-op or undefined.
  log?: (msg: string) => void;
}

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

export function compareVersions(a: string, b: string): number {
  const pa = a.split('.').map((n) => parseInt(n, 10));
  const pb = b.split('.').map((n) => parseInt(n, 10));
  for (let i = 0; i < 3; i++) {
    if ((pa[i] ?? 0) < (pb[i] ?? 0)) return -1;
    if ((pa[i] ?? 0) > (pb[i] ?? 0)) return 1;
  }
  return 0;
}

export function resolveAssetName(platform: NodeJS.Platform, arch: string): string | null {
  if (platform !== 'darwin' && platform !== 'linux') return null;
  let normalized: 'amd64' | 'arm64';
  if (arch === 'x64') normalized = 'amd64';
  else if (arch === 'arm64') normalized = 'arm64';
  else return null;
  return `moorpost-${platform}-${normalized}`;
}

export function parseShaLine(sumsBody: string, asset: string): string | null {
  for (const line of sumsBody.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
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

/**
 * Write the verified buffer to a temp file, ensure ~/.local/bin exists,
 * move into place, chmod 0755, and update the moorpost.cliPath setting
 * if ~/.local/bin isn't on the inherited PATH.
 */
export async function installBinary(deps: InstallerDeps, binary: Buffer): Promise<string> {
  const tmpPath = path.join(deps.os.tmpdir(), 'moorpost.download');
  const targetDir = path.join(deps.os.homedir(), '.local', 'bin');
  const targetPath = path.join(targetDir, 'moorpost');

  await deps.fs.writeFile(tmpPath, binary);
  await deps.fs.mkdir(targetDir, { recursive: true });
  try {
    await deps.fs.rename(tmpPath, targetPath);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'EXDEV') {
      // Cross-device rename — fall back to copy + unlink.
      await deps.fs.copyFile(tmpPath, targetPath);
      await deps.fs.unlink(tmpPath);
    } else {
      throw err;
    }
  }
  await deps.fs.chmod(targetPath, 0o755);

  if (!isOnPath(targetDir, deps.pathEnv)) {
    await vscode.workspace
      .getConfiguration('moorpost')
      .update('cliPath', targetPath, vscode.ConfigurationTarget.Global);
  }
  return targetPath;
}

function isOnPath(dir: string, pathEnv: string): boolean {
  // POSIX-only — installer is darwin/linux only.
  return pathEnv.split(':').some((entry) => entry === dir);
}

export async function ensureCliInstalled(deps: InstallerDeps): Promise<void> {
  const log = deps.log ?? (() => {});
  log(`ensureCliInstalled: probing ${deps.cliPathSetting || 'moorpost'} --version`);
  const installed = readInstalledVersion(deps);
  if (installed && compareVersions(installed, MIN_CLI_VERSION) >= 0) {
    log(`ensureCliInstalled: installed v${installed} >= min v${MIN_CLI_VERSION}; skipping install`);
    return;
  }
  log(`ensureCliInstalled: installed=${installed ?? 'none'}, min=${MIN_CLI_VERSION}; install needed`);

  const asset = resolveAssetName(deps.platform, deps.arch);
  if (!asset) {
    const msg =
      deps.platform === 'win32'
        ? 'Moorpost CLI: Windows is not supported. Use WSL or install manually from the release page.'
        : `Moorpost CLI: no automated install for ${deps.platform}/${deps.arch}. Install manually from the release page.`;
    log(`ensureCliInstalled: unsupported platform ${deps.platform}/${deps.arch}`);
    showFailureToast(msg);
    return;
  }
  log(`ensureCliInstalled: target asset ${asset} for v${MIN_CLI_VERSION}`);

  try {
    await vscode.window.withProgress(
      {
        location: vscode.ProgressLocation.Notification,
        title: `Installing Moorpost CLI v${MIN_CLI_VERSION}…`,
        cancellable: false,
      },
      async (progress) => {
        progress.report({ message: 'downloading…' });
        log('install: downloading binary + SHA256SUMS');
        const binary = await downloadAndVerify({
          version: MIN_CLI_VERSION,
          asset,
          httpsGet: deps.httpsGet,
        });
        log(`install: download ok (${binary.length} bytes); SHA verified`);
        progress.report({ message: 'installing…' });
        const installedPath = await installBinary(deps, binary);
        log(`install: wrote binary to ${installedPath}`);
        progress.report({ message: 'verifying…' });
        const post = readInstalledVersion({ ...deps, cliPathSetting: installedPath });
        if (!post) {
          throw new Error(`installed binary at ${installedPath} did not respond to --version`);
        }
        log(`install: post-install version check → v${post}`);
        // Fire-and-forget: showInformationMessage's Thenable only resolves
        // when the user dismisses the toast. Awaiting it would hang
        // activation indefinitely. The toast still appears; we just don't
        // care when the user closes it.
        void vscode.window.showInformationMessage(`Moorpost CLI installed (v${post}).`);
      },
    );
  } catch (err) {
    const reason = err instanceof Error ? err.message : String(err);
    log(`install: failed — ${reason}`);
    showFailureToast(`Moorpost CLI auto-install failed: ${reason}. Install manually from the release page.`);
  }
}

// Fire-and-forget. The button-click handler runs off the chained .then
// so callers don't have to await user interaction.
function showFailureToast(message: string): void {
  void vscode.window.showErrorMessage(message, 'Open release page').then((choice) => {
    if (choice === 'Open release page') {
      void vscode.env.openExternal(
        vscode.Uri.parse(`https://github.com/latent-advisory/moorpost/releases/tag/v${MIN_CLI_VERSION}`),
      );
    }
  });
}
