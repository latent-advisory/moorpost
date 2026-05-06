package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/session"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
	"github.com/spf13/cobra"
)

// returnCmd uses a non-reserved Go identifier; the user-facing name is
// "return" via Use:.
var returnCmd = &cobra.Command{
	Use:   "return",
	Short: "Pull the active Claude session back from the remote to local",
	Long: `Mirror of ` + "`moorpost handoff`" + `: pulls project files +
agent session state back from the remote, resumes the agent locally, and
optionally stops the VM (default).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunReturn(cmd.Context(), cmd.OutOrStdout(), c, ReturnOptions{
			Stop:         returnFlagStop,
			PreferLocal:  returnFlagPreferLocal,
			PreferRemote: returnFlagPreferRemote,
			SessionID:    returnFlagSession,
			AllSessions:  returnFlagAll,
			Now:          nil,
		})
	},
}

var (
	returnFlagStop         bool
	returnFlagPreferLocal  bool
	returnFlagPreferRemote bool
	returnFlagSession      string
	returnFlagAll          bool
)

func init() {
	returnCmd.Flags().BoolVar(&returnFlagStop, "stop", true, "stop the VM after returning (saves money)")
	returnCmd.Flags().BoolVar(&returnFlagPreferLocal, "prefer-local", false, "abort if local has changed since last sync (use when local should win)")
	returnCmd.Flags().BoolVar(&returnFlagPreferRemote, "prefer-remote", false, "force-pull remote state, overwriting any divergence on local")
	returnCmd.Flags().StringVar(&returnFlagSession, "session", "", "return a specific session ID currently routed to remote (per-session return; VM stays running until all sessions are returned)")
	returnCmd.Flags().BoolVar(&returnFlagAll, "all", false, "return every session currently routed to remote (loops, then stops the VM)")
	rootCmd.AddCommand(returnCmd)
}

// ReturnOptions controls RunReturn.
type ReturnOptions struct {
	Stop bool
	// PreferLocal asserts the local copy is authoritative; refuse to
	// overwrite local with remote if local has diverged since the last
	// successful sync.
	PreferLocal bool
	// PreferRemote forces a pull regardless of local divergence;
	// equivalent to "I know remote is right, overwrite anything else."
	PreferRemote bool
	// SessionID, if non-empty, switches return to per-session mode: pull
	// only that session's JSONL, remove it from RemoteSIDs, and leave the
	// VM running unless RemoteSIDs becomes empty as a result.
	SessionID string
	// AllSessions, if true, loops over every entry in RemoteSIDs and
	// returns each one. Mutually exclusive with SessionID.
	AllSessions bool
	Now         func() time.Time
}

// RunReturn executes the remote→local pull and updates state.
func RunReturn(ctx context.Context, out io.Writer, c *Context, opts ReturnOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil || c.Agent == nil || c.Sync == nil {
		return errors.New("return: incomplete context")
	}
	if opts.PreferLocal && opts.PreferRemote {
		return errors.New("return: --prefer-local and --prefer-remote are mutually exclusive")
	}
	if opts.SessionID != "" && opts.AllSessions {
		return errors.New("return: --session and --all are mutually exclusive")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("return: project not provisioned")
	}

	// Per-session routing decision tree:
	//
	//   --session <sid>        → per-session return for that SID
	//   --all                  → loop per-session return over every SID
	//   no flag, RemoteSIDs=[] → legacy whole-project return (back-compat)
	//   no flag, RemoteSIDs≠[] → ambiguous; require explicit flag
	//
	// The legacy whole-project path keeps working for state.json files
	// written before the per-session schema landed (RemoteSIDs is
	// omitempty / zero-length).
	switch {
	case opts.SessionID != "":
		return runReturnSession(ctx, out, c, ps, opts, now, opts.SessionID)
	case opts.AllSessions:
		// Snapshot the SIDs up-front; runReturnSession mutates RemoteSIDs.
		sids := append([]string(nil), ps.RemoteSIDs...)
		if len(sids) == 0 {
			return errors.New("return: --all but no sessions are routed to remote")
		}
		for _, sid := range sids {
			// Re-read project state so we see the post-removal RemoteSIDs.
			cur, _ := c.State.GetProject(projectKey(c))
			if err := runReturnSession(ctx, out, c, cur, opts, now, sid); err != nil {
				return err
			}
		}
		return nil
	case len(ps.RemoteSIDs) > 0:
		return fmt.Errorf("return: specify --session <sid> or --all (currently routed to remote: %s)", strings.Join(ps.RemoteSIDs, ", "))
	}

	// Legacy whole-project return (no per-session SIDs recorded).
	if ps.ActiveSide != state.SideRemote {
		return errors.New("return: active side is not remote — nothing to bring back")
	}
	return runReturnWholeProject(ctx, out, c, ps, opts, now)
}

// runReturnWholeProject is the pre-per-session behavior: pull everything,
// flip ActiveSide=local, optionally stop the VM. Kept for back-compat with
// state.json files that don't yet have RemoteSIDs.
func runReturnWholeProject(ctx context.Context, out io.Writer, c *Context, ps state.ProjectState, opts ReturnOptions, now func() time.Time) error {
	tgt, err := c.Provider.SSHTarget(ctx, ps.VMID)
	if err != nil || tgt.Host == "" {
		return fmt.Errorf("return: cannot resolve remote SSH target: %w", err)
	}

	// Stop the continuous project-file sync. With continuous bidirectional
	// sync running since handoff, the local working tree is already current
	// — no project-files OneShot pull needed. We just terminate the session.
	if ps.SyncSessionID != "" {
		fmt.Fprintf(out, "Stopping sync session %s ...\n", ps.SyncSessionID)
		if err := c.Sync.Stop(ctx, mpsync.SyncSessionID(ps.SyncSessionID)); err != nil {
			// Stale session is recoverable; log and continue.
			fmt.Fprintf(out, "warning: sync stop failed (session may be stale): %v\n", err)
		}
	} else {
		fmt.Fprintln(out, "(no continuous sync session recorded; this handoff predates v0.2.1)")
	}

	// Pull session state.
	localState := c.Agent.SessionStatePath(c.ProjectDir)
	var newWatermark string
	if localState != "" {
		// Symmetric to handoff: detect local divergence before overwriting
		// local with remote. If local has changed since the last sync
		// (indicating something modified local while remote was active —
		// shouldn't normally happen but matches PLUGIN.md §6.5 line 261's
		// "both sides modified" case), --prefer-local refuses to overwrite.
		localManifest, manifestErr := session.LocalManifest(localState)
		if manifestErr != nil {
			fmt.Fprintf(out, "warning: could not compute local session-state manifest: %v\n", manifestErr)
		}
		localDiverged := manifestErr == nil && ps.LastSessionSyncHash != "" && localManifest != ps.LastSessionSyncHash
		if localDiverged && opts.PreferLocal {
			return fmt.Errorf("return: local session state has changed since last sync (--prefer-local refuses to overwrite local with remote; use --prefer-remote if remote should win, or run `moorpost handoff --prefer-local` first to push local)")
		}

		remoteState := remoteSessionStateFor(c)
		fmt.Fprintf(out, "Syncing agent session state ← %s ...\n", tgt.Host)
		if err := c.Sync.OneShot(ctx,
			mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remoteState + "/"},
			mpsync.Endpoint{Path: localState + "/"},
			mpsync.DirectionRemoteToLocal,
		); err != nil {
			fmt.Fprintf(out, "warning: session state sync failed: %v\n", err)
		}
		// Recompute the manifest AFTER the pull to capture remote's state
		// as the new watermark. If the OneShot failed, this is still a
		// reasonable best-effort — we'll detect divergence on the next sync.
		newWatermark, _ = session.LocalManifest(localState)
	}

	// Update state: active_side=local, LastReturn stamped, clear sync session,
	// refresh the session-state watermark to local (post-pull) content.
	if err := withProjectState(c, func(p *state.ProjectState) error {
		p.LastReturn = now()
		p.ActiveSide = state.SideLocal
		p.SyncSessionID = ""
		if newWatermark != "" {
			p.LastSessionSyncHash = newWatermark
		}
		return nil
	}); err != nil {
		return err
	}

	if opts.Stop {
		fmt.Fprintf(out, "Stopping %s...\n", ps.VMID)
		if err := c.Provider.Stop(ctx, ps.VMID); err != nil {
			fmt.Fprintf(out, "warning: stop failed: %v\n", err)
		} else {
			if err := withVM(c, ps.VMID, func(rec *state.VMRecord) error {
				rec.StateCache = "stopped"
				return nil
			}); err != nil {
				fmt.Fprintf(out, "warning: state cache update: %v\n", err)
			}
		}
	}

	fmt.Fprintln(out, "Done. Local Claude is active again. Run `claude --resume` to pick up where remote left off.")
	return nil
}

// runReturnSession returns a single session by SID:
//
//  1. Validate sid ∈ ps.RemoteSIDs.
//  2. Pull just that session's JSONL (rsync remote-to-local).
//  3. Remove the SID from RemoteSIDs.
//  4. If RemoteSIDs is now empty: stop the VM (if opts.Stop), set
//     ActiveSide=local, stamp LastReturn. Otherwise leave VM + ActiveSide
//     alone and tell the user how many remain.
func runReturnSession(ctx context.Context, out io.Writer, c *Context, ps state.ProjectState, opts ReturnOptions, now func() time.Time, sid string) error {
	if !ps.HasRemoteSID(sid) {
		return fmt.Errorf("session not on remote: %s", sid)
	}
	tgt, err := c.Provider.SSHTarget(ctx, ps.VMID)
	if err != nil || tgt.Host == "" {
		return fmt.Errorf("return: cannot resolve remote SSH target: %w", err)
	}

	localState := c.Agent.SessionStatePath(c.ProjectDir)
	if localState != "" {
		remoteState := remoteSessionStateFor(c)
		// Pull just <sid>.jsonl. OneShot accepts a file path on either
		// endpoint; this scopes the pull to a single session, leaving
		// every other JSONL on remote untouched (other live sessions
		// keep writing to remote and will sync on their own return).
		remotePath := remoteState + "/" + sid + ".jsonl"
		localPath := localState + "/" + sid + ".jsonl"
		fmt.Fprintf(out, "Pulling session %s ← %s ...\n", sid, tgt.Host)
		if err := c.Sync.OneShot(ctx,
			mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remotePath},
			mpsync.Endpoint{Path: localPath},
			mpsync.DirectionRemoteToLocal,
		); err != nil {
			fmt.Fprintf(out, "warning: session %s sync failed: %v\n", sid, err)
		}
	}

	// Remove the SID and decide whether to stop the VM. Done in a single
	// state-lock window so the "is empty?" check is consistent with the
	// removal we just made.
	var remaining int
	if err := withProjectState(c, func(p *state.ProjectState) error {
		for i, s := range p.RemoteSIDs {
			if s == sid {
				p.RemoteSIDs = append(p.RemoteSIDs[:i], p.RemoteSIDs[i+1:]...)
				break
			}
		}
		// PendingResumeSID baton: hand the just-returned SID to the
		// wrapper so the next plugin-spawned local claude resumes it.
		p.PendingResumeSID = sid
		remaining = len(p.RemoteSIDs)
		if remaining == 0 {
			p.LastReturn = now()
			p.ActiveSide = state.SideLocal
			// Clear the project-level sync session — symmetric to the
			// whole-project return, since no live remote work is left.
			p.SyncSessionID = ""
		}
		return nil
	}); err != nil {
		return err
	}

	if remaining > 0 {
		fmt.Fprintf(out, "%d session(s) still on remote — VM kept running. Run `moorpost return --session <id>` for each to fully return.\n", remaining)
		return nil
	}

	// RemoteSIDs empty → safe to stop the VM.
	if opts.Stop {
		fmt.Fprintf(out, "Stopping %s...\n", ps.VMID)
		if err := c.Provider.Stop(ctx, ps.VMID); err != nil {
			fmt.Fprintf(out, "warning: stop failed: %v\n", err)
		} else {
			if err := withVM(c, ps.VMID, func(rec *state.VMRecord) error {
				rec.StateCache = "stopped"
				return nil
			}); err != nil {
				fmt.Fprintf(out, "warning: state cache update: %v\n", err)
			}
		}
	}
	fmt.Fprintln(out, "Done. No sessions left on remote. Run `claude --resume` locally to pick up where remote left off.")
	return nil
}

// silence unused import if we drop one in future edits
var _ = agent.OSUbuntu
