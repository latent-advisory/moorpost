package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Snapshot the project's VM disk (for backup or pre-reset)",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunSnapshot(cmd.Context(), cmd.OutOrStdout(), c, snapshotFlagLabel)
	},
}

var snapshotFlagLabel string

func init() {
	snapshotCmd.Flags().StringVar(&snapshotFlagLabel, "label", "manual", "label for the snapshot (sanitized)")
	rootCmd.AddCommand(snapshotCmd)
}

// RunSnapshot creates a disk snapshot via the configured Provider.
func RunSnapshot(ctx context.Context, out io.Writer, c *Context, label string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil {
		return errors.New("snapshot: incomplete context")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("snapshot: project not provisioned")
	}
	if label == "" {
		label = "manual"
	}
	id, err := c.Provider.Snapshot(ctx, ps.VMID, label)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	fmt.Fprintf(out, "Snapshot created: %s\n", id)
	return nil
}
