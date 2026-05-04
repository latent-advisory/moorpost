package cmd

import (
	"github.com/spf13/cobra"
)

// Version is the CLI version. Overridden at build time via -ldflags.
var Version = "0.0.0-dev"

var rootCmd = &cobra.Command{
	Use:           "moorpost",
	Short:         "Tether your laptop to a remote forward base where Claude Code keeps working.",
	Long:          "Moorpost lets you work locally by default and hand off to a remote VM when stepping away.\nSee https://github.com/latent-advisory/moorpost for documentation.",
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command and is called by main.
func Execute() error {
	return rootCmd.Execute()
}
