# Moorpost — Design Doc (v1)

> Tether your laptop to a remote forward base where Claude Code keeps working.

**Status:** Draft v0.1 · 2026-05-04
**License:** Apache 2.0

---

## 1. One-line pitch

Moorpost is a VSCode extension + companion CLI that lets a solo developer **work locally by default, then hand off to a remote GCP VM with one click when stepping away** — so Claude Code keeps working through laptop sleep, overnight tasks, or device hops, without paying for an always-on VM.

The core insight: an always-running remote VM costs ~$50/mo and adds latency to every keystroke when you're at your laptop. A *handoff* model — local for active work, remote only when you're not there — costs **$6-13/mo** and is faster locally. Same surviving-laptop-close benefit, ~80% cheaper, better local UX.

## 2. Problem

Claude Code's autonomy ceiling has outgrown the laptop. Sonnet 4.5 routinely runs 30+ hour sessions; Opus 4.6 produces ~14.5 hours of human-expert work per run. Nobody babysits that on a laptop that sleeps on the train.

Every solution today forces a tradeoff:

| Solution                     | Tradeoff                                                                 |
|-----------------------------|--------------------------------------------------------------------------|
| Run locally + don't sleep    | Laptop heat, battery, can't move                                          |
| GitHub Codespaces / Cursor cloud | Vendor lock-in, no BYO-cloud, no non-git file sync                    |
| Coder / DevPod              | Container-first (no native tmux UX); enterprise-shaped; not Claude-aware  |
| Anthropic Routines / Cloud Sandbox | Anthropic-managed only; can't load custom images                    |
| Hand-rolled VPS + tmux + mutagen | Works (it's what we use) but takes a day to set up per project       |

**Moorpost target:** the developer who currently hand-rolls VPS + tmux + mutagen + VSCode Remote-SSH and wants that to be a 60-second one-time setup per project.

## 3. Strategic positioning

The 2026 lanes are clearly divided:

- **Coder** owns enterprise self-hosted dev environments
- **GitHub Codespaces / Cursor Cloud** own managed-cloud
- **DevPod** owns container portability
- **Anthropic** owns Anthropic-hosted Claude Code sessions
- **Moorpost** owns BYO-cloud-VM + persistent Claude Code + non-git sync for solo devs and small teams

Anthropic's own engineers run Claude Code on Coder.com (per Coder's "Building for 2026" post), which is the strongest signal that Anthropic is delegating the BYO-cloud lane rather than building it themselves. Window of opportunity: 6-12 months before this lane gets contested.

## 4. Goals & non-goals

### v1 goals

1. **Local-first with one-click handoff** — default mode is "work locally, push to remote when stepping away, pull back when returning." Always-remote is opt-in (`--persistent`).
2. **Session-state migration** — Claude Code's `~/.claude/projects/<path>/` syncs alongside project files so `claude --resume` picks up seamlessly on the other side.
3. **One-click VM provisioning on GCP** from inside VSCode (and matching CLI command).
4. **Automatic bootstrap**: Claude Code, tmux, ripgrep, fd, uv, Node 20, basic dev deps installed on first boot.
5. **Persistent Claude Code session** under tmux on remote — survives laptop close, SSH disconnect, VSCode reload.
6. **Bidirectional file sync** between local project folder and remote, including non-git files (e.g., `.docx`, `.xlsx`, design docs). Mutagen-backed.
7. **Cost-aware UX**: VSCode status bar shows VM state + monthly cost-to-date; default-stopped VM ($4/mo disk) keeps cost predictable.
8. **Coding-agent-friendly CLI**: every UI action has a stable CLI equivalent, so a Claude Code instance running on the user's local machine can self-handoff to a Moorpost VM and continue its work there.
9. **Open source from day 1** under `latent-advisory/moorpost`, Apache 2.0.

### v1 non-goals

- Multi-cloud (AWS, Azure, etc.) — defer to v2.
- Multi-user / team workspaces — defer to v2.
- Web UI / phone control — Anthropic ships Remote Control; don't compete on that surface.
- AI-tool-agnostic abstraction (Cursor, Codex, Aider) — earn the right to expand by being the best Claude Code tool first.
- Custom container images / devcontainers — bare VM in v1.0; opt-in `moorpost devcontainer up` mode arrives in v2 only if user demand surfaces.
- Windows local client — macOS + Linux only in v1 (Windows in v1.1 if demand exists).
- Anything that competes with Anthropic's own roadmap (Routines, Managed Agents, Cloud Sandbox, Ultraplan, Ultrareview).

## 5. UX walkthrough — the magic moment

**First time ever** (any project, any machine): User runs `moorpost auth` (or clicks "Sign in" in the extension). A local browser window opens to claude.ai. User signs in, copies a code, pastes it back. Done — token cached in macOS Keychain / Linux Secret Service. **This happens entirely on the local machine; no SSH, no remote OAuth dance.**

**First time for a project:** User installs the Moorpost VSCode extension. They open a project folder. Status bar shows a `Moorpost: not configured` chip. They click it.

```
┌──────────────────────────────────────────────┐
│  Configure Moorpost for this project         │
├──────────────────────────────────────────────┤
│  GCP project: [your-gcp-project ▾]           │
│  Region:      [us-central1   ▾]              │
│  Machine:     [e2-standard-2 ▾]              │
│   $0/mo stopped · ~$0.067/hr running          │
│  Disk:        [100 GB pd-standard, $4/mo]    │
│  Mode:        [☑ Local-first (recommended)]  │
│               [☐ Always-remote]              │
│                                              │
│  [ Provision (VM stays stopped) ]            │
└──────────────────────────────────────────────┘
```

VM is provisioned and bootstrapped (~90s), then **stopped**. Total cost so far: $0. Local Claude Code works as it always has. Status bar:

```
🟢 Local · VM ready (stopped) · this month: $0.04
```

**The handoff moment** (the actual product):

User has been coding locally with Claude for 4 hours. Now they need to leave for dinner but Claude is mid-refactor. They press **Cmd-Shift-P → Moorpost: Handoff** (or click the status bar):

1. Local Claude is paused at the next turn boundary
2. VM starts (~15s from stopped)
3. Mutagen syncs the project + `~/.claude/projects/<path>/` session state to the VM
4. Remote `claude --resume <session-id>` picks up the conversation in tmux
5. Status bar flips to:

```
🌊 Remote · 0h 12m · this month: $0.18 · [return]
```

User closes the laptop. Claude continues on the VM.

User comes back at midnight. Opens VSCode. Status bar shows `🌊 Remote · 6h 04m · this month: $0.41`. They click **Return**. Mutagen syncs back, remote Claude is paused, local `claude --resume` picks up. They're back to local with all the overnight progress.

**Subsequent handoffs**: ~15 seconds total. Cost per handoff hour: ~$0.07. Realistic monthly cost for 8 hours/week of remote use: **~$6**.

**Always-remote opt-in:** `moorpost up --persistent` keeps the VM running 24/7. Status bar shows continuous remote-attach. For users who want it.

**Coding-agent flow:** A Claude Code instance running locally can run `moorpost handoff --no-block` and continue its task on the remote without user interaction. The agent self-hands-off when it knows the user is stepping away.

## 6. Architecture

```
┌────────────────────────── Local (macOS/Linux) ──────────────────────────┐
│                                                                          │
│  VSCode                                                                  │
│   └─ Moorpost extension (TypeScript)                                    │
│        ├─ status bar item                                                │
│        ├─ tree view: VMs, sync sessions, claude sessions                 │
│        ├─ commands (provision, connect, stop, attach, etc.)              │
│        └─ shells out to ──┐                                              │
│                            ▼                                              │
│  moorpost CLI (Go, single static binary)                                 │
│   ├─ subcommands:                                                        │
│   │     init / auth / provision / handoff / return / attach              │
│   │     status / cost / down / destroy / snapshot / reset / doctor       │
│   ├─ GCP SDK (compute, billing)                                          │
│   ├─ mutagen wrapper (driver — talks to mutagen daemon)                  │
│   ├─ ssh config writer (~/.ssh/config moorpost-managed block)            │
│   ├─ tmux wrapper (over SSH)                                             │
│   ├─ keychain wrapper (security / secret-tool)                           │
│   └─ state file: ~/.moorpost/state.json (project ↔ VM ↔ sync mappings)   │
│                                                                          │
│  mutagen daemon (existing, brew)                                         │
│  ssh / gcloud (existing)                                                 │
│                                                                          │
└──────────────────────────────────┬───────────────────────────────────────┘
                                   │ SSH (port 22) + mutagen sync
                                   ▼
┌────────────────────────── Remote (GCP VM, Ubuntu 24.04) ────────────────┐
│                                                                          │
│  • cloud-init bootstrap (installs Node 20, claude-code, tmux, uv, etc)   │
│  • symlink: <local-abs-path> → ~/moorpost/<project-slug>                 │
│  • tmux session per project, named <project-slug>                        │
│      └─ window 0: claude (running)                                       │
│      └─ window 1: shell                                                  │
│  • mutagen receiver (daemon launched on first sync)                      │
│  • ~/.claude/projects/<encoded-path>/ syncs alongside project files      │
│    so claude --resume works on either side of the handoff                │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

### 6.1 Why extension + CLI, not just extension?

- **Stability:** the CLI is the source of truth; the extension is a thin UI. When VSCode crashes, sync and tmux keep running.
- **Coding-agent compatibility:** Claude Code can self-provision via the CLI without needing the extension at all.
- **Terminal users are first-class.** Many Claude Code power users live in the terminal entirely (no VSCode, or using Cursor/JetBrains/neovim). For them, the flow is `moorpost init && moorpost auth && moorpost provision`, then `moorpost handoff` / `moorpost return` as needed. The extension is a convenience for VSCode users, not a gate.
- **Reuse:** Cursor/JetBrains/CLI-only users can use Moorpost via the CLI in v1.x with no extra work.
- **Testability:** the CLI is easier to integration-test than a VSCode extension.

### 6.2 Why Go for the CLI?

- Single static binary across macOS arm64/x86_64 and Linux — no runtime dependencies for end users.
- First-class GCP SDK (`cloud.google.com/go/compute/apiv1`).
- Sub-100ms cold start (matters when the extension shells out on every status refresh).
- Mutagen itself is Go — patterns to crib from.
- Alternatives considered: TypeScript (bigger binary, slower start), Rust (slower to ship v1), Python (packaging hell for end users).

### 6.3 Why Mutagen for sync?

- **Bidirectional with conflict resolution** — required because the user's desktop app writes `.docx` files at the project root while remote Claude writes code.
- **Ignores non-git files correctly** — git-based sync (the Codespaces approach) wouldn't work here.
- **Fast** — sub-second propagation for typical edits.
- **Mature** — v0.18 in 2026, used in production by many teams.
- Alternative considered: rsync on a timer (loses conflict semantics, polls), Unison (slower, less maintained).

### 6.4 Why VMs, not containers? (long version)

The decision isn't really "VM vs container" — it's two architectural questions hidden behind one label:

1. **Persistence model**: long-lived machine (VM) or recreated-from-spec workspace (container)?
2. **Isolation model**: per-project sandbox, or trust the user on their own machine?

For Moorpost's workload, six core requirements push hard toward VMs:

| Requirement                                          | VM       | Managed container |
|------------------------------------------------------|----------|-------------------|
| Survive laptop close + 30-hour Claude sessions       | trivial  | scheduler can evict; lifecycle mismatch |
| Bidirectional sync incl. non-git files (`.docx`)     | direct mutagen on FS | bind-mount fragility on Linux |
| Stop-when-idle: disk-only $4-10/mo                   | yes      | most container hosts pay-while-running |
| Run dev servers, browsers, DBs without port chains   | native   | port-forward through container + host |
| Single-tenant (no isolation needed)                  | n/a      | isolation buys nothing here |

Container isolation is genuinely valuable in **multi-tenant** systems (Anthropic Cloud Sandbox, Coder enterprise). Moorpost is single-tenant by design — the user owns the VM, and the agent already has access to everything that matters (project files, OAuth token, SSH config). A container wall doesn't stop a buggy agent from leaking those.

The strategic argument: Anthropic is shipping container-based managed execution themselves (Cloud Sandbox, Managed Agents, Routines). Coder owns enterprise containers. DevPod owns container portability. **VM-first is the lane Anthropic is leaving open precisely because they're occupying the container lane themselves.**

The migration argument: VM → hybrid (containers on a VM) is easy to add. Container-first → VM means undoing image builds, devcontainer spec compatibility, registry auth, multi-stage networking. **Asymmetric reversibility favors VM-first.**

**Implications baked into v1:**

- **Multi-project per VM** (one VM, many tmux sessions in `~/moorpost/<project-slug>/`) addresses the "wasteful VM per project" critique. Cuts cost ~70% for users with multiple projects. v0.1 deliverable, not v2.
- **`moorpost reset` and snapshot ergonomics** counter the VM bit-rot risk. v0.2 deliverable.
- **`moorpost devcontainer up`** opt-in container mode running on the persistent VM is reserved for v2 — for users who specifically want devcontainer reproducibility. The C-hybrid (container on VM) deferred but architected for.

### 6.5 Sync and state model

> *The concrete paths and commands below (`~/.claude/projects/...`, `claude --resume`, `CLAUDE_CODE_OAUTH_TOKEN`) are what the v1 Claude Code Agent implementation provides. Other agents implementing the `Agent` interface (§6.6) supply their own paths/commands; the rest of the sync logic is unchanged.*

Three distinct things travel between local and remote, with different rules:

| What                                            | Direction        | Sync trigger              | Conflict policy                       |
|-------------------------------------------------|------------------|---------------------------|---------------------------------------|
| Project files (`~/moorpost/<slug>/`)            | bidirectional    | continuous (mutagen watch) | alpha-wins (local) for `.docx`/`.xlsx`; two-way-resolved otherwise |
| Claude session state (`~/.claude/projects/<encoded-path>/`) | bidirectional, but never simultaneous | only at handoff/return boundaries | strict-winner: source side at the moment of `handoff`/`return` |
| Local-only state (`~/.moorpost/state.json`, Keychain token) | never syncs    | n/a                       | n/a — per-machine                     |

**Authority model.** At any moment exactly one side is *active* — the side where Claude Code is currently running. The active side owns session state until handoff/return. The inactive side's session state is read-only-from-the-user's-perspective; mutagen will overwrite it on next handoff.

**Why two sync streams.** Project files need continuous bidirectional sync so the desktop app's `.docx` writes are visible to remote Claude and vice versa. Session state must NOT be continuously synced — that would race with Claude's own writes mid-tool-call and corrupt the conversation log. Instead, session state syncs only at handoff/return, when both sides are quiesced.

**Concretely, what `moorpost handoff` does:**

1. Send "graceful pause" signal to local `claude` (it finishes the current turn, then waits)
2. Confirm local Claude is paused (poll for "waiting for user input" state)
3. If VM is stopped, start it (~15s)
4. Run a one-shot mutagen sync of `~/.claude/projects/<encoded-path>/` from local to remote
5. (Project files are already in continuous sync — nothing extra needed)
6. SSH to VM, run `claude --resume <session-id>` inside the project's tmux window
7. Update status: local marked inactive, remote marked active
8. Return success to caller

**`moorpost return` is the mirror:**

1. Send "graceful pause" to remote `claude` via tmux send-keys
2. Confirm pause
3. One-shot sync of `~/.claude/projects/<encoded-path>/` from remote to local
4. Run `claude --resume <session-id>` locally
5. Update status: remote inactive, local active
6. Optionally `moorpost down` to stop VM

**The active-side flag** lives in `~/.moorpost/state.json` per project. The CLI refuses to start Claude on a side marked inactive without an explicit `--force`. This prevents the "Claude running on both sides simultaneously, syncing fights with each turn" failure mode.

**Conflict resolution** for project files:
- `.docx`, `.xlsx`, and other binary office files: alpha (local) always wins. Reason: desktop apps lock these files; conflicting bytes from remote would corrupt them.
- Source code and other text: standard mutagen `two-way-resolved` (the side modified more recently wins; manual override available).
- Build artifacts, `.venv`, `node_modules`, caches: never sync (gitignore-style ignore list in `mutagen.yml`).

**Conflict resolution** for session state: the active side's copy always wins at handoff/return. Conflicts are fatal — if both sides modified `~/.claude/projects/<encoded-path>/` since last sync (because something went wrong), `moorpost` aborts the handoff with a diagnostic message and asks the user to pick a side via `--prefer-local` or `--prefer-remote`.

### 6.6 Extension points (so this isn't a Claude-and-GCP-only product forever)

v1 ships with **one implementation behind each of three Go interfaces**, with the CLI talking only to interfaces. Adding AWS/Azure (v2) or another agent (Cursor, Aider, Codex — v2+) is then a new file, not a refactor. The interfaces are intentionally small — minimal surface area is what makes them easy to implement.

**`Provider`** — abstracts cloud-provider concerns (VM lifecycle, networking, billing).

```go
// internal/provider/provider.go
type Provider interface {
    Provision(ctx context.Context, spec ProvisionSpec) (VM, error)
    Start(ctx context.Context, vmID string) error
    Stop(ctx context.Context, vmID string) error
    Destroy(ctx context.Context, vmID string) error
    Status(ctx context.Context, vmID string) (VMState, error)
    Snapshot(ctx context.Context, vmID string, label string) (SnapshotID, error)
    Cost(ctx context.Context, vmID string, period TimeRange) (CostBreakdown, error)
    SSHTarget(ctx context.Context, vmID string) (SSHTarget, error)
}
```

What each provider owns: provisioning APIs, IAM/credential model, cost APIs, networking primitives, machine-type catalog. v1 has `provider/gcp/`. v2 adds `provider/aws/`, `provider/azure/`.

**`Agent`** — abstracts which AI coding tool is being remoted.

```go
// internal/agent/agent.go
type Agent interface {
    ID() string                                    // "claude-code", "cursor-cli", "aider"
    InstallScript(os OSFamily) string              // shell snippet for bootstrap
    AuthenticateLocal(ctx context.Context) (Credential, error)
    InjectCredential(ctx context.Context, vm SSHTarget, c Credential) error
    SessionStatePath(projectAbsPath string) string // e.g. ~/.claude/projects/<encoded>
    Pause(ctx context.Context, vm SSHTarget, projectSlug string) error
    Resume(ctx context.Context, vm SSHTarget, projectSlug string, sessionID string) error
    IsActive(ctx context.Context, vm SSHTarget, projectSlug string) (bool, error)
}
```

What each agent owns: install command, auth flow (OAuth setup-token, API key, device code, etc.), credential env var name, session state directory, pause/resume semantics. v1 has `agent/claudecode/`. v2 adds `agent/cursorcli/`, `agent/aider/`, `agent/codexcli/`, `agent/geminicli/`.

**`Sync`** — abstracts the file-sync engine.

```go
// internal/sync/sync.go
type Sync interface {
    StartSession(ctx context.Context, spec SyncSpec) (SyncSessionID, error)
    Pause(ctx context.Context, id SyncSessionID) error
    Resume(ctx context.Context, id SyncSessionID) error
    OneShot(ctx context.Context, src, dst Endpoint, dir Direction) error
    Status(ctx context.Context, id SyncSessionID) (SyncStatus, error)
    Stop(ctx context.Context, id SyncSessionID) error
}
```

What each sync engine owns: bidirectional vs one-way, conflict policy implementation, ignore-list format, file-event watching. v1 has `sync/mutagen/`. v2 adds `sync/rsync/` (fallback), potentially `sync/syncthing/` if mutagen ossifies.

**Wiring.** A `Session` struct holds the configured trio for a project:

```go
type Session struct {
    Provider provider.Provider
    Agent    agent.Agent
    Sync     sync.Sync
    Config   *config.ProjectConfig
}
```

CLI commands construct a `Session` from `.moorpost/config.yaml` and call methods on it. Concrete providers/agents/syncs are picked by string ID at runtime via small registry maps:

```go
provider.Register("gcp", gcp.New)
provider.Register("aws", aws.New)         // future
agent.Register("claude-code", claudecode.New)
agent.Register("cursor-cli", cursorcli.New) // future
sync.Register("mutagen", mutagen.New)
```

**The yardstick: a new provider should be ~500 lines of Go in one file.** A new agent should be ~200 lines. If implementing the second one of either tells us the interface is wrong, we refactor — but the v1 surface stays narrow specifically so the refactor is small.

**What v1 does NOT abstract** (because over-abstracting is its own bug):
- The bootstrap script — kept as a shell script per OS family (`bootstrap/ubuntu-24.04.sh`, eventually `debian-12.sh`, `amazon-linux-2023.sh`). Not an interface.
- The state file format — single canonical schema, versioned via `schema_version: 1`.
- The config file format — single canonical schema, with provider-specific and agent-specific subsections nested under typed keys.
- The IPC contract between extension and CLI — single JSON-on-stdout contract, versioned.

## 7. Key technical decisions

| Decision                          | Choice                                                       | Rationale                                                              |
|-----------------------------------|--------------------------------------------------------------|------------------------------------------------------------------------|
| Extension language                | TypeScript                                                   | Only viable option for VSCode                                          |
| CLI language                      | Go                                                           | Single binary, fast, GCP SDK, matches mutagen                          |
| Provisioning                      | GCP SDK directly (v1); abstract for Pulumi/Terraform in v2   | Fewer deps, faster v1                                                  |
| Extensibility model               | Three Go interfaces (`Provider`, `Agent`, `Sync`); one impl each in v1; new clouds/agents are new files, not refactors | YAGNI-balanced future-proofing — interfaces upfront, features deferred. See §6.6. |
| Auth (GCP)                        | Reuse user's gcloud ADC; never store credentials in the extension | Standard practice, zero new attack surface                       |
| Auth (Claude Code, subscription)  | Run `claude setup-token` **locally** once; capture the long-lived `sk-ant-oat01-...` token; store in macOS Keychain / Linux Secret Service; inject into VM as `CLAUDE_CODE_OAUTH_TOKEN` | Browser opens natively on the user's machine — no SSH OAuth dance, no remote port forwarding. Subsequent `up` calls reuse the cached token. |
| Auth (Claude Code, API key)       | Read from `ANTHROPIC_API_KEY` env var; CLI prompts at first `up` if missing | Fallback for users without a Pro/Max subscription                            |
| Auth (Claude Code, fallbacks)     | (a) SSH `-L` local port forward of the OAuth callback port; (b) PTY-watcher + `tmux send-keys` for the OOB code-paste flow | Documented but rarely needed once `setup-token` works                |
| Sync                              | Mutagen via project files generated by the CLI                | Best-in-class for this use case                                        |
| Session persistence               | tmux session named `<project-slug>` per project (one VM hosts many)         | Universally available, scriptable; consistent slug = predictable attach |
| State                             | `~/.moorpost/state.json` (per-user)                          | No service to run; survives reboots                                    |
| IPC (extension ↔ CLI)             | CLI emits structured JSON on stdout; extension parses        | Same pattern as `gh`, `kubectl -o json`                                |
| Cost data                         | Cloud Billing API (read-only); fallback to flat per-machine-hour estimate if Billing API not enabled | Graceful degradation                  |
| Config file                       | `.moorpost/config.yaml` at project root                       | Project-local; checked into git                                        |
| Telemetry                         | Off by default; opt-in only; no PII                           | OSS hygiene                                                            |

### 7.1 Security model

**Trust boundary.** The VM is single-tenant — only the user's own machines connect to it. The threat model is *not* multi-tenant isolation; it's *protecting the user's credentials and code* from accidental loss, leak, or compromise.

**Secrets at rest:**

| Secret                         | Where                                                  | Protection                                                       |
|--------------------------------|--------------------------------------------------------|------------------------------------------------------------------|
| `CLAUDE_CODE_OAUTH_TOKEN`      | macOS Keychain / Linux Secret Service (local)         | OS-level encryption + per-app ACL                                |
| `CLAUDE_CODE_OAUTH_TOKEN`      | VM `/etc/moorpost/env` (mode 0600, root:root)         | Read by systemd unit; injected into tmux env on session start    |
| `ANTHROPIC_API_KEY` (fallback) | Same as OAuth token                                    | Same                                                             |
| GCP service account key        | **Not stored** — uses user's gcloud ADC; never written | Reuses Google's standard credential model                        |
| SSH keys                       | Existing `~/.ssh/google_compute_engine` (per gcloud)  | OS file permissions; passphrase if user set one                  |

**Secrets in transit:** SSH (port 22) is the only ingress to the VM. Mutagen rides over the same SSH connection. No additional ports opened by Moorpost; users who want dev-server port forwarding use `ssh -L` themselves or `moorpost forward` (v1.1).

**IAM scope on GCP:** Moorpost provisions VMs with a *minimum-privilege* service account that has `roles/compute.instanceAdmin.v1` (start/stop/snapshot itself only) and `roles/monitoring.viewer` (read its own metrics). It does NOT have project-wide IAM, billing-admin, or storage roles. Documented in `docs/security.md`.

**Threat: VM compromise.** If the VM is compromised, the attacker has the OAuth token and can call Claude Code on behalf of the user (incurring usage charges) but cannot exfiltrate or modify Anthropic account credentials, since OAuth tokens are scoped. They also have whatever's in `~/moorpost/<project>/` — i.e., the project files. This is the same blast radius as if the user's laptop were compromised.

**Threat: laptop loss/theft.** Token in macOS Keychain requires login password. Even root access cannot read Keychain items without re-prompt for the user's password (assuming default Keychain security level). On Linux, Secret Service is similarly gated. If the user worries about this, they `moorpost auth --revoke` from any other device, which invalidates the token at Anthropic.

**Threat: malicious npm/pip install on the VM.** Standard supply-chain risk — out of scope for Moorpost itself, but `moorpost reset` provides easy recovery (recreate VM from clean bootstrap; project files restored from local).

**Default firewall:** GCP VM created with only port 22 open (default), restricted to user's current public IP via `--source-ranges`. User can widen with `--public-ssh` flag. Docs warn about the risk.

**Audit:** every CLI invocation logs to `~/.moorpost/logs/`. `moorpost audit` (v0.3) prints the last N actions with timestamps for security review.

### 7.2 CLI auto-install

The extension is published to the VS Code Marketplace as `LatentAdvisory.moorpost-vscode` (the bare `moorpost` name is reserved at the Marketplace from a prior, deleted listing — to be reclaimed via Microsoft support and renamed back at that point). The Marketplace listing only ships the TypeScript bundle — the Go CLI is a separate per-platform binary on the GitHub release page.

To remove the manual-binary-download step from the install flow, the extension carries `extension/src/cliInstaller.ts`:

- On every activation, it spawns `<cliBinary()> --version` and parses the leading semver token.
- If the binary is missing (`ENOENT`) or its version is below the extension-declared `MIN_CLI_VERSION`, it picks the right asset for `process.platform` × `process.arch`, downloads it from `https://github.com/latent-advisory/moorpost/releases/download/v<MIN_CLI_VERSION>/<asset>`, fetches `SHA256SUMS` from the same release, verifies the hash, writes the binary to `~/.local/bin/moorpost` (mode 0755), and updates `moorpost.cliPath` if `~/.local/bin` isn't on the inherited `PATH`.
- All Node I/O is dependency-injected so the orchestrator is unit-testable through the existing `node --test` + loader-shim setup; see `extension/src/cliInstaller.test.mts`.
- `MIN_CLI_VERSION` is bumped only when the extension actually depends on a new CLI feature — the floor is one-directional (extension → CLI) and not a strict lock-step. Each release that bumps the floor triggers a re-download for users on stale binaries.

Failures (offline, sandboxed CI, unsupported platform like Windows, SHA mismatch, FS error) surface as a non-modal error toast with a one-click "Open release page" link. The extension does not block activation on auto-install failure — users with a working binary are unaffected.

Out of scope: GPG signature verification, Windows support, hot updates when the extension is older than the installed CLI.

## 8. Repo structure

```
moorpost/
├── README.md                  # public-facing intro
├── PLUGIN.md                  # this file
├── LICENSE                    # Apache 2.0
├── extension/                 # VSCode extension (TypeScript, esbuild)
│   ├── package.json
│   ├── src/
│   │   ├── extension.ts
│   │   ├── statusBar.ts
│   │   ├── treeView.ts
│   │   ├── commands/
│   │   └── cli/               # CLI wrapper / IPC
│   └── test/
├── cli/                       # moorpost CLI (Go)
│   ├── go.mod
│   ├── main.go
│   ├── cmd/
│   │   ├── init.go            # first-run scaffold (config, GCP project check)
│   │   ├── auth.go            # local claude setup-token + Keychain stash
│   │   ├── provision.go       # create VM, bootstrap, leave stopped
│   │   ├── handoff.go         # local → remote (the central command)
│   │   ├── return_.go         # remote → local (Go reserved-word workaround)
│   │   ├── attach.go          # SSH + tmux attach
│   │   ├── status.go          # local/remote state, sync, cost
│   │   ├── cost.go            # cost breakdown
│   │   ├── down.go            # stop VM
│   │   ├── destroy.go         # delete VM + disk (confirmation)
│   │   ├── snapshot.go        # GCE disk snapshot
│   │   ├── reset.go           # rebootstrap VM (auto-snapshots first)
│   │   ├── doctor.go          # diagnostics: gcloud, mutagen, SSH, tmux, claude
│   │   └── up.go              # alias for handoff (with --persistent)
│   ├── internal/
│   │   ├── provider/          # cloud-provider abstraction
│   │   │   ├── provider.go    # Provider interface + registry
│   │   │   └── gcp/           # v1 implementation
│   │   │   # aws/, azure/ added in v2
│   │   ├── agent/             # AI-agent abstraction
│   │   │   ├── agent.go       # Agent interface + registry
│   │   │   └── claudecode/    # v1 implementation
│   │   │   # cursorcli/, aider/, codexcli/ added in v2+
│   │   ├── sync/              # file-sync abstraction
│   │   │   ├── sync.go        # Sync interface + registry
│   │   │   └── mutagen/       # v1 implementation
│   │   │   # rsync/ as fallback in v2
│   │   ├── session/           # ties Provider+Agent+Sync together per project
│   │   ├── ssh/               # ~/.ssh/config writer, ControlMaster (provider-agnostic)
│   │   ├── tmux/              # send-keys, session mgmt over SSH (agent-agnostic)
│   │   ├── keychain/          # macOS security / Linux secret-tool
│   │   ├── state/             # ~/.moorpost/state.json read/write
│   │   └── config/            # .moorpost/config.yaml schema
│   └── testdata/
├── bootstrap/                 # cloud-init / shell scripts run on the VM
│   └── ubuntu-24.04.sh
├── docs/
│   ├── architecture.md
│   ├── quickstart.md
│   ├── coding-agent-mode.md
│   └── troubleshooting.md
├── .github/
│   ├── workflows/             # CI: build CLI for darwin/linux, test extension
│   └── ISSUE_TEMPLATE/
└── scripts/                   # dev scripts (build all, release, etc)
```

### 8.1 Config file schema (`.moorpost/config.yaml`)

Project-local. Checked into git so teammates see the same settings.

```yaml
# .moorpost/config.yaml
schema_version: 1
project_slug: webapp                # used for tmux session name and ~/moorpost/<slug>

# --- Provider (cloud) ---------------------------------------------------------
provider:
  type: gcp                          # gcp | aws (v2) | azure (v2)
  gcp:                               # only one provider section is read, by `type`
    project: your-gcp-project
    region: us-central1
    zone: us-central1-a
    machine_type: e2-standard-2
    disk_size_gb: 100
    disk_type: pd-standard
    static_ip: true
    network_tags: [moorpost]

# --- Agent (AI tool) ----------------------------------------------------------
agent:
  type: claude-code                  # claude-code | cursor-cli | aider | codex (v2+)
  claude-code:
    auth_method: oauth-subscription  # oauth-subscription | api-key
    # token stored in OS keychain; never written to this file
  # cursor-cli: { ... }              # v2+

# --- Sync engine --------------------------------------------------------------
sync:
  engine: mutagen                    # mutagen | rsync (v2)
  conflict_policy: alpha-wins
  ignore:
    - "**/node_modules"
    - "**/.venv"
    - "**/__pycache__"
    - "**/dist"
    - "**/.next"
    - "**/.cache"

# --- Operating mode -----------------------------------------------------------
mode: local-first                    # local-first | persistent

handoff:
  pause_timeout_seconds: 30          # how long to wait for graceful agent pause
  prompts:
    on_lid_close: true
    on_idle_minutes: 30              # 0 disables
    on_battery_below: 20             # 0 disables
    on_vscode_quit: true

cost:
  monthly_cap_usd: 50                # 0 disables; CLI refuses to start VM if exceeded
  alert_thresholds: [10, 25]
```

### 8.2 State file schema (`~/.moorpost/state.json`)

Per-machine. Not synced. Records which projects are configured, which side is currently active, recent VM state cache.

```json
{
  "schema_version": 1,
  "machine_id": "uuid-...",
  "telemetry_opt_in": false,
  "projects": {
    "/Users/alice/code/webapp": {
      "slug": "webapp",
      "vm_id": "webapp-vm",
      "vm_zone": "us-central1-a",
      "active_side": "local",
      "last_handoff": null,
      "last_return": "2026-05-04T18:32:11Z",
      "claude_session_id": "01J...",
      "mutagen_session_id": "sync-..."
    }
  },
  "vms": {
    "webapp-vm": {
      "provider": "gcp",
      "external_ip": "35.x.x.x",
      "state_cache": "stopped",
      "state_cache_at": "2026-05-04T18:32:15Z",
      "month_to_date_usd": 0.42
    }
  }
}
```

## 9. Implementation milestones

### v0.1 — Walking skeleton (target: 1-2 weeks of focused work)

Goal: someone (you) can run `moorpost init && moorpost auth && moorpost provision`, work on Claude locally, then `moorpost handoff` to a tmux'd Claude session on a fresh GCP VM with the agent already authenticated and mutagen sync running. `moorpost return` brings the work back. No VSCode extension yet.

- [ ] CLI scaffolding (Cobra), state file, project config schema
- [ ] **Define `Provider`, `Agent`, `Sync` interfaces** (per §6.6) and ship one impl each: `provider/gcp`, `agent/claudecode`, `sync/mutagen`. **CLI commands consume only the interfaces**, never the concrete impls. This is the v1 hinge that makes new clouds / agents / sync engines drop-in additions later. Cost: ~half a day of design time.
- [ ] `moorpost init`: first-run command in a project directory; writes `.moorpost/config.yaml`, validates the configured provider, prompts for missing settings
- [ ] `moorpost auth`: wraps `claude setup-token` locally; stashes token in macOS Keychain / Linux Secret Service
- [ ] `moorpost doctor`: diagnostics — checks gcloud auth, mutagen install, SSH config, claude CLI, Keychain access, GCP API enablement; returns a checklist with remediation hints
- [ ] `moorpost provision`: creates VM (or attaches new project to existing VM), bootstraps, **leaves VM stopped**. Default for first-time setup.
- [ ] `moorpost handoff`: starts the VM if stopped (~15s); pauses local Claude at next turn boundary; syncs project + `~/.claude/projects/<path>/` to remote; runs `claude --resume <session-id>` in tmux on remote; reports ready
- [ ] `moorpost return`: pauses remote Claude; syncs state back; resumes local Claude with `claude --resume`; optionally stops VM
- [ ] **Multi-project on one VM** by default — projects live at `~/moorpost/<project-slug>/`, each gets its own tmux session, all share the VM
- [ ] `moorpost attach`: opens an interactive SSH that attaches to the tmux session for the current project (when remote is active)
- [ ] `moorpost up --persistent`: opt-in always-remote mode (keeps VM running)
- [ ] `moorpost down`: stops the VM (does not delete)
- [ ] `moorpost destroy`: deletes VM + disk (with confirmation)
- [ ] `moorpost status`: prints local/remote state, sync state, session state per project, this month's cost
- [ ] Bootstrap script: idempotent, installs everything, handles re-runs

### v0.2 — VSCode extension MVP (target: +1 week)

Goal: status bar + the full handoff UX, replicating CLI surface.

- [ ] Status bar item with state machine: `Local` / `Handing off…` / `Remote` / `Returning…` / `Error`
- [ ] Commands: `Moorpost: Sign in`, `Provision`, `Handoff`, `Return`, `Attach Claude`, `Stop VM`, `Destroy`, `Doctor`
- [ ] Status-bar quick actions: click to handoff/return; right-click for menu
- [ ] Tree view: VM (per project's home VM), projects on that VM, sync sessions, active claude sessions
- [ ] Settings: default GCP project, default machine type, default region, default mode (local-first vs always-remote)
- [ ] Marketplace listing (private, hand-test)

### v0.2 (continued) — Bit-rot mitigation

- [ ] `moorpost snapshot`: takes a Compute Engine disk snapshot. Manual command + auto-snapshot before `moorpost reset` and before any destructive operation
- [ ] `moorpost reset`: recreates the VM from a fresh bootstrap, restoring user projects from mutagen sync (since local is the source of truth for project files). Counters VM bit-rot.

### v0.2 (continued) — Smart handoff prompts (the safety net)

The handoff design is **manual-primary, smart-prompts-as-safety-net**. The user is always in control; the extension surfaces well-timed prompts so they don't have to remember.

- [ ] **Lid-close prompt** (highest value): when the user is about to suspend the laptop with active local Claude session, system notification *"Claude is mid-task. Hand off to Moorpost? [Yes / No / Don't ask again]"*. macOS: NSWorkspace will-sleep notification. Linux: systemd-logind PrepareForSleep dbus signal.
- [ ] **VSCode quit prompt**: if the user quits VSCode with active local Claude, prompt to hand off before quitting.
- Note: the CLI stays dumb (manual commands only). All event-watching lives in the VSCode extension. Terminal-only users can wire their own triggers (e.g., `pmset` hooks, systemd suspend hooks) that call `moorpost handoff`.

### v0.3 — Cost surface + smart-prompt polish (target: +1 week)

- [ ] Real cost integration via Cloud Billing API; status-bar shows month-to-date spend
- [ ] Notifications: "VM running for 12h, daily spend $X"
- [ ] **Auto-stop on idle in `--persistent` mode** (configurable; **default ON**, 60-minute idle threshold; idle = no SSH session AND no tmux input AND no mutagen sync activity). Only applies to opt-in always-remote users.
- [ ] Hard cost cap: refuse to start VM if month-to-date spend exceeds user-set ceiling
- [ ] Additional smart-handoff prompts (all opt-in, default ON):
  - [ ] Idle-threshold prompt (default 30 min)
  - [ ] Battery-low prompt (default <20% and unplugged)
- [ ] Conflict UX for mutagen sync conflicts
- [ ] Auto-return remains **never silent**. User clicks `[Return]` chip when ready. Status bar: `🌊 Remote · running 6h · [Return]`.

### v1.0 — Public release (target: +1 week)

- [ ] Docs site (just `docs/` for now)
- [ ] Quickstart that runs in <60 seconds end-to-end
- [ ] `docs/security.md` — token storage, IAM scope, threat model
- [ ] README ToS note re: OAuth-token-forwarding compliance (per §10 #4)
- [ ] `moorpost audit`: prints last N CLI invocations from `~/.moorpost/logs/`
- [ ] CI: cross-compile CLI (darwin-arm64, darwin-amd64, linux-amd64, linux-arm64), package extension, sign + publish to marketplace
- [ ] Telemetry opt-in scaffold (per §10 #12 — strictly opt-in, no default prompt)
- [ ] Versioning: semver, signed releases, auto-update check (`moorpost update`)
- [ ] Public launch: HN, Twitter, Anthropic Discord, /r/ClaudeAI

### v1.1 — terminal-first polish + cost visibility (target: +2 weeks)

- [ ] First-class terminal/CLI ergonomics: shell completions (bash/zsh/fish), `moorpost shell` for raw SSH, `--json` flag everywhere
- [ ] Real Cloud Billing API integration behind `--actual` (replacing the v1.0 list-price estimator)
- [ ] Windows local client (CLI only initially)

### v2 — Multi-cloud, team, opt-in containers

- AWS provider
- Azure provider
- Team workspaces (shared VM pool)
- Custom bootstrap scripts per project (`.moorpost/bootstrap.sh`)
- Pulumi/Terraform-based provisioning under the hood
- **`moorpost devcontainer up`** — opt-in container mode for users who want devcontainer reproducibility, running on top of the persistent VM. The C-hybrid (container on VM). Adds project-level isolation without giving up VM persistence or cost-control. Only built when there's a real user demand.

## 10. Open questions

1. **Marketplace publisher identity** — "latent-advisory" or a separate "moorpost" publisher? Probably separate, so the project is portable if we ever spin it out.
2. **Bootstrap script delivery** — embedded in the CLI binary, or fetched from GitHub at provision time? Embedded is more reliable (offline, version-locked); fetched is more updatable. Recommend: embedded with override flag.
3. **Claude Code auth — the `setup-token` shortcut.** Anthropic ships `claude setup-token` which produces a 1-year `sk-ant-oat01-...` OAuth token usable as `CLAUDE_CODE_OAUTH_TOKEN`. Moorpost runs it once **on the user's local Mac** (browser opens natively), stashes the token in Keychain, and injects it into every VM. **No OAuth flow ever runs on the VM.** This is a dramatically cleaner UX than running OAuth over SSH. Caveat: requires Pro/Max/Team/Enterprise subscription. Free-tier and API-key users get the `ANTHROPIC_API_KEY` env var path.
4. **ToS for OAuth token forwarding** — Anthropic's Feb 2026 ToS update prohibits using OAuth tokens in third-party tools/SDKs. Moorpost is compliant because it forwards the user's own token to the user's own `claude` process on the user's own VM — no proxying, no Messages-API calls. README must state this explicitly from day 1.
5. **Token storage location** — macOS Keychain via `security` CLI; Linux Secret Service (libsecret) via `secret-tool`. Plain dotfile fallback only with `--unsafe-token-storage` flag.
6. **Idle auto-stop heuristic (`--persistent` mode only)** — combination signal: no SSH session attached AND no tmux input within window AND no recent mutagen sync activity. Conservative default of 60 min. Wrong heuristic kills work mid-flight; over-conservative wastes money. (Local-first mode doesn't need this — the VM is stopped between handoffs by default.)
7. **Naming the binary** — `moorpost` (verbose) or `mp` (terse)? Recommend: install as `moorpost` with `mp` symlink.
8. **GCP project bootstrap** — do we require an existing GCP project, or offer to create one? v1: require existing project (simpler IAM story).
9. **Static IP** — reserved by default, or only on opt-in? Recommend: reserved by default (avoids broken SSH config when VMs restart). User can opt out for cost.
10. **Cost-cap enforcement** — soft warning vs. hard refuse-to-start? Recommend: hard cap, with override flag for the rare power user.
11. **Multi-machine usage** — user has laptop + desktop, both Moorpost-installed, sharing one VM. State file is per-machine; sync conflicts on `~/.claude/projects/` if both initiate handoff. Recommend: v1 documents the limitation ("treat each machine's Moorpost as independent; only one machine should be active at a time"). v2 considers a "fleet mode" with a server-side lock.
12. **Telemetry scope** — when opt-in, what's collected? Recommend: command name, exit code, command duration, OS, CLI version. No project names, no file paths, no GCP project IDs, no error messages (might leak paths). Endpoint: a small Cloudflare Worker; raw events appended to a Cloudflare D1 table; aggregated weekly. Decision: opt-in only, ever; never default-on, even with prompt.
13. **Logging** — local CLI logs at `~/.moorpost/logs/<date>.log`, rotated daily, 30-day retention. `moorpost doctor --logs` tails them. Remote logs in `/var/log/moorpost/` on the VM, accessible via `moorpost logs --remote`.

## 11. Risks

| Risk                                                | Mitigation                                                          |
|-----------------------------------------------------|---------------------------------------------------------------------|
| Anthropic ships a competing first-party feature     | Position as BYO-cloud (their roadmap is Anthropic-cloud); pivot to multi-tool support if needed |
| Mutagen project ossifies / acquired                 | Abstract the sync layer; rsync fallback driver in v2                |
| Claude Code CLI breaking changes                    | Version-pin in bootstrap; integration test on every release         |
| `claude --resume` semantics change between versions | Detect via `claude --version` in `doctor`; warn if remote and local diverge |
| Session state file format changes                   | Treat `~/.claude/projects/` as opaque; only ever sync, never parse  |
| Coder / DevPod pivot to compete                     | They're container-first by DNA; v1 ships before they could pivot    |
| GCP quota / billing surprises for users             | Pre-flight checks; cost-cap setting that hard-stops VM at threshold |
| OSS maintenance burden                              | Keep v1 surface minimal; say no to feature requests outside the lane |
| User runs Claude on both sides simultaneously       | Active-side flag in state.json; CLI refuses without `--force`; detected at next handoff |
| Multi-machine state divergence (laptop + desktop)   | v1 documents the limitation; v2 considers fleet mode with server-side lock |

## 12. Failure modes & recovery

Every interaction with the cloud is a chance for something to break. Documented failure paths and what Moorpost does in each:

| Scenario                                                          | What Moorpost does                                                                                              |
|-------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------|
| GCP API unreachable during `provision` / `handoff`                 | Retry with exponential backoff (3 attempts); on final failure, abort cleanly, leave no half-provisioned state. Status command shows last error. |
| Network drops mid-handoff (after VM start, before sync complete)   | VM is running but session not migrated. `moorpost status` detects: "VM running, sync incomplete." User retries `moorpost handoff` (idempotent). |
| Network drops mid-return                                           | Same as above, mirrored. Local state file marks "return-pending"; retry resumes.                                 |
| Mutagen sync conflict on `~/.claude/projects/` during handoff      | Abort handoff. Keep local Claude active. Show diff. User picks side via `--prefer-local` / `--prefer-remote`.   |
| Mutagen sync conflict on a `.docx` file                            | Alpha-wins (local) per policy. User notified via VSCode notification. Backup of remote version stored at `<file>.remote.bak` for inspection. |
| GCP quota hit (CPU, IP)                                            | Pre-flight check in `provision` / `up` catches this with a friendly message: "Project X has Y/Z CPUs in use; request increase at <url>." |
| Bootstrap script fails partway                                     | Bootstrap is idempotent; re-running picks up where it left off. If truly broken, `moorpost reset` recreates from clean state. |
| User deletes `~/.moorpost/state.json`                              | `moorpost doctor` re-discovers: lists VMs in their GCP project tagged `moorpost`, prompts to re-link to projects. No data loss; state file is a cache. |
| User force-kills `claude` mid-tool-call before handoff             | `~/.claude/projects/` may have an in-flight tool result without a turn. `claude --resume` typically handles this. Worst case: user re-prompts the last instruction. |
| Two laptops running `moorpost handoff` simultaneously              | First-write-wins on the remote tmux session create. Second laptop's handoff fails with "session already exists." User must `moorpost return` from the winning side first. |
| VM disk fills up                                                   | `doctor` checks free space. If >80%, warns. If >95%, bootstrap install scripts refuse. User runs `moorpost reset` to wipe + restart. |
| Static IP is released (e.g., user manually releases)               | SSH config breaks. `doctor` detects mismatch between state.json and live IP. `moorpost up` re-attaches a new static IP. |
| Claude Code subscription expires / token revoked                   | Remote `claude` exits on next API call. `moorpost status` detects and prompts user to re-run `moorpost auth`.    |
| User's local clock is wildly skewed                                | GCP API rejects auth with "JWT expired"; `doctor` checks NTP offset and warns.                                   |

**The recovery contract:** Moorpost never destroys data on failure. Every command either completes successfully, fails cleanly with state.json reverted, or leaves a "pending" marker that a retry resumes. `moorpost reset` is always available as the nuclear option (recreate VM from clean bootstrap; project files are safe because mutagen has a copy on the local machine).

## 13. Future (v2+, not committed)

Organized by which v1 interface unlocks each — this clarifies what's a small addition vs. a big one.

**Adds via `Provider` interface** (each ~500 lines of Go in one file):
- AWS provider (EC2 + Cost Explorer + IAM)
- Azure provider (Compute + Cost Management + AAD)
- Oracle Cloud Always-Free provider (ARM64 only; high friction but $0)
- Fly.io provider (per-second billing, scale-to-zero)
- DigitalOcean / Vultr / Linode providers (commodity VPS)

**Adds via `Agent` interface** (each ~200 lines of Go in one file):
- Cursor CLI agent — when/if Cursor ships a remote-friendly CLI
- Aider agent — open-source, well-defined session model, easy port
- Codex CLI agent — OpenAI's CLI offering
- Gemini CLI agent — Google's offering
- "Multi-agent" mode: configure several agents in one project, each in its own tmux window

**Adds via `Sync` interface**:
- rsync fallback driver (when mutagen isn't installable or has bugs)
- Syncthing driver (decentralized; useful for multi-machine fleets)

**Doesn't need an interface** (just product work):
- Team mode: shared VM pool, RBAC, fleet-mode lock for multi-machine
- "Bring your own bootstrap": custom `.moorpost/bootstrap.sh` per project
- Devcontainer support (containers on the VM — `moorpost devcontainer up`)
- JetBrains Gateway plugin (CLI is reused; UI is new)
- Phone notification when long-running agent session finishes (push, not control — Anthropic owns control for Claude Code)
- Managed-service tier: hosted Moorpost where we run the VMs (revenue path)
- Web dashboard for cost / status across many Moorpost VMs

---

## Appendix A: Naming rationale

A *moorpost* is a real maritime term: the post on a dock to which a ship is moored. The metaphor:

- The **ship** = your laptop. Mobile, comes and goes.
- The **moorpost** = the remote VM. Fixed, always there, where work is anchored.
- The **mooring line** = the SSH + sync connection that tethers the two.

## Appendix B: Comparison snapshot (from research, 2026-05)

### Dev-environment platforms

| Tool                    | Provisions VMs | Persistent agent | Bi-dir non-git sync | Claude-aware |
|-------------------------|----------------|-------------------|----------------------|---------------|
| DevPod                  | yes (containers)| no               | no                   | no            |
| Coder                   | yes             | partial           | no                   | yes (module)  |
| Codespaces              | yes (managed)   | container suspend | no                   | no            |
| Google Cloud Workstations| yes (managed)  | container         | no                   | no            |
| Cursor cloud agents     | yes (Cursor's)  | yes               | no (PR-based)        | no            |
| Anthropic Routines      | n/a (managed)   | yes               | no                   | native        |
| 247-claude-code-remote  | yes (Fly.io)    | yes               | no                   | yes           |
| **Moorpost**            | **yes (BYO GCP)** | **yes (tmux)** | **yes (mutagen)**     | **yes (native)** |

### Adjacent terminal-first tools (not substitutes — useful as components or inspiration)

| Tool                  | What it is                                                              | Why it isn't Moorpost                       |
|-----------------------|-------------------------------------------------------------------------|----------------------------------------------|
| Eternal Terminal (et) | Best-in-class persistent SSH (scrollback, `tmux -CC`, tunnels)          | Doesn't provision, install, or auth          |
| mosh                  | UDP-based SSH that survives network changes                              | Same — transport only                        |
| tmate                 | Instant shared-tmux pairing                                              | Different problem (sharing, not persistence) |
| claudebox             | Drop-in `claude` wrapper that runs in Docker with profiles/hooks         | Local-only container wrapper                 |
| Omnara                | Cross-device handoff/observability layer for Claude Code                 | Different niche (observability, not infra)   |
| nielsgroen/claude-tmux | Local TUI for managing many Claude sessions across tmux panes           | Local-only; useful inspiration for session manager |
| Claude Remote (clauderc.com) | Hosted SaaS — phone access to your Mac's Claude session            | Proprietary; different shape                 |
