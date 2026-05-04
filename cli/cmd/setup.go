package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "One-shot install of Moorpost's local prerequisites",
	Long: `Detects which Moorpost prerequisites are missing on this machine
(gcloud, mutagen, tmux, ripgrep, claude) and offers to install each via the
host's package manager. Saves you ~6 manual `+"`brew install`"+` commands
on a fresh Mac.

Already-installed binaries are skipped. Pass --yes to skip the per-binary
prompts. Use --dry-run to see what would be installed without doing
anything.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return RunSetup(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), SetupOptions{
			Yes:    setupFlagYes,
			DryRun: setupFlagDryRun,
		})
	},
}

var (
	setupFlagYes    bool
	setupFlagDryRun bool
)

func init() {
	setupCmd.Flags().BoolVarP(&setupFlagYes, "yes", "y", false, "skip the per-prereq confirmation prompts")
	setupCmd.Flags().BoolVar(&setupFlagDryRun, "dry-run", false, "show what would be installed without running anything")
	rootCmd.AddCommand(setupCmd)
}

// SetupOptions controls RunSetup.
type SetupOptions struct {
	Yes    bool
	DryRun bool
	// Lookup overrides exec.LookPath; used by tests to simulate missing binaries.
	Lookup func(name string) (string, error)
	// Run overrides the install runner; used by tests to capture commands.
	Run func(name string, args []string) error
	// OS overrides runtime.GOOS; used by tests.
	OS string
}

// prereq describes one binary Moorpost expects on PATH.
type prereq struct {
	Bin     string
	Why     string
	Install map[string][]string // OS → install command (first arg = binary, rest = args)
}

// allPrereqs is the canonical list. Order is install-dependency order:
// node first (so npm exists for claude), then everything else.
var allPrereqs = []prereq{
	{
		Bin: "node",
		Why: "Required to install claude-code via npm",
		Install: map[string][]string{
			"darwin": {"brew", "install", "node"},
			"linux":  {"sudo", "apt-get", "install", "-y", "nodejs", "npm"},
		},
	},
	{
		Bin: "claude",
		Why: "Anthropic's Claude Code CLI; the agent Moorpost manages",
		Install: map[string][]string{
			"darwin": {"npm", "install", "-g", "@anthropic-ai/claude-code"},
			"linux":  {"sudo", "npm", "install", "-g", "@anthropic-ai/claude-code"},
		},
	},
	{
		Bin: "gcloud",
		Why: "Google Cloud CLI; used to provision GCP VMs",
		Install: map[string][]string{
			"darwin": {"brew", "install", "--cask", "google-cloud-sdk"},
			"linux":  {}, // apt path is multi-step; print manual instructions instead
		},
	},
	{
		Bin: "mutagen",
		Why: "Bidirectional file sync engine; used by handoff/return",
		Install: map[string][]string{
			"darwin": {"brew", "install", "mutagen-io/mutagen/mutagen"},
			"linux":  {}, // mutagen recommends a manual install on Linux
		},
	},
	{
		Bin: "tmux",
		Why: "Terminal multiplexer; runs Claude Code persistently on the VM",
		Install: map[string][]string{
			"darwin": {"brew", "install", "tmux"},
			"linux":  {"sudo", "apt-get", "install", "-y", "tmux"},
		},
	},
	{
		Bin: "ripgrep",
		Why: "Fast grep; bundled in the bootstrap and useful locally too",
		Install: map[string][]string{
			"darwin": {"brew", "install", "ripgrep"},
			"linux":  {"sudo", "apt-get", "install", "-y", "ripgrep"},
		},
	},
	{
		Bin: "rsync",
		Why: "Used for one-shot remote-to-local syncs at handoff/return boundaries",
		Install: map[string][]string{
			"darwin": {}, // pre-installed on macOS
			"linux":  {"sudo", "apt-get", "install", "-y", "rsync"},
		},
	},
}

// RunSetup orchestrates the prereq install flow. Exposed for testing.
func RunSetup(ctx context.Context, out io.Writer, in io.Reader, opts SetupOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	osName := opts.OS
	if osName == "" {
		osName = runtime.GOOS
	}
	lookup := opts.Lookup
	if lookup == nil {
		lookup = exec.LookPath
	}
	runFn := opts.Run
	if runFn == nil {
		runFn = func(name string, args []string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdout = out
			cmd.Stderr = out
			return cmd.Run()
		}
	}

	// Linux gets a friendlier story: many users prefer to install via their
	// distro package manager themselves. We surface what's missing and the
	// suggested commands but don't try to apt-install for them by default.
	if osName != "darwin" && osName != "linux" {
		return fmt.Errorf("setup: unsupported OS %q (supported: darwin, linux)", osName)
	}

	fmt.Fprintf(out, "Moorpost setup — OS: %s\n\n", osName)

	missing := 0
	installed := 0
	skipped := 0
	for _, p := range allPrereqs {
		if _, err := lookup(p.Bin); err == nil {
			installed++
			continue
		}
		missing++
		fmt.Fprintf(out, "[missing] %s — %s\n", p.Bin, p.Why)

		cmd, ok := p.Install[osName]
		if !ok || len(cmd) == 0 {
			fmt.Fprintf(out, "          (no automated install for %s on %s; install it manually and re-run)\n\n", p.Bin, osName)
			skipped++
			continue
		}

		shellLine := strings.Join(cmd, " ")
		if opts.DryRun {
			fmt.Fprintf(out, "          would run: %s\n\n", shellLine)
			continue
		}

		proceed := opts.Yes
		if !proceed {
			fmt.Fprintf(out, "          install with: %s ? [y/N] ", shellLine)
			proceed = readYesNo(in)
		}
		if !proceed {
			fmt.Fprintln(out, "          skipped")
			skipped++
			continue
		}

		fmt.Fprintf(out, "          running: %s\n", shellLine)
		if err := runFn(cmd[0], cmd[1:]); err != nil {
			return fmt.Errorf("setup: install %s: %w", p.Bin, err)
		}
		// Re-verify on PATH (some installs require a new shell — warn rather than fail).
		if _, err := lookup(p.Bin); err != nil {
			fmt.Fprintf(out, "          warning: %s still not on PATH; you may need to reopen your terminal\n", p.Bin)
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintf(out, "\nSummary: %d already installed · %d missing · %d skipped\n", installed, missing, skipped)
	if opts.DryRun {
		fmt.Fprintln(out, "\nDry run complete. Re-run without --dry-run to actually install.")
		return nil
	}
	if missing == 0 {
		fmt.Fprintln(out, "All prerequisites satisfied. Next: `moorpost auth` then `moorpost init` in your project directory.")
	} else {
		fmt.Fprintln(out, "\nNext: handle the skipped items manually, then re-run `moorpost setup` to verify.")
	}
	return nil
}

// silence import lint
var _ = errors.New
var _ = bufio.NewReader
