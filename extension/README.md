# Moorpost — VSCode extension

> Hand off your Claude Code session between your laptop and a remote VM, from inside VSCode.

Companion UI to the [moorpost](https://github.com/latent-advisory/moorpost) CLI. The extension is a thin shell — the CLI does all the real work — but it surfaces every piece of state you care about and removes the need to remember commands.

## Requirements

- macOS or Linux (Windows is not supported; use WSL).
- A **Google Cloud Platform** project. GCP is the only provider Moorpost ships with today; AWS / Azure are on the roadmap. You'll need permission to create Compute Engine instances in the project.
- A Claude Code subscription (or `ANTHROPIC_API_KEY`).

## Install

Install from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=LatentAdvisory.moorpost-vscode):

```sh
code --install-extension LatentAdvisory.moorpost-vscode
```

The first time the extension activates, it auto-downloads the matching `moorpost` CLI binary from the GitHub release, verifies its SHA-256 against the published `SHA256SUMS`, and installs it to `~/.local/bin/moorpost`. If that directory isn't on your `PATH`, the extension also writes the absolute path to its own `moorpost.cliPath` setting so commands keep working without shell changes.

If the auto-install fails (offline, sandboxed CI environment, etc.), a toast surfaces a one-click link to the [GitHub release page](https://github.com/latent-advisory/moorpost/releases/latest) where you can grab the binary manually.

## The flow

A click-by-click walkthrough of what you actually see in VSCode.

### Step 1 — open VSCode in a project

After installing the `.vsix`, the **Moorpost** entry appears in the activity bar (left rail, cloud icon). Click it.

```
┌────────────────────────────────────────────────────────────────┐
│ MOORPOST                                                  [↻]  │
├────────────────────────────────────────────────────────────────┤
│  No Moorpost project here yet.                                 │
│                                                                │
│  Moorpost lets you hand off Claude Code sessions               │
│  between your laptop and a remote VM.                          │
│                                                                │
│              ┌──────────────────┐                              │
│              │   Bootstrap      │  ← click here                │
│              └──────────────────┘                              │
└────────────────────────────────────────────────────────────────┘
```

### Step 2 — Bootstrap (one-shot wizard)

Clicking **Bootstrap** opens a guided sequence in an integrated terminal. Each step is skipped if it's already done, so re-running is safe.

| # | What you see | What it does |
|---|--------------|--------------|
| 1 | Terminal output: `Detecting prereqs...` followed by per-tool checkmarks or `[install]` prompts | Probes `gcloud`, `mutagen`, `tmux`, `ripgrep`, `rsync`, `node`, `claude` on PATH; installs missing via `brew` / `apt`. |
| 2 | A browser tab opens to **claude.ai** | Standard Claude OAuth flow. The token is cached in your OS keychain. |
| 3a | Quick-pick: **"Pick a gcloud configuration for Moorpost"** with one row per existing configuration plus an **"Add a new gcloud account"** option | Pins the chosen configuration in `.moorpost/config.yaml` so all subsequent `gcloud` calls use it. |
| 3b | Quick-pick: **"Pick a GCP machine type for the remote VM (★ = recommended)"** with hourly rate + ~$X/mo estimate per row | Stores `machine_type` in `.moorpost/config.yaml`. |
| 4 | Modal: **"Provision the VM now? (You can also do this later.)"** → Yes / No | If yes: creates a GCE instance, runs the bootstrap script (Node + Claude Code), leaves the VM **stopped**. ~3 min wall-clock. |

After Bootstrap finishes, the Moorpost panel updates:

```
┌────────────────────────────────────────────────────────────────┐
│ MOORPOST                                                  [↻]  │
├────────────────────────────────────────────────────────────────┤
│  Project:        my-app                                        │
│  Provider:       gcp                                           │
│  Active side:    local                          ← click to     │
│  VM:             my-app-vm (stopped)              switch       │
│  Sync engine:    mutagen                                       │
│  Cost (MTD):     $0.04                                         │
└────────────────────────────────────────────────────────────────┘
```

And the status bar at the bottom right reads:

```
☁ local · stopped · $0.04
```

### Step 3 — work locally as usual

Open a terminal and run `claude` (or use Anthropic's official Claude Code VSCode extension's panel — Moorpost auto-detects it). Nothing routes through Moorpost yet; you're paying $0.

### Step 4 — Handoff (when you step away)

> **If your session runs in Anthropic's Claude Code panel:** close that panel before triggering handoff (Moorpost shows a pre-flight prompt). VSCode doesn't allow one extension to close another's panel programmatically. See [Working with Anthropic's Claude Code VSCode extension](#working-with-anthropics-claude-code-vscode-extension) below for details.

Three ways to trigger:

- **Click the status bar** (`☁ local · stopped · $0.04`) → pick **"Handoff a session to remote"**.
- **Right-click "Active side: local" in the Moorpost panel** → **Handoff to remote**.
- **Wait for a smart prompt**: a non-modal notification *"Hand off to remote? You've been away for 30 min."* appears automatically (see [Smart handoff prompts](#smart-handoff-prompts) below).

What you see, in order:

1. Notification: *"Pause local Claude (be at a turn boundary), then continue?"* → Yes.
2. Notification: *"Starting my-app-vm…"* (~15s).
3. Notification: *"Syncing project files → my-app-vm:~/moorpost/my-app …"* (mutagen progress).
4. Notification: *"Syncing agent session state …"*.
5. An integrated terminal appears titled **"Moorpost: Claude (remote)"**, already attached to the remote tmux session running `claude --resume <id>`. Type prompts here as if Claude were local — they go to the VM.
6. Status bar updates to `☁ 1 on remote · running · $0.04`.

Close the laptop. The VM keeps running, the sync keeps mirroring, Claude keeps working.

### Step 5 — Return (when you come back)

> **Same caveat as handoff:** if the session runs in Anthropic's Claude Code panel on the remote, close that panel before triggering return.

Same three triggers, mirrored:

- **Click the status bar** (`☁ 1 on remote · running · $0.42`) → pick **"Return a session to local"**.
- **Right-click "Active side: 1 on remote"** in the Moorpost panel → **Return to local**.
- **Right-click an individual remote session** under the **Remote sessions** group in the panel → **Return** (per-session granularity if you have multiple).

What you see, in order:

1. Notification: *"Pulling project files ← my-app-vm:~/moorpost/my-app …"*.
2. Notification: *"Pulling agent session state ←"*.
3. The "Moorpost: Claude (remote)" terminal closes; a new one opens titled **"Moorpost: Claude (local)"**, attached to your local `claude --resume`.
4. Notification: *"Stopping my-app-vm…"* (default; toggle via `moorpost.autoStopWhenNoRemoteSessions`).
5. Status bar back to `☁ local · stopped · $0.42`.

### Always-on remote (skip the return)

If you don't care about cost optimization and just want the laptop-close-doesn't-matter benefit, you can stay on remote indefinitely after the first handoff:

- Trigger handoff once. The session is now on the VM.
- Keep working from the **"Moorpost: Claude (remote)"** terminal (or the Anthropic Claude Code panel, if that's your routed surface). Files round-trip via mutagen continuously, so editing a file in VSCode locally is reflected in the VM and vice versa.
- Skip the return. Open VSCode tomorrow morning, the session is still running on the VM, the terminal reattaches automatically.

The VM still auto-stops on idle (default 60 min, configurable via `persistent.auto_stop_minutes` in `.moorpost/config.yaml`), and the next prompt re-starts it transparently — so your bill is bounded even in always-on mode. The tradeoff vs. local-first: latency is whatever your VM's region adds (~30 ms typical), and you're paying the full hourly rate while the VM is running, not just while you're actively prompting.

Mix and match: the per-session model means you can have one session always-on-remote (a long-running agent loop) while another session does the local-first dance for ergonomic editing.

### Smart handoff prompts

The extension watches three signals and offers a non-modal *"Hand off to remote?"* notification when you appear to have stepped away — so you don't have to remember:

- **Window focus loss** — VSCode unfocused for `moorpost.promptOnFocusLossMinutes` (default 30 min)
- **Editor inactivity** — no edits or selection changes for `moorpost.promptOnIdleMinutes` (default 15 min)
- **OS sleep** — wall-clock drift > 60s on the heartbeat tick, indicating the laptop slept

Cooldown of 15 min between prompts. Set either threshold to `0` to disable.

### Conflict surface

If `mutagen` flags a sync conflict (file edited on both sides), the handoff or return aborts with a notification: *"3 sync conflicts. Resolve?"* Click it to open the **Sync conflicts** view, which lists each path and offers per-conflict **Prefer local** / **Prefer remote** buttons. Re-run the handoff/return after resolving.

### Working with Anthropic's Claude Code VSCode extension

If [Anthropic's official Claude Code extension](https://marketplace.visualstudio.com/items?itemName=anthropic.claude-code) is installed, Moorpost auto-detects it. On handoff, instead of (or in addition to) opening the "Moorpost: Claude (remote)" terminal, Moorpost re-routes that extension's panel to talk to the remote `claude` process. The model retains full conversation context; the panel scrollback resets (prior messages remain in Claude Code's history list).

> **⚠️ You must close the Claude Code panel before handoff/return.** VSCode does not let one extension programmatically close another extension's panel, so Moorpost cannot do this for you. When you trigger handoff or return on a session running in Anthropic's panel, Moorpost shows a pre-flight notification (*"Moorpost: close the Claude Code panel for …, then click 'I closed it' to handoff."*) — close the tab in VSCode first, then click the button. Terminal sessions don't need this step; only the Anthropic-plugin surface does.

Control which surface gets swapped via the `moorpost.handoffSurface` setting:

- `auto` (default) — terminal if a "Moorpost: Claude" terminal is open, plugin if Anthropic's extension is installed and routed, otherwise terminal.
- `terminal` — only ever swap the Moorpost terminal.
- `plugin` — only ever swap the Anthropic extension's panel.

## What you can see

### Status bar (bottom right)

A one-line summary, refreshed every 10s (configurable via `moorpost.statusBarRefreshSeconds`). The side label reflects per-session routing:

```
☁ local · stopped · $0.42                  ← all sessions on this laptop
☁ 1 on remote · running · $0.42            ← one session routed to the VM
☁ 3 on remote · running · $1.08            ← multiple sessions on the VM
```

Click it to open a quick-pick that routes to the next-needed step (bootstrap → sign-in → provision when setup is incomplete) or, once configured, lets you pick **Handoff a session to remote**, **Return a session to local** (when any session is on remote), or **Show status details**.

### Project tree (activity bar)

A persistent panel showing every piece of state at a glance:

| Row | Example value | Notes |
|-----|---------------|-------|
| Project | `my-app` | Slug from `.moorpost/config.yaml` |
| Provider | `gcp` | The configured cloud |
| Active side | `local` / `1 on remote` / `N on remote` | Per-session: counts how many sessions are routed to the VM. Click to open the Handoff/Return/Status quick-pick. |
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
| Switch local ↔ remote | Opens the Handoff/Return/Status quick-pick (also bound to the status-bar click) |
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
