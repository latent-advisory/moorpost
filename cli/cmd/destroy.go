package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/latent-advisory/moorpost/cli/internal/state"
	"github.com/spf13/cobra"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Delete the project's VM and its disk (irreversible)",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunDestroy(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), c, destroyFlagYes)
	},
}

var destroyFlagYes bool

func init() {
	destroyCmd.Flags().BoolVarP(&destroyFlagYes, "yes", "y", false, "skip the confirmation prompt")
	rootCmd.AddCommand(destroyCmd)
}

// RunDestroy permanently deletes the project's VM and removes the project
// from state. The caller can pass auto-confirm via skipPrompt or pipe "y\n"
// to in.
func RunDestroy(ctx context.Context, out io.Writer, in io.Reader, c *Context, skipPrompt bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil {
		return errors.New("destroy: incomplete context")
	}
	if c.State == nil {
		return errors.New("destroy: no state available")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("destroy: project not provisioned")
	}
	if !skipPrompt {
		fmt.Fprintf(out, "About to permanently delete VM %s and its disk. Type 'yes' to confirm: ", ps.VMID)
		reader := bufio.NewReader(in)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("destroy: confirmation read failed: %w", err)
		}
		if strings.TrimSpace(strings.ToLower(line)) != "yes" {
			return errors.New("destroy: aborted (confirmation not received)")
		}
	}
	fmt.Fprintf(out, "Destroying %s...\n", ps.VMID)
	if err := c.Provider.Destroy(ctx, ps.VMID); err != nil {
		return fmt.Errorf("destroy: %w", err)
	}
	// Remove the project + VM records from state.
	err := state.WithLock(c.StatePath, func(s *state.State) error {
		delete(s.Projects, projectKey(c))
		delete(s.VMs, ps.VMID)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "VM %s destroyed and removed from state.\n", ps.VMID)
	return nil
}
