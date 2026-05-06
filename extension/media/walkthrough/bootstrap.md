# Bootstrap (one-shot)

Single command. Handles the whole setup → sign-in → init → provision
flow, skipping work that's already done.

## What it does

| Step | What | Time |
| --- | --- | --- |
| 1. Setup | Detects gcloud, mutagen, tmux, ripgrep, rsync, node, claude on your PATH; installs missing via brew/apt | ~5s if all installed, ~30s per missing tool |
| 2. Auth | Opens a browser for the Claude OAuth flow; caches the token in your OS keychain | ~30s (interactive) |
| 3. Init | Asks which workspace folder, gcloud configuration, and machine type to use; writes `.moorpost/config.yaml`. The machine-type picker shows hourly rate + monthly estimate; `e2-standard-2` is the recommended default | <1s |
| 4. Provision *(optional)* | Creates the GCE VM (left stopped to save cost). Polls SSH until `claude` is on PATH on the VM | ~3 min wall-clock |

The wizard prompts you to opt in/out of step 4 — useful for cost-conscious
first-runs (you can `Provision` later from the palette).

## After bootstrap

| Daily action | What |
| --- | --- |
| `Moorpost: Handoff` | Pause local Claude → sync to VM → resume on remote inside an integrated terminal |
| Type prompts in that terminal | Goes to remote Claude, which keeps working on the VM |
| `Moorpost: Return` | Pulls session + files back; VM stops |

## Trouble?

- **OAuth doesn't capture the token** (`no sk-ant-*-* token found in setup-token output`): newer Claude Code releases sometimes don't print the token to stdout. Fix: `moorpost auth --token <paste>` or set `ANTHROPIC_API_KEY` and re-run.
- **`Run doctor (diagnostics)`** from the palette runs `moorpost doctor` to verify everything's wired up.
