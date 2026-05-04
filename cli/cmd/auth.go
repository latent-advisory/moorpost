package cmd

import (
	"context"
	"fmt"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	_ "github.com/latent-advisory/moorpost/cli/internal/agent/claudecode"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate the configured AI agent on this machine",
	Long: `Runs the configured agent's local auth flow (e.g. ` + "`claude setup-token`" + `
for Claude Code). The captured credential is stored in the OS keychain
and forwarded to remote VMs at provision/handoff time.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunAuth(cmd.Context(), cmd.OutOrStdout(), c.Agent)
	},
}

func init() {
	rootCmd.AddCommand(authCmd)
}

// RunAuth invokes the agent's local-auth flow and prints a summary. Exposed
// for testing.
func RunAuth(ctx context.Context, out interface{ Write([]byte) (int, error) }, a agent.Agent) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil {
		return fmt.Errorf("auth: no agent configured")
	}
	cred, err := a.AuthenticateLocal(ctx)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	fmt.Fprintf(out, "Authenticated %s (%s) — token cached locally.\n", a.ID(), cred.Kind)
	return nil
}
