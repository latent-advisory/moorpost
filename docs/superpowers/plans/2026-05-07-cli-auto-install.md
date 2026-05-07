# CLI Auto-Install Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the Moorpost VSCode extension activates and finds no compatible `moorpost` CLI, silently download, verify, and install the right binary so first-time Marketplace users get a working setup with zero manual steps.

**Architecture:** New `extension/src/cliInstaller.ts` module exports `MIN_CLI_VERSION` and `ensureCliInstalled(deps?)`. Activation in `extension.ts` awaits this before the first-run nudge fires. Dependencies (`https`, `fs/promises`, `child_process`, `os`) are injected so the module is unit-testable through the existing Node-native test runner with the loader-shim pattern. Failures surface as a non-modal toast with a "Open release page" button.

**Tech Stack:** TypeScript (Node 18+), VSCode Extension API, Node's built-in `https` / `crypto` / `fs` modules, native `node --test` runner with type-stripping.

**Spec:** [`docs/superpowers/specs/2026-05-07-cli-auto-install-design.md`](../specs/2026-05-07-cli-auto-install-design.md)

---

## File Structure

**New files:**

- `extension/src/cliInstaller.ts` — module under test. Pure functions (asset resolution, SHA-line parsing) at top; I/O wrappers next; `ensureCliInstalled()` orchestrator at bottom.
- `extension/src/cliInstaller.test.mts` — unit tests, sibling to source per existing convention (`treeView.test.mts`).
- `extension/test/cliInstaller-fixtures.mjs` — small fixture helpers (mock `https.get` stream, mock `SHA256SUMS` body) shared between tests.

**Modified files:**

- `extension/src/extension.ts` — wire `await ensureCliInstalled()` into `activate()`.
- `extension/test/vscode-shim.mjs` — extend with the new vscode surface the installer touches (`window.withProgress`, `ConfigurationTarget`, `env.openExternal`, `workspace.getConfiguration().update`).
- `extension/package.json` — bump `version` 1.1.4 → 1.1.5.
- `extension/README.md` — install section rewritten; GCP-required note added at top.
- `README.md` (repo root) — install section updated to mention auto-install.
- `PLUGIN.md` — new short subsection under §10 describing auto-install.

**Why this layout:** the installer is self-contained — no imports from existing `cli.ts` or `commands/`. Keeping `MIN_CLI_VERSION` and the install logic in one module means future bumps touch one file. Splitting tests into a sibling `.test.mts` matches the precedent set by `treeView.test.mts`.

---

## Task 1: Scaffold cliInstaller module with version-check happy paths

**Files:**

- Create: `extension/src/cliInstaller.ts`
- Create: `extension/src/cliInstaller.test.mts`
- Modify: `extension/test/vscode-shim.mjs`

This task establishes the module skeleton, the dependency-injection shape, and the simplest behavior: when `moorpost --version` reports a version ≥ `MIN_CLI_VERSION`, do nothing.

- [ ] **Step 1: Extend the vscode shim with the surface we'll use later**

The shim today only covers tree-view needs. The installer will touch `window.withProgress`, `ConfigurationTarget`, `env.openExternal`, and `workspace.getConfiguration().update`. Add them now so subsequent steps can rely on them.

Edit `extension/test/vscode-shim.mjs`. Replace the file with:

```js
// Minimal vscode API shim for unit tests.
// Only the surface the extension's unit tests touch is implemented.

export class ThemeIcon {
  constructor(id, color) {
    this.id = id;
    if (color !== undefined) this.color = color;
  }
}

export const TreeItemCollapsibleState = Object.freeze({
  None: 0,
  Collapsed: 1,
  Expanded: 2,
});

export const ConfigurationTarget = Object.freeze({
  Global: 1,
  Workspace: 2,
  WorkspaceFolder: 3,
});

export const ProgressLocation = Object.freeze({
  SourceControl: 1,
  Window: 10,
  Notification: 15,
});

export class TreeItem {
  constructor(label, collapsibleState) {
    this.label = label;
    this.collapsibleState = collapsibleState ?? TreeItemCollapsibleState.None;
  }
}

export class EventEmitter {
  constructor() {
    this._listeners = [];
    this.event = (listener) => {
      this._listeners.push(listener);
      return { dispose: () => {} };
    };
  }
  fire(arg) {
    for (const l of this._listeners) l(arg);
  }
  dispose() {
    this._listeners = [];
  }
}

// Tracks the most-recent calls so tests can assert on them.
export const __callLog = {
  showInformationMessage: [],
  showErrorMessage: [],
  withProgress: [],
  openExternal: [],
  configUpdate: [],
};

export function __resetCallLog() {
  for (const k of Object.keys(__callLog)) __callLog[k] = [];
}

export const window = {
  showInformationMessage: (...args) => {
    __callLog.showInformationMessage.push(args);
    return Promise.resolve(undefined);
  },
  showErrorMessage: (...args) => {
    __callLog.showErrorMessage.push(args);
    // Default: pretend the user dismissed (no button click).
    return Promise.resolve(undefined);
  },
  withProgress: async (options, body) => {
    __callLog.withProgress.push({ options });
    // Run the body with a no-op progress reporter and a never-cancelling token.
    const progress = { report: () => {} };
    const token = { isCancellationRequested: false, onCancellationRequested: () => ({ dispose: () => {} }) };
    return body(progress, token);
  },
};

export const env = {
  openExternal: (uri) => {
    __callLog.openExternal.push(uri);
    return Promise.resolve(true);
  },
};

// Mutable config store: tests can pre-populate values via __setConfig().
const __configValues = new Map();
export function __setConfig(section, key, value) {
  __configValues.set(`${section}.${key}`, value);
}
export function __resetConfig() {
  __configValues.clear();
}

export const workspace = {
  workspaceFolders: undefined,
  getConfiguration: (section) => ({
    get: (key) => __configValues.get(`${section}.${key}`),
    update: (key, value, target) => {
      __callLog.configUpdate.push({ section, key, value, target });
      __configValues.set(`${section}.${key}`, value);
      return Promise.resolve();
    },
  }),
};

export const Uri = {
  file: (p) => ({ fsPath: p, scheme: 'file' }),
  parse: (s) => ({ toString: () => s, scheme: 'https' }),
};

export default {
  ThemeIcon,
  TreeItemCollapsibleState,
  ConfigurationTarget,
  ProgressLocation,
  TreeItem,
  EventEmitter,
  window,
  env,
  workspace,
  Uri,
};
```

- [ ] **Step 2: Write the failing test for version-skip behavior**

Create `extension/src/cliInstaller.test.mts`:

```ts
// Unit tests for cliInstaller.ts.
// vscode is shimmed by ../test/loader.mjs; node modules (https, fs, child_process)
// are injected via the deps argument to ensureCliInstalled().

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { ensureCliInstalled, MIN_CLI_VERSION, type InstallerDeps } from './cliInstaller.js';
// @ts-expect-error -- shim exposes test-only mutators
import { __callLog, __resetCallLog, __resetConfig } from '../test/vscode-shim.mjs';

interface SpawnResult {
  status: number | null;
  stdout: string;
  stderr: string;
}

function makeDeps(over: Partial<InstallerDeps> = {}): InstallerDeps {
  // Defaults all throw — every test must override the deps it actually exercises.
  const fail = (name: string) => () => {
    throw new Error(`unexpected dep call: ${name}`);
  };
  return {
    spawnSync: over.spawnSync ?? fail('spawnSync'),
    httpsGet: over.httpsGet ?? fail('httpsGet'),
    fs: over.fs ?? (fail('fs') as unknown as InstallerDeps['fs']),
    os: over.os ?? { tmpdir: () => '/tmp', homedir: () => '/home/test' },
    platform: over.platform ?? 'darwin',
    arch: over.arch ?? 'arm64',
    pathEnv: over.pathEnv ?? '/usr/bin:/bin',
    cliPathSetting: over.cliPathSetting ?? undefined,
  };
}

describe('ensureCliInstalled', () => {
  beforeEach(() => {
    __resetCallLog();
    __resetConfig();
  });

  it('does nothing when CLI version meets MIN_CLI_VERSION', async () => {
    const spawnCalls: Array<{ cmd: string; args: string[] }> = [];
    const deps = makeDeps({
      spawnSync: (cmd, args) => {
        spawnCalls.push({ cmd, args });
        return { status: 0, stdout: `v${MIN_CLI_VERSION} (commit abc, built …)\n`, stderr: '' };
      },
    });
    await ensureCliInstalled(deps);
    assert.equal(spawnCalls.length, 1);
    assert.equal(spawnCalls[0].cmd, 'moorpost');
    assert.deepEqual(spawnCalls[0].args, ['--version']);
    assert.equal(__callLog.withProgress.length, 0, 'no install attempted');
    assert.equal(__callLog.showInformationMessage.length, 0);
  });

  it('does nothing when CLI version is above MIN_CLI_VERSION', async () => {
    const deps = makeDeps({
      spawnSync: () => ({ status: 0, stdout: 'v999.99.99\n', stderr: '' }),
    });
    await ensureCliInstalled(deps);
    assert.equal(__callLog.withProgress.length, 0);
  });
});
```

- [ ] **Step 3: Run the test and verify it fails**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost/extension" && npm test -- --test-name-pattern='ensureCliInstalled'
```

Expected: FAIL with `Cannot find module './cliInstaller.js'` or similar.

- [ ] **Step 4: Implement the minimal module to make these tests pass**

Create `extension/src/cliInstaller.ts`:

```ts
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
```

- [ ] **Step 5: Run the tests and verify they pass**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost/extension" && npm test -- --test-name-pattern='ensureCliInstalled'
```

Expected: 2 passing tests.

- [ ] **Step 6: Commit**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add extension/src/cliInstaller.ts extension/src/cliInstaller.test.mts extension/test/vscode-shim.mjs
git commit -m "feat(extension): scaffold cliInstaller with version-skip happy path

ensureCliInstalled() probes the configured binary's --version and
no-ops when it meets MIN_CLI_VERSION. Subsequent commits add download,
verification, and install. Tests use the existing loader-shim pattern;
deps (spawn, https, fs, os) are injected so unit tests stay hermetic."
```

---

## Task 2: Asset resolution + download with SHA-256 verification

**Files:**

- Modify: `extension/src/cliInstaller.ts`
- Modify: `extension/src/cliInstaller.test.mts`

This task adds two pure helpers (`resolveAssetName`, `parseShaLine`) and the `downloadAndVerify` orchestrator that fetches the binary, fetches `SHA256SUMS`, and confirms the hash. No filesystem writes yet — just download + verify.

- [ ] **Step 1: Write failing tests for asset resolution**

Append to `extension/src/cliInstaller.test.mts`:

```ts
import { resolveAssetName, parseShaLine, downloadAndVerify } from './cliInstaller.js';
import { createHash } from 'node:crypto';

describe('resolveAssetName', () => {
  it('maps darwin arm64 → moorpost-darwin-arm64', () => {
    assert.equal(resolveAssetName('darwin', 'arm64'), 'moorpost-darwin-arm64');
  });
  it('maps darwin x64 → moorpost-darwin-amd64', () => {
    assert.equal(resolveAssetName('darwin', 'x64'), 'moorpost-darwin-amd64');
  });
  it('maps linux arm64 → moorpost-linux-arm64', () => {
    assert.equal(resolveAssetName('linux', 'arm64'), 'moorpost-linux-arm64');
  });
  it('maps linux x64 → moorpost-linux-amd64', () => {
    assert.equal(resolveAssetName('linux', 'x64'), 'moorpost-linux-amd64');
  });
  it('returns null for win32', () => {
    assert.equal(resolveAssetName('win32', 'x64'), null);
  });
  it('returns null for darwin ia32', () => {
    assert.equal(resolveAssetName('darwin', 'ia32'), null);
  });
});

describe('parseShaLine', () => {
  it('extracts the hash for a matching asset name', () => {
    const sums = [
      '1111111111111111111111111111111111111111111111111111111111111111  moorpost-darwin-amd64',
      '2222222222222222222222222222222222222222222222222222222222222222  moorpost-darwin-arm64',
      '3333333333333333333333333333333333333333333333333333333333333333  moorpost-linux-amd64',
    ].join('\n');
    assert.equal(
      parseShaLine(sums, 'moorpost-darwin-arm64'),
      '2222222222222222222222222222222222222222222222222222222222222222',
    );
  });
  it('returns null when asset is missing', () => {
    assert.equal(parseShaLine('aaa  other-asset', 'moorpost-darwin-arm64'), null);
  });
});

describe('downloadAndVerify', () => {
  it('downloads, computes hash, and returns the buffer when SHA matches', async () => {
    const binary = Buffer.from('fake-binary-bytes');
    const expected = createHash('sha256').update(binary).digest('hex');
    const sums = `${expected}  moorpost-darwin-arm64\n`;
    const calls: string[] = [];
    const httpsGet = async (url: string) => {
      calls.push(url);
      if (url.endsWith('moorpost-darwin-arm64')) {
        return { status: 200, body: binary };
      }
      if (url.endsWith('SHA256SUMS')) {
        return { status: 200, body: Buffer.from(sums, 'utf8') };
      }
      throw new Error(`unexpected url: ${url}`);
    };
    const result = await downloadAndVerify({
      version: '1.1.5',
      asset: 'moorpost-darwin-arm64',
      httpsGet,
    });
    assert.equal(result.length, binary.length);
    assert.deepEqual(Array.from(result), Array.from(binary));
    assert.equal(calls.length, 2);
    assert.ok(calls[0].includes('v1.1.5/moorpost-darwin-arm64'));
    assert.ok(calls[1].endsWith('v1.1.5/SHA256SUMS'));
  });

  it('rejects on SHA mismatch', async () => {
    const binary = Buffer.from('fake');
    const wrongSum = '0'.repeat(64);
    const sums = `${wrongSum}  moorpost-darwin-arm64\n`;
    const httpsGet = async (url: string) => {
      if (url.endsWith('moorpost-darwin-arm64')) return { status: 200, body: binary };
      return { status: 200, body: Buffer.from(sums, 'utf8') };
    };
    await assert.rejects(
      downloadAndVerify({ version: '1.1.5', asset: 'moorpost-darwin-arm64', httpsGet }),
      /checksum mismatch/i,
    );
  });

  it('rejects when SHA256SUMS lacks an entry for the asset', async () => {
    const binary = Buffer.from('fake');
    const httpsGet = async (url: string) => {
      if (url.endsWith('moorpost-darwin-arm64')) return { status: 200, body: binary };
      return { status: 200, body: Buffer.from('aa  some-other-file\n', 'utf8') };
    };
    await assert.rejects(
      downloadAndVerify({ version: '1.1.5', asset: 'moorpost-darwin-arm64', httpsGet }),
      /no SHA256SUMS entry/i,
    );
  });
});
```

- [ ] **Step 2: Run tests and verify they fail**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost/extension" && npm test
```

Expected: failures referencing `resolveAssetName`, `parseShaLine`, `downloadAndVerify` not exported.

- [ ] **Step 3: Implement the helpers and orchestrator**

In `extension/src/cliInstaller.ts`, add the following exports (place them above `ensureCliInstalled`):

```ts
import { createHash } from 'node:crypto';

const RELEASE_BASE = 'https://github.com/latent-advisory/moorpost/releases/download';

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
```

- [ ] **Step 4: Run tests and verify they pass**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost/extension" && npm test
```

Expected: all tests pass (the 2 from Task 1 + 9 added in this task).

- [ ] **Step 5: Commit**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add extension/src/cliInstaller.ts extension/src/cliInstaller.test.mts
git commit -m "feat(extension): asset resolution + SHA-verified download

resolveAssetName maps platform/arch to the GitHub release asset.
parseShaLine pulls the per-file hash from a SHA256SUMS body.
downloadAndVerify fetches both, checks the hash, returns the buffer."
```

---

## Task 3: Filesystem install + PATH plumbing

**Files:**

- Modify: `extension/src/cliInstaller.ts`
- Modify: `extension/src/cliInstaller.test.mts`

This task adds `installBinary(deps, version, asset)` — writes the verified buffer to `~/.local/bin/moorpost`, chmods it, and writes `moorpost.cliPath` if `~/.local/bin` isn't on PATH. It also wires `ensureCliInstalled` to actually call download + install on the unhappy path, completing the orchestrator.

- [ ] **Step 1: Write failing tests for the install path**

Append to `extension/src/cliInstaller.test.mts`:

```ts
import { installBinary, type InstallerFs } from './cliInstaller.js';

class FakeFs implements InstallerFs {
  public ops: Array<{ kind: string; args: unknown[] }> = [];
  public renameError: NodeJS.ErrnoException | null = null;
  async mkdir(path: string, opts: { recursive: boolean }): Promise<void> {
    this.ops.push({ kind: 'mkdir', args: [path, opts] });
  }
  async writeFile(path: string, data: Buffer): Promise<void> {
    this.ops.push({ kind: 'writeFile', args: [path, data.length] });
  }
  async rename(from: string, to: string): Promise<void> {
    if (this.renameError) {
      const e = this.renameError;
      this.renameError = null;
      throw e;
    }
    this.ops.push({ kind: 'rename', args: [from, to] });
  }
  async copyFile(from: string, to: string): Promise<void> {
    this.ops.push({ kind: 'copyFile', args: [from, to] });
  }
  async unlink(path: string): Promise<void> {
    this.ops.push({ kind: 'unlink', args: [path] });
  }
  async chmod(path: string, mode: number): Promise<void> {
    this.ops.push({ kind: 'chmod', args: [path, mode] });
  }
}

describe('installBinary', () => {
  beforeEach(() => {
    __resetCallLog();
    __resetConfig();
  });

  it('writes the binary to ~/.local/bin/moorpost with mode 0755', async () => {
    const fs = new FakeFs();
    const buf = Buffer.from('binary');
    await installBinary({
      ...makeDeps({ fs, os: { tmpdir: () => '/tmp', homedir: () => '/u/landy' } }),
    }, buf);

    const kinds = fs.ops.map((o) => o.kind);
    assert.deepEqual(kinds, ['writeFile', 'mkdir', 'rename', 'chmod']);
    assert.equal(fs.ops[0].args[0], '/tmp/moorpost.download');
    assert.equal(fs.ops[1].args[0], '/u/landy/.local/bin');
    assert.deepEqual(fs.ops[1].args[1], { recursive: true });
    assert.equal(fs.ops[2].args[0], '/tmp/moorpost.download');
    assert.equal(fs.ops[2].args[1], '/u/landy/.local/bin/moorpost');
    assert.equal(fs.ops[3].args[0], '/u/landy/.local/bin/moorpost');
    assert.equal(fs.ops[3].args[1], 0o755);
  });

  it('falls back to copyFile+unlink on EXDEV from rename', async () => {
    const fs = new FakeFs();
    fs.renameError = Object.assign(new Error('cross-device'), { code: 'EXDEV' }) as NodeJS.ErrnoException;
    const buf = Buffer.from('binary');
    await installBinary({
      ...makeDeps({ fs, os: { tmpdir: () => '/tmp', homedir: () => '/u/landy' } }),
    }, buf);
    const kinds = fs.ops.map((o) => o.kind);
    assert.deepEqual(kinds, ['writeFile', 'mkdir', 'copyFile', 'unlink', 'chmod']);
  });

  it('writes moorpost.cliPath when ~/.local/bin is not on PATH', async () => {
    const fs = new FakeFs();
    await installBinary({
      ...makeDeps({
        fs,
        os: { tmpdir: () => '/tmp', homedir: () => '/u/landy' },
        pathEnv: '/usr/bin:/bin',
      }),
    }, Buffer.from('x'));
    assert.equal(__callLog.configUpdate.length, 1);
    assert.deepEqual(__callLog.configUpdate[0], {
      section: 'moorpost',
      key: 'cliPath',
      value: '/u/landy/.local/bin/moorpost',
      target: 1, // ConfigurationTarget.Global
    });
  });

  it('does not touch moorpost.cliPath when ~/.local/bin is already on PATH', async () => {
    const fs = new FakeFs();
    await installBinary({
      ...makeDeps({
        fs,
        os: { tmpdir: () => '/tmp', homedir: () => '/u/landy' },
        pathEnv: '/usr/bin:/u/landy/.local/bin:/bin',
      }),
    }, Buffer.from('x'));
    assert.equal(__callLog.configUpdate.length, 0);
  });
});

describe('ensureCliInstalled — full install path', () => {
  beforeEach(() => {
    __resetCallLog();
    __resetConfig();
  });

  it('downloads, verifies, installs, and re-confirms when CLI missing', async () => {
    const binary = Buffer.from('binary');
    const expected = createHash('sha256').update(binary).digest('hex');
    const sums = `${expected}  moorpost-darwin-arm64\n`;
    const fs = new FakeFs();
    let spawnCount = 0;
    const deps = makeDeps({
      fs,
      os: { tmpdir: () => '/tmp', homedir: () => '/u/landy' },
      platform: 'darwin',
      arch: 'arm64',
      pathEnv: '/usr/bin:/u/landy/.local/bin',
      spawnSync: () => {
        spawnCount += 1;
        if (spawnCount === 1) return { status: 1, stdout: '', stderr: 'not found' };
        return { status: 0, stdout: `v${MIN_CLI_VERSION}\n`, stderr: '' };
      },
      httpsGet: async (url) => {
        if (url.endsWith('moorpost-darwin-arm64')) return { status: 200, body: binary };
        return { status: 200, body: Buffer.from(sums, 'utf8') };
      },
    });

    await ensureCliInstalled(deps);

    assert.equal(__callLog.withProgress.length, 1, 'progress notification fired');
    const kinds = fs.ops.map((o) => o.kind);
    assert.ok(kinds.includes('rename') || kinds.includes('copyFile'), 'binary was installed');
    assert.equal(spawnCount, 2, 'pre-check + post-check');
    assert.equal(__callLog.showInformationMessage.length, 1, 'success toast shown');
  });

  it('shows error toast and never writes binary on unsupported platform', async () => {
    const fs = new FakeFs();
    const deps = makeDeps({
      fs,
      platform: 'win32',
      arch: 'x64',
      spawnSync: () => ({ status: 1, stdout: '', stderr: 'not found' }),
    });
    await ensureCliInstalled(deps);
    assert.equal(fs.ops.length, 0);
    assert.equal(__callLog.showErrorMessage.length, 1);
    assert.match(__callLog.showErrorMessage[0][0], /Windows is not supported/i);
  });

  it('shows error toast on SHA mismatch and writes nothing to ~/.local/bin', async () => {
    const fs = new FakeFs();
    const deps = makeDeps({
      fs,
      os: { tmpdir: () => '/tmp', homedir: () => '/u/landy' },
      platform: 'darwin',
      arch: 'arm64',
      spawnSync: () => ({ status: 1, stdout: '', stderr: 'not found' }),
      httpsGet: async (url) => {
        if (url.endsWith('moorpost-darwin-arm64')) return { status: 200, body: Buffer.from('x') };
        return { status: 200, body: Buffer.from(`${'0'.repeat(64)}  moorpost-darwin-arm64\n`) };
      },
    });
    await ensureCliInstalled(deps);
    // Temp file is written, but neither rename nor copyFile should fire.
    const kinds = fs.ops.map((o) => o.kind);
    assert.ok(!kinds.includes('rename'));
    assert.ok(!kinds.includes('copyFile'));
    assert.equal(__callLog.showErrorMessage.length, 1);
    assert.match(__callLog.showErrorMessage[0][0], /checksum mismatch|auto-install failed/i);
  });
});
```

- [ ] **Step 2: Run tests and verify they fail**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost/extension" && npm test
```

Expected: failures referencing `installBinary` not exported and the new `ensureCliInstalled` paths.

- [ ] **Step 3: Implement installBinary and finish ensureCliInstalled**

Replace `installBinary` (new) and the body of `ensureCliInstalled` in `extension/src/cliInstaller.ts`. The full file should now read:

```ts
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

import * as path from 'node:path';
import { createHash } from 'node:crypto';
import * as vscode from 'vscode';

export const MIN_CLI_VERSION = '1.1.5';

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
  const installed = readInstalledVersion(deps);
  if (installed && compareVersions(installed, MIN_CLI_VERSION) >= 0) {
    return;
  }

  const asset = resolveAssetName(deps.platform, deps.arch);
  if (!asset) {
    const msg =
      deps.platform === 'win32'
        ? 'Moorpost CLI: Windows is not supported. Use WSL or install manually from the release page.'
        : `Moorpost CLI: no automated install for ${deps.platform}/${deps.arch}. Install manually from the release page.`;
    await showFailureToast(msg);
    return;
  }

  try {
    await vscode.window.withProgress(
      {
        location: vscode.ProgressLocation.Notification,
        title: `Installing Moorpost CLI v${MIN_CLI_VERSION}…`,
        cancellable: false,
      },
      async () => {
        const binary = await downloadAndVerify({
          version: MIN_CLI_VERSION,
          asset,
          httpsGet: deps.httpsGet,
        });
        const installedPath = await installBinary(deps, binary);
        const post = readInstalledVersion({ ...deps, cliPathSetting: installedPath });
        if (!post) {
          throw new Error(`installed binary at ${installedPath} did not respond to --version`);
        }
        await vscode.window.showInformationMessage(`Moorpost CLI installed (v${post}).`);
      },
    );
  } catch (err) {
    const reason = err instanceof Error ? err.message : String(err);
    await showFailureToast(`Moorpost CLI auto-install failed: ${reason}. Install manually from the release page.`);
  }
}

async function showFailureToast(message: string): Promise<void> {
  const choice = await vscode.window.showErrorMessage(message, 'Open release page');
  if (choice === 'Open release page') {
    await vscode.env.openExternal(
      vscode.Uri.parse(`https://github.com/latent-advisory/moorpost/releases/tag/v${MIN_CLI_VERSION}`),
    );
  }
}
```

- [ ] **Step 4: Run all tests and verify they pass**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost/extension" && npm test
```

Expected: all tests pass (the 11 from prior tasks + 7 added in this task).

- [ ] **Step 5: Commit**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add extension/src/cliInstaller.ts extension/src/cliInstaller.test.mts
git commit -m "feat(extension): finish auto-installer — install + PATH plumbing

installBinary writes the verified buffer to ~/.local/bin/moorpost,
chmods 0755, falls back to copyFile+unlink on EXDEV, and writes the
moorpost.cliPath setting when ~/.local/bin isn't on PATH.

ensureCliInstalled now drives the full unhappy path: resolve asset →
download → verify → install → re-confirm. Failures surface as a
non-modal toast with an 'Open release page' button. Unsupported
platforms (Windows) skip the download entirely."
```

---

## Task 4: Wire ensureCliInstalled into extension activation

**Files:**

- Modify: `extension/src/extension.ts`

The installer module is complete; this task wires it into activation. We instantiate the real `InstallerDeps` from Node built-ins here (not in `cliInstaller.ts`) so the module stays unit-testable.

- [ ] **Step 1: Edit extension/src/extension.ts**

Replace the file contents with:

```ts
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
```

- [ ] **Step 2: Build the extension to confirm types compile**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost" && make extension-build
```

Expected: `dist/extension.js` builds without TypeScript or esbuild errors.

- [ ] **Step 3: Run tests once more to ensure nothing regressed**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost/extension" && npm test
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add extension/src/extension.ts
git commit -m "feat(extension): wire ensureCliInstalled into activation

activate() now awaits the auto-installer before any other setup.
Real Node I/O (https with redirect-follow, fs/promises, spawnSync,
process.platform/arch/PATH) is wired here so cliInstaller stays
import-free of node:* and unit-testable."
```

---

## Task 5: Update extension/README.md (install + GCP-required note)

**Files:**

- Modify: `extension/README.md`

The README's "Install" section currently tells users to clone the repo and run make commands. With Marketplace publishing live and the auto-installer landed, the install path is one line. Also add a one-paragraph "Requirements" callout that GCP is the only supported provider for now.

- [ ] **Step 1: Replace the Install section**

Open `extension/README.md` and find the section starting with `## Install`. Replace everything from that heading through (and including) the line `The extension expects the \`moorpost\` CLI on your \`PATH\`...` with:

```markdown
## Requirements

- macOS or Linux (Windows is not supported; use WSL).
- A **Google Cloud Platform** project. GCP is the only provider Moorpost ships with today; AWS / Azure are on the roadmap. You'll need permission to create Compute Engine instances in the project.
- A Claude Code subscription (or `ANTHROPIC_API_KEY`).

## Install

Install from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=LatentAdvisory.moorpost):

```sh
code --install-extension LatentAdvisory.moorpost
```

The first time the extension activates, it auto-downloads the matching `moorpost` CLI binary from the GitHub release, verifies its SHA-256 against the published `SHA256SUMS`, and installs it to `~/.local/bin/moorpost`. If that directory isn't on your `PATH`, the extension also writes the absolute path to its own `moorpost.cliPath` setting so commands keep working without shell changes.

If the auto-install fails (offline, sandboxed CI environment, etc.), a toast surfaces a one-click link to the [GitHub release page](https://github.com/latent-advisory/moorpost/releases/latest) where you can grab the binary manually.
```

- [ ] **Step 2: Sanity check the rendered structure**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost" && grep -n "^## " extension/README.md | head -20
```

Expected: the section ordering reads `Requirements` → `Install` → `The flow` (or whatever the existing next section is).

- [ ] **Step 3: Commit**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add extension/README.md
git commit -m "docs(extension): rewrite install section + GCP-required callout

Marketplace install is one line now. Auto-install brings the CLI down
on first activation, so the README no longer tells users to clone the
repo. New 'Requirements' section makes the GCP-only provider story
visible up front for Marketplace browsers."
```

---

## Task 6: Update root README install section

**Files:**

- Modify: `README.md`

The repo root README's Install section still tells everyone to build from source. After the Marketplace + auto-install changes, VSCode-extension users have a one-line path. CLI-only users still need to install from a binary or build from source.

- [ ] **Step 1: Edit README.md**

Find the section starting with `## Install` and ending before `## Quickstart`. Replace its body with:

```markdown
You'll need macOS or Linux, a Google Cloud Platform account (GCP for v1.x), and a Claude Code subscription (or `ANTHROPIC_API_KEY`).

### VS Code extension (recommended)

```sh
code --install-extension LatentAdvisory.moorpost
```

The extension auto-downloads the matching `moorpost` CLI on first activation — no separate binary install step.

### CLI only

Download the binary for your platform from the [latest release](https://github.com/latent-advisory/moorpost/releases/latest) and put it on your `PATH`:

```sh
# macOS Apple Silicon
curl -L https://github.com/latent-advisory/moorpost/releases/latest/download/moorpost-darwin-arm64 \
  -o /usr/local/bin/moorpost && chmod +x /usr/local/bin/moorpost
```

Or build from source:

```sh
git clone https://github.com/latent-advisory/moorpost.git
cd moorpost
make build install
```
```

- [ ] **Step 2: Commit**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add README.md
git commit -m "docs: rewrite root README install section

VSCode extension is now the primary install path (Marketplace + auto-install
of the CLI). CLI-only and from-source paths remain documented for users who
don't want the extension."
```

---

## Task 7: Update PLUGIN.md auto-install subsection

**Files:**

- Modify: `PLUGIN.md`

PLUGIN.md is the authoritative design doc. Any future contributor reading `cliInstaller.ts` should be able to find a paragraph here that explains *why* it exists.

- [ ] **Step 1: Find and edit the §10 extension surface**

Open `PLUGIN.md` and locate the §10 (extension surface) section. Add a new subsection just before §11 (or at the end of §10's content, as appropriate):

```markdown
### 10.x CLI auto-install

The extension is published to the VS Code Marketplace as `LatentAdvisory.moorpost`. The Marketplace listing only ships the TypeScript bundle — the Go CLI is a separate per-platform binary on the GitHub release page.

To remove the manual-binary-download step from the install flow, the extension carries `extension/src/cliInstaller.ts`:

- On every activation, it spawns `<cliBinary()> --version` and parses the leading semver token.
- If the binary is missing (`ENOENT`) or its version is below the extension-declared `MIN_CLI_VERSION`, it picks the right asset for `process.platform` × `process.arch`, downloads it from `https://github.com/latent-advisory/moorpost/releases/download/v<MIN_CLI_VERSION>/<asset>`, fetches `SHA256SUMS` from the same release, verifies the hash, writes the binary to `~/.local/bin/moorpost` (mode 0755), and updates `moorpost.cliPath` if `~/.local/bin` isn't on the inherited `PATH`.
- All Node I/O is dependency-injected so the orchestrator is unit-testable through the existing `node --test` + loader-shim setup; see `extension/src/cliInstaller.test.mts`.
- `MIN_CLI_VERSION` is bumped only when the extension actually depends on a new CLI feature — the floor is one-directional (extension → CLI) and not a strict lock-step. Each release that bumps the floor triggers a re-download for users on stale binaries.

Failures (offline, sandboxed CI, unsupported platform like Windows, SHA mismatch, FS error) surface as a non-modal error toast with a one-click "Open release page" link. The extension does not block activation on auto-install failure — users with a working binary are unaffected.

Out of scope: GPG signature verification, Windows support, hot updates when the extension is older than the installed CLI.
```

- [ ] **Step 2: Commit**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add PLUGIN.md
git commit -m "docs(plugin): document CLI auto-install design"
```

---

## Task 8: Bump version to 1.1.5, smoke gate, package, release

**Files:**

- Modify: `extension/package.json`

The constant `MIN_CLI_VERSION = '1.1.5'` is already in place from Task 1. This task bumps the extension package version to match, runs the full smoke gate, packages the .vsix, and tags + builds + uploads the release.

- [ ] **Step 1: Bump extension/package.json**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
```

Edit `extension/package.json` and change:

```json
"version": "1.1.4",
```

to:

```json
"version": "1.1.5",
```

- [ ] **Step 2: Run the smoke gate**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost" && make smoke
```

Expected output ends with `✓ smoke gate passed`. This runs CLI build + race tests + lint + extension bundle.

- [ ] **Step 3: Package the .vsix**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost" && make extension-package
```

Expected: `extension/moorpost-1.1.5.vsix` exists.

- [ ] **Step 4: Commit, tag, push**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
git add extension/package.json
git commit -m "release: v1.1.5 — auto-install moorpost CLI from VSCode extension

First-time Marketplace users no longer need to download the CLI binary
manually. On activation the extension probes the configured binary's
--version and silently downloads, SHA-verifies, and installs the matching
release asset to ~/.local/bin/moorpost when the binary is missing or
below the declared MIN_CLI_VERSION (1.1.5).

Docs updated: extension README + root README + PLUGIN.md."
git tag -a v1.1.5 -m "Moorpost v1.1.5"
git push origin main
git push origin v1.1.5
```

- [ ] **Step 5: Build cross-platform binaries and create the GitHub release**

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost" && make release
```

Then:

```bash
cd "/Users/landytang/Documents/Claude/Projects/AI M&A/code/moorpost"
gh release create v1.1.5 \
    --title "Moorpost v1.1.5" \
    --notes "$(cat <<'EOF'
## What's new in v1.1.5

### Auto-install of the CLI from the VSCode extension

First-time Marketplace users no longer need to download the `moorpost` binary by hand. On activation the extension:

1. Probes the configured binary's `--version`.
2. If absent or below `MIN_CLI_VERSION` (1.1.5), maps `process.platform × process.arch` to the right release asset.
3. Downloads it, verifies the SHA-256 against this release's `SHA256SUMS`, writes it to `~/.local/bin/moorpost` (mode 0755), and writes `moorpost.cliPath` if `~/.local/bin` isn't on `PATH`.
4. Re-runs `--version` to confirm.

Failures (offline, unsupported platform like Windows, SHA mismatch, FS error) surface as a one-click toast linking to this release page.

### Docs

- Extension README: install section is now a one-line marketplace command. Added a "Requirements" callout that GCP is the only supported provider for now.
- Root README: VSCode extension is the primary install path; CLI-only users still get a `curl` snippet and a `make build install` fallback.
- PLUGIN.md: new §10.x subsection describing the auto-install design.

## Install

```sh
code --install-extension LatentAdvisory.moorpost
```

Or upload the attached `moorpost-1.1.5.vsix` manually via the [Marketplace publisher page](https://marketplace.visualstudio.com/manage/publishers/LatentAdvisory).
EOF
)" \
    dist/moorpost-darwin-amd64 \
    dist/moorpost-darwin-arm64 \
    dist/moorpost-linux-amd64 \
    dist/moorpost-linux-arm64 \
    dist/SHA256SUMS \
    extension/moorpost-1.1.5.vsix
```

Expected: the command prints the release URL.

- [ ] **Step 6: Upload .vsix to the Marketplace**

The vsce publish PAT path is broken (see prior session). Manual upload via the Marketplace publisher UI:

1. Open `https://marketplace.visualstudio.com/manage/publishers/LatentAdvisory` in a browser.
2. Find the **moorpost** extension row → **⋯** menu → **Update**.
3. Upload `extension/moorpost-1.1.5.vsix`.
4. Wait for the listing to flip from "Pending" to "Verified" (typically <15 min).

- [ ] **Step 7: Verify end-to-end**

In a clean environment (or after running the same teardown as the previous session — `code --uninstall-extension LatentAdvisory.moorpost && rm -rf ~/.local/bin/moorpost ~/.moorpost`):

```sh
code --install-extension LatentAdvisory.moorpost
```

Open VS Code in any project, observe the "Installing Moorpost CLI v1.1.5…" progress toast, then the "Moorpost CLI installed (v1.1.5)." confirmation. Confirm `~/.local/bin/moorpost --version` reports `v1.1.5`.

If anything regresses, fix it on a hotfix and cut `v1.1.6` rather than retagging `v1.1.5`.

---

## Definition of done

- [ ] All unit tests pass (`npm test` in `extension/`).
- [ ] `make smoke` passes.
- [ ] `make extension-package` produces `moorpost-1.1.5.vsix`.
- [ ] GitHub release v1.1.5 is created with all 5 asset attachments.
- [ ] Marketplace listing shows version 1.1.5.
- [ ] End-to-end install on a clean environment shows the auto-install toast and lands a working `moorpost` binary.
- [ ] Extension README, root README, and PLUGIN.md all reflect the new install flow.
