# Releasing Moorpost

This is the human-author checklist for cutting a Moorpost release. The
implementation loop produced the v1.0 functional code; this doc captures
the steps to *ship* it.

For semver, every release is **v0.x.y** (pre-1.0) or **v1.x.y** (post-1.0).
`cli/internal/version/version.go` carries the embedded version; the binary
also reads `git describe --tags` at build time via `Makefile` ldflags.

---

## v1.0 acceptance criteria

Before tagging `v1.0.0`, all of these must hold:

- [ ] `make test-race` passes (all 19 packages, no race warnings)
- [ ] `make lint` passes (`go vet ./...`)
- [ ] `make e2e-autostop` passes against real GCP — validates iter 33's auto-stop end-to-end
- [ ] `make extension-build` and `make extension-package` succeed; `tsc --noEmit` clean
- [ ] `moorpost --version` reports the tag, not `dev`
- [ ] `moorpost doctor` in a fresh project reports OK on all checks (or only `[WARN]` on optional ones)
- [ ] Manual smoke: `moorpost init` + `moorpost provision` + `moorpost handoff` + `moorpost return` + `moorpost destroy` happy path with a single project
- [ ] `IMPLEMENTATION_LOG.md` does NOT have unresolved `BLOCKED:` entries from any in-flight iteration

---

## Pre-flight (before the actual release)

1. **Working tree clean.** `git status` shows no uncommitted changes; `git push origin main` succeeded; no in-flight WIP branches.

2. **All unit tests pass with `-race`:**
   ```sh
   make test-race
   ```

3. **Lint clean:**
   ```sh
   make lint
   ```

4. **Real-GCP validation.** Run the auto-stop E2E once on a healthy network:
   ```sh
   make e2e-autostop
   ```
   This pre-flights for orphan VMs, then runs ~15-25 min of real-GCP work
   for ~$0.005. If `apt-get update` is throttled (security.ubuntu.com
   sometimes is at night), retry during US business hours.

5. **VSCode extension builds + packages:**
   ```sh
   make extension-build
   make extension-package
   ls extension/*.vsix
   ```

6. **Manual CLI smoke (optional but recommended for v1.0):**
   ```sh
   cd /tmp/moorpost-smoke && mkdir -p test-project && cd test-project
   moorpost init --provider=gcp --project=YOUR_GCP_PROJECT
   moorpost doctor
   moorpost provision
   moorpost handoff
   # observe Claude resumes on remote
   moorpost return
   moorpost destroy --yes
   ```

---

## Release steps

### 1. Tag

```sh
TAG=v1.0.0
git tag -a $TAG -m "Moorpost $TAG"
git push origin $TAG
```

### 2. Build cross-platform binaries

```sh
make release    # populates dist/ with darwin/linux × amd64/arm64 + SHA256SUMS
ls dist/
```

### 3. Create GitHub Release

```sh
gh release create $TAG \
    --title "Moorpost $TAG" \
    --notes-file dist/RELEASE_NOTES.md \
    dist/moorpost-darwin-amd64 \
    dist/moorpost-darwin-arm64 \
    dist/moorpost-linux-amd64 \
    dist/moorpost-linux-arm64 \
    dist/SHA256SUMS
```

Release notes: distill highlights from `.loop/IMPLEMENTATION_LOG.md`. For
v1.0, the headline items are:
- VSCode extension shell + tree view + smart prompts + conflict UX
- VM-side auto-stop on idle (persistent mode)
- Cost guardrails (pre-flight cap, post-flight auto-stop, transparent estimates)
- `moorpost conflicts` for sync diagnosis
- Three-interface extensibility (Provider / Agent / Sync)

### 4. (Optional, v1.1+) VSCode Marketplace upload

The `.vsix` is produced but not published in v1.0 — marketplace listing
requires the extension to be signed (`vsce publish` with a Marketplace
account). Sign + publish is a separate one-time setup; defer to v1.1.

For users who want to install v1.0: download `moorpost-X.Y.Z.vsix` from
the GitHub Release and run:
```sh
code --install-extension moorpost-X.Y.Z.vsix
```

### 5. Post-release housekeeping

1. Bump `cli/internal/version/version.go`'s `Version` from the tag to
   `dev` so subsequent local builds show their commit, not the released
   tag. (Or leave it — `make build` always overrides via ldflags.)

2. Announce: README badges, blog post (if any), changelog entry.

3. Watch for issues — first-week bugs from real users tend to surface
   quickly. Hotfix branches go to `vX.Y.{Z+1}`.

---

## Hotfix releases

For a bug-fix release on top of `v1.0.0`:

```sh
git checkout v1.0.0
git checkout -b hotfix/v1.0.1
# ... patch the bug, write a test, commit ...
git tag -a v1.0.1 -m "Moorpost v1.0.1: <one-line>"
git push origin v1.0.1
make release && gh release create v1.0.1 ...
git checkout main && git merge --no-ff hotfix/v1.0.1
```

---

## What's NOT in v1.0

Per the implementation loop's scoping decisions:

- **Real Cloud Billing API** integration (deferred to v1.1 as `--actual` flag).
- **Additional cloud providers** (out of scope; v1 is GCP-only — AWS / Azure arrive in v2).
- **Native power-monitor sidecar** for the extension (extension-side detection works).
- **Multi-machine fleet mode** (per PLUGIN.md §10 #11).
- **Telemetry sender** (opt-in gate exists since iter 27, but no actual data path).
- **VSCode marketplace publishing** (signed upload).
- **Settings-panel webview** (the `moorpost setup` CLI flow covers initial setup).

These are tracked in `.loop/PLAN.md`'s "Out of scope" section.
