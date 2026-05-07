# Design: auto-install Moorpost CLI from the VSCode extension

**Status:** approved 2026-05-07
**Owner:** Landy
**Target release:** Moorpost v1.1.5

## Problem

Users who install the Moorpost extension from the VS Code Marketplace today
end up with a working extension but no CLI binary on their machine. Every
extension command shells out to `moorpost`, so the extension is effectively
inert until the user manually downloads the right binary from the GitHub
releases page and puts it on their PATH. That gap is the single biggest
friction point in the install story.

The fix: when the extension activates and finds no compatible `moorpost`
binary, it silently downloads the right one for the user's platform,
verifies it, installs it, and continues.

## Goals

- First-time users who install the extension from the Marketplace get a
  working setup with no manual binary install.
- Compatibility is enforced — the extension never tries to talk to a CLI
  it knows is too old.
- Failure modes are visible: if auto-install can't run (offline,
  unsupported OS, FS error), the user gets a clear notification with a
  fallback link rather than a silently broken extension.

## Non-goals

- Windows support — the CLI is darwin/linux only and the extension itself
  carries no plan to change that.
- Hot-update notifications when an extension is older than the installed
  CLI. The compatibility check is one-directional (extension declares the
  minimum CLI it needs).
- GPG signature verification of downloads. SHA-256 from the release's
  `SHA256SUMS` file is the integrity check.
- Installing other prereqs (gcloud, mutagen, tmux, etc.). Those still go
  through `moorpost setup` once the binary is in place.

## Design

### Compatibility check

A new module `extension/src/cliInstaller.ts` exports:

```ts
export const MIN_CLI_VERSION = '1.1.5';
export async function ensureCliInstalled(): Promise<void>;
```

`MIN_CLI_VERSION` is a hard-coded constant in the extension, bumped each
release that requires a newer CLI. (Lock-step with extension version is
not required — bump only when the extension actually depends on a new
CLI feature.)

On activation (after `onStartupFinished` runs), `extension.ts` calls
`ensureCliInstalled()`. The check:

1. Spawn `<cliBinary()> --version` with a 3-second timeout.
2. Parse the leading `vX.Y.Z` token from stdout. (The binary emits
   `v1.1.4 (commit abc1234, built …)` from `version.Info()`.)
3. If the spawn fails (`ENOENT`) **or** the version is below
   `MIN_CLI_VERSION`, trigger install.
4. Otherwise, no-op.

The check is silent on the happy path — no UI surface unless install
runs.

### Install flow

The install runs silently as a `vscode.window.withProgress` notification
in the `Notification` location with `cancellable: false`. Steps:

1. **Resolve target.** Map `process.platform` × `process.arch` to a
   release asset name:

   | platform | arch  | asset                  |
   |----------|-------|------------------------|
   | darwin   | arm64 | `moorpost-darwin-arm64`|
   | darwin   | x64   | `moorpost-darwin-amd64`|
   | linux    | arm64 | `moorpost-linux-arm64` |
   | linux    | x64   | `moorpost-linux-amd64` |

   Anything else → fall through to the manual-install error path
   (see below).

2. **Download.** GET
   `https://github.com/latent-advisory/moorpost/releases/download/v<MIN_CLI_VERSION>/<asset>`
   into a temp file under `os.tmpdir()`. Use Node's `https` module with
   redirect-following (GitHub redirects release assets to `objects.githubusercontent.com`).

3. **Verify.** GET the `SHA256SUMS` file from the same release. Parse it
   for the line matching `<asset>`, hash the downloaded file with
   `crypto.createHash('sha256')`, and compare. Mismatch → abort with an
   error.

4. **Install.** `mkdir -p ~/.local/bin`, `fs.rename` the temp file to
   `~/.local/bin/moorpost`, `chmod 0755`. If `rename` fails with
   `EXDEV` (cross-device — temp dir on a different volume), fall back
   to `fs.copyFile` + `fs.unlink`.

5. **PATH plumbing.** If `~/.local/bin` is not on `process.env.PATH`,
   write the absolute path to the `moorpost.cliPath` user setting via
   `vscode.workspace.getConfiguration('moorpost').update('cliPath', '~/.local/bin/moorpost', vscode.ConfigurationTarget.Global)`.
   Subsequent `cliBinary()` calls will pick it up. PATH detection uses
   the current `process.env.PATH` split on `:` rather than spawning a
   shell, since the test only matters for what this VSCode process can
   see.

6. **Confirm.** Re-run `<cliBinary()> --version` to verify the install
   landed. On success, show a one-line info toast: `Moorpost CLI
   installed (v<X.Y.Z>).`

### Error handling

Any failure in steps 1–6 is caught and surfaces as a non-modal error
notification:

> Moorpost CLI auto-install failed: <reason>. Install manually from the
> release page.

with a single button **Open release page** that opens
`https://github.com/latent-advisory/moorpost/releases/tag/v<MIN_CLI_VERSION>`
in the user's browser via `vscode.env.openExternal`.

The extension does not block activation on the failure — the user can
still install manually and reload, and existing users with a working
binary are unaffected.

Error categories the user might hit:

- **Unsupported platform** (Windows, exotic arch). Step 1 short-circuits
  with the message "Windows is not supported; use WSL or install
  manually from the release page."
- **Network failure** during download or SHA fetch. Surface the OS error.
- **SHA mismatch.** Surface "checksum mismatch — refusing to install."
- **Filesystem error** writing to `~/.local/bin`. Surface the OS error.

### Settings surface

No new user-facing settings. The existing `moorpost.cliPath` setting is
written automatically when needed; the user can override it manually if
they want a different binary location.

### Documentation updates

Three docs need to change as part of this work:

1. **`extension/README.md`** — add an "Install" section that says the
   CLI is auto-installed on first activation. Also add a one-liner
   prerequisite note up top that **GCP is required for now** (the only
   provider implementation), so Marketplace browsers know what they're
   committing to before installing. Today the README implies provider
   choice exists.

2. **`README.md`** (repo root) — add a one-paragraph note in the
   install section that VSCode users no longer need to download the
   binary separately; CLI users still do.

3. **`PLUGIN.md`** — add a short subsection under §10 (extension
   surface) describing the auto-install flow. PLUGIN.md is the
   authoritative design doc; future contributors need this to make sense
   of `cliInstaller.ts`.

## Component layout

- **New file**: `extension/src/cliInstaller.ts` (~150 lines). Exports
  `MIN_CLI_VERSION` and `ensureCliInstalled()`. Self-contained — no
  imports from existing extension modules beyond `vscode`.
- **Edit**: `extension/src/extension.ts`. Add a single
  `await ensureCliInstalled()` call at activation, after the existing
  setup but before the first-run nudge fires (so the nudge runs against
  a working CLI).
- **Edit**: `extension/README.md` — install section + GCP-required note.
- **Edit**: `README.md` — install section update.
- **Edit**: `PLUGIN.md` — auto-install subsection.

No changes to the CLI itself in this iteration.

## Testing

`cliInstaller.ts` is mostly I/O so unit tests need to mock the network
and filesystem:

- Mock `https.get` to return canned binary + canned `SHA256SUMS`.
- Mock `fs/promises` write/rename calls.
- Mock `child_process.execFile` for `--version`.

Test cases:

1. CLI missing → install runs end-to-end → `--version` succeeds.
2. CLI present at min version → no install.
3. CLI present below min version → install runs.
4. CLI present above min version → no install.
5. Unsupported platform → manual-install error surfaces, no download attempted.
6. SHA mismatch → install aborts, error surfaces, no file written to ~/.local/bin.
7. Network error mid-download → install aborts, error surfaces, temp file cleaned up.
8. `~/.local/bin` not on PATH → `moorpost.cliPath` is written.
9. `~/.local/bin` on PATH → `moorpost.cliPath` is not touched.

The existing extension test setup uses `mocha` + bundled VSCode test
runner; new tests slot into `extension/src/test/` if a test directory
exists, otherwise into a co-located `cliInstaller.test.ts`.

## Rollout

- Bump extension version to `1.1.5` in `extension/package.json`.
- Bump `MIN_CLI_VERSION` constant to `1.1.5`. (Even though the binary
  itself doesn't change in this release, keeping the floor in lock-step
  with the extension on the first introduction of auto-install means
  every new user gets a known-good pairing.)
- Build and publish the .vsix to the Marketplace via the manual upload
  path (vsce publish PAT issue is unresolved as of this spec).
- Update the GitHub release notes for v1.1.5 to mention auto-install.
