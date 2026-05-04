# Moorpost

> Tether your laptop to a remote forward base where Claude Code keeps working.

**Status:** v1.0 functional surface complete. Works end-to-end on macOS + GCP, validated against real Ubuntu 24.04 with `claude --version` returning successfully on a fresh VM. Not yet on the VSCode marketplace (deferred to v1.1); install from source via `make install` + `code --install-extension extension/moorpost-X.Y.Z.vsix`. See [RELEASING.md](RELEASING.md) for the v1.0 tagging checklist.

Moorpost lets a developer **work locally by default and hand off to a remote VM with one click when stepping away**. Same surviving-laptop-close benefit as always-remote setups, ~80% cheaper because the VM is stopped between handoffs.

## What it solves

Claude Code's autonomy ceiling has outgrown the laptop. Sonnet 4.5 routinely runs 30+ hour sessions; Opus 4.6 produces ~14.5 hours of human-expert work per run. You can't babysit that on a laptop that sleeps on the train.

Existing solutions force a tradeoff: Codespaces / Cursor cloud lock you in; Coder/DevPod are container-first and not Claude-aware; rolling your own VPS+tmux+mutagen takes a day per project. Moorpost is **BYO-cloud, Claude-aware, and one command** — which is the lane Anthropic itself is leaving open (their Q1 2026 roadmap is Anthropic-managed execution, not BYO-VM).

## Quickstart (60 seconds, given prerequisites)

```bash
# Build moorpost:
cd /path/to/moorpost && make build && make install

# One-time setup — wraps brew/npm installs + gcloud auth + Compute API:
moorpost setup                         # interactive prereq installer
moorpost auth                          # stash Claude OAuth token

# In your project directory:
moorpost init                          # auto-detects gcloud project
moorpost provision                     # creates VM, leaves it stopped
moorpost handoff                       # when stepping away
# laptop sleeps; Claude keeps working on the VM
moorpost return                        # when you're back; stops VM
```

`moorpost setup` covers brew (gcloud, mutagen, tmux), npm (`@anthropic-ai/claude-code`),
`gcloud auth login`, and Compute Engine API enablement so first-run users
don't have to remember each step. Full walkthrough in [docs/quickstart.md](docs/quickstart.md).

## Architecture

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
┌─────────── GCP VM (stopped between handoffs) ───────┐
│  /etc/moorpost/env  ← Claude OAuth token (0600)     │
│  ~/moorpost/<slug>/ ← project working tree (synced) │
│  tmux session "<slug>" running claude --resume      │
└──────────────────────────────────────────────────────┘
```

## How handoff works

- **`moorpost handoff`** verifies you're at a turn boundary, starts the VM (~15s from stopped), syncs project files + `~/.claude/projects/<encoded>/` session state, then runs `claude --resume <session-id>` on the remote.
- **`moorpost return`** is the mirror — pulls files + state back, runs `claude --resume` locally, stops the VM (default).
- The **active-side flag** in `~/.moorpost/state.json` prevents Claude running on both sides simultaneously.

The full design is in [PLUGIN.md](PLUGIN.md) (~770 lines). Highlights:

- **VM-first, not container-first** — native tmux, $4/mo when stopped, no scheduler fights ([§6.4](PLUGIN.md#64-why-vms-not-containers-long-version))
- **Three Go interfaces** (`Provider`, `Agent`, `Sync`) ready for v1.1 Hetzner / v2 AWS+Azure / v2 Aider+Cursor ([§6.6](PLUGIN.md#66-extension-points-so-this-isnt-a-claude-and-gcp-only-product-forever))
- **Sync model** — continuous bidirectional for project files; one-shot at handoff/return for agent session state ([§6.5](PLUGIN.md#65-sync-and-state-model))

## Cost

Realistic monthly cost on GCP us-central1 (e2-standard-2):

| Usage pattern              | Monthly cost (approx) |
|----------------------------|-----------------------|
| 30 hrs/mo remote (overnight tasks) | **~$6** |
| 8 hrs/wk remote (typical handoff)  | **~$13** |
| Always-on `--persistent` mode      | **~$54** |

Default is local-first; opt into always-on with `moorpost up --persistent`. v1.1 ships a Hetzner provider for ~$8/mo always-on.

## Status & roadmap

- **v1.0** (current — functional surface complete, see [RELEASING.md](RELEASING.md) for tag checklist):
  - CLI: 19 commands across 19 internal packages
  - VSCode extension: tree view, smart handoff prompts (focus-loss / idle / OS-sleep), conflict surface, right-click context menu, status bar
  - Cost protection: pre-flight monthly cap, transparent rescaled estimates, VM-side auto-stop on idle (`mode: persistent`)
  - Sync model: continuous bidirectional mutagen for project files; one-shot rsync for agent session state at handoff/return boundaries
  - Conflict resolution: `moorpost conflicts` listing UX + `--prefer-local` / `--prefer-remote` flags on handoff/return
  - Three-interface extensibility (`Provider` / `Agent` / `Sync`) ready for v1.1
- **v1.1**: Hetzner Cloud provider (~$8/mo always-on baseline), real Cloud Billing API integration (replacing the v1.0 list-price estimator behind a `--actual` flag), VSCode marketplace listing.
- **v2**: AWS, Azure, multi-agent (Cursor / Aider / Codex / Gemini), team mode with multi-laptop coordination (per [PLUGIN.md §10 #11](PLUGIN.md)).

Full milestone breakdown in [PLUGIN.md §9](PLUGIN.md#9-implementation-milestones).

## Anthropic Terms-of-Service note

Moorpost stores and forwards the user's **own** Claude Code OAuth token (sourced via `claude setup-token` on the local machine) into a 0600 env file on a VM that the user owns. The token is consumed by the user's own `claude` process running there.

This is **not** a third-party SDK using OAuth tokens (which Anthropic's Feb 2026 ToS update prohibits). Moorpost does not call the Anthropic Messages API directly, does not proxy traffic through Moorpost-controlled infrastructure, and never reads the credential after writing it to the VM env file. See [docs/security.md](docs/security.md) for the full argument and the threat model.

## License

[Apache 2.0](LICENSE). Open source from day 1.

## Acknowledgments

This project exists because of [Latent Advisory](https://latentadvisory.com), an AI-native M&A firm. Moorpost is the OSS spinoff of the internal remote-dev workflow built for the Argus M&A platform.
