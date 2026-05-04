package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/latent-advisory/moorpost/cli/internal/state"
	"github.com/spf13/cobra"
)

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Snapshot, destroy, and re-provision the project's VM (counters bit-rot)",
	Long: `Reset rebuilds the VM from a clean bootstrap. It first snapshots
the existing disk so nothing is lost, then destroys the VM, then re-provisions
with the same spec. Project files come back at the next handoff (mutagen
keeps a copy locally).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunReset(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), c, ResetOptions{
			SkipSnapshot: resetFlagSkipSnapshot,
			SSHKeyPath:   resetFlagSSHKey,
			SkipPrompt:   resetFlagYes,
		})
	},
}

var (
	resetFlagYes          bool
	resetFlagSkipSnapshot bool
	resetFlagSSHKey       string
)

func init() {
	resetCmd.Flags().BoolVarP(&resetFlagYes, "yes", "y", false, "skip confirmation prompt")
	resetCmd.Flags().BoolVar(&resetFlagSkipSnapshot, "skip-snapshot", false, "skip the auto-snapshot before destroy")
	resetCmd.Flags().StringVar(&resetFlagSSHKey, "ssh-key", "", "ssh public key path (default: ~/.ssh/google_compute_engine.pub)")
	rootCmd.AddCommand(resetCmd)
}

// ResetOptions controls RunReset.
type ResetOptions struct {
	SkipSnapshot bool
	SSHKeyPath   string
	SkipPrompt   bool
}

// RunReset performs snapshot → destroy → re-provision.
func RunReset(ctx context.Context, out io.Writer, in io.Reader, c *Context, opts ResetOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil {
		return errors.New("reset: incomplete context")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("reset: project not provisioned")
	}

	if !opts.SkipPrompt {
		fmt.Fprintf(out, "About to snapshot, destroy, and re-create %s. Continue? [y/N]: ", ps.VMID)
		if !readYesNo(in) {
			return errors.New("reset: aborted")
		}
	}

	if !opts.SkipSnapshot {
		fmt.Fprintf(out, "Snapshotting %s before destroy...\n", ps.VMID)
		id, err := c.Provider.Snapshot(ctx, ps.VMID, "pre-reset")
		if err != nil {
			return fmt.Errorf("reset: pre-snapshot failed (use --skip-snapshot to bypass): %w", err)
		}
		fmt.Fprintf(out, "Snapshot: %s\n", id)
	}

	fmt.Fprintf(out, "Destroying %s...\n", ps.VMID)
	if err := c.Provider.Destroy(ctx, ps.VMID); err != nil {
		return fmt.Errorf("reset: destroy: %w", err)
	}

	// Remove old VM record but keep the project record (we'll reattach it).
	if err := state.WithLock(c.StatePath, func(s *state.State) error {
		delete(s.VMs, ps.VMID)
		// Clear VM-related fields on the project but keep the slug.
		p := s.Projects[projectKey(c)]
		p.VMID = ""
		p.AgentSessionID = ""
		s.SetProject(projectKey(c), p)
		c.State = s
		return nil
	}); err != nil {
		return err
	}

	fmt.Fprintln(out, "Re-provisioning with fresh bootstrap...")
	return RunProvision(ctx, out, c, ProvisionOptions{
		SSHKeyPath: opts.SSHKeyPath,
		Start:      false, // local-first: leave stopped after reset
	})
}

// readYesNo reads one line from in and returns true if it's "y" or "yes"
// (case-insensitive, with surrounding whitespace stripped).
func readYesNo(in io.Reader) bool {
	b := make([]byte, 16)
	n, _ := in.Read(b)
	s := strings.TrimSpace(string(b[:n]))
	switch strings.ToLower(s) {
	case "y", "yes":
		return true
	}
	return false
}
