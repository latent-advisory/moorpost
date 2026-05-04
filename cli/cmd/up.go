package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/latent-advisory/moorpost/cli/internal/state"
	"github.com/spf13/cobra"
)

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Start the project's VM",
	Long: `Starts a previously-provisioned VM. Refreshes the cached external
IP. With --persistent the VM is configured for always-on mode (auto-stop
discipline lives in v0.3).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunUp(cmd.Context(), cmd.OutOrStdout(), c, UpOptions{
			Persistent:  upFlagPersistent,
			OverrideCap: upFlagOverrideCap,
		})
	},
}

var (
	upFlagPersistent  bool
	upFlagOverrideCap bool
)

func init() {
	upCmd.Flags().BoolVar(&upFlagPersistent, "persistent", false, "always-remote mode (do not auto-stop)")
	upCmd.Flags().BoolVar(&upFlagOverrideCap, "override-cap", false, "bypass cost.monthly_cap_usd")
	rootCmd.AddCommand(upCmd)
}

// UpOptions are the runtime knobs for RunUp.
type UpOptions struct {
	Persistent  bool
	OverrideCap bool
}

// RunUp starts the project's VM and refreshes the cached IP.
func RunUp(ctx context.Context, out io.Writer, c *Context, opts UpOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil {
		return errors.New("up: incomplete context")
	}
	if c.State == nil {
		return errors.New("up: no state available")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("up: project not provisioned (run `moorpost provision` first)")
	}
	if err := enforceCostCap(ctx, c, opts.OverrideCap); err != nil {
		return fmt.Errorf("up: %w", err)
	}
	fmt.Fprintf(out, "Starting %s...\n", ps.VMID)
	if err := c.Provider.Start(ctx, ps.VMID); err != nil {
		return fmt.Errorf("up: %w", err)
	}
	tgt, err := c.Provider.SSHTarget(ctx, ps.VMID)
	if err != nil {
		// VM is started but IP not yet available — not fatal; warn and continue.
		fmt.Fprintf(out, "Started, but could not resolve external IP: %v\n", err)
	}
	if err := withVM(c, ps.VMID, func(rec *state.VMRecord) error {
		rec.StateCache = "running"
		if tgt.Host != "" {
			rec.ExternalIP = tgt.Host
		}
		return nil
	}); err != nil {
		return err
	}
	if opts.Persistent {
		fmt.Fprintf(out, "VM %s running (persistent mode).\n", ps.VMID)
	} else {
		fmt.Fprintf(out, "VM %s running. Connect with `moorpost attach`.\n", ps.VMID)
	}
	return nil
}
