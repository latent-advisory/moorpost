package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
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
			Stop: returnFlagStop,
			Now:  nil,
		})
	},
}

var returnFlagStop bool

func init() {
	returnCmd.Flags().BoolVar(&returnFlagStop, "stop", true, "stop the VM after returning (saves money)")
	rootCmd.AddCommand(returnCmd)
}

// ReturnOptions controls RunReturn.
type ReturnOptions struct {
	Stop bool
	Now  func() time.Time
}

// RunReturn executes the remote→local pull and updates state.
func RunReturn(ctx context.Context, out io.Writer, c *Context, opts ReturnOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil || c.Agent == nil || c.Sync == nil {
		return errors.New("return: incomplete context")
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

	// Pull project files.
	remoteProjectPath := remoteProjectPathFor(c)
	fmt.Fprintf(out, "Syncing project files ← %s:%s ...\n", tgt.Host, remoteProjectPath)
	if err := c.Sync.OneShot(ctx,
		mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remoteProjectPath + "/"},
		mpsync.Endpoint{Path: c.ProjectDir + "/"},
		mpsync.DirectionRemoteToLocal,
	); err != nil {
		return fmt.Errorf("return: sync project: %w", err)
	}

	// Pull session state.
	localState := c.Agent.SessionStatePath(c.ProjectDir)
	if localState != "" {
		remoteState := remoteSessionStateFor(c)
		fmt.Fprintf(out, "Syncing agent session state ← %s ...\n", tgt.Host)
		if err := c.Sync.OneShot(ctx,
			mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remoteState + "/"},
			mpsync.Endpoint{Path: localState + "/"},
			mpsync.DirectionRemoteToLocal,
		); err != nil {
			fmt.Fprintf(out, "warning: session state sync failed: %v\n", err)
		}
	}

	// Update state: active_side=local, LastReturn stamped.
	if err := withProjectState(c, func(p *state.ProjectState) error {
		p.LastReturn = now()
		p.ActiveSide = state.SideLocal
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
