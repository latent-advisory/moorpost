package cmd

import (
	"context"
	"fmt"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/agent/claudecode"
	"github.com/spf13/cobra"
)

// defaultAgentID is the agent used for `moorpost auth` outside a project. Auth
// is per-machine (token is cached in the OS keychain), so it must work before
// `moorpost init` — which is the order the quickstart and walkthrough teach.
const defaultAgentID = claudecode.AgentID

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate the configured AI agent on this machine",
	Long: `Runs the configured agent's local auth flow (e.g. ` + "`claude setup-token`" + `
for Claude Code). The captured credential is stored in the OS keychain
and forwarded to remote VMs at provision/handoff time.

Runs project-aware when invoked inside a project (uses the configured agent),
otherwise falls back to the default agent (` + defaultAgentID + `) so first-run
machines can authenticate before initializing a project.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: false,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		a := c.Agent
		if a == nil {
			a, err = agent.Get(defaultAgentID, nil)
			if err != nil {
				return fmt.Errorf("auth: default agent %q: %w", defaultAgentID, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "No project config found — authenticating default agent (%s).\n", defaultAgentID)
		}
		return RunAuth(cmd.Context(), cmd.OutOrStdout(), a)
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
