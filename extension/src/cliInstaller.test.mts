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
