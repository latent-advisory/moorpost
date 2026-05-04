package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
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
			Now:          nil,
		})
	},
}

var (
	returnFlagStop         bool
	returnFlagPreferLocal  bool
	returnFlagPreferRemote bool
)

func init() {
	returnCmd.Flags().BoolVar(&returnFlagStop, "stop", true, "stop the VM after returning (saves money)")
	returnCmd.Flags().BoolVar(&returnFlagPreferLocal, "prefer-local", false, "abort if local has changed since last sync (use when local should win)")
	returnCmd.Flags().BoolVar(&returnFlagPreferRemote, "prefer-remote", false, "force-pull remote state, overwriting any divergence on local")
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
	Now          func() time.Time
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
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("return: project not provisioned")
	}
	if ps.ActiveSide != state.SideRemote {
		return errors.New("return: active side is not remote — nothing to bring back")
	}

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

// silence unused import if we drop one in future edits
var _ = agent.OSUbuntu
