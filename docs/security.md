# Moorpost Security Model

**Trust boundary.** The VM is single-tenant — only your own machines connect to it. The threat model is *not* multi-tenant isolation; it's *protecting your credentials and your code* from accidental loss, leak, or compromise.

## Secrets at rest

| Secret                         | Where                                                   | Protection                                                  |
|--------------------------------|---------------------------------------------------------|-------------------------------------------------------------|
| `CLAUDE_CODE_OAUTH_TOKEN`      | macOS Keychain / Linux Secret Service (local)          | OS-level encryption + per-app ACL; requires login password to read |
| `CLAUDE_CODE_OAUTH_TOKEN`      | VM `/etc/moorpost/env` (mode 0600, root-owned)         | Read by the agent process at session start; never logged    |
| `ANTHROPIC_API_KEY` (fallback) | Same as OAuth token                                     | Same                                                        |
| GCP service account key        | **Not stored** — uses your gcloud Application Default Credentials | Reuses Google's standard credential model       |
| SSH keys                       | Existing `~/.ssh/google_compute_engine` (per gcloud)   | OS file permissions; passphrase if you set one              |

## Secrets in transit

- **SSH (port 22)** is the only ingress path to the VM. Mutagen rides over the same SSH connection.
- Moorpost **does not** open additional ports. Users who want dev-server port forwarding use `ssh -L` themselves (or `moorpost forward` once it lands in v1.1).
- The OAuth token is sent **once at handoff**, in a single `cat > /etc/moorpost/env.tmp && chmod 600 && mv` operation over SSH (the bytes never touch a process listing or shell history).

## IAM scope on GCP

Today (v0.1): Moorpost provisions VMs using the user's gcloud credentials. The created VM gets the **default Compute Engine service account**, which has broad project-level access by default. **Production deployments should override this.**

Roadmap (v1.0): Moorpost will provision VMs with a dedicated **minimum-privilege service account**:
- `roles/compute.instanceAdmin.v1` — start/stop/snapshot itself only
- `roles/monitoring.viewer` — read its own metrics

It will **not** have project-wide IAM, billing-admin, or storage roles. See [PLUGIN.md §7.1](../PLUGIN.md#71-security-model) for the full plan.

## Threat model

**Threat: laptop loss/theft.** Token in macOS Keychain requires your login password. Even root access cannot read Keychain items without re-prompt for the user's password (assuming default Keychain security level). On Linux, Secret Service is similarly gated. If you worry about this, run `claude logout` from any other device — that invalidates the OAuth token at Anthropic.

**Threat: VM compromise.** If the VM is compromised, the attacker has the OAuth token and can call Claude Code on behalf of you (incurring usage charges) but cannot exfiltrate or modify Anthropic account credentials, since OAuth tokens are scoped. They also have whatever's in `~/moorpost/<project>/` — i.e., the project files. **Same blast radius as if your laptop were compromised.**

**Threat: malicious npm/pip install on the VM.** Standard supply-chain risk — out of scope for Moorpost itself, but `moorpost reset` provides easy recovery (recreate VM from clean bootstrap; project files restored from local since mutagen has a copy).

**Threat: someone else's GCP project ID accidentally configured.** Moorpost commands reject silently-invalid project configs and surface clear "preflight failed" errors before any destructive call.

## Default firewall

GCP creates the VM with **default-allow-ssh** open to `0.0.0.0/0`. v0.1 doesn't restrict this. v1.0 will provision a Moorpost-specific firewall rule scoped to the user's current public IP, and the user can widen with `--public-ssh` to cover laptop IP changes.

If you want stricter access today: run `gcloud compute firewall-rules update default-allow-ssh --source-ranges=YOUR_IP/32` on the project after `moorpost provision`.

## Audit

Every CLI invocation logs to `~/.moorpost/logs/<date>.log` (rotated daily, 30-day retention). `moorpost audit` (v1.0 deliverable, not yet shipped) prints the last N actions with timestamps for security review.

## Anthropic ToS compliance argument

Anthropic's February 2026 Terms of Service update prohibits using OAuth tokens in third-party tools or SDKs. Moorpost is compliant for these reasons:

1. **Token sourced via the official `claude setup-token` flow** — Moorpost does not implement OAuth itself; it wraps Anthropic's CLI command which produces a long-lived token.
2. **Token forwarded to the user's own Claude Code process** — the token is written to `/etc/moorpost/env` on a VM the user owns, then read by the user's own `claude` binary. Moorpost itself never makes Anthropic API calls.
3. **No proxying** — Moorpost is not a "third-party tool" in the API sense; it's a deployment helper that copies a credential from machine A (yours) to machine B (also yours) and tells the same `claude` binary on B to use it.
4. **No persistence beyond user's machines** — Moorpost-controlled infrastructure does not exist; the project is OSS and stateless from the user's perspective.

The README has a parallel summary aimed at end users; this page exists as the formal reasoning for security-conscious adopters and any future Anthropic clarification request.

## Reporting

Security issues should be reported via [security@latentadvisory.com](mailto:security@latentadvisory.com) (placeholder; will move to GitHub Security Advisories once the repo is public).
