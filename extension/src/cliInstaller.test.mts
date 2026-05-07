// Unit tests for cliInstaller.ts.
// vscode is shimmed by ../test/loader.mjs; node modules (https, fs, child_process)
// are injected via the deps argument to ensureCliInstalled().

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { ensureCliInstalled, MIN_CLI_VERSION, type InstallerDeps } from './cliInstaller.js';
import { resolveAssetName, parseShaLine, downloadAndVerify } from './cliInstaller.js';
import { createHash } from 'node:crypto';
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
