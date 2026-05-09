#!/bin/bash
# moorpost-claude-wrapper
#
# Goes in the Anthropic Claude Code plugin's `claudeCode.claudeProcessWrapper`
# setting. Replaces the plugin's default `claude` invocation with one routed
# by moorpost's per-project active_side:
#
#   active_side=local  → exec the real local claude binary (transparent)
#   active_side=remote → ssh into the project's VM and exec claude there,
#                        piping stdin/stdout/stderr back to the plugin
#
# The plugin sees what looks like normal claude either way; the compute
# (LLM calls, tool execution) happens wherever active_side points.

set -euo pipefail

STATE="$HOME/.moorpost/state.json"

# Find the real claude binary. Prefer MOORPOST_REAL_CLAUDE env override
# (CI / advanced users); otherwise probe PATH via `which -a` (works on
# both macOS bash 3.2 and modern Linux; bash builtin `command -v -a`
# does NOT exist on macOS's stock bash).
find_real_claude() {
  if [[ -n "${MOORPOST_REAL_CLAUDE:-}" ]] && [[ -x "$MOORPOST_REAL_CLAUDE" ]]; then
    printf '%s' "$MOORPOST_REAL_CLAUDE"
    return 0
  fi
  local self
  self="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
  local candidate resolved
  while IFS= read -r candidate; do
    [[ -n "$candidate" ]] || continue
    # Resolve symlinks so we can compare against $self reliably.
    resolved="$candidate"
    if command -v readlink >/dev/null 2>&1; then
      while [[ -L "$resolved" ]]; do
        resolved="$(readlink "$resolved")"
      done
    fi
    [[ "$resolved" == "$self" ]] && continue
    [[ "$candidate" == "$self" ]] && continue
    printf '%s' "$candidate"
    return 0
  done < <(which -a claude 2>/dev/null || true)
  return 1
}

fallback_local() {
  # When invoked by the Anthropic plugin's claudeProcessWrapper hook,
  # $1 is the real claude binary the plugin already resolved; just run
  # what the plugin asked us to run. When invoked directly from a shell
  # (e.g. `claude-wrapper --version`), $1 is a claude flag — fall back
  # to a PATH probe.
  #
  # If the consume_resume_baton block (run after we know the project
  # match) populated $pending_resume, splice `--resume <sid>` in before
  # the caller's args so a `moorpost return`-induced local relaunch
  # picks up the conversation the user was just having on remote.
  # Symmetric to how the remote path injects --resume into remote_cmd.
  local extra=()
  if [[ -n "${pending_resume:-}" ]]; then
    extra=(--resume "$pending_resume")
  fi
  # `set -u` errors on `${empty_array[@]}` expansion, so use the
  # defined-only-expand form: `${arr[@]+"${arr[@]}"}` produces zero
  # words when the array is empty/unset, the array's elements when set.
  if [[ $# -gt 0 ]] && [[ "$1" == /* ]] && [[ -x "$1" ]]; then
    local bin="$1"; shift
    exec "$bin" ${extra[@]+"${extra[@]}"} "$@"
  fi
  local real
  if real="$(find_real_claude)"; then
    exec "$real" ${extra[@]+"${extra[@]}"} "$@"
  fi
  echo "claude-wrapper: no claude binary on PATH (and MOORPOST_REAL_CLAUDE unset)" >&2
  exit 127
}

# No state file: not a moorpost-managed machine. Use real claude.
[[ -f "$STATE" ]] || fallback_local "$@"

# Walk up from cwd looking for the project key in state.json that matches
# (state keys are absolute project paths). Use jq for json parsing; if jq
# is missing or the project isn't found, fall through to local.
command -v jq >/dev/null 2>&1 || fallback_local "$@"

cwd="$PWD"
match=""
while [[ "$cwd" != "/" ]]; do
  if jq -e --arg p "$cwd" '.projects[$p]' "$STATE" >/dev/null 2>&1; then
    match="$cwd"
    break
  fi
  cwd="$(dirname "$cwd")"
done
[[ -n "$match" ]] || fallback_local "$@"

# Single-use "migrate this conversation" baton set by `moorpost handoff`
# (active_side flipping local→remote) OR `moorpost return` (remote→local).
# When present, the wrapper injects `--resume <sid>` into whichever claude
# it ends up exec'ing (remote via SSH or local via fallback_local), so the
# plugin-spawned claude continues the conversation that was active before
# the side-swap. Cleared immediately after read (atomic write via mktemp +
# mv) so a later fresh spawn doesn't accidentally re-resume the same
# session forever — strictly single-use.
#
# Read here (before the active_side dispatch) so both routes can consume
# it. Caller's --resume wins if explicit; the on-disk baton is still
# cleared either way (it's stale).
#
# Capture the caller's explicit --resume <sid> too. This drives two
# decisions: (a) whether to discard the baton (caller intent wins), and
# (b) whether to fall back to local when the SID isn't on remote (multi-
# tab safety — see remote-existence precheck below).
user_resume_sid=""
prev=""
for arg in "$@"; do
  if [[ "$prev" == "--resume" ]]; then
    user_resume_sid="$arg"
    break
  fi
  if [[ "$arg" == --resume=* ]]; then
    user_resume_sid="${arg#--resume=}"
    break
  fi
  prev="$arg"
done

# Per-spawn structured record for the moorpost VSCode extension's
# tab-to-session tracker: when the plugin opens a new Claude Code panel,
# the extension correlates the panel-open event with the most recent
# spawn record here to learn the panel's session id. This is the only
# externally observable signal — the plugin doesn't expose channelId
# or sessionId via context keys, tab labels, or storage.
#
# Written BEFORE the routing decision so the tracker sees every spawn
# (local OR remote). Without this, sessions that route local would
# never get tab→SID associations and the focus tracker can't pick the
# right session for handoff.
SPAWNS_LOG_DIR="$HOME/.moorpost/log"
mkdir -p "$SPAWNS_LOG_DIR" 2>/dev/null || true
SPAWNS="$SPAWNS_LOG_DIR/spawns.jsonl"
spawn_ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
printf '{"ts":"%s","sid":"%s","pid":%s,"ppid":%s,"cwd":"%s"}\n' \
  "$spawn_ts" "$user_resume_sid" "$$" "$PPID" "${PWD//\"/\\\"}" \
  >> "$SPAWNS" 2>/dev/null || true

pending_resume=$(jq -r --arg p "$match" '.projects[$p].pending_resume_sid // ""' "$STATE")
if [[ -n "$pending_resume" ]]; then
  if [[ -n "$user_resume_sid" ]]; then
    pending_resume=""
  fi
  state_tmp=$(mktemp "${STATE}.XXXXXX")
  if jq --arg p "$match" '.projects[$p].pending_resume_sid = ""' "$STATE" > "$state_tmp" 2>/dev/null; then
    mv "$state_tmp" "$STATE"
  else
    rm -f "$state_tmp" 2>/dev/null || true
  fi
fi

# Per-SID routing decision:
#   - If user explicitly passed --resume <sid> and that sid is in
#     remote_sids → route remote.
#   - If user did NOT pass --resume (fresh spawn / newConversation)
#     BUT the baton has a SID and that SID is in remote_sids →
#     route remote. This catches the post-handoff "Open in new tab"
#     flow: the plugin spawns a fresh claude with no --resume, the
#     wrapper sees the baton (set by `moorpost handoff`), and routing
#     should follow the baton's SID. Without this branch, the wrapper
#     falls back local and injects --resume from the baton, ending up
#     as `local claude --resume <remote-sid>` — wrong destination.
#   - If user did NOT pass --resume and the project's active_side is
#     "remote" → route remote (legacy whole-project mode).
#   - Otherwise → fallback_local.
active=$(jq -r --arg p "$match" '.projects[$p].active_side // "local"' "$STATE")
route_remote=0
if [[ -n "$user_resume_sid" ]]; then
  # Caller explicitly passed --resume <sid>. Caller intent is the
  # final word: if the SID is in remote_sids, route remote;
  # otherwise route local (even if active_side=remote, since the
  # caller is asking for a specific SID's local-routed instance).
  is_remote_sid=$(jq -r --arg p "$match" --arg s "$user_resume_sid" \
    '.projects[$p].remote_sids // [] | index($s) // empty | tostring' \
    "$STATE" 2>/dev/null || true)
  if [[ -n "$is_remote_sid" ]]; then
    route_remote=1
  fi
else
  # No explicit --resume from caller. The plugin spawned us for a
  # newConversation or similar fresh-start case. Two ways this can
  # route remote:
  #   (a) The pending_resume_sid baton was set by `moorpost handoff`
  #       and the baton SID is in remote_sids. We'll be injecting
  #       --resume <baton> into the spawn below, so routing must
  #       follow the baton's destination.
  #   (b) Project-level active_side is "remote" (legacy whole-project
  #       handoff before per-session existed).
  if [[ -n "$pending_resume" ]]; then
    is_remote_sid=$(jq -r --arg p "$match" --arg s "$pending_resume" \
      '.projects[$p].remote_sids // [] | index($s) // empty | tostring' \
      "$STATE" 2>/dev/null || true)
    if [[ -n "$is_remote_sid" ]]; then
      route_remote=1
    fi
  fi
  if [[ "$route_remote" == "0" && "$active" == "remote" ]]; then
    route_remote=1
  fi
fi
[[ "$route_remote" == "1" ]] || fallback_local "$@"

vm_id=$(jq -r --arg p "$match" '.projects[$p].vm_id // ""' "$STATE")
vm_ip=$(jq -r --arg v "$vm_id" '.vms[$v].external_ip // ""' "$STATE")
[[ -n "$vm_ip" ]] || fallback_local "$@"

# Anthropic's plugin invokes `wrapper <localClaudeBinaryPath> [args]`,
# or in node-module mode `wrapper <nodePath> <cli.js> [args]`. We need
# to drop the binary-path prefix(es) before forwarding to remote
# (where remote claude is its own install). Heuristic: skip args that
# look like absolute file paths to existing files until we hit
# something that doesn't (claude's actual flags).
skip=0
for arg in "$@"; do
  if [[ "$arg" == /* && -e "$arg" ]]; then
    skip=$((skip + 1))
  else
    break
  fi
done
shift "$skip" 2>/dev/null || true

# SSH ControlMaster multiplexing: every wrapper invocation that routes to
# remote does up to 4 ssh round-trips (existence precheck, rsync of
# CLAUDE_CONFIG_DIR, mkdir+symlink bridge, final exec). Without
# multiplexing, each pays full TCP+TLS+auth handshake cost — typically
# 1-3s per connection on cross-region links. With multiple plugin chat
# tabs all relaunching their claudes in parallel post-handoff, that
# overhead stacks and contributes to plugin's 60s subprocess-init
# deadline being missed.
#
# ControlMaster=auto + ControlPersist=60s: the first ssh becomes a
# master that opens a multiplexing socket; subsequent ssh calls (and
# the rsync's `-e ssh ${ssh_opts[*]}` invocation) reuse it — channel
# open instead of full handshake. Master exits 60s after the last
# client disconnects, so a typical post-handoff burst (re-launch all
# tabs within seconds) shares one TCP, but a stale master from hours
# ago doesn't tie up resources.
#
# ControlPath uses %C (OpenSSH hash of user/host/port) so different VMs
# don't collide on the same socket; tucked under /tmp to avoid bloating
# state dirs. Hashed form keeps paths short — Unix socket paths are
# capped at ~104 chars on macOS.
#
# ServerAlive*: detect dead/stalled connections after 5s × 3 = 15s of
# unresponsiveness instead of waiting for TCP to time out (minutes).
# Critical because the plugin's subprocess-init deadline is 60s — a
# stalled SSH that never errors silently consumes that whole budget
# and the user gets "Subprocess initialization did not complete within
# 60000ms" with no actionable signal.
ssh_ctl_dir="/tmp/moorpost-ssh-$(id -u)"
mkdir -p "$ssh_ctl_dir" 2>/dev/null || true
ssh_opts=(
  -i "$HOME/.ssh/google_compute_engine"
  -o BatchMode=yes
  -o ConnectTimeout=5
  -o ConnectionAttempts=1
  -o ServerAliveInterval=5
  -o ServerAliveCountMax=3
  -o StrictHostKeyChecking=accept-new
  -o ControlMaster=auto
  -o ControlPath="$ssh_ctl_dir/%C"
  -o ControlPersist=60s
)

# --- Diagnostic log ---
# Per-invocation timestamped log to ~/.moorpost/log/wrapper.log so silent
# failures (rsync stalls, ssh hangs) are diagnosable. The Anthropic plugin
# only reports "subprocess init did not complete within 60000ms" when the
# wrapper times out; without this log the user has no way to know which
# step ate the budget.
LOG_DIR="$HOME/.moorpost/log"
mkdir -p "$LOG_DIR" 2>/dev/null || true
LOG="$LOG_DIR/wrapper.log"
log() {
  printf '[%s pid=%s] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$$" "$*" >> "$LOG" 2>/dev/null || true
}
log "wrapper invoked argc=$# cwd=$PWD vm_ip=$vm_ip"

# --- Reachability probe (with retries, no silent local fallback) ---
# We're routing remote (route_remote=1 enforced upstream). The VM should be
# running and reachable. Probe with retries to absorb transient failures
# typical of sleep/wake (network or VPN takes a few seconds to recover).
#
# CRITICAL: do NOT silently fall back to local on probe failure when the
# session is in remote_sids — that causes JSONL drift (local claude writes
# to a JSONL the user expected to be remote-only, and subsequent rsyncs
# overwrite one side or the other). Instead, exit with a clear error so
# the user can take an explicit action (start the VM, return the session,
# wait for network).
#
# Total probe budget: 3 attempts × (5s ConnectTimeout + 1s sleep) ≈ 18s,
# well under the plugin's 60s subprocess-init deadline.
probe_ok=0
for probe_attempt in 1 2 3; do
  if ssh "${ssh_opts[@]}" -T -o ConnectTimeout=5 "moorpost@${vm_ip}" -- exit 0 \
       </dev/null >>"$LOG" 2>&1; then
    probe_ok=1
    break
  fi
  log "reachability probe attempt $probe_attempt/3 failed (vm_ip=$vm_ip)"
  if [[ $probe_attempt -lt 3 ]]; then
    sleep 1
  fi
done

if [[ $probe_ok -eq 0 ]]; then
  log "reachability probe failed after 3 attempts (vm_ip=$vm_ip) — aborting (NOT routing local: session is in remote_sids)"
  cat >&2 <<EOF
Moorpost: cannot reach the VM at $vm_ip after 3 attempts.

This Claude Code session is routed to the remote VM, but the VM is
unreachable right now. Possible fixes:

  - Wait a moment and retry — the network or VPN may still be
    reconnecting after sleep/wake.
  - Run \`moorpost up\` to start the VM if it has been stopped.
  - Run \`moorpost return\` (or click "Return" on the session in the
    Moorpost sidebar) to bring this session back to local.

Falling back to local automatically would cause JSONL drift between
local and remote — the wrapper refuses to do that for a remote-routed
session.
EOF
  exit 2
fi
log "reachability probe ok"

# Compute the encoded form of the local cwd ONCE — claude derives this
# same encoding via getcwd() (or sees it in JSONL paths), so it's the
# project key for both the history-rsync source/target AND the optional
# precheck below. Done before the first rsync because the eager history
# sync needs it.
encoded_cwd=$(printf '%s' "$PWD" | sed 's/[^a-zA-Z0-9-]/-/g')

# Bridge resolved-cwd-encoded session dir → synced session dir on remote.
#
# Background: the bootstrap creates /Users/<user>/.../<projdir> as a
# symlink to /home/moorpost/moorpost/<slug>. So `cd <localpath>` works
# on remote — but when claude opens a session it calls getcwd(), which
# RESOLVES the symlink and returns /home/moorpost/moorpost/<slug>.
# Claude then encodes that physical path for session lookup
# (`-home-moorpost-moorpost-<slug>`), while `moorpost handoff`'s rsync
# delivered files to the unresolved-encoded path
# (`-Users-...-<projdir>`). Two parallel session dirs, claude reads the
# wrong one, every `--resume <sid>` fails with "No conversation found".
#
# Fix: idempotently symlink the resolved-encoded path to the synced
# dir. From now on, claude's reads (and writes) flow through the
# symlink and end up co-located with the synced JSONLs. If a real
# (non-symlink) dir already exists at the resolved-encoded path
# (claude wrote a session there before this bridge was established),
# move those JSONLs into the synced dir first so they're not orphaned.
#
# Self-healing: this runs on every routed-to-remote spawn so existing
# misaligned VMs (provisioned before this fix shipped) get repaired
# the next time the wrapper is invoked. Idempotent: `ln -sfn` updates
# in place; the consolidation block only fires when the bridge isn't
# already set up.
#
# Same logic also lives in `moorpost handoff` (cmd/handoff.go's
# bridgeRemoteSessionDirs), so a fresh handoff sets it up too. Doing
# it here covers the "wrapper invoked while active=remote, no handoff
# in between" case.
slug=$(jq -r --arg p "$match" '.projects[$p].slug // ""' "$STATE")
if [[ -n "$slug" ]]; then
  match_encoded=$(printf '%s' "$match" | sed 's/[^a-zA-Z0-9-]/-/g')
  resolved_encoded="-home-moorpost-moorpost-${slug}"
  if [[ "$match_encoded" != "$resolved_encoded" ]]; then
    bridge_synced="/home/moorpost/.claude/projects/${match_encoded}"
    bridge_resolved="/home/moorpost/.claude/projects/${resolved_encoded}"
    bridge_synced_q=$(printf '%q' "$bridge_synced")
    bridge_resolved_q=$(printf '%q' "$bridge_resolved")
    log "bridge ssh start: $bridge_synced ↔ $bridge_resolved"
    ssh "${ssh_opts[@]}" -T -o ConnectTimeout=5 "moorpost@${vm_ip}" -- "
      set -e
      mkdir -p $bridge_synced_q
      if [ -d $bridge_resolved_q ] && [ ! -L $bridge_resolved_q ]; then
        for f in $bridge_resolved_q/*.jsonl; do
          [ -f \"\$f\" ] && mv -n \"\$f\" $bridge_synced_q/ || true
        done
        for d in $bridge_resolved_q/*/; do
          [ -d \"\$d\" ] && mv -n \"\$d\" $bridge_synced_q/ || true
        done
        rmdir $bridge_resolved_q 2>/dev/null || rm -rf $bridge_resolved_q
      fi
      ln -sfn $bridge_synced_q $bridge_resolved_q
    " </dev/null 2>>"$LOG" || log "bridge ssh failed (non-fatal)"
    log "bridge ssh done"
  fi
fi

# --- Eager history rsync (always blocks) ---
# Ensure the canonical session JSONL store on remote is up to date before
# remote claude tries `--resume <sid>` or scans the dir to list recent
# conversations. `moorpost handoff` did this once at handoff time, but the
# wrapper can be invoked again later (new chat tab, plugin subprocess for
# title generation, etc.) — and any local writes to ~/.claude/projects/
# <encoded>/ in the meantime would be invisible to remote claude without
# this rsync.
#
# CRITICAL: --update is what makes this safe. Without it, rsync would
# overwrite remote JSONLs with the older local snapshot every time the
# wrapper fires — clobbering claude's just-written turns on remote. The
# repro: send turn A on remote, then turn B; wrapper fires for B,
# rsyncs local (still pre-handoff snapshot) over remote, wiping turn A's
# events. On return, only turn B survives. With --update, rsync compares
# mtime and only copies when local is NEWER than remote — safe in both
# directions: new local-only sessions still propagate; ongoing remote
# work isn't trampled.
#
# Incremental: after the first push, this is typically <1s for a few KB
# of JSONL deltas. Always blocks (history correctness > startup latency).
# --timeout=15: rsync aborts if no I/O for 15s, preventing a stalled
# transfer from consuming the plugin's 60s subprocess-init budget.
local_session_dir="$HOME/.claude/projects/${encoded_cwd}"
if [[ -d "$local_session_dir" ]]; then
  remote_session_dir="/home/moorpost/.claude/projects/${encoded_cwd}"
  log "history rsync start: $local_session_dir → ${vm_ip}:$remote_session_dir"
  if ! rsync -a --update --timeout=15 \
      -e "ssh ${ssh_opts[*]}" \
      "${local_session_dir}/" "moorpost@${vm_ip}:${remote_session_dir}/" \
      2>>"$LOG"; then
    log "history rsync failed — relying on prior handoff sync (next --resume may miss recent JSONL writes)"
  else
    log "history rsync done"
  fi
fi

# Multi-tab safety: when caller passes --resume <sid>, verify the JSONL
# is on remote BEFORE proceeding to the (heavier) CLAUDE_CONFIG_DIR
# rsync + final SSH exec. Runs AFTER the bridge above so we check the
# canonical synced dir (which the bridge just made claude's lookup path
# resolve to). If the SID isn't there — e.g., chat tab opened with a
# locally-created session that never made it through handoff's rsync —
# fall back to local. Mutagen still continuously syncs project files,
# so editing code in that local-fallback tab still propagates to remote.
#
# Skipped when only the baton is in play (no caller --resume): the
# baton is presumed-synced as part of `moorpost handoff` itself.
if [[ -n "$user_resume_sid" ]]; then
  remote_jsonl="/home/moorpost/.claude/projects/${encoded_cwd}/${user_resume_sid}.jsonl"
  if ! ssh "${ssh_opts[@]}" -T -o ConnectTimeout=5 "moorpost@${vm_ip}" -- \
       "test -f $(printf '%q' "$remote_jsonl")" </dev/null 2>>"$LOG"; then
    log "precheck: SID $user_resume_sid not on remote — falling back to local"
    fallback_local "$@"
  fi
fi

# Plugin-mode tempdir sync: when the Anthropic Claude Code plugin spawns
# claude, it sets CLAUDE_CONFIG_DIR to a per-conversation tempdir which
# claude treats as the override for ~/.claude/. Without staging this on
# remote, remote claude with our remote CLAUDE_CONFIG_DIR set looks in
# <remote_config_dir>/projects/<encoded>/ for sessions — but the
# canonical synced session JSONLs live at ~/.claude/projects/<encoded>/
# (synced eagerly above). Result: `claude --resume <sid>` on remote
# panics with "No conversation found".
#
# Two-phase rsync (eager + lazy) — replaces the previous single rsync
# of the full CLAUDE_CONFIG_DIR which routinely took 20-30s on the hot
# path and tripped the plugin's 60s subprocess-init deadline:
#
#   PHASE 1 (eager, blocks before exec): only files claude reads at
#   process start — `.claude.json`, `backups/`, `tasks/`, `hooks/`,
#   `mcp-servers/`. Excludes `agents/`, `commands/`, `skills/`,
#   `plugins/` (consulted only on user action) and `local/`,
#   `shell-snapshots/`, `statsig/`, `todos/`, `*.log` (never useful
#   on remote: machine-local or platform-specific).
#
#   PHASE 2 (lazy, backgrounded after the wrapper exec's the final
#   ssh): everything else, with the never-useful set still excluded.
#   Fire-and-forget — if the user invokes a slash command / subagent
#   / skill before this completes, remote claude reports "not found"
#   and the next spawn picks up the synced files. Acceptable trade-
#   off: cold-start latency saved typically dominates.
#
# After phase 1, we also bridge <remote_config_dir>/projects →
# ~/.claude/projects so the plugin's CLAUDE_CONFIG_DIR override
# transparently uses the unified moorpost-synced session store.
remote_config_dir=""
if [[ -n "${CLAUDE_CONFIG_DIR:-}" ]] && [[ -d "$CLAUDE_CONFIG_DIR" ]]; then
  config_hash=$(printf '%s' "$CLAUDE_CONFIG_DIR" | shasum -a 256 | cut -c1-16)
  remote_config_dir="/home/moorpost/.moorpost/plugin-config/$config_hash"

  # Excludes split: items in `eager_only_excludes` are deferred to phase 2;
  # items in `never_excludes` are never relevant on remote and are excluded
  # from BOTH phases.
  never_excludes=(
    --exclude='projects' --exclude='projects/'
    --exclude='local/'
    --exclude='shell-snapshots/'
    --exclude='statsig/'
    --exclude='todos/'
    --exclude='*.log'
  )
  eager_only_excludes=(
    --exclude='agents/'
    --exclude='commands/'
    --exclude='skills/'
    --exclude='plugins/'
  )

  log "phase1 (eager) cfg rsync start config_hash=$config_hash"
  if ! rsync -a --delete --timeout=15 \
      "${never_excludes[@]}" "${eager_only_excludes[@]}" \
      -e "ssh ${ssh_opts[*]}" \
      "${CLAUDE_CONFIG_DIR}/" "moorpost@${vm_ip}:${remote_config_dir}/" \
      2>>"$LOG"; then
    # rsync may fail if remote path doesn't exist yet — create and retry.
    log "phase1 cfg rsync failed first try — creating remote dir and retrying"
    # </dev/null on every inline ssh: without it, ssh inherits the
    # wrapper's stdin and drains the plugin's stream-json pipe while
    # this command runs. The user's first turn message gets consumed
    # here instead of forwarded to the final claude exec, and claude
    # never responds. Same pattern below.
    ssh "${ssh_opts[@]}" -o ConnectTimeout=5 "moorpost@${vm_ip}" -- \
      "mkdir -p $(printf '%q' "$remote_config_dir")" </dev/null 2>>"$LOG" || true
    if ! rsync -a --delete --timeout=15 \
        "${never_excludes[@]}" "${eager_only_excludes[@]}" \
        -e "ssh ${ssh_opts[*]}" \
        "${CLAUDE_CONFIG_DIR}/" "moorpost@${vm_ip}:${remote_config_dir}/" \
        2>>"$LOG"; then
      log "phase1 cfg rsync failed retry — clearing remote_config_dir; remote claude will use its own ~/.claude"
      remote_config_dir=""
    fi
  fi
  [[ -n "$remote_config_dir" ]] && log "phase1 cfg rsync done"

  # Bridge the projects/ subdir to the canonical session store. Best-effort:
  # if this ssh fails, claude on remote will find no sessions and fall back
  # to a fresh conversation, which is what the previous behavior already
  # produced — so a failure here cannot make things worse.
  if [[ -n "$remote_config_dir" ]]; then
    remote_dir_q=$(printf '%q' "$remote_config_dir")
    ssh "${ssh_opts[@]}" -o ConnectTimeout=5 "moorpost@${vm_ip}" -- "
      set -e
      mkdir -p $remote_dir_q
      if [ -e $remote_dir_q/projects ] && [ ! -L $remote_dir_q/projects ]; then
        rm -rf $remote_dir_q/projects
      fi
      ln -sfn /home/moorpost/.claude/projects $remote_dir_q/projects
    " </dev/null 2>>"$LOG" || log "projects symlink ssh failed (non-fatal)"

    # Phase 2: backgrounded full sync of the previously-deferred subdirs.
    # The wrapper exec's the final ssh below; this subshell becomes
    # orphaned to init/launchd and continues independently. </dev/null
    # detaches stdin so it can't hold the plugin's pipe open.
    log "phase2 (lazy) cfg rsync dispatched in background"
    (
      rsync -a --timeout=30 \
        "${never_excludes[@]}" \
        -e "ssh ${ssh_opts[*]}" \
        "${CLAUDE_CONFIG_DIR}/" "moorpost@${vm_ip}:${remote_config_dir}/" \
        >>"$LOG" 2>&1
      rc=$?
      if [[ $rc -eq 0 ]]; then
        printf '[%s pid=%s] phase2 cfg rsync done\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$$" >> "$LOG" 2>/dev/null || true
      else
        printf '[%s pid=%s] phase2 cfg rsync FAILED rc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$$" "$rc" >> "$LOG" 2>/dev/null || true
      fi
    ) </dev/null >>"$LOG" 2>&1 &
    disown 2>/dev/null || true
  fi
fi

# Compose the remote command. The bootstrap script's abs-path symlink
# means $PWD on the local machine resolves to a valid path on the
# remote (via /Users/.../ → /home/moorpost/moorpost/<slug>), so we can
# pass the literal local cwd as the remote cd target.
remote_cmd="cd $(printf '%q' "$PWD") && "
# Source /etc/moorpost/env so claude on remote sees CLAUDE_CODE_OAUTH_TOKEN
# (and any future auth env vars). `moorpost handoff` populates this file
# via InjectCredential. Without this, the plugin's claude — which runs
# via THIS wrapper, not the moorpost-managed tmux session — has no
# OAuth token and pops the login screen on first prompt. Use `set -a`
# so vars in the file are exported automatically; then `set +a` to
# return to default behavior. The `[ -f ... ]` guard means a fresh
# bootstrap (env not yet populated by handoff) doesn't error out —
# claude will still launch and trigger its own auth flow as before.
remote_cmd+="if [ -f /etc/moorpost/env ]; then set -a; . /etc/moorpost/env; set +a; fi && "
if [[ -n "$remote_config_dir" ]]; then
  remote_cmd+="export CLAUDE_CONFIG_DIR=$(printf '%q' "$remote_config_dir") && "
fi
remote_cmd+="exec claude"
# If the handoff CLI left a pending resume baton (and the caller didn't
# already pass --resume), inject it before the user's args so the remote
# claude session continues the pre-handoff conversation.
if [[ -n "$pending_resume" ]]; then
  remote_cmd+=" --resume $(printf '%q' "$pending_resume")"
fi
for arg in "$@"; do
  remote_cmd+=" $(printf '%q' "$arg")"
done
# Per-project MCP allowlist on remote: skip cloud MCPs (which hang on
# first connect from a freshly-started VM and trip the plugin's 60s
# subprocess-init deadline), keep project-local MCPs (defined in
# <project>/.mcp.json) so any stdio/HTTP servers the project depends on
# still work after handoff.
#
# Mechanism: claude's --strict-mcp-config flag means "only load servers
# from --mcp-config; ignore everything else (including the user's
# claude.ai cloud servers fetched via api.anthropic.com/v1/mcp_servers)".
# Pairing it with --mcp-config <project>/.mcp.json gives us "load ONLY
# this project's local MCPs". If no .mcp.json exists, --strict-mcp-config
# alone loads zero servers — claude inits in ~1s, resumed history loads,
# moorpost's "continue conversation on remote" goal is met.
#
# The bootstrap's abs-path symlink (/Users/<u>/.../<projdir> →
# /home/moorpost/moorpost/<slug>) means $PWD resolves on remote; the
# .mcp.json that mutagen continuously syncs is reachable at the same
# path. No extra rsync needed.
#
# Opt-out for users who DO want all MCPs on remote (manually-authed
# cloud servers, ~/.claude.json mcpServers, etc.): export
# MOORPOST_REMOTE_KEEP_MCP=1. Trade-off: 60s cold-start risk returns
# until claude.ai MCP proxy auth is warm on the VM.
#
# Placed at the END of the arg list so wrapper_test.go assertions on
# substrings like "claude --resume <sid>" still match.
if [[ "${MOORPOST_REMOTE_KEEP_MCP:-0}" != "1" ]]; then
  remote_cmd+=" --strict-mcp-config"
  if [[ -f "$PWD/.mcp.json" ]]; then
    remote_cmd+=" --mcp-config $(printf '%q' "$PWD/.mcp.json")"
  fi
fi

# PTY mode is conditional on whether our stdin is a real terminal:
#   - Interactive shell (`moorpost attach`, direct shell) → -t allocates
#     a PTY so claude's ink TUI renders properly.
#   - Piped invocation (Anthropic Claude Code plugin spawns us with
#     stdin/stdout pipes, no TTY) → -T disables PTY; with -t and no
#     local TTY, ssh prints "Pseudo-terminal will not be allocated"
#     and the remote command fails with exit 255 — which is exactly
#     what the plugin reports as "Claude Code process exited with
#     code 255".
log "exec ssh remote_cmd=$remote_cmd"

# Anthropic Claude Code plugin watchdog kickstart for --resume mode.
#
# The plugin watches the subprocess's stdout for ~60s after spawn; if
# nothing arrives in that window it errors with "Subprocess
# initialization did not complete within 60000ms".
#
# Fresh `claude` (no --resume) on remote produces output ASAP because
# SessionStart hooks (e.g. superpowers' `startup` matcher) fire and
# emit `hook_started` events. But on `claude --resume <sid>`, those
# hooks are skipped — claude considers a resumed session as already
# initialized — so it stays silent until something arrives on stdin.
# The plugin doesn't proactively push input; it WAITS for init. Result:
# 60s timeout, even when remote claude is otherwise healthy.
#
# Workaround: prepend a `control_request` (interrupt) to the stdin
# stream. claude responds with a `control_response` immediately —
# 82-byte stdout output clears the plugin's watchdog. The `init`
# message follows naturally, the user's first turn is unaffected, and
# control_response is a normal protocol message so the plugin doesn't
# misinterpret it.
#
# Only fires for --resume + non-TTY (the only path that hits the
# silence). Fresh sessions and interactive `moorpost attach` paths
# stay on the original `exec ssh` route.
inject_kickstart=0
if [[ -n "$user_resume_sid" ]] && [[ ! -t 0 ]]; then
  # Stream-json input format requires a stable structured message; a
  # control_request with subtype=interrupt is a documented no-op-ish
  # request that always returns success without affecting session
  # state.
  inject_kickstart=1
fi

if [[ -t 0 ]]; then
  exec ssh "${ssh_opts[@]}" -t "moorpost@${vm_ip}" -- "$remote_cmd"
elif [[ "$inject_kickstart" == "1" ]]; then
  # Watchdog-bypass approach: emit a fake stream-json `system/notification`
  # line on OUR stdout (which the Anthropic plugin reads) BEFORE exec'ing
  # ssh. The plugin's "subprocess initialization" timer clears as soon as
  # ANY stdout output arrives. Then ssh+claude run normally — when the
  # user types, claude emits its real `init` and processes the turn.
  #
  # Why not stdin injection: tried `{ printf kickstart; cat; } | ssh ...`
  # and `{ printf kickstart; while read; do echo; done; } | ssh ...`
  # patterns. Both delivered the kickstart fine but subsequent user
  # messages from the plugin's stdin pipe were swallowed somewhere
  # between the wrapper bash and remote claude — could not reproduce
  # the success of running the same pipeline by hand.
  #
  # Why `system/notification` is safe: the plugin's stream-json renderer
  # treats unknown notification keys as no-ops (just adds a status-bar
  # entry). Doesn't disrupt session state, doesn't trigger a turn, and
  # doesn't conflict with the real `init` claude will emit later.
  printf '{"type":"system","subtype":"notification","key":"moorpost-kickstart","text":"Connecting to remote VM...","priority":"info","session_id":"%s"}\n' "$user_resume_sid"
  exec ssh "${ssh_opts[@]}" -T "moorpost@${vm_ip}" -- "$remote_cmd"
else
  exec ssh "${ssh_opts[@]}" -T "moorpost@${vm_ip}" -- "$remote_cmd"
fi
