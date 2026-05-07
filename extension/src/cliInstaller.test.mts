// Unit tests for cliInstaller.ts.
// vscode is shimmed by ../test/loader.mjs; node modules (https, fs, child_process)
// are injected via the deps argument to ensureCliInstalled().

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { ensureCliInstalled, MIN_CLI_VERSION, type InstallerDeps } from './cliInstaller.js';
import { resolveAssetName, parseShaLine, downloadAndVerify } from './cliInstaller.js';
import { installBinary, type InstallerFs } from './cliInstaller.js';
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
