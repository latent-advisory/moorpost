# Moorpost Troubleshooting

## Compute Engine API not enabled

**Symptom (running `moorpost provision`):**

```
provision: gcp preflight failed:
  - Compute Engine API not enabled on project "your-project"
    fix: gcloud services enable compute.googleapis.com --project=your-project
```

**Fix:** copy-paste the `gcloud services enable` command from the error message. One-time, free, additive.

---

## No active gcloud account

**Symptom:** `provision: gcp preflight failed: no active gcloud account; run: gcloud auth login`

**Fix:** `gcloud auth login`, follow the browser flow, retry.

---

## SSH key propagation delay on first attach

**Symptom (running `moorpost attach` immediately after `moorpost up`):**

```
ssh: connect to host x.x.x.x port 22: Connection refused
```

**Cause:** GCP's OS Login key propagation takes 20-60 seconds after a VM starts.

**Fix:** wait 30 seconds and retry.

---

## `claude` command not found on the remote

**Symptom (running `moorpost handoff`):**

```
claudecode.Resume: ... claude: command not found
```

**Cause:** the bootstrap script is still installing Node + Claude Code (~5-7 min on `e2-small`).

**Fix:** wait. Check progress with:
```bash
gcloud compute ssh <vm-name> --zone=<zone> --command='tail -f /var/log/moorpost-bootstrap.log'
```

Look for `[moorpost] bootstrap complete`. If the log is stuck or shows an error, run `moorpost reset` to recreate the VM with a fresh bootstrap.

---

## Mutagen sync conflict on `.docx` files

**Symptom:** mutagen reports a conflict on a `.docx` file written by the desktop Claude app.

**Cause:** the file was modified on both sides between syncs.

**Fix (v0.1):** Moorpost's sync policy is `alpha-wins` (local takes precedence) for `.docx`/`.xlsx`. The remote copy is overwritten on next sync. **Don't have remote Claude Code edit `.docx` files at the project root** — treat them as read-only research input on the remote side.

---

## `moorpost handoff` says "active side is already remote"

**Symptom:** `handoff: active side is already remote — nothing to do`

**Cause:** state.json says you already handed off (and didn't return).

**Fix:** `moorpost return` first. Or, if you know the remote state is broken, `moorpost status --json | jq .active_side` to confirm, then manually edit `~/.moorpost/state.json` to set `active_side: local` for the project. (v0.2 will add a `--force-active=local|remote` flag.)

---

## VM hits CPU/RAM quota errors

**Symptom (running `moorpost provision`):**

```
QUOTA_EXCEEDED: Quota 'CPUS' exceeded. Limit: 8.0 in region us-central1.
```

**Fix:** request a quota increase via [console.cloud.google.com/iam-admin/quotas](https://console.cloud.google.com/iam-admin/quotas), or change to a smaller machine type in `.moorpost/config.yaml`:

```yaml
provider:
  type: gcp
  gcp:
    machine_type: e2-small  # was e2-standard-2
```

---

## Bootstrap fails partway

**Symptom:** `claude --version` on the remote fails after handoff.

**Diagnosis:** SSH into the VM and read the log:
```bash
gcloud compute ssh <vm> --zone=<zone> --command='tail -50 /var/log/moorpost-bootstrap.log'
```

**Fix:** the bootstrap script is idempotent; running `moorpost reset` recreates the VM and re-runs bootstrap from clean state. Project files are preserved (mutagen has a local copy).

---

## Mutagen daemon not running

**Symptom:** `moorpost handoff` errors with "mutagen daemon not running".

**Fix:** `mutagen daemon start`. Also verify with `mutagen version`.

---

## Cost surprises

**Symptom:** monthly GCP bill higher than expected.

**Likely causes:**
- VM was left running (use `moorpost down` between sessions)
- Persistent disk size larger than needed (smaller disks → smaller per-GB cost)
- Egress charges (rare; only matters if you transfer large files often)

**Fix v0.1:** `moorpost cost` for an estimate. Real Cloud Billing API integration arrives in v0.3.

**Mitigation:** edit `.moorpost/config.yaml`:
```yaml
cost:
  monthly_cap_usd: 50    # Moorpost will refuse to start the VM if MTD spend exceeds
  alert_thresholds: [10, 25]
```

The cap-enforcement landing in v0.3 will hard-stop provisioning above the cap.

---

## "Anthropic API authentication failed" on the remote

**Symptom (from inside `moorpost attach`):**

```
$ claude
Error: Authentication failed. Please run `claude login`.
```

**Cause:** the OAuth token didn't get injected, or the cached token has been revoked.

**Fix:** `moorpost auth --re-authenticate` (forces a fresh `claude setup-token` flow), then `moorpost handoff` again to push the new token.

---

## Need help

- [GitHub Issues](https://github.com/latent-advisory/moorpost/issues) (once the repo is public)
- [PLUGIN.md](../PLUGIN.md) — full design doc; sections are linked from each command's source
- [security.md](security.md) — security model and threat analysis
