// Command registrations. Each command is a thin wrapper that defers to the
// CLI — the extension is intentionally a UI shell, not where the work happens.

import * as vscode from 'vscode';
import { runInTerminal, getStatus, workspaceRoot } from '../cli';
import type { MoorpostTreeProvider } from '../treeView';
import { bootstrapProject, initProject } from './getStarted';
import { editConfig, toggleSide } from './extras';
import { runCliInOutput, logToChannel } from '../output';
import {
  closeClaudeTerminalQuietly,
  hasAnyClaudeTerminal,
  openLocalClaude,
  openRemoteClaude,
} from '../claudeTerminal';
import {
  pluginCurrentlyRouted,
  pluginInstalled,
  routePluginToRemote,
} from '../claudePluginIntegration';
import { getSessionTracker } from '../extension';
import { listLocalSessions } from '../sessionList';

/**
 * Decides which surface the user is using — a Moorpost: Claude terminal
 * or the Anthropic Claude Code plugin panel — so handoff and return swap
 * only the corresponding one and don't open a stray terminal alongside
 * the plugin (or vice versa).
 *
 * Resolution order:
 *  1. Explicit setting `moorpost.handoffSurface` if set to "terminal" or
 *     "plugin" — overrides everything else.
 *  2. The plugin is currently routed through our wrapper → "plugin"
 *     (we already routed it; return should unroute it). Takes priority
 *     over any open Moorpost terminal — if the user routed the plugin
 *     to remote, the terminal is incidental.
 *  3. A Moorpost: Claude terminal is open → "terminal" (regardless of
 *     in-memory tracking — covers post-reload orphans).
 *  4. Plugin is installed but never routed → "plugin" (assume plugin is
 *     the surface the user wants to drive; the alternative would be to
 *     spawn a terminal they didn't ask for).
 *  5. Fallback → "terminal".
 */
type HandoffSurface = 'terminal' | 'plugin';

function pickHandoffSurface(): HandoffSurface {
  const explicit = vscode.workspace
    .getConfiguration('moorpost')
    .get<string>('handoffSurface');
  if (explicit === 'terminal' || explicit === 'plugin') return explicit;

  if (pluginCurrentlyRouted()) return 'plugin';
  if (hasAnyClaudeTerminal()) return 'terminal';
  if (pluginInstalled()) return 'plugin';
  return 'terminal';
}

/**
 * Outcome of pickHandoffTarget — caller dispatches the right CLI args
 * based on whether the user picked an existing session or "start new".
 */
type HandoffPick =
  | { kind: 'session'; sessionId: string }
  | { kind: 'new' };

/**
 * Show a QuickPick of local sessions for handoff. Sessions already
 * on remote (sid in remoteSids) are dimmed and described as "(already
 * on remote)" — picking one is a no-op the CLI will accept idempotently
 * but the user gets a clear signal. Default-selected = focused panel's
 * SID. The "Start new session on remote" item appears at the top.
 *
 * Returns undefined if the user cancelled. The caller handles the
 * subsequent CLI invocation.
 */
async function pickHandoffTarget(
  cwd: string,
  focusedSid: string | undefined,
  remoteSids: Set<string>,
): Promise<HandoffPick | undefined> {
  const sessions = await listLocalSessions(cwd);
  type Item = vscode.QuickPickItem & { pick: HandoffPick };
  const items: Item[] = [];
  items.push({
    label: '$(plus) Start new session on remote',
    description: 'Open a fresh Claude Code panel routed to the remote VM',
    pick: { kind: 'new' },
  });
  if (sessions.length > 0) {
    items.push({
      label: 'Existing sessions',
      kind: vscode.QuickPickItemKind.Separator,
      pick: { kind: 'new' }, // unused; separator items aren't returned
    });
  }
  for (const s of sessions) {
    const onRemote = remoteSids.has(s.sessionId);
    const focused = s.sessionId === focusedSid;
    const tags: string[] = [];
    if (focused) tags.push('focused');
    if (onRemote) tags.push('on remote');
    const tagSuffix = tags.length ? ` (${tags.join(', ')})` : '';
    items.push({
      label: s.firstUserText + tagSuffix,
      description: s.sessionId.slice(0, 8) + ' · ' + formatRelative(s.mtimeMs),
      detail: formatBytes(s.sizeBytes),
      pick: { kind: 'session', sessionId: s.sessionId },
    });
  }

  // Pre-select priority:
  //   1. The SID the SessionTracker reports as focused (most accurate
  //      when the user opens panels after the extension activates and
  //      the wrapper's spawn-record fires).
  //   2. Fallback: the most-recently-modified session JSONL — that's
  //      whichever session the wrapper just appended to, i.e. what the
  //      user is currently typing into. Robust even when tab→SID
  //      bootstrap mapping is unknown (e.g., panels opened before
  //      activation).
  // We never pre-select "+ Start new on remote" — that's a deliberate
  // choice the user must navigate to.
  const findSession = (sid: string) =>
    items.find(
      (it) =>
        it.kind !== vscode.QuickPickItemKind.Separator &&
        it.pick.kind === 'session' &&
        it.pick.sessionId === sid,
    );
  const focusedItem =
    (focusedSid ? findSession(focusedSid) : undefined) ??
    (sessions[0] ? findSession(sessions[0].sessionId) : undefined);
  const usedFocusFallback =
    focusedItem !== undefined && (!focusedSid || !findSession(focusedSid));

  return new Promise<HandoffPick | undefined>((resolve) => {
    const qp = vscode.window.createQuickPick<Item>();
    qp.items = items;
    qp.placeholder = focusedItem
      ? usedFocusFallback
        ? 'Pick a session to migrate to remote (default = most recently active)'
        : 'Pick a session to migrate to remote (default = your focused panel)'
      : 'Pick a session to migrate to remote';
    qp.matchOnDescription = true;
    qp.matchOnDetail = true;
    if (focusedItem) qp.activeItems = [focusedItem];
    qp.onDidAccept(() => {
      const sel = qp.selectedItems[0];
      qp.hide();
      resolve(sel?.pick);
    });
    qp.onDidHide(() => {
      qp.dispose();
      resolve(undefined);
    });
    qp.show();
  });
}

/**
 * Outcome of pickReturnTarget. `legacy` covers the pre-Phase-2 case
 * where active_side=remote but RemoteSIDs is empty — those projects
 * still use the whole-project return CLI path (no flag).
 */
type ReturnPick =
  | { kind: 'session'; sessionId: string }
  | { kind: 'all' }
  | { kind: 'legacy' };

async function pickReturnTarget(
  cwd: string,
  remoteSids: string[],
  focusedSid: string | undefined,
  legacyMode: boolean,
): Promise<ReturnPick | undefined> {
  if (legacyMode) {
    const ok = await vscode.window.showInformationMessage(
      'Return the project session to your laptop?',
      {
        modal: true,
        detail:
          'This project was handed off in legacy whole-project mode. Returning syncs all remote state back and stops the VM.',
      },
      'Return',
    );
    return ok === 'Return' ? { kind: 'legacy' } : undefined;
  }

  // Per-session picker: list remote sessions only, with first-message
  // labels read from the local JSONLs (handoff already synced the
  // JSONL back, so the file is on disk locally either way).
  const all = await listLocalSessions(cwd);
  const remoteSet = new Set(remoteSids);
  const remoteSessions = all.filter((s) => remoteSet.has(s.sessionId));
  // Surface SIDs that are listed in remote_sids but have no local
  // JSONL — that shouldn't normally happen, but if it does we still
  // want the user to be able to return them (CLI handles the rsync).
  for (const sid of remoteSids) {
    if (!remoteSessions.find((s) => s.sessionId === sid)) {
      remoteSessions.push({
        sessionId: sid,
        mtimeMs: 0,
        sizeBytes: 0,
        firstUserText: '(no local JSONL — registered as remote)',
        jsonlPath: '',
      });
    }
  }

  type Item = vscode.QuickPickItem & { pick: ReturnPick };
  const items: Item[] = [];
  if (remoteSessions.length > 1) {
    items.push({
      label: '$(arrow-down) Return all',
      description: `Return ${remoteSessions.length} sessions and stop the VM`,
      pick: { kind: 'all' },
    });
    items.push({
      label: 'Sessions on remote',
      kind: vscode.QuickPickItemKind.Separator,
      pick: { kind: 'all' }, // unused
    });
  }
  for (const s of remoteSessions) {
    const focused = s.sessionId === focusedSid;
    const tagSuffix = focused ? ' (focused)' : '';
    items.push({
      label: s.firstUserText + tagSuffix,
      description:
        s.sessionId.slice(0, 8) +
        (s.mtimeMs ? ' · ' + formatRelative(s.mtimeMs) : ''),
      detail: s.sizeBytes ? formatBytes(s.sizeBytes) : undefined,
      pick: { kind: 'session', sessionId: s.sessionId },
    });
  }

  if (items.length === 0) return undefined;

  // Pre-select priority (mirrors handoff picker):
  //   1. The SID the SessionTracker reports as focused.
  //   2. Fallback: the most-recently-modified remote session JSONL.
  // Skip "Return all" as the default — picking one specific session is
  // a more conservative default than fanning out to all of them.
  const findSession = (sid: string) =>
    items.find(
      (it) =>
        it.kind !== vscode.QuickPickItemKind.Separator &&
        it.pick.kind === 'session' &&
        it.pick.sessionId === sid,
    );
  const focusedItem =
    (focusedSid ? findSession(focusedSid) : undefined) ??
    (remoteSessions[0] ? findSession(remoteSessions[0].sessionId) : undefined);
  const usedFocusFallback =
    focusedItem !== undefined && (!focusedSid || !findSession(focusedSid));

  return new Promise<ReturnPick | undefined>((resolve) => {
    const qp = vscode.window.createQuickPick<Item>();
    qp.items = items;
    qp.placeholder =
      remoteSessions.length === 1
        ? `One session on remote — return it?`
        : focusedItem
          ? usedFocusFallback
            ? 'Pick a session to return to local (default = most recently active)'
            : 'Pick a session to return to local (default = your focused panel)'
          : 'Pick a session to return to local';
    qp.matchOnDescription = true;
    if (focusedItem) qp.activeItems = [focusedItem];
    qp.onDidAccept(() => {
      const sel = qp.selectedItems[0];
      qp.hide();
      resolve(sel?.pick);
    });
    qp.onDidHide(() => {
      qp.dispose();
      resolve(undefined);
    });
    qp.show();
  });
}

function formatRelative(mtimeMs: number): string {
  const ageMs = Date.now() - mtimeMs;
  const sec = Math.floor(ageMs / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const days = Math.floor(hr / 24);
  return `${days}d ago`;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/**
 * Poll the SessionTracker for up to timeoutMs after triggering
 * newConversation, returning the SID the tracker associates with the
 * fresh tab. The tracker waits ~1.5s on tab-open before reading the
 * spawns log, so allow ~5-6s before giving up.
 */
async function waitForNewClaudeSid(
  tracker: ReturnType<typeof getSessionTracker>,
  timeoutMs: number,
): Promise<string | undefined> {
  const startSid = tracker?.getFocusedClaudeSid();
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 250));
    const sid = tracker?.getFocusedClaudeSid();
    if (sid && sid !== startSid) return sid;
  }
  return undefined;
}

import { refreshStatusBarNow } from '../statusBar';

export function registerCommands(
  context: vscode.ExtensionContext,
  treeProvider?: MoorpostTreeProvider,
): void {
  // Tree + status bar refresh after state-changing commands. The CLI runs
  // out-of-process; refresh on a short delay so the new state.json is
  // visible to `moorpost status`.
  const refreshTreeAfter = (ms: number) => {
    setTimeout(() => {
      if (treeProvider) treeProvider.refresh();
      refreshStatusBarNow();
    }, ms);
  };

  const sessionPreview = async (cwd: string, sid: string): Promise<string> => {
    try {
      const sessions = await listLocalSessions(cwd);
      const match = sessions.find((s) => s.sessionId === sid);
      if (match?.firstUserText) return match.firstUserText;
    } catch {
      /* fall through */
    }
    return sid.slice(0, 8) + '…';
  };

  // Pre-CLI confirmation: sticky non-blocking notification.
  //
  // VSCode notifications shown via showWarningMessage with action buttons
  // do NOT auto-dismiss — they stay in the notification toaster until the
  // user clicks one of the buttons. So the user can freely interact with
  // editors (close the Claude Code panel) while the notification waits.
  // Then they click "I closed it" to proceed, or "Cancel" to abort.
  //
  // We use showWarningMessage (yellow icon) instead of Information so it's
  // more visually prominent than a regular toast.
  const preflightClosePrompt = async (
    cwd: string,
    sid: string,
    destLabel: 'remote' | 'local',
  ): Promise<boolean> => {
    const preview = await sessionPreview(cwd, sid);
    const previewShort = preview.length > 60 ? preview.slice(0, 60) + '…' : preview;
    logToChannel(`preflightClosePrompt(${sid}): dest=${destLabel} preview="${previewShort}"`);

    const action = destLabel === 'remote' ? 'hand off' : 'return';
    const choice = await vscode.window.showWarningMessage(
      `Moorpost: close the Claude Code panel for "${previewShort}" (${sid.slice(0, 8)}…), then click "I closed it" to ${action}.`,
      'I closed it',
      'Cancel',
    );
    if (choice !== 'I closed it') {
      logToChannel(`preflightClosePrompt(${sid}): cancelled (choice=${choice ?? 'dismissed'})`);
      return false;
    }
    logToChannel(`preflightClosePrompt(${sid}): user confirmed close, proceeding`);
    return true;
  };

  // Post-CLI: open a fresh panel on the new destination. Pre-flight already
  // closed the old panel; this is just the auto-open step.
  const finishMoveSession = async (sid: string): Promise<void> => {
    await openSessionWithHistory(sid);
  };

  // openSessionWithHistory: opens a fresh Claude Code panel for the
  // given SID, with conversation history rendered in scrollback.
  //
  // openSessionWithHistory: opens (or reveals) a Claude Code panel for
  // the given SID with conversation history. Trusts editor.open to
  // succeed unless it throws — the previous tab-appearance timeout
  // mistakenly fell back to a fresh empty conversation when the plugin
  // revealed an existing panel (no new-tab event fires).
  const openSessionWithHistory = async (sid: string): Promise<void> => {
    try {
      await vscode.commands.executeCommand('claude-vscode.editor.open', sid);
      logToChannel(`editor.open(${sid}) succeeded`);
    } catch (e) {
      logToChannel(`editor.open(${sid}) threw: ${String(e)} — falling back to newConversation`);
      await vscode.commands.executeCommand('claude-vscode.newConversation');
    }
  };

  context.subscriptions.push(
    vscode.commands.registerCommand('moorpost.bootstrap', bootstrapProject),

    vscode.commands.registerCommand('moorpost.runSetup', async () => {
      await runCliInOutput(['setup', '--yes'], {
        title: 'Installing prerequisites',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.runDoctor', async () => {
      await runCliInOutput(['doctor'], {
        cwd: workspaceRoot(),
        title: 'Running diagnostics',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.initProject', initProject),

    vscode.commands.registerCommand('moorpost.signIn', async () => {
      runInTerminal(['auth']);
    }),

    vscode.commands.registerCommand('moorpost.provision', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      // Soft-warn on missing auth so the user doesn't end up with a
      // provisioned-but-unhandoffable VM. Not blocking — provisioning
      // itself doesn't need auth.
      const status = await getStatus(cwd);
      if (status && status.auth_cached === false) {
        const pick = await vscode.window.showWarningMessage(
          'No Claude credential cached. The VM will provision fine, but you won\'t be able to hand off until you sign in.',
          'Sign in first',
          'Provision anyway',
          'Cancel',
        );
        if (pick === 'Cancel' || !pick) return;
        if (pick === 'Sign in first') {
          await vscode.commands.executeCommand('moorpost.signIn');
          return;
        }
      }
      // --wait makes the CLI poll SSH until claude is on PATH on the VM,
      // so the user gets a single "ready to handoff" signal instead of
      // a misleading "VM running" while the 5-7min bootstrap continues
      // silently in the background.
      await runCliInOutput(['provision', '--wait'], {
        cwd,
        title: 'Provisioning VM (waiting for bootstrap)',
        reveal: 'always',
      });
      refreshTreeAfter(2000);
    }),

    vscode.commands.registerCommand('moorpost.handoff', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }

      // Preflight: surface specific, actionable errors instead of letting
      // the CLI fail mid-flight after we've already spun up a VM (or
      // shown the modal confirm).
      const status = await getStatus(cwd);
      if (!status) {
        const pick = await vscode.window.showWarningMessage(
          'No Moorpost project here. Run Bootstrap first.',
          'Run Bootstrap',
          'Dismiss',
        );
        if (pick === 'Run Bootstrap') {
          await vscode.commands.executeCommand('moorpost.bootstrap');
        }
        return;
      }
      if (status.auth_cached === false) {
        const pick = await vscode.window.showWarningMessage(
          'Not signed in to Claude. Sign in before handoff.',
          'Sign in',
          'Dismiss',
        );
        if (pick === 'Sign in') {
          await vscode.commands.executeCommand('moorpost.signIn');
        }
        return;
      }
      if (!status.vm_id) {
        const pick = await vscode.window.showWarningMessage(
          'No VM provisioned yet. Provision one before handoff.',
          'Provision now',
          'Dismiss',
        );
        if (pick === 'Provision now') {
          await vscode.commands.executeCommand('moorpost.provision');
        }
        return;
      }
      // Pick which session to migrate via a QuickPick over local
      // sessions, with the focused panel's SID pre-selected. With
      // per-session routing (Phase 2), individual sessions can be on
      // remote independently — so multiple handoffs are normal and
      // we no longer short-circuit when active_side=remote.
      const focusedSid = getSessionTracker()?.getFocusedClaudeSid();
      logToChannel(
        `handoff picker open: focusedSid=${focusedSid ?? '(unknown — falls back to most-recent JSONL)'}`,
      );
      const remoteSids = new Set<string>(status.remote_sids ?? []);
      const picked = await pickHandoffTarget(cwd, focusedSid, remoteSids);
      if (!picked) return;

      const handoffArgs = ['handoff', '--yes'];
      if (picked.kind === 'session') {
        handoffArgs.push('--session', picked.sessionId);
        logToChannel(`handoff: targeting session sid=${picked.sessionId}`);
      } else {
        // "Start new on remote" — open a fresh local Claude Code panel,
        // wait for the SessionTracker to capture its new SID, then
        // hand THAT SID off. We deliberately don't use the CLI's
        // --new-session flag (which flips active_side=remote project-
        // wide) — per-session routing is cleaner: only THIS one SID
        // ends up routed to remote, other panels stay local.
        logToChannel(`handoff: starting new session, waiting to capture SID...`);
        await vscode.commands.executeCommand('claude-vscode.newConversation');
        const newSid = await waitForNewClaudeSid(getSessionTracker(), 6000);
        if (!newSid) {
          vscode.window.showErrorMessage(
            'Could not capture the new session ID — handoff aborted. ' +
              'Try again, or open a chat panel first and re-run handoff.',
          );
          return;
        }
        logToChannel(`handoff: captured new session sid=${newSid}`);
        handoffArgs.push('--session', newSid);
      }
      // Pre-flight: for plugin surface, ask user to close the panel
      // before we run the CLI. Non-blocking so they can interact with
      // VSCode to close the tab, then click Continue.
      const handoffSurface = pickHandoffSurface();
      const handoffSid = handoffArgs.find((_, i) => handoffArgs[i - 1] === '--session');
      if (handoffSurface === 'plugin' && handoffSid) {
        const confirmed = await preflightClosePrompt(cwd, handoffSid, 'remote');
        if (!confirmed) return;
      }

      const exit = await runCliInOutput(handoffArgs, {
        cwd,
        title: 'Handing off to remote',
        reveal: 'on-error',
      });
      refreshTreeAfter(2000);
      if (exit === 0) {
        const auto = vscode.workspace
          .getConfiguration('moorpost')
          .get<boolean>('autoAttachOnHandoff', true);
        if (auto && handoffSurface === 'terminal') {
          openRemoteClaude(cwd);
        } else if (auto && handoffSurface === 'plugin') {
          await routePluginToRemote();
          const refreshed = await getStatus(cwd);
          logToChannel(
            `post-handoff: surface=plugin pending_resume_sid=${refreshed?.pending_resume_sid ?? '(empty)'}`,
          );
          if (refreshed?.pending_resume_sid) {
            await finishMoveSession(refreshed.pending_resume_sid);
          }
        }
      }
    }),

    vscode.commands.registerCommand('moorpost.return', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }

      const status = await getStatus(cwd);
      if (!status) {
        vscode.window.showWarningMessage(
          'No Moorpost project here. Nothing to return.',
        );
        return;
      }
      if (!status.vm_id) {
        vscode.window.showWarningMessage(
          'No VM provisioned. There is no remote session to return from.',
        );
        return;
      }
      const remoteSids = status.remote_sids ?? [];
      const legacyWholeProjectRemote =
        remoteSids.length === 0 && (status.active_side ?? 'local') === 'remote';
      if (remoteSids.length === 0 && !legacyWholeProjectRemote) {
        vscode.window.showInformationMessage(
          'No sessions on remote — nothing to return.',
        );
        return;
      }

      // Build a picker over remote sessions. With per-session routing
      // a project can have multiple sessions on remote; user picks
      // which one to bring back. Pre-select the focused panel if
      // it's currently on remote. "Return all" appears at the top
      // when there are 2+ remote sessions.
      const focusedSid = getSessionTracker()?.getFocusedClaudeSid();
      logToChannel(
        `return picker open: focusedSid=${focusedSid ?? '(unknown — falls back to most-recent JSONL)'} remoteSids=${remoteSids.length}`,
      );
      const picked = await pickReturnTarget(
        cwd,
        remoteSids,
        focusedSid,
        legacyWholeProjectRemote,
      );
      if (!picked) return;

      const returnArgs = ['return'];
      if (picked.kind === 'session') {
        returnArgs.push('--session', picked.sessionId);
      } else if (picked.kind === 'all') {
        returnArgs.push('--all');
      }
      // Legacy whole-project return: no flag, CLI keeps old behavior.

      const surface = pickHandoffSurface();
      closeClaudeTerminalQuietly();

      // Pre-flight: for plugin surface, ask user to close the panel(s)
      // before the CLI runs. Non-blocking so they can interact with VSCode.
      if (surface === 'plugin') {
        if (picked.kind === 'session') {
          const confirmed = await preflightClosePrompt(cwd, picked.sessionId, 'local');
          if (!confirmed) return;
        } else if (picked.kind === 'all') {
          const choice = await vscode.window.showInformationMessage(
            'Close all remote Claude Code panels',
            { modal: false, detail: 'Close every remote-routed Claude Code panel, then click Continue.' },
            'Continue',
          );
          if (choice !== 'Continue') return;
        }
      }

      const exit = await runCliInOutput(returnArgs, {
        cwd,
        title:
          picked.kind === 'all'
            ? 'Returning all sessions to local'
            : picked.kind === 'session'
              ? `Returning session ${picked.sessionId.slice(0, 8)} to local`
              : 'Returning to local',
        reveal: 'on-error',
      });
      refreshTreeAfter(2000);
      if (exit === 0) {
        const auto = vscode.workspace
          .getConfiguration('moorpost')
          .get<boolean>('autoAttachOnHandoff', true);
        if (auto && surface === 'terminal') {
          const refreshed = await getStatus(cwd);
          openLocalClaude(cwd, refreshed?.agent_session_id);
        } else if (auto && surface === 'plugin') {
          // Wrapper reads active_side=local from state.json on next spawn,
          // routes local. PendingResumeSID baton injects --resume so the
          // panel opens the right session with full history.
          const refreshed = await getStatus(cwd);
          logToChannel(
            `post-return: surface=plugin pending_resume_sid=${refreshed?.pending_resume_sid ?? '(empty)'}`,
          );
          if (refreshed?.pending_resume_sid) {
            await finishMoveSession(refreshed.pending_resume_sid);
          }
        }
      }
    }),

    vscode.commands.registerCommand('moorpost.status', async () => {
      const cwd = workspaceRoot();
      const status = await getStatus(cwd);
      if (!status) {
        vscode.window.showWarningMessage(
          'No Moorpost project found here. Run `moorpost init` in a project directory.',
        );
        return;
      }
      const lines = [
        `Project: ${status.project}`,
        `Provider: ${status.provider}`,
        `Active side: ${status.active_side ?? 'local'}`,
      ];
      if (status.vm_id) lines.push(`VM: ${status.vm_id} (${status.vm_state ?? '?'})`);
      if (status.month_to_date_usd) lines.push(`MTD cost: $${status.month_to_date_usd.toFixed(2)}`);
      void vscode.window.showInformationMessage(lines.join(' · '));
    }),

    vscode.commands.registerCommand('moorpost.returnSession', async (arg: unknown) => {
      // Direct per-session return — invoked by clicking a session in the
      // tree's "Remote sessions" group. No picker; the user already chose
      // by clicking. Confirms via a non-modal toast.
      //
      // VSCode passes different args depending on invocation:
      //   - row click (via TreeItem.command.arguments) → arg is the SID string
      //   - inline action button (via menus view/item/context) → arg is the
      //     TreeItem itself; pull the SID from its command.arguments
      const sid =
        typeof arg === 'string'
          ? arg
          : (arg as { command?: { arguments?: unknown[] } } | undefined)
              ?.command?.arguments?.[0];
      const cwd = workspaceRoot();
      if (!cwd || typeof sid !== 'string' || !sid) {
        vscode.window.showWarningMessage('moorpost.returnSession: missing session id');
        return;
      }
      const choice = await vscode.window.showInformationMessage(
        `Return session ${sid.slice(0, 8)}… to local?`,
        { modal: false, detail: 'Pulls the session JSONL back from the VM. The VM stays running unless this is the last remote session.' },
        'Return',
      );
      if (choice !== 'Return') return;
      closeClaudeTerminalQuietly();
      if (pickHandoffSurface() === 'plugin') {
        const confirmed = await preflightClosePrompt(cwd, sid, 'local');
        if (!confirmed) return;
      }
      const exit = await runCliInOutput(['return', '--session', sid], {
        cwd,
        title: `Returning session ${sid.slice(0, 8)} to local`,
        reveal: 'on-error',
      });
      refreshTreeAfter(2000);
      if (exit === 0) {
        await finishMoveSession(sid);
      }
    }),

    vscode.commands.registerCommand('moorpost.openRemoteSession', async (arg: unknown) => {
      // Opens a remote session in a Claude Code panel without returning it.
      // The wrapper sees --resume <sid> in argv, checks remote_sids, and
      // routes the subprocess to remote — so the panel runs on the VM.
      // Invoked by clicking a session label in the "Remote sessions" tree.
      const sid =
        typeof arg === 'string'
          ? arg
          : (arg as { command?: { arguments?: unknown[] } } | undefined)
              ?.command?.arguments?.[0];
      if (typeof sid !== 'string' || !sid) {
        vscode.window.showWarningMessage('moorpost.openRemoteSession: missing session id');
        return;
      }
      logToChannel(`openRemoteSession: opening panel for remote sid=${sid}`);
      await openSessionWithHistory(sid);
    }),

    vscode.commands.registerCommand('moorpost.showSessions', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      // Defer to the CLI: it knows the live-on-remote check (ssh + pgrep
      // claude) and the JSONL parsing for first-message previews. We
      // just show the human-readable form in an output channel.
      await runCliInOutput(['sessions', 'list'], {
        cwd,
        title: 'Listing sessions (local + remote)',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.showConflicts', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      await runCliInOutput(['conflicts'], {
        cwd,
        title: 'Listing sync conflicts',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.attach', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      // Route through the shared remote-session manager so this terminal
      // is tracked alongside the auto-attached one (single-tracked
      // attach session, disconnect warning, etc.).
      openRemoteClaude(cwd);
    }),

    vscode.commands.registerCommand('moorpost.destroy', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      const choice = await vscode.window.showWarningMessage(
        'Permanently destroy this VM and its boot disk? This cannot be undone.',
        { modal: true },
        'Destroy',
      );
      if (choice !== 'Destroy') return;
      runInTerminal(['destroy', '--yes'], cwd);
      refreshTreeAfter(8000);
    }),

    vscode.commands.registerCommand('moorpost.showCost', async () => {
      const cwd = workspaceRoot();
      if (!cwd) {
        vscode.window.showWarningMessage('Open a workspace folder first.');
        return;
      }
      await runCliInOutput(['cost', '--explain'], {
        cwd,
        title: 'Computing cost details',
        reveal: 'always',
      });
    }),

    vscode.commands.registerCommand('moorpost.editConfig', editConfig),

    vscode.commands.registerCommand('moorpost.toggleSide', toggleSide),
  );
}
