# Provision the remote VM

`moorpost provision` creates the GCP VM defined by your `.moorpost/config.yaml`. The VM is left **stopped** by default so you don't pay running costs until you hand off.

**What happens:**

1. ~30s to create the VM and its boot disk
2. A bootstrap script runs in the background for ~5–7 min installing Node + Claude Code on the VM
3. You don't have to wait for the bootstrap — it'll be ready by the time you do your first `handoff`

**Daily flow after provisioning:**

| Action | Command |
| --- | --- |
| Step away from the laptop | `Moorpost: Handoff to remote` |
| Come back | `Moorpost: Return to local` |
| Inspect remote work mid-flight | `Moorpost: Attach to remote tmux` |
| Check spend | `Moorpost: Show cost details` |

**Cleanup at any time:** `Moorpost: Destroy VM…` removes both the VM and its disk. Snapshots survive so you can re-create.
