# Moorpost Quickstart

This walks you through Moorpost end-to-end on macOS. Linux is similar but file paths differ. Total time: ~15 minutes (mostly waiting for the bootstrap to install Node + Claude Code on the VM).

## 1. Prerequisites

You need:

- **macOS or Linux** (Windows: not yet)
- **GCP project** with billing enabled (cost cap below)
- A **Claude Code subscription** (Pro/Max/Team) for the OAuth token, or an `ANTHROPIC_API_KEY` for API-key mode
- **Homebrew** (macOS) or apt (Linux) for installing the rest

## 2. Build moorpost

```bash
git clone https://github.com/latent-advisory/moorpost.git
cd moorpost
make build install   # installs `moorpost` binary to /usr/local/bin
moorpost --version
```

## 3. One-shot setup (the `moorpost setup` command)

```bash
moorpost setup
# Detects which prereqs are missing (gcloud, mutagen, tmux, ripgrep, rsync,
# node, claude). For each missing, prompts to install via brew/npm.
#
# Use --yes to skip per-prereq prompts. Use --dry-run to preview.
```

After setup completes:

```bash
gcloud auth login
gcloud config set project YOUR_GCP_PROJECT
gcloud services enable compute.googleapis.com --project=YOUR_GCP_PROJECT
```

If you skip the last command, `moorpost provision` will fail with a clear hint. See [troubleshooting.md](troubleshooting.md#compute-engine-api-not-enabled).

## 4. Authenticate

This runs `claude setup-token` on your local machine. A browser opens to claude.ai; sign in; copy the code; paste back. The OAuth token is stashed in your **macOS Keychain** (or Linux Secret Service) and reused for every project.

```bash
moorpost auth
# expected: "Authenticated claude-code (oauth-subscription) — token cached locally."
```

You only need to do this once per machine, not per project.

## 5. Initialize a project

```bash
cd /path/to/your/project   # e.g., your existing repo
moorpost init             # auto-detects GCP project from gcloud config
# Or specify explicitly:
# moorpost init --gcp-project=YOUR_GCP_PROJECT --slug=myproject
```

Inspect/edit `.moorpost/config.yaml` if needed. Defaults: `e2-standard-2`, `100GB pd-standard`, `us-central1-a`, local-first mode. Cost: ~$0.067/hr running, $4/mo disk when stopped.

## 6. Provision the VM (one-time per project)

```bash
moorpost provision
# Provisioning myproject-vm in us-central1-a...
# Done. VM myproject-vm (stopped).
# VM is stopped. Run `moorpost handoff` when stepping away, or `moorpost up` for always-on.
```

Takes ~30s for the VM to be created (the bootstrap script will continue running in the background for ~5-7 min installing Node + Claude Code, but you don't need to wait for it).

## 7. The handoff cycle

```bash
# You've been working locally with claude. Now stepping away:
moorpost handoff
# Pause local Claude (be at a turn boundary), then continue? [y/N]: y
# Starting myproject-vm...
# VM running at 35.x.y.z
# Syncing project files → 35.x.y.z:~/moorpost/myproject ...
# Syncing agent session state ...
# Resuming claude on remote (slug=myproject)...
# Done. Local Claude is now inactive.

# Close laptop. Claude keeps working on the VM.

# When you come back:
moorpost return
# Syncing project files ← 35.x.y.z:~/moorpost/myproject ...
# Syncing agent session state ←
# Stopping myproject-vm...
# Done. Local Claude is active again. Run `claude --resume` to pick up where remote left off.
```

## 8. Other useful commands

```bash
moorpost status            # show project state, active side, VM cache, MTD cost
moorpost status --json     # machine-readable
moorpost doctor            # diagnostics: gcloud, mutagen, tmux, claude on PATH; GCP preflight
moorpost up                # start the VM without doing handoff (e.g., for `moorpost attach`)
moorpost attach            # ssh + tmux attach to remote claude session
moorpost down              # stop the VM (preserves disk)
moorpost snapshot          # back up the disk before risky operations
moorpost reset             # snapshot + destroy + re-provision (counters bit-rot)
moorpost destroy           # permanently delete VM + disk
moorpost cost              # current period's cost estimate
```

`moorpost --help` lists all 16.

## 9. Daily workflow

1. `moorpost handoff` when stepping away (or in v0.2 the lid-close prompt fires automatically)
2. Close laptop / leave
3. `moorpost return` when back; VM stops; local `claude --resume` picks up

Between handoffs you pay only the disk fee ($4-10/mo depending on disk size).

## What's next

- [security.md](security.md) — what secrets are where, threat model, ToS argument
- [troubleshooting.md](troubleshooting.md) — common errors and fixes
- [../PLUGIN.md](../PLUGIN.md) — full design doc
