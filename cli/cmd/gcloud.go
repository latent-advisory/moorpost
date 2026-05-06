package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// gcloudCmd groups gcloud-related introspection helpers. Currently the only
// child is `configs`, used by the VSCode extension to render a native
// picker before launching `moorpost init` / `moorpost bootstrap` with the
// chosen configuration as flags.
var gcloudCmd = &cobra.Command{
	Use:   "gcloud",
	Short: "Inspect gcloud state without going through the interactive picker",
}

var gcloudConfigsCmd = &cobra.Command{
	Use:   "configs",
	Short: "List local gcloud configurations (for tooling: prefer --json)",
	Long: `Lists all gcloud configurations on this machine. Identical data to
` + "`gcloud config configurations list`" + `, returned in a stable shape so the
VSCode extension and other tooling can render their own picker UI.

Pass --json for machine-readable output. Without --json, prints a
two-column human-readable summary.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		configs, err := listGcloudConfigurations()
		if err != nil {
			return fmt.Errorf("gcloud config configurations list: %w (is gcloud installed?)", err)
		}
		if gcloudConfigsJSON {
			// Always emit a JSON array (never `null`) so consumers don't
			// need to special-case the empty case.
			if configs == nil {
				configs = []gcloudConfig{}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(configs)
		}
		if len(configs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No gcloud configurations found.")
			return nil
		}
		for _, c := range configs {
			marker := " "
			if c.IsActive {
				marker = "*"
			}
			fmt.Fprintf(cmd.OutOrStdout(), " %s %-20s account=%s  project=%s\n",
				marker, c.Name, c.Account, c.Project)
		}
		return nil
	},
}

var gcloudConfigsJSON bool

func init() {
	gcloudConfigsCmd.Flags().BoolVar(&gcloudConfigsJSON, "json", false, "emit JSON output instead of the human-readable table")
	gcloudCmd.AddCommand(gcloudConfigsCmd)
	rootCmd.AddCommand(gcloudCmd)
}
