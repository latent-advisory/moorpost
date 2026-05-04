package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/latent-advisory/moorpost/cli/internal/release"
	"github.com/latent-advisory/moorpost/cli/internal/version"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check whether a newer moorpost release is available",
	Long: `Asks GitHub Releases for the latest moorpost version and prints
a friendly message if you're behind. Does NOT auto-install (signed binary
replacement is fraught; opt into the install yourself with the printed
command).

Result is cached for 1 hour at ~/.moorpost/release-cache.json. Use
--no-cache to force a fresh check.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cachePath := ""
		if !updateFlagNoCache {
			home, err := os.UserHomeDir()
			if err == nil {
				cachePath = filepath.Join(home, ".moorpost", "release-cache.json")
			}
		}
		return RunUpdate(cmd.Context(), cmd.OutOrStdout(),
			release.NewHTTPFetcher(), cachePath, version.Version)
	},
}

var updateFlagNoCache bool

func init() {
	updateCmd.Flags().BoolVar(&updateFlagNoCache, "no-cache", false, "force a fresh check, ignoring the 1-hour cache")
	rootCmd.AddCommand(updateCmd)
}

// RunUpdate compares the running version against the latest GitHub release
// and prints a one- or two-line user-facing message. Network failures
// produce a friendly error rather than crashing.
func RunUpdate(ctx context.Context, out io.Writer, f release.Fetcher, cachePath, current string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r, err := release.LatestRelease(ctx, f, cachePath, "")
	if err != nil {
		fmt.Fprintf(out, "Could not check for updates: %v\n", err)
		fmt.Fprintln(out, "Tip: visit https://github.com/latent-advisory/moorpost/releases for the latest version.")
		return nil // not a hard failure for the user
	}
	if release.IsCurrent(current, r) {
		fmt.Fprintf(out, "You're on the latest moorpost (%s).\n", r.TagName)
		return nil
	}
	fmt.Fprintf(out, "moorpost %s is available (you have %s).\n", r.TagName, current)
	fmt.Fprintf(out, "Release notes: %s\n", r.HTMLURL)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Install with one of:")
	fmt.Fprintln(out, "  • macOS: brew upgrade moorpost              # if you installed via brew")
	fmt.Fprintln(out, "  • from source: git pull && make install")
	fmt.Fprintln(out, "  • binary: download from the release page above and replace your moorpost binary")
	return nil
}
