package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/latent-advisory/moorpost/cli/internal/state"
	"github.com/spf13/cobra"
)

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop the project's VM (preserves disk)",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunDown(cmd.Context(), cmd.OutOrStdout(), c)
	},
}

func init() {
	rootCmd.AddCommand(downCmd)
}

// RunDown stops the project's VM.
func RunDown(ctx context.Context, out io.Writer, c *Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil {
		return errors.New("down: incomplete context")
	}
	if c.State == nil {
		return errors.New("down: no state available")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("down: project not provisioned")
	}
	fmt.Fprintf(out, "Stopping %s...\n", ps.VMID)
	if err := c.Provider.Stop(ctx, ps.VMID); err != nil {
		return fmt.Errorf("down: %w", err)
	}
	if err := withVM(c, ps.VMID, func(rec *state.VMRecord) error {
		rec.StateCache = "stopped"
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "VM %s stopped.\n", ps.VMID)
	return nil
}
