# Moorpost

> Tether your laptop to a remote forward base where Claude Code keeps working.

**Status:** Pre-alpha · design phase. Not yet usable.

Moorpost is a VSCode extension and companion CLI that gives a solo developer a one-click, persistent, bring-your-own-cloud Claude Code workstation:

- **Provisions** a GCP VM in ~60 seconds
- **Bootstraps** Claude Code, tmux, and dev tools on the VM
- **Persists** your Claude Code session under tmux — close the laptop, the agent keeps working
- **Syncs** your project folder bidirectionally between local and remote, including non-git files
- **Stops** the VM with one click when you're done, so you only pay for what you use

It exists because the autonomy ceiling of Claude Code (Sonnet 4.5: 30+ hour sessions; Opus 4.6: ~14.5 hours of human-expert work per run) has outgrown the laptop, but every existing remote dev tool either locks you into a managed cloud or doesn't know what Claude Code is.

## Status

Design doc only. See [PLUGIN.md](./PLUGIN.md) for the full spec, architecture, milestones, and open questions.

Implementation begins after the v0.1 walking-skeleton scope is locked.

## Apache 2.0

Open source from day 1.
