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
  if [[ $# -gt 0 ]] && [[ "$1" == /* ]] && [[ -x "$1" ]]; then
    exec "$@"
  fi
  local real
  if real="$(find_real_claude)"; then
    exec "$real" "$@"
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

active=$(jq -r --arg p "$match" '.projects[$p].active_side // "local"' "$STATE")
[[ "$active" == "remote" ]] || fallback_local "$@"

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

ssh_opts=(
  -i "$HOME/.ssh/google_compute_engine"
  -o BatchMode=yes
  -o ConnectTimeout=15
  -o StrictHostKeyChecking=accept-new
)

# Plugin-mode tempdir sync: when the Anthropic Claude Code plugin spawns
# claude, it sets CLAUDE_CONFIG_DIR to a per-conversation tempdir holding
# the resumable session JSONL. Without staging this on remote, remote
# claude's `-r <sid>` finds nothing → conversation appears fresh in the
# panel after handoff. Sync the dir to a stable remote path so remote
# claude resumes correctly. The remote path is keyed off the local one
# so subsequent invocations against the same tempdir reuse the same
# remote dir (rsync incremental, fast).
remote_config_dir=""
if [[ -n "${CLAUDE_CONFIG_DIR:-}" ]] && [[ -d "$CLAUDE_CONFIG_DIR" ]]; then
  config_hash=$(printf '%s' "$CLAUDE_CONFIG_DIR" | shasum -a 256 | cut -c1-16)
  remote_config_dir="/home/moorpost/.moorpost/plugin-config/$config_hash"
  if ! rsync -a --delete \
      -e "ssh ${ssh_opts[*]}" \
      "${CLAUDE_CONFIG_DIR}/" "moorpost@${vm_ip}:${remote_config_dir}/" \
      2>/dev/null; then
    # rsync may fail if remote path doesn't exist yet — create and retry.
    ssh "${ssh_opts[@]}" "moorpost@${vm_ip}" -- \
      "mkdir -p $(printf '%q' "$remote_config_dir")" 2>/dev/null || true
    rsync -a --delete \
      -e "ssh ${ssh_opts[*]}" \
      "${CLAUDE_CONFIG_DIR}/" "moorpost@${vm_ip}:${remote_config_dir}/" \
      >&2 || remote_config_dir=""
  fi
fi

# Compose the remote command. The bootstrap script's abs-path symlink
# means $PWD on the local machine resolves to a valid path on the
# remote (via /Users/.../ → /home/moorpost/moorpost/<slug>), so we can
# pass the literal local cwd as the remote cd target.
remote_cmd="cd $(printf '%q' "$PWD") && "
if [[ -n "$remote_config_dir" ]]; then
  remote_cmd+="export CLAUDE_CONFIG_DIR=$(printf '%q' "$remote_config_dir") && "
fi
remote_cmd+="exec claude"
for arg in "$@"; do
  remote_cmd+=" $(printf '%q' "$arg")"
done

# -t allocates a pty for interactive claude (required for ink-style TUI).
exec ssh "${ssh_opts[@]}" -t "moorpost@${vm_ip}" -- "$remote_cmd"
