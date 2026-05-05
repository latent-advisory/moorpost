# Prerequisites

Moorpost orchestrates several existing tools rather than reinventing them. The setup command detects which are missing and prompts to install each.

| Tool | Purpose |
| --- | --- |
| `gcloud` | Google Cloud CLI — provisions and manages the remote VM |
| `mutagen` | Bidirectional file sync between laptop and VM |
| `tmux` | Persistent terminal session on the VM that survives disconnects |
| `ripgrep`, `rsync` | Fast file ops used by sync and discovery |
| `node` (≥18) | Runtime for the Claude Code CLI |
| `claude` | Claude Code CLI itself |

**What `moorpost setup` does:** detects each missing tool, prompts you per-item, and installs via Homebrew (macOS) or the appropriate package manager (Linux). Use `--yes` to skip prompts, `--dry-run` to preview.

After setup completes you'll still need:

```bash
gcloud auth login
gcloud config set project YOUR_GCP_PROJECT
gcloud services enable compute.googleapis.com --project=YOUR_GCP_PROJECT
```
