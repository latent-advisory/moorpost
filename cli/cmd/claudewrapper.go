package cmd

import (
	"fmt"

	"github.com/latent-advisory/moorpost/cli/internal/claudewrapper"
	"github.com/spf13/cobra"
)

var claudeWrapperCmd = &cobra.Command{
	Use:   "install-claude-wrapper",
	Short: "Install the moorpost shim that routes Anthropic Claude Code plugin calls to local or remote",
	Long: `Writes a bash script to ~/.moorpost/bin/claude-wrapper that wraps the
Claude Code plugin's claude invocation. When the project's active_side is
'remote', the wrapper SSHes to the VM and runs claude there; when local,
it transparently runs the local claude binary.

After installing, set Anthropic Claude Code plugin setting:

  "claudeCode.claudeProcessWrapper": "<path printed below>"

Then handoff/return seamlessly switch the plugin's panel between local
and remote claude — typing in the panel forwards to whichever side is
active. The moorpost VSCode extension does this setting management
automatically.

Idempotent: re-running overwrites the script so it always matches the
moorpost build that wrote it.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		path, err := claudewrapper.Install()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Installed: %s\n", path)
		fmt.Fprintln(cmd.OutOrStdout(), "Point Anthropic Claude Code plugin's `claudeCode.claudeProcessWrapper` setting at this path.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(claudeWrapperCmd)
}
