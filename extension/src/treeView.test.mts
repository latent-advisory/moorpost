// Unit tests for treeView.ts — focused on buildRemoteSessionsItem.
//
// buildRemoteSessionsItem is module-private, so we exercise it through
// the public surface: MoorpostTreeProvider.getChildren() returns the
// top-level rows (including the "Remote sessions" expandable parent),
// and getChildren(parent) returns its children.
//
// The vscode API and the cli/sessionList modules are mocked via the
// loader at test/loader.mjs (registered on the npm-test command line).

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { MoorpostTreeProvider, MoorpostTreeItem } from './treeView.js';
// @ts-expect-error -- shim exposes test-only mutators
import { __setStatus, __setCwd } from '../test/cli-shim.mjs';
// @ts-expect-error -- shim exposes test-only mutators
import { __setSessions } from '../test/sessionList-shim.mjs';

interface FakeStatus {
  project: string;
  provider: string;
  agent: string;
  sync: string;
  mode: string;
  remote_sids?: string[];
}

function baseStatus(over: Partial<FakeStatus> = {}): FakeStatus {
  return {
    project: 'webapp',
    provider: 'gcp',
    agent: 'claude',
    sync: 'mutagen',
    mode: 'persistent',
    ...over,
  };
}

function makeSession(sessionId: string, firstUserText: string) {
  return {
    sessionId,
    mtimeMs: Date.now(),
    sizeBytes: 100,
    firstUserText,
    jsonlPath: `/tmp/${sessionId}.jsonl`,
  };
}

async function getRemoteSessionsRoot(provider: MoorpostTreeProvider): Promise<MoorpostTreeItem | undefined> {
  const top = await provider.getChildren();
  return top.find((i) => i.contextValue === 'moorpost.remoteSessionsRoot');
}

describe('buildRemoteSessionsItem (via MoorpostTreeProvider.getChildren)', () => {
  it('omits the "Remote sessions" parent entirely when remoteSids is empty', async () => {
    __setCwd('/fake/cwd');
    __setStatus(baseStatus({ remote_sids: [] }));
    __setSessions([]);
    const provider = new MoorpostTreeProvider();
    const top = await provider.getChildren();
    const root = top.find((i) => i.contextValue === 'moorpost.remoteSessionsRoot');
    assert.equal(root, undefined, 'no Remote sessions parent should be added');
  });

  it('renders a single remote SID with its firstUserText label and no "Return all"', async () => {
    const sid = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
    __setCwd('/fake/cwd');
    __setStatus(baseStatus({ remote_sids: [sid] }));
    __setSessions([makeSession(sid, 'fix the bug in foo()')]);
    const provider = new MoorpostTreeProvider();
    const root = await getRemoteSessionsRoot(provider);
    assert.ok(root, 'Remote sessions parent should exist');
    assert.equal(root!.contextValue, 'moorpost.remoteSessionsRoot');

    const children = await provider.getChildren(root);
    assert.equal(children.length, 1, 'single SID → 1 child, no Return all');
    const sole = children[0];
    assert.equal(sole.label, 'fix the bug in foo()');
    assert.equal(sole.contextValue, 'moorpost.remoteSession');
    assert.notEqual(
      sole.contextValue,
      'moorpost.remoteSessions.all',
      'should not include the Return-all row',
    );
  });

  it('renders 3 children (Return all + 2 sessions) for two remote SIDs, with Return all first', async () => {
    const sidA = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
    const sidB = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';
    __setCwd('/fake/cwd');
    __setStatus(baseStatus({ remote_sids: [sidA, sidB] }));
    __setSessions([makeSession(sidA, 'task A'), makeSession(sidB, 'task B')]);
    const provider = new MoorpostTreeProvider();
    const root = await getRemoteSessionsRoot(provider);
    assert.ok(root);
    const children = await provider.getChildren(root);
    assert.equal(children.length, 3, 'expected Return all + 2 SID rows');
    assert.equal(children[0].contextValue, 'moorpost.remoteSessions.all', 'Return all must be first');
    assert.equal(children[0].label, 'Return all');
    assert.equal(children[1].contextValue, 'moorpost.remoteSession');
    assert.equal(children[2].contextValue, 'moorpost.remoteSession');
    // SID order preserved from remote_sids.
    assert.equal(children[1].label, 'task A');
    assert.equal(children[2].label, 'task B');
  });

  it('falls back to "(no preview)" when a remote SID has no matching local session', async () => {
    const sid = 'cccccccc-cccc-cccc-cccc-cccccccccccc';
    __setCwd('/fake/cwd');
    __setStatus(baseStatus({ remote_sids: [sid] }));
    __setSessions([]); // SID has no matching local JSONL
    const provider = new MoorpostTreeProvider();
    const root = await getRemoteSessionsRoot(provider);
    assert.ok(root);
    const children = await provider.getChildren(root);
    assert.equal(children.length, 1);
    const child = children[0];
    assert.equal(child.label, '(no preview)');
    assert.equal(child.contextValue, 'moorpost.remoteSession');
    // SID still surfaced via the value (truncated to 8 chars by treeView).
    assert.equal(child.value, sid.slice(0, 8));
  });

  it('per-SID children carry a moorpost.openRemoteSession command with the SID as argument', async () => {
    const sidA = 'dddddddd-dddd-dddd-dddd-dddddddddddd';
    const sidB = 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee';
    __setCwd('/fake/cwd');
    __setStatus(baseStatus({ remote_sids: [sidA, sidB] }));
    __setSessions([makeSession(sidA, 'A'), makeSession(sidB, 'B')]);
    const provider = new MoorpostTreeProvider();
    const root = await getRemoteSessionsRoot(provider);
    assert.ok(root);
    const children = await provider.getChildren(root);
    // children[0] is "Return all" — skip; check the per-SID rows.
    const sidRows = children.filter((c) => c.contextValue === 'moorpost.remoteSession');
    assert.equal(sidRows.length, 2);
    const cmdA = sidRows[0].command;
    const cmdB = sidRows[1].command;
    assert.ok(cmdA);
    assert.ok(cmdB);
    assert.equal(cmdA!.command, 'moorpost.openRemoteSession');
    assert.equal(cmdB!.command, 'moorpost.openRemoteSession');
    assert.deepEqual(cmdA!.arguments, [sidA]);
    assert.deepEqual(cmdB!.arguments, [sidB]);
  });

  it('parent contextValue is moorpost.remoteSessionsRoot and per-SID is moorpost.remoteSession', async () => {
    const sid = 'ffffffff-ffff-ffff-ffff-ffffffffffff';
    __setCwd('/fake/cwd');
    __setStatus(baseStatus({ remote_sids: [sid] }));
    __setSessions([makeSession(sid, 'do the thing')]);
    const provider = new MoorpostTreeProvider();
    const root = await getRemoteSessionsRoot(provider);
    assert.ok(root);
    assert.equal(root!.contextValue, 'moorpost.remoteSessionsRoot');
    const children = await provider.getChildren(root);
    for (const child of children) {
      // Every child here is a per-SID row (no "Return all" since N=1).
      assert.equal(child.contextValue, 'moorpost.remoteSession');
    }
  });
});
