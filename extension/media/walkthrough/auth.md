# Sign in to Claude

`moorpost auth` runs `claude setup-token` for you and stashes the resulting OAuth token in your **macOS Keychain** (or Linux Secret Service).

**What you'll see:**

1. A browser opens to claude.ai
2. Sign in with your Pro / Max / Team subscription
3. Copy the device code; paste it back in the terminal
4. The terminal prints: `Authenticated claude-code (oauth-subscription) — token cached locally.`

**One-time per machine.** All projects share the same token; you don't need to re-auth per workspace.

**Alternative:** if you don't have a subscription, set `ANTHROPIC_API_KEY` in your environment before running `moorpost auth` — Moorpost will use API-key mode instead.
