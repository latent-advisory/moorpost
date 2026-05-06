# Moorpost — VSCode extension

> Hand off your Claude Code session between your laptop and a remote VM, from inside VSCode.

Companion UI to the [moorpost](https://github.com/latent-advisory/moorpost) CLI. The extension is a thin shell — the CLI does all the real work — but it surfaces every piece of state you care about and removes the need to remember commands.

## Install

The marketplace listing arrives with v1.1. For now, install from a packaged `.vsix`:

```bash
git clone https://github.com/latent-advisory/moorpost.git
cd moorpost
make build install                       # installs the `moorpost` CLI
make package-extension                   # produces extension/moorpost-X.Y.Z.vsix
code --install-extension extension/moorpost-*.vsix
```

The extension expects the `moorpost` CLI on your `PATH`. Override the path via the `moorpost.cliPath` setting if it lives elsewhere.

## The flow

The first time you open a project that hasn't been initialized, the **Moorpost** activity bar item shows a single "Bootstrap" button. Click it once and the wizard:

1. **Detects missing prereqs** (`gcloud`, `mutagen`, `tmux`, `ripgrep`, `rsync`, `node`, `claude`) and offers to `brew`/`apt` install them.
2. **Signs you into Claude** — opens a browser for the OAuth flow, caches the token in your OS keychain.
3. **Picks a workspace folder** and writes `.moorpost/config.yaml` with sensible GCP defaults.
4. **(Optional) provisions the VM** — creates a stopped GCE instance and runs the bootstrap script that installs Node and Claude Code on it.

After that, your daily loop is two commands:

| When | Action | What happens |
|------|--------|--------------|
| Stepping away from the laptop | **Moorpost: Handoff to remote** | Pause local Claude → start the VM (~15s) → sync project files + `~/.claude/projects/<encoded>/` session state → resume `claude --resume <id>` in a remote tmux session → open an integrated terminal attached to it |
| Coming back to the laptop | **Moorpost: Return to local** | Pull project files + session state back → resume `claude --resume` locally → stop the VM |

Both commands are idempotent and abort cleanly if there's a sync conflict — the extension surfaces conflicts with `--prefer-local` / `--prefer-remote` resolutions inline.

### Smart handoff prompts

The extension watches three signals and offers a non-modal "Hand off to remote?" notification when you appear to have stepped away:

- **Window focus loss** — VSCode unfocused for `moorpost.promptOnFocusLossMinutes` (default 30 min)
- **Editor inactivity** — no edits or selection changes for `moorpost.promptOnIdleMinutes` (default 15 min)
- **OS sleep** — wall-clock drift > 60s on the heartbeat tick, indicating the laptop slept

Cooldown of 15 min between prompts. Set either threshold to `0` to disable.

## What you can see

### Status bar (bottom right)

A one-line summary, refreshed every 10s (configurable via `moorpost.statusBarRefreshSeconds`):

```
🟢 Local · VM ready (stopped) · this month: $0.42
```

Click it for the full status pane.

### Project tree (activity bar)

A persistent panel showing every piece of state at a glance:

| Row | Example value | Notes |
|-----|---------------|-------|
| Project | `my-app` | Slug from `.moorpost/config.yaml` |
| Provider | `gcp` | The configured cloud |
| Active side | `local` / `remote` | Which side is currently running Claude |
| VM | `my-app-vm (running)` | Only when provisioned |
| Sync engine | `mutagen` | |
| Cost (MTD) | `$0.42` | Month-to-date estimate, click for breakdown |
| Remote sessions (N) | _expandable_ | Each remote SID with first-message preview |

Right-click any row for context actions (Handoff, Return, Attach, Destroy, Show cost, Show conflicts). The "Refresh" button in the view title re-runs `moorpost status --json`.

### Output panel

`Output → Moorpost` shows activation messages, command exits, and any CLI stderr. Useful when something fails silently.

### Sessions view (commands)

- **Moorpost: Show sessions (local + remote)** — a quick-pick listing every session under `~/.claude/projects/<encoded>/`, marked by side. Pick one to open or return it.
- **Moorpost: Open remote session** — open a session that's currently routed to remote in a fresh terminal.
- **Moorpost: Return this session to local** — return a single session without affecting others.

## Commands reference

All commands live under the **Moorpost** category in the command palette (`⌘⇧P` / `Ctrl⇧P`).

| Command | What it does |
|---------|--------------|
| Bootstrap (one-shot setup) | Wizard: prereqs → sign-in → init → (optional) provision |
| Install prerequisites (setup) | Just the prereq check + install |
| Run doctor (diagnostics) | `moorpost doctor` — verifies gcloud, mutagen, tmux, claude, GCP API enablement |
| Initialize project (choose folder) | Writes `.moorpost/config.yaml` for the chosen folder |
| Sign in (claude OAuth) | `moorpost auth` — caches the OAuth token |
| Provision VM | Creates the GCE VM (left stopped) |
| Handoff to remote | The handoff command |
| Return to local | The return command |
| Switch local ↔ remote | Same as handoff or return, depending on current side |
| Attach to remote tmux | SSH + `tmux attach` to the live Claude session |
| Show status | Full status pane (active side, VM, sync, cost) |
| Show sync conflicts | Lists mutagen conflicts with resolution actions |
| Show sessions (local + remote) | Quick-pick over every session, both sides |
| Show cost details | MTD breakdown by VM-hours, disk, IP |
| Edit project config | Opens `.moorpost/config.yaml` |
| Destroy VM… | Confirms, then deletes the VM (disk preserved by default) |

## Settings

| Setting | Default | Purpose |
|---------|---------|---------|
| `moorpost.cliPath` | `moorpost` | Path to the CLI binary |
| `moorpost.statusBarRefreshSeconds` | `10` | Status-bar poll interval |
| `moorpost.promptOnFocusLossMinutes` | `30` | Idle-prompt threshold for window focus loss; `0` disables |
| `moorpost.promptOnIdleMinutes` | `15` | Idle-prompt threshold for editor inactivity; `0` disables |
| `moorpost.autoAttachOnHandoff` | `true` | After handoff, open a terminal attached to the remote Claude session |
| `moorpost.autoMigrateOnHandoff` | `false` | Skip the "Migrate this conversation?" prompt when the Anthropic Claude Code plugin is the active surface |
| `moorpost.autoStopWhenNoRemoteSessions` | `true` | Auto-stop the VM when no sessions are routed to remote |
| `moorpost.handoffSurface` | `auto` | Which surface handoff swaps: `terminal`, `plugin`, or `auto`-detect |

## Architecture

The extension is **intentionally tiny**. Per [PLUGIN.md §6.1](../PLUGIN.md#61-why-extension--cli-not-just-extension), the CLI is the source of truth; the extension is a thin UI shell.

- **Resilience:** when VSCode crashes or restarts, the remote tmux session and the mutagen sync keep running. Re-opening VSCode picks up exactly where it left off.
- **Terminal-only users** use the CLI directly without ever installing this extension. Every UI action has a stable CLI equivalent.
- **One source of state:** all UI panels read from `moorpost status --json`. There's no parallel state-tracking in the extension.

```
extension/
├── package.json            # manifest, commands, settings, walkthroughs
├── esbuild.js              # bundler config
├── media/walkthrough/      # markdown shown in Get-Started walkthroughs
└── src/
    ├── extension.ts        # activate(): wires everything up
    ├── cli.ts              # child_process wrapper around `moorpost`
    ├── statusBar.ts        # right-aligned status bar item
    ├── treeView.ts         # activity-bar Project tree
    ├── idleMonitor.ts      # focus-loss / idle / OS-sleep heuristics
    ├── claudeTerminal.ts   # detect & re-route Moorpost: Claude terminals
    ├── claudePluginIntegration.ts  # bridge to Anthropic's Claude Code plugin
    ├── sessionTracker.ts   # which sessions are routed where
    ├── sessionList.ts      # parse ~/.claude/projects/<encoded>/<sid>.jsonl
    ├── runState.ts         # transient operation state (e.g., handoff in flight)
    ├── output.ts           # output channel helpers
    └── commands/
        ├── index.ts        # registers every command
        ├── extras.ts       # walkthrough, first-run nudge, context watcher
        └── getStarted.ts   # bootstrap wizard
```

## Develop

```bash
cd extension
npm install
npm run build         # esbuild bundles src → dist/extension.js
npm run watch         # rebuild on change
npm test              # node --test on src/**/*.test.mts
npm run package       # produces moorpost-X.Y.Z.vsix
```

To debug inside VSCode:

1. Open the `extension/` folder in VSCode.
2. Press `F5` — opens an Extension Development Host with Moorpost loaded.
3. Run any **Moorpost** command from the palette.

## License

[Apache 2.0](../LICENSE).
