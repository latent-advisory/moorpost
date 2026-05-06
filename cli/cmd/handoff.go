package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/session"
	"github.com/latent-advisory/moorpost/cli/internal/sshconfig"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
	"github.com/spf13/cobra"
)

// nonSlugRE mirrors claudecode.nonSlugRE — claude encodes session-store
// dirs by replacing every non-[a-zA-Z0-9-] rune with `-`. We compute
// the resolved-cwd encoding (`-home-moorpost-moorpost-<slug>`) here to
// stage a symlink bridge on remote.
var nonSlugRE = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// lastPathSegment returns the trailing component of a /-delimited path,
// stripping a "~/" home prefix if present. Used to extract the encoded
// dirname from remoteState (`~/.claude/projects/<encoded>`).
func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// bridgeRemoteSessionDirs ensures remote claude's getcwd-resolved-encoded
// session dir resolves to the same content as the synced session dir.
// Without this, claude on remote (which calls getcwd() and gets the
// physical path /home/moorpost/moorpost/<slug>) looks at
// ~/.claude/projects/-home-moorpost-moorpost-<slug>/ for sessions, while
// our local→remote rsync delivers them to
// ~/.claude/projects/-Users-...-<projdir>/. Two parallel dirs, claude
// reads the wrong one, every --resume fails.
//
// This op is idempotent: if a real (non-symlink) dir already exists at
// the resolved-encoded path with sessions claude wrote there, we move
// those JSONLs into the synced dir before replacing it with the
// symlink. If the symlink is already in place, `ln -sfn` re-points it
// (no-op when already correct).
func bridgeRemoteSessionDirs(ctx context.Context, sshHost, identityFile, syncedEncoded, resolvedEncoded string) error {
	syncedQ := shellQuote("/home/moorpost/.claude/projects/" + syncedEncoded)
	resolvedQ := shellQuote("/home/moorpost/.claude/projects/" + resolvedEncoded)
	// shell script: only consolidate when the resolved path is a real
	// dir (not yet a symlink), and only mv files that don't already
	// exist at the destination.
	script := fmt.Sprintf(`set -e
mkdir -p %s
if [ -d %s ] && [ ! -L %s ]; then
  for f in %s/*.jsonl; do
    [ -f "$f" ] && mv -n "$f" %s/ || true
  done
  for d in %s/*/; do
    [ -d "$d" ] && mv -n "$d" %s/ || true
  done
  rmdir %s 2>/dev/null || rm -rf %s
fi
ln -sfn %s %s
`, syncedQ, resolvedQ, resolvedQ, resolvedQ, syncedQ, resolvedQ, syncedQ, resolvedQ, resolvedQ, syncedQ, resolvedQ)

	args := []string{"-i", identityFile, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", sshHost, "--", script}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh bridge: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shellQuote(s string) string {
	// Single-quote escaping: 'foo' becomes 'foo', 'fo\'o' becomes 'fo'\''o'.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hashPluginsDir computes a stable fingerprint of the local plugins
// tree by walking it and SHA-256ing a sorted manifest of {relative
// path, size, mtime} for every regular file. We don't hash file
// CONTENT because rsync already does delta detection — we just need
// a quick "did anything change here?" signal to skip the rsync entirely
// when nothing did. Returns "" on any walk error (caller treats empty
// as "always sync").
//
// Excludes the same directories rsync excludes (install-counts-cache,
// blocklist) so a hash recompute after a successful sync matches —
// otherwise these auto-rebuilt files would always force a re-sync.
func hashPluginsDir(root string) string {
	type entry struct {
		path string
		size int64
		mod  int64
	}
	var entries []entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		if base == "install-counts-cache.json" || base == "blocklist.json" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		entries = append(entries, entry{path: rel, size: info.Size(), mod: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", e.path, e.size, e.mod)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// syncPluginEnvToRemote mirrors the user's ~/.claude/plugins/ tree to
// the VM and rewrites local-only paths in the manifest files.
//
// Why this exists: the Anthropic Claude Code plugin spawns claude with
// `--input-format stream-json`. In that mode, claude writes nothing to
// stdout until either (a) it receives a stream-json message on stdin,
// or (b) a SessionStart hook fires and emits a `hook_started` event.
// The plugin's 60s "subprocess initialization did not complete" timer
// watches stdout for ANY output to confirm the subprocess is alive.
//
// On local, the user's installed plugins (e.g. `superpowers`) provide
// SessionStart hooks that fire on startup → plugin sees output → init
// timer clears. On remote (a fresh VM), no plugins are installed →
// claude is silent → 60s timer fires → plugin shows the timeout error
// to the user.
//
// Fix: rsync the local plugin tree into /home/moorpost/.claude/plugins/
// and rewrite the absolute /Users/<user>/.claude/plugins paths in
// installed_plugins.json + known_marketplaces.json to /home/moorpost/...
// so claude on remote can resolve the install locations.
//
// Idempotent: re-running on each handoff keeps remote in sync as the
// user adds/removes plugins on local. Errors are non-fatal: handoff
// proceeds even if plugin sync fails (the user just hits the 60s
// timeout on first claude spawn — recoverable by re-running handoff).
func syncPluginEnvToRemote(ctx context.Context, sshHost, identityFile, localPluginsDir, remoteHome string) error {
	if _, err := os.Stat(localPluginsDir); err != nil {
		// No local plugins to sync — fine, nothing to do.
		return nil
	}
	src := strings.TrimRight(localPluginsDir, "/") + "/"
	dst := sshHost + ":" + remoteHome + "/.claude/plugins/"

	rsyncArgs := []string{
		"-az",
		"-e", "ssh -i " + identityFile + " -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10",
		// Skip caches that auto-rebuild on remote and node_modules-style
		// detritus. We DO want the marketplace clones since claude reads
		// marketplace.json for plugin lookups.
		"--exclude=install-counts-cache.json",
		"--exclude=blocklist.json",
		"--delete-after",
		src,
		dst,
	}
	cmd := exec.CommandContext(ctx, "rsync", rsyncArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync plugins: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	// Path rewrite: remote claude reads installed_plugins.json's
	// `installPath` and known_marketplaces.json's `installLocation` to
	// resolve plugins. Both contain absolute /Users/<user>/... paths
	// that don't exist on the VM. Replace them with /home/moorpost/...
	// in-place. Without this rewrite, claude emits "Plugin <name> not
	// found in marketplace" for every plugin and disables them all →
	// no SessionStart hooks → 60s timeout returns.
	localHomePrefix, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home: %w", err)
	}
	// sed delimiter is `#`: localHomePrefix and remoteHome contain `/`
	// but no `#`, so this avoids any escaping of the path separators.
	manifest := remoteHome + "/.claude/plugins/installed_plugins.json"
	marketplace := remoteHome + "/.claude/plugins/known_marketplaces.json"
	rewriteScript := fmt.Sprintf(
		`sed -i 's#%s#%s#g' %s %s 2>/dev/null || true`,
		localHomePrefix,
		remoteHome,
		shellQuote(manifest),
		shellQuote(marketplace),
	)
	args := []string{"-i", identityFile, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", sshHost, "--", rewriteScript}
	if out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("plugin path rewrite: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Hand off the active Claude session from local to the remote VM",
	Long: `Pauses local agent (caller's responsibility — be at a turn
boundary), starts the VM if stopped, syncs the project + agent session
state to the remote, and resumes the agent there. After this, you can close
the laptop and the work continues on the VM.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunHandoff(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), c, HandoffOptions{
			SkipPrompt:   handoffFlagYes,
			OverrideCap:  handoffFlagOverrideCap,
			PreferLocal:  handoffFlagPreferLocal,
			PreferRemote: handoffFlagPreferRemote,
			SessionID:    handoffFlagSession,
			NewSession:   handoffFlagNewSession,
		})
	},
}

var (
	handoffFlagYes          bool
	handoffFlagOverrideCap  bool
	handoffFlagPreferLocal  bool
	handoffFlagPreferRemote bool
	handoffFlagSession      string
	handoffFlagNewSession   bool
)

func init() {
	handoffCmd.Flags().BoolVarP(&handoffFlagYes, "yes", "y", false, "skip the confirmation prompt")
	handoffCmd.Flags().BoolVar(&handoffFlagOverrideCap, "override-cap", false, "bypass cost.monthly_cap_usd")
	handoffCmd.Flags().BoolVar(&handoffFlagPreferLocal, "prefer-local", false, "force-push local session state, overwriting any divergence on the remote")
	handoffCmd.Flags().BoolVar(&handoffFlagPreferRemote, "prefer-remote", false, "abort handoff if local has diverged since last sync (use this when remote should win)")
	handoffCmd.Flags().StringVar(&handoffFlagSession, "session", "", "explicit session id to migrate (overrides AgentSessionID + most-recently-modified-JSONL fallback)")
	handoffCmd.Flags().BoolVar(&handoffFlagNewSession, "new-session", false, "hand off for a brand-new session (no SID yet); flips ActiveSide=remote so fresh spawns default to remote")
	rootCmd.AddCommand(handoffCmd)
}

// HandoffOptions controls RunHandoff.
type HandoffOptions struct {
	SkipPrompt  bool
	OverrideCap bool
	// PreferLocal forces a push regardless of local divergence; equivalent
	// to "I know my local copy is right, overwrite anything else."
	PreferLocal bool
	// PreferRemote refuses to push when local has diverged since last
	// sync — i.e., asserts remote should win. Use when local is suspected
	// to be corrupt or stale.
	PreferRemote bool
	// SessionID, when non-empty, pins the handoff to this specific session
	// JSONL — overriding both state.json's AgentSessionID and the
	// most-recently-modified-JSONL fallback. Set by the VSCode extension
	// to migrate the user's FOCUSED panel rather than whichever JSONL
	// was last written. Empty string means "use the existing fallback".
	SessionID string
	// NewSession, when true, signals the handoff is for a brand-new
	// session (no SID yet) — the wrapper's "fresh spawn defaults to
	// remote" path. In this mode we skip the SID resolution + per-SID
	// route registration, and instead flip ActiveSide=remote so any
	// fresh `claude` spawn picks up the remote target. Mutually
	// exclusive with SessionID.
	NewSession bool
	// Now overrides time.Now for deterministic testing of timestamps.
	Now func() time.Time
	// IPWaitTimeout overrides the default 60s.
	IPWaitTimeout time.Duration
	// SkipSSHWait skips the post-Start TCP poll that waits for sshd to
	// accept connections. Set by tests against fakes that don't open a
	// real TCP listener; production callers leave it false.
	SkipSSHWait bool
}

// RunHandoff executes the full local→remote handoff flow.
func RunHandoff(ctx context.Context, out io.Writer, in io.Reader, c *Context, opts HandoffOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil || c.Agent == nil || c.Sync == nil {
		return errors.New("handoff: incomplete context (need provider, agent, sync)")
	}
	if opts.PreferLocal && opts.PreferRemote {
		return errors.New("handoff: --prefer-local and --prefer-remote are mutually exclusive")
	}
	if opts.NewSession && opts.SessionID != "" {
		return errors.New("handoff: --new-session and --session are mutually exclusive")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ipWaitTimeout := opts.IPWaitTimeout
	if ipWaitTimeout == 0 {
		ipWaitTimeout = 60 * time.Second
	}

	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("handoff: project not provisioned (run `moorpost provision` first)")
	}
	// Per-session routing: ActiveSide is only flipped to remote by the
	// --new-session path (so fresh spawns default remote). Explicit
	// per-SID handoffs leave ActiveSide untouched, so the "already
	// remote" guard only applies in the NewSession case.
	if opts.NewSession && ps.ActiveSide == state.SideRemote {
		return errors.New("handoff: active side is already remote — nothing to do (use `moorpost return` to bring it back)")
	}

	if !opts.SkipPrompt {
		fmt.Fprintf(out, "Pause local Claude (be at a turn boundary), then continue? [y/N]: ")
		reader := bufio.NewReader(in)
		line, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			return errors.New("handoff: aborted")
		}
	}

	// Pre-flight credential check BEFORE starting the VM. Use the passive
	// LoadCachedCredential to avoid triggering an OAuth browser flow from
	// inside handoff (which surprised users who clicked the status bar
	// expecting handoff but got setup-token). If no token is cached, fail
	// fast with a clear hint.
	cred, err := c.Agent.LoadCachedCredential()
	if err != nil {
		return fmt.Errorf("handoff: %w (run `moorpost auth` first)", err)
	}
	// Sanity-check the cached credential. A previous bug allowed a
	// non-token value (e.g. a literal "-" from a misuse of `auth --token`)
	// to land in the keychain. With that bad value, InjectCredential
	// silently succeeds — but remote claude treats the token as invalid
	// and prompts for an interactive OAuth, which the user has to redo
	// every handoff. Fail fast here so the fix happens locally (re-run
	// `moorpost auth`) rather than mysteriously on the VM.
	if !strings.HasPrefix(cred.Value, "sk-ant-") || len(cred.Value) < 20 {
		return fmt.Errorf("handoff: cached credential does not look like a Claude token; clear it and re-authenticate: `security delete-generic-password -s 'moorpost.claude-code.token' -a 'default' && moorpost auth`")
	}

	// Cost cap.
	if err := enforceCostCap(ctx, c, opts.OverrideCap); err != nil {
		return fmt.Errorf("handoff: %w", err)
	}

	// Speed opt: skip Provider.Start + waitForIP when state.json's cache
	// already says the VM is running AND we can verify with a fast TCP
	// probe to sshd. Saves ~3-5s per warm handoff (the GCE/cloud API
	// roundtrips for Start + IP poll). On any failure (cache stale, sshd
	// not yet ready, network blip), we fall through to the full Start
	// path — best-effort optimization, not a contract change.
	var tgt provider.SSHTarget
	skipStart := false
	if rec, ok := c.State.VMs[ps.VMID]; ok && rec.StateCache == "running" && rec.ExternalIP != "" {
		probeTarget, err := c.Provider.SSHTarget(ctx, ps.VMID)
		if err == nil && probeTarget.Host == rec.ExternalIP && tcpReachable(probeTarget.Host, probeTarget.Port, 2*time.Second) {
			tgt = probeTarget
			skipStart = true
			fmt.Fprintf(out, "VM already running at %s (skipping start)\n", tgt.Host)
		}
	}

	if !skipStart {
		fmt.Fprintf(out, "Starting %s...\n", ps.VMID)
		if err := c.Provider.Start(ctx, ps.VMID); err != nil {
			return fmt.Errorf("handoff: start VM: %w", err)
		}
		// Poll for IP readiness.
		t, err := waitForIP(ctx, c.Provider, ps.VMID, ipWaitTimeout)
		if err != nil {
			return fmt.Errorf("handoff: %w", err)
		}
		tgt = t
		fmt.Fprintf(out, "VM running at %s\n", tgt.Host)
	}

	// Ephemeral GCE IPs change across stop/start cycles. Re-clear any
	// stale ~/.ssh/known_hosts entry for the new IP and (re-)write the
	// moorpost-managed ssh_config block so mutagen/rsync/ssh all
	// transparently use the right user + identity for this IP.
	clearStaleKnownHostsEntry(out, tgt.Host)
	if tgt.IdentityFile != "" {
		if err := sshconfig.EnsureHost(sshconfig.HostBlock{
			Host: tgt.Host, User: tgt.User, IdentityFile: tgt.IdentityFile, Port: tgt.Port,
		}); err != nil {
			fmt.Fprintf(out, "  (note: could not write moorpost ssh_config for %s: %v)\n", tgt.Host, err)
		}
	}

	// Provider.Start returns once GCE has assigned an IP, but sshd on
	// the booting VM isn't accepting connections yet — typical 20-60s
	// gap. Without this poll, the next step (InjectCredential) hits
	// "ssh: connect to host ... port 22: Operation timed out". Poll
	// the OS-level TCP handshake until 22/tcp is open, capped at 90s.
	//
	// Skipped on the warm fast path (skipStart=true): tcpReachable
	// already verified sshd is up.
	if !opts.SkipSSHWait && !skipStart {
		if err := waitForSSH(ctx, out, tgt.Host, tgt.Port, 90*time.Second); err != nil {
			return fmt.Errorf("handoff: %w", err)
		}
	}

	// Inject the cached credential into /etc/moorpost/env (or wherever the
	// agent puts it).
	agentTarget := agent.SSHTarget{Host: tgt.Host, Port: tgt.Port, User: tgt.User, IdentityFile: tgt.IdentityFile}
	if err := c.Agent.InjectCredential(ctx, agentTarget, cred); err != nil {
		return fmt.Errorf("handoff: inject credential: %w", err)
	}

	// One-shot push: agent session state directory. This must happen
	// BEFORE Resume so the remote claude --resume sees the latest state.
	// (Project files are handled by the continuous sync below.)
	localState := c.Agent.SessionStatePath(c.ProjectDir)
	var localManifest string
	if localState != "" {
		// Compute the local session-state manifest. If it's diverged from
		// the watermark recorded at the last successful sync, the user has
		// modified local since last handoff/return. That's expected on
		// handoff (active=local), so by default we proceed. But if the
		// user passed --prefer-remote, they want the remote to win — we
		// must NOT silently overwrite remote with diverged local. Abort.
		var manifestErr error
		localManifest, manifestErr = session.LocalManifest(localState)
		if manifestErr != nil {
			fmt.Fprintf(out, "warning: could not compute local session-state manifest: %v\n", manifestErr)
		}
		localDiverged := manifestErr == nil && ps.LastSessionSyncHash != "" && localManifest != ps.LastSessionSyncHash
		if localDiverged && opts.PreferRemote {
			return fmt.Errorf("handoff: local session state has changed since last sync (--prefer-remote refuses to overwrite local with remote without explicit --prefer-local; if you really want remote to win, run `moorpost return --prefer-remote` first)")
		}

		remoteState := remoteSessionStateFor(c)
		// Pre-create the remote session-state dir's parent. rsync only
		// auto-creates one level; without this, the very first handoff
		// fails with "mkdir failed: No such file or directory" because
		// ~/.claude/projects/ doesn't exist yet on a fresh VM. Without
		// the session state actually landing, remote `claude --resume`
		// starts with an empty conversation — the user's "fork to
		// remote" intent is silently dropped. Best-effort: errors here
		// are non-fatal, the rsync below will produce a clearer one.
		// Gated on SkipSSHWait so tests against fakes don't try to
		// shell out to a real ssh against a fake host.
		if !opts.SkipSSHWait {
			if err := ensureRemoteDir(ctx, hostFromTarget(tgt), tgt.IdentityFile, remoteState); err != nil {
				fmt.Fprintf(out, "warning: could not pre-create %s: %v\n", remoteState, err)
			}
		}
		fmt.Fprintf(out, "Syncing agent session state → %s ...\n", tgt.Host)
		if err := c.Sync.OneShot(ctx,
			mpsync.Endpoint{Path: localState + "/"},
			mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remoteState + "/"},
			mpsync.DirectionLocalToRemote,
		); err != nil {
			// Session-state may not exist yet (first run); warn but don't fail.
			fmt.Fprintf(out, "warning: session state sync failed (this may be the first run): %v\n", err)
		}

		// Bridge resolved-cwd encoding ↔ synced encoding on remote.
		//
		// Background: the bootstrap creates /Users/<user>/.../<projdir> as
		// a symlink to /home/moorpost/moorpost/<slug> so `cd <localpath>`
		// works on remote. But when claude opens a session, it calls
		// getcwd() which RESOLVES the symlink and returns the physical
		// path. Claude then encodes that physical path for session lookup
		// (e.g. `-home-moorpost-moorpost-<slug>`), while our rsync just
		// delivered files to the unresolved-path encoding (e.g.
		// `-Users-...-<projdir>`). Two parallel session dirs, claude
		// reads the wrong one, every `--resume <sid>` fails with
		// "No conversation found" — even though the JSONL is right
		// there on disk under the other encoding.
		//
		// Fix: after rsync, move any sessions claude wrote under the
		// resolved-encoded dir into the synced-encoded dir, then replace
		// the resolved-encoded dir with a symlink. From now on, claude's
		// writes flow through the symlink and end up co-located with
		// the synced files. Idempotent: re-running a handoff repairs
		// any drift.
		if !opts.SkipSSHWait {
			resolvedEncoded := "-home-moorpost-moorpost-" + nonSlugRE.ReplaceAllString(c.Config.ProjectSlug, "-")
			syncedEncoded := lastPathSegment(remoteState)
			if syncedEncoded != "" && resolvedEncoded != "" && syncedEncoded != resolvedEncoded {
				if err := bridgeRemoteSessionDirs(ctx, hostFromTarget(tgt), tgt.IdentityFile, syncedEncoded, resolvedEncoded); err != nil {
					fmt.Fprintf(out, "warning: could not bridge remote session dirs (%s ↔ %s): %v\n", syncedEncoded, resolvedEncoded, err)
				}
			}

			// Sync the user's plugin environment to remote so SessionStart
			// hooks fire there too — without this, the Anthropic plugin's
			// 60s "subprocess initialization did not complete" timer
			// fires because remote claude in stream-json mode emits
			// nothing on stdout until something kicks it off. See
			// syncPluginEnvToRemote's docs for the full mechanism.
			//
			// Speed opt: skip the rsync when the local plugin tree's
			// hash matches LastPluginsSyncHash — saves ~3-5s per warm
			// handoff when the user hasn't changed plugins since the
			// last handoff to this VM.
			homeDir, _ := os.UserHomeDir()
			localPluginsDir := homeDir + "/.claude/plugins"
			currentHash := hashPluginsDir(localPluginsDir)
			if currentHash != "" && currentHash == ps.LastPluginsSyncHash {
				fmt.Fprintf(out, "Plugin environment unchanged on local — skipping sync\n")
			} else {
				fmt.Fprintf(out, "Syncing plugin environment → %s ...\n", tgt.Host)
				if err := syncPluginEnvToRemote(ctx, hostFromTarget(tgt), tgt.IdentityFile, localPluginsDir, "/home/moorpost"); err != nil {
					fmt.Fprintf(out, "warning: plugin environment sync failed: %v\n", err)
					fmt.Fprintf(out, "  remote claude may hit the 60s init timeout on first spawn — re-run handoff to retry\n")
				} else if currentHash != "" {
					// Persist the hash so the next handoff can skip.
					if err := withProjectState(c, func(p *state.ProjectState) error {
						p.LastPluginsSyncHash = currentHash
						return nil
					}); err != nil {
						fmt.Fprintf(out, "warning: could not persist plugin sync hash: %v\n", err)
					}
				}
			}
		}
	}

	// Start the continuous bidirectional project-file sync. Per
	// PLUGIN.md §6.5, project files are continuously synced (so the
	// desktop app's .docx edits at the root propagate to the remote
	// while Claude Code is running there).
	remoteProjectPath := remoteProjectPathFor(c)
	fmt.Fprintf(out, "Starting continuous sync %s ↔ %s:%s ...\n", c.ProjectDir, tgt.Host, remoteProjectPath)
	syncID, err := c.Sync.StartSession(ctx, mpsync.SyncSpec{
		Label:          c.Config.ProjectSlug + "-handoff",
		Alpha:          mpsync.Endpoint{Path: c.ProjectDir},
		Beta:           mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remoteProjectPath},
		ConflictPolicy: c.Config.Sync.ConflictPolicy,
		IgnorePatterns: c.Config.Sync.Ignore,
	})
	if err != nil {
		return fmt.Errorf("handoff: start sync session: %w", err)
	}

	// Resume the agent on the remote. Resolution order:
	//   1. opts.SessionID (set by the VSCode extension to migrate the
	//      user's FOCUSED panel; flag --session on the CLI).
	//   2. ps.AgentSessionID (recorded by the wrapper on the last spawn).
	//   3. most-recently-modified JSONL in ~/.claude/projects/<encoded>/.
	// (3) handles the "user has been chatting in claude, hits handoff
	// for the first time" path without an explicit session-pick UI; it
	// gets the wrong session when multiple panels are open and the
	// user is focused on one whose JSONL isn't the latest write —
	// (1) is the fix for that case.
	//
	// --new-session bypasses resolution entirely: there's no SID yet,
	// so we pass an empty SessionRef.SessionID and let claude on remote
	// spawn a fresh one. Per-SID routing isn't relevant in that mode;
	// instead we flip ActiveSide=remote below so the wrapper routes
	// fresh spawns to remote.
	var sessionID string
	if !opts.NewSession {
		sessionID = opts.SessionID
		if sessionID != "" {
			fmt.Fprintf(out, "  (using explicit --session %s)\n", sessionID)
		}
		if sessionID == "" {
			sessionID = ps.AgentSessionID
		}
		if sessionID == "" && localState != "" {
			if id := mostRecentSession(localState); id != "" {
				fmt.Fprintf(out, "  (auto-selected most recent session %s)\n", id)
				sessionID = id
			}
		}
	} else {
		fmt.Fprintln(out, "  (--new-session: spawning fresh remote session)")
	}
	ref := agent.SessionRef{
		ProjectSlug:   c.Config.ProjectSlug,
		ProjectAbsDir: c.ProjectDir,
		SessionID:     sessionID,
	}
	fmt.Fprintf(out, "Resuming claude on remote (slug=%s)...\n", ref.ProjectSlug)
	if err := c.Agent.Resume(ctx, agentTarget, ref); err != nil {
		return fmt.Errorf("handoff: resume on remote: %w", err)
	}

	// Update state with handoff metadata + new continuous sync session ID.
	// Also bump the session-state watermark to the manifest we just pushed,
	// so the next handoff/return can detect divergence against this point.
	//
	// Routing model (per-session):
	//   * --new-session: no specific SID to register; flip ActiveSide=
	//     remote so the wrapper's fresh-spawn fallback routes new
	//     panels to remote. RemoteSIDs is left untouched.
	//   * Explicit/auto-resolved SID: register that SID into RemoteSIDs
	//     (idempotently) so the wrapper routes only THAT session to
	//     remote. Leave ActiveSide alone — handing off one session must
	//     not flip every fresh panel to remote.
	if err := withProjectState(c, func(p *state.ProjectState) error {
		p.LastHandoff = now()
		// ActiveSide flips to remote when:
		//   - --new-session: explicit "fresh spawns go remote" intent.
		//   - no explicit --session flag (legacy CLI invocation
		//     `moorpost handoff` with no args): preserve old whole-
		//     project semantics so existing CLI users / scripts / e2e
		//     tests aren't broken. The legacy return path also requires
		//     ActiveSide=remote, so this keeps the round-trip working.
		// When --session is explicit (extension's per-session UX), leave
		// ActiveSide alone so other panels keep their local default.
		if opts.NewSession || opts.SessionID == "" {
			p.ActiveSide = state.SideRemote
		}
		p.SyncSessionID = string(syncID)
		if localManifest != "" {
			p.LastSessionSyncHash = localManifest
		}
		// Plugin-mode "Migrate this conversation" baton: the moorpost
		// VSCode extension reads this after handoff and offers to
		// trigger claude-vscode.newConversation; the wrapper picks it
		// up on the next claude spawn and injects --resume so remote
		// claude continues this exact conversation. Single-use:
		// wrapper clears it after consume. No-op outside plugin mode.
		// --new-session has no SID to migrate-resume to, so skip.
		if !opts.NewSession && sessionID != "" {
			p.PendingResumeSID = sessionID
		}
		return nil
	}); err != nil {
		return err
	}
	// Per-SID routing: register the resolved SID into RemoteSIDs so the
	// wrapper routes that specific session to remote (other sessions
	// stay on whatever ActiveSide says). Idempotent: a second handoff
	// of the same SID is a no-op.
	//
	// Only fires when the caller EXPLICITLY passed --session (the
	// extension's per-session UX). Legacy `moorpost handoff` with no
	// flags falls through to the ActiveSide=remote path above and
	// leaves RemoteSIDs empty — which keeps the legacy whole-project
	// return semantics working (return with no flag walks the
	// ActiveSide=remote branch when RemoteSIDs is empty).
	if opts.SessionID != "" {
		if err := withProjectState(c, func(p *state.ProjectState) error {
			for _, existing := range p.RemoteSIDs {
				if existing == opts.SessionID {
					return nil
				}
			}
			p.RemoteSIDs = append(p.RemoteSIDs, opts.SessionID)
			return nil
		}); err != nil {
			return err
		}
	}
	if err := withVM(c, ps.VMID, func(rec *state.VMRecord) error {
		rec.StateCache = "running"
		rec.ExternalIP = tgt.Host
		return nil
	}); err != nil {
		return err
	}

	fmt.Fprintln(out, "Done. Local Claude is now inactive. Use `moorpost attach` to view, `moorpost return` to bring back.")
	return nil
}

// waitForIP polls Provider.SSHTarget until the VM has an external IP, up to
// timeout. Returns the resolved target.
func waitForIP(ctx context.Context, p provider.Provider, vmID string, timeout time.Duration) (provider.SSHTarget, error) {
	deadline := time.Now().Add(timeout)
	delay := 250 * time.Millisecond
	for {
		tgt, err := p.SSHTarget(ctx, vmID)
		if err == nil && tgt.Host != "" {
			return tgt, nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return provider.SSHTarget{}, fmt.Errorf("VM %s not reachable within %s: %w", vmID, timeout, err)
			}
			return provider.SSHTarget{}, fmt.Errorf("VM %s has no external IP within %s", vmID, timeout)
		}
		select {
		case <-ctx.Done():
			return provider.SSHTarget{}, ctx.Err()
		case <-time.After(delay):
		}
		// Modest exponential backoff capped at 2s.
		if delay < 2*time.Second {
			delay = delay * 2
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
		}
	}
}

// mostRecentSession scans dir for *.jsonl files and returns the session ID
// (filename without extension) of the most recently modified one. Returns
// "" if the dir doesn't exist, isn't readable, or has no .jsonl files.
// Used to disambiguate when state.json's AgentSessionID isn't set — the
// active session in the current terminal almost always has the most
// recent mtime.
func mostRecentSession(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var bestName string
	var bestMtime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestMtime) {
			bestMtime = info.ModTime()
			bestName = strings.TrimSuffix(e.Name(), ".jsonl")
		}
	}
	return bestName
}

// ensureRemoteDir runs `mkdir -p` over SSH to make sure the remote path
// exists before any rsync target it. Best-effort: returns the SSH error
// so callers can decide whether to surface it; the typical caller treats
// failure as non-fatal (rsync below will emit a clearer error).
func ensureRemoteDir(ctx context.Context, sshHost, identityFile, remotePath string) error {
	if sshHost == "" || remotePath == "" {
		return nil
	}
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if identityFile != "" {
		args = append(args, "-i", identityFile)
	}
	args = append(args, sshHost, "--", "mkdir", "-p", remotePath)
	cmd := exec.CommandContext(ctx, "ssh", args...) // #nosec G204 — args are static + provider data
	return cmd.Run()
}

// waitForSSH polls the VM's 22/tcp until it accepts connections, or until
// tcpReachable is a single-shot TCP probe with a hard timeout. Used by
// the warm-handoff fast path to verify sshd is actually accepting
// connections before we trust state.json's "VM is running" cache.
// Returns false on any error (refused, timeout, DNS failure) — the
// caller falls through to the full Provider.Start path.
func tcpReachable(host string, port int, timeout time.Duration) bool {
	if port == 0 {
		port = 22
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// timeout elapses. Avoids the "Provider.Start returned, but sshd hasn't
// finished booting" race that surfaces as "Operation timed out" on the
// next ssh-using step (credential inject, sync, etc).
func waitForSSH(ctx context.Context, out io.Writer, host string, port int, timeout time.Duration) error {
	if port == 0 {
		port = 22
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	fmt.Fprintf(out, "Waiting for sshd at %s ...\n", addr)
	deadline := time.Now().Add(timeout)
	delay := 1 * time.Second
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var d net.Dialer
		conn, err := d.DialContext(dialCtx, "tcp", addr)
		cancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sshd at %s not reachable within %s: %w", addr, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		// Backoff: 1s → 2s → 4s, capped at 4s.
		if delay < 4*time.Second {
			delay = delay * 2
			if delay > 4*time.Second {
				delay = 4 * time.Second
			}
		}
	}
}

// hostFromTarget returns the SSH host string used by Sync engines. We prefer
// `<user>@<host>` so rsync/mutagen log into the right account.
func hostFromTarget(tgt provider.SSHTarget) string {
	if tgt.User == "" {
		return tgt.Host
	}
	return tgt.User + "@" + tgt.Host
}

// remoteProjectPathFor returns the conventional remote path for the
// project's working tree: ~/moorpost/<slug>.
func remoteProjectPathFor(c *Context) string {
	if c == nil || c.Config == nil {
		return ""
	}
	return "~/moorpost/" + c.Config.ProjectSlug
}

// remoteSessionStateFor returns the remote path for the agent's session
// state directory. Uses the agent's encoder against the project's local
// absolute path; the iter 14 bootstrap symlink keeps the encoded form
// matching on both sides.
func remoteSessionStateFor(c *Context) string {
	if c == nil || c.Agent == nil {
		return ""
	}
	local := c.Agent.SessionStatePath(c.ProjectDir)
	if local == "" {
		return ""
	}
	// Take only the last path component (the encoded slug).
	if i := strings.LastIndex(local, "/"); i >= 0 {
		return "~/.claude/projects/" + local[i+1:]
	}
	return "~/.claude/projects/" + local
}
