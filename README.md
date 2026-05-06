# Moorpost

> Tether your laptop to a remote forward base where Claude Code keeps working.

[![License](https://img.shields.io/badge/license-Apache_2.0-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-v1.0_release_candidate-orange.svg)](RELEASING.md)

Moorpost is a CLI + VSCode extension that lets you **work locally by default and hand off your Claude Code session to a remote VM with one click when stepping away** — laptop sleeps, VM keeps working, you pull the work back when you return.

Same surviving-laptop-close benefit as always-on remote setups, **~80% cheaper** because the VM is stopped between handoffs.

---

## Table of contents

- [Why Moorpost](#why-moorpost)
- [Install](#install)
- [Quickstart](#quickstart-60-seconds)
- [How it works](#how-it-works)
- [Cost](#cost)
- [Roadmap](#roadmap)
- [Anthropic ToS note](#anthropic-terms-of-service-note)
- [Contributing](#contributing)
- [License](#license)

## Why Moorpost

Claude Code's autonomy has outgrown the laptop. Sonnet 4.5 routinely runs 30+ hour sessions; Opus 4.6 produces ~14.5 hours of human-expert work per run. You can't babysit that on a laptop that sleeps on the train.

The existing options force a tradeoff:

| Option                                    | Tradeoff                                                              |
|-------------------------------------------|-----------------------------------------------------------------------|
| Run locally, prevent sleep                | Heat, battery drain, can't move                                        |
| Codespaces / Cursor cloud                 | Vendor lock-in, no BYO-cloud, no non-git file sync                     |
| Coder / DevPod                            | Container-first, enterprise-shaped, not Claude-aware                   |
| Hand-rolled VPS + tmux + mutagen          | Works, but a day of setup per project                                  |
| Anthropic-managed Claude execution        | Anthropic-only; no custom images, no BYO-cloud                         |

**Moorpost is the BYO-cloud, Claude-aware, one-command option.** The lane Anthropic itself is leaving open: their roadmap is Anthropic-managed execution, not BYO-VM.

### Relationship to the official Claude Code VSCode extension

[Anthropic's Claude Code extension](https://marketplace.visualstudio.com/items?itemName=anthropic.claude-code) gives you a local Claude session inside VSCode. Moorpost doesn't replace it — Moorpost adds the **handoff layer** so the same conversation can move between your laptop and a remote VM. If you have the Anthropic extension installed, the Moorpost extension auto-detects it and routes its panel through the local↔remote swap on every handoff (configurable via `moorpost.handoffSurface`). If you don't, Moorpost spawns its own integrated terminal running `claude` — same UX, terminal-based.

## Install

You'll need macOS or Linux, a cloud provider account (GCP for v1.0), and a Claude Code subscription (or `ANTHROPIC_API_KEY`).

```bash
# Build from source (marketplace listing comes with v1.1)
git clone https://github.com/latent-advisory/moorpost.git
cd moorpost
make build install         # installs `moorpost` to /usr/local/bin

# Optional: VSCode extension
make package-extension     # produces extension/moorpost-X.Y.Z.vsix
code --install-extension extension/moorpost-*.vsix
```

See [docs/quickstart.md](docs/quickstart.md) for the full walkthrough.

## Quickstart (60 seconds)

```bash
# One-time setup — installs prereqs (gcloud, mutagen, tmux), runs OAuth login
moorpost setup
moorpost auth                          # stash Claude OAuth token in OS keychain

# Per-project setup
cd /path/to/your/project
moorpost init                          # auto-detects gcloud project
moorpost provision                     # creates VM, leaves it stopped

# The handoff cycle
moorpost handoff                       # when stepping away
# laptop sleeps; Claude keeps working on the VM
moorpost return                        # when you're back; stops VM
```

That's it. `moorpost --help` lists all commands.

## How it works

```
┌──────────────────────── Local ────────────────────────┐
│  Claude Code (active by default)                      │
│  moorpost CLI ─── handoff ──▶ ┐                       │
│  moorpost CLI ◀── return ──── │                       │
└───────────────────────────────┼───────────────────────┘
                                │
                          SSH + mutagen
                                │
                                ▼
┌────────── Cloud VM (stopped between handoffs) ──────┐
│  /etc/moorpost/env  ← Claude OAuth token (0600)     │
│  ~/moorpost/<slug>/ ← project working tree (synced) │
│  tmux session "<slug>" running claude --resume      │
└──────────────────────────────────────────────────────┘
```

- **`moorpost handoff`** verifies you're at a turn boundary, starts the VM (~15s from stopped), syncs project files + `~/.claude/projects/<encoded>/` session state, then runs `claude --resume <session-id>` on the remote.
- **`moorpost return`** is the mirror — pulls files + session state back, runs `claude --resume` locally, stops the VM.
- An **active-side flag** in `~/.moorpost/state.json` prevents Claude running on both sides simultaneously.

The full design is in [PLUGIN.md](PLUGIN.md). Highlights:

- **VM-first, not container-first** — native tmux, $4/mo when stopped, no scheduler fights
- **Three Go interfaces** (`Provider`, `Agent`, `Sync`) — adding a cloud, agent, or sync engine is a new file, not a refactor
- **Sync model** — continuous bidirectional for project files; one-shot at handoff/return for agent session state
- **Conflict resolution** — `moorpost conflicts` UX + `--prefer-local` / `--prefer-remote` flags

## Cost

Realistic monthly cost on GCP `us-central1` (`e2-standard-2`):

| Usage pattern                             | Monthly cost (approx) |
|-------------------------------------------|-----------------------|
| 30 hrs/mo remote (overnight tasks)        | **~$6**               |
| 8 hrs/wk remote (typical handoff cadence) | **~$13**              |
| Always-on (`--persistent` mode)           | **~$54**              |

Default is local-first. Opt into always-on with `moorpost up --persistent`.

`moorpost cost` shows the current month-to-date estimate. Set a hard cap in `.moorpost/config.yaml`:

```yaml
cost:
  monthly_cap_usd: 50
  alert_thresholds: [10, 25]
```

Moorpost refuses to start the VM if the cap would be exceeded.

## Roadmap

### v1.0 (release candidate)

- CLI: 19 commands across 19 internal packages, 75% test coverage
- VSCode extension: tree view with per-session routing, smart handoff prompts (focus-loss / idle / OS-sleep), conflict surface, right-click context menu, status bar with Handoff/Return/Status quick-pick, machine-type picker on init
- Cost protection: pre-flight monthly cap, list-price estimator, VM-side auto-stop on idle in `--persistent` mode
- Sync model: continuous bidirectional mutagen for project files; one-shot rsync for agent session state at handoff/return boundaries
- Three-interface extensibility (`Provider` / `Agent` / `Sync`)

### v1.1 — broader local clients + better cost visibility

- **Real Cloud Billing API integration** behind `--actual` (replacing the v1.0 list-price estimator)
- **VSCode marketplace listing**
- **Windows local client** (CLI only initially)

### v1.2 — terminal-first ergonomics

The CLI is already first-class, but the terminal experience can be sharper:

- **Shell completions** (bash / zsh / fish) for all commands and flags
- **`moorpost shell`** — raw SSH into the VM, no tmux attach
- **`--json` everywhere** — every read command emits machine-parseable JSON for scripting
- **TUI dashboard** — `moorpost watch` shows live status, sync conflicts, cost in a single screen
- **Claude Code terminal-mode plugin** — a small companion that reads/writes Moorpost state from inside a `claude` session, so the agent can self-handoff via tool calls

### v2 — multi-cloud, multi-agent, teams

- **AWS** (EC2 + Cost Explorer + IAM)
- **Azure** (Compute + Cost Management + AAD)
- **DigitalOcean / Vultr / Linode** (commodity VPS)
- **Fly.io** (per-second billing, scale-to-zero)
- **Cursor CLI** agent
- **Aider** agent
- **Codex** agent
- **Gemini** agent
- **Multi-agent mode** — several agents in one project, each in its own tmux window
- **Team mode** — shared VM pool, RBAC, fleet-mode lock for multi-machine coordination
- **Devcontainer support** — opt-in `moorpost devcontainer up` for reproducible images on top of the persistent VM
- **Syncthing sync driver** — for decentralized multi-machine fleets
- **JetBrains Gateway plugin** (CLI is reused; UI is new)

Full milestone breakdown in [PLUGIN.md §9](PLUGIN.md#9-implementation-milestones).

## Anthropic Terms-of-Service note

Moorpost stores and forwards the user's **own** Claude Code OAuth token (sourced via `claude setup-token` on the local machine) into a `0600` env file on a VM that the user owns. The token is consumed by the user's own `claude` process running there.

This is **not** a third-party SDK using OAuth tokens (which Anthropic's Feb 2026 ToS update prohibits). Moorpost does not call the Anthropic Messages API directly, does not proxy traffic through Moorpost-controlled infrastructure, and never reads the credential after writing it to the VM env file. See [docs/security.md](docs/security.md) for the full argument and the threat model.

## Contributing

Issues, discussions, and PRs welcome at [github.com/latent-advisory/moorpost](https://github.com/latent-advisory/moorpost).

If you're adding a new cloud, a new agent, or a new sync engine, see [PLUGIN.md §6.6](PLUGIN.md#66-extension-points-so-this-isnt-a-claude-and-gcp-only-product-forever) — there's a defined interface for each, and the existing implementations are short enough to use as templates.

Releases follow the checklist in [RELEASING.md](RELEASING.md).

## License

[Apache 2.0](LICENSE). Open source from day 1.

## Acknowledgements

Sponsored by [Latent Advisory](https://latentadvisory.com).
