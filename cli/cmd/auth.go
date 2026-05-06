package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/agent/claudecode"
	"github.com/latent-advisory/moorpost/cli/internal/keychain"
	"github.com/spf13/cobra"
)

// defaultAgentID is the agent used for `moorpost auth` outside a project. Auth
// is per-machine (token is cached in the OS keychain), so it must work before
// `moorpost init` — which is the order the quickstart and walkthrough teach.
const defaultAgentID = claudecode.AgentID

var authFlagToken string

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate the configured AI agent on this machine",
	Long: `Runs the configured agent's local auth flow (e.g. ` + "`claude setup-token`" + `
for Claude Code). The captured credential is stored in the OS keychain
and forwarded to remote VMs at provision/handoff time.

Runs project-aware when invoked inside a project (uses the configured agent),
otherwise falls back to the default agent (` + defaultAgentID + `) so first-run
machines can authenticate before initializing a project.

If --token is supplied, store it directly without running the agent's auth
flow. Useful when the OAuth-token regex doesn't match a newer Claude Code
release, or when scripting in CI. Setting the ANTHROPIC_API_KEY env var
has the same effect.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// --token bypass: stash whatever the user pasted, but validate it
		// looks like an actual Claude credential first. Without this, a
		// stray invocation like `moorpost auth --token -` (or any
		// short/garbage value) silently stores a useless string and the
		// failure surfaces much later when remote claude prompts for OAuth
		// — far from the actual cause. Real credentials are either
		// long-lived OAuth tokens (sk-ant-oat01-…) or API keys
		// (sk-ant-api03-…); both share the sk-ant-* prefix and are >20 chars.
		if t := strings.TrimSpace(authFlagToken); t != "" {
			if !strings.HasPrefix(t, "sk-ant-") || len(t) < 20 {
				return fmt.Errorf("auth: --token value does not look like a Claude credential (expected sk-ant-* prefix, got %q); paste the actual token from `claude setup-token` or your API console", truncateForError(t))
			}
			kc, err := keychain.New()
			if err != nil {
				return fmt.Errorf("auth: keychain: %w", err)
			}
			if err := kc.Store(claudecode.KeychainService, claudecode.KeychainAccount, []byte(t)); err != nil {
				return fmt.Errorf("auth: keychain store: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stored token in keychain (%d chars). No OAuth flow needed.\n", len(t))
			return nil
		}

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
	authCmd.Flags().StringVar(&authFlagToken, "token", "", "store this token directly in the keychain (skip the OAuth flow)")
	rootCmd.AddCommand(authCmd)
}

// truncateForError returns a short, safe rendering of a candidate token
// for error messages. Tokens are sensitive — even invalid ones may
// echo onto a CI log — so we cap at 8 chars and add an ellipsis when
// trimmed. Empty input renders as "" so the error message stays grammatical.
func truncateForError(s string) string {
	const max = 8
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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
