package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/latent-advisory/moorpost/cli/internal/agent/claudecode"
	"github.com/latent-advisory/moorpost/cli/internal/claudewrapper"
	"github.com/latent-advisory/moorpost/cli/internal/keychain"
	"github.com/spf13/cobra"
)

// One-shot orchestration for first-run setup. Each step shells out to the
// same binary via os.Executable() so we don't duplicate the per-command
// argument parsing, prompting, and exit-code semantics of setup/auth/init/
// provision.
//
// Skips work that's already done where it's safe to detect: prereq install
// (setup is itself idempotent and detects what's missing), auth (we check
// the keychain for a cached token), and init (refuses to overwrite without
// --force, which we don't pass).

var (
	bootstrapFlagYes        bool
	bootstrapFlagProvision  bool
	bootstrapFlagGCPProject string
	bootstrapFlagGCPConfig  string
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "One-shot setup: prereqs → sign in → init → (optional) provision",
	Long: `Runs setup, auth, init, and (with --provision) provision in sequence,
skipping steps that are already done.

Without --provision, leaves the project ready but unprovisioned — you can
run ` + "`moorpost provision`" + ` later when you're ready to spend money on a VM.

Examples:
  moorpost bootstrap                       # interactive; provision is opt-in
  moorpost bootstrap --yes                 # no confirmation prompt
  moorpost bootstrap --yes --provision     # full unattended (still needs OAuth)
  moorpost bootstrap --gcp-project=my-gcp  # override gcloud auto-detect`,
	RunE: runBootstrap,
}

func init() {
	bootstrapCmd.Flags().BoolVar(&bootstrapFlagYes, "yes", false, "skip the confirmation prompt and run setup non-interactively")
	bootstrapCmd.Flags().BoolVar(&bootstrapFlagProvision, "provision", false, "also run `moorpost provision` at the end (creates a GCP VM)")
	bootstrapCmd.Flags().StringVar(&bootstrapFlagGCPProject, "gcp-project", "", "GCP project ID (default: auto-detect from gcloud)")
	bootstrapCmd.Flags().StringVar(&bootstrapFlagGCPConfig, "gcp-config", "", "gcloud configuration name to pin moorpost to (default: prompt during init)")
	rootCmd.AddCommand(bootstrapCmd)
}

func runBootstrap(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()

	plan := []string{
		"1. Install prerequisites  (moorpost setup)",
		"2. Sign in to Claude      (moorpost auth — skipped if already cached)",
		"3. Initialize project     (moorpost init in current directory)",
	}
	if bootstrapFlagProvision {
		plan = append(plan, "4. Provision the VM       (moorpost provision)")
	} else {
		plan = append(plan, "4. (skipped) Provision    — re-run with --provision to include")
	}
	fmt.Fprintln(out, "Moorpost bootstrap plan:")
	for _, line := range plan {
		fmt.Fprintln(out, "  "+line)
	}
	fmt.Fprintln(out)

	if !bootstrapFlagYes {
		fmt.Fprint(out, "Proceed? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "y" && ans != "yes" {
			return errors.New("bootstrap: aborted by user")
		}
	}

	// Step 1: setup. Always run (it's idempotent and does its own per-tool detection).
	if err := stepShell(out, "setup", buildSetupArgs()...); err != nil {
		return fmt.Errorf("bootstrap: setup failed: %w", err)
	}

	// Step 2: auth. Skip if a token is already cached in the keychain.
	if cached := authCached(); cached {
		fmt.Fprintln(out, "\n→ auth: already authenticated (token in OS keychain) — skipping")
	} else {
		// Note: stepShell takes (label, args...). The first arg after the
		// label is the CLI subcommand — easy to forget. Earlier this was
		// `stepShell(out, "auth")` which silently ran `moorpost` (no args
		// → help screen → exit 0), making auth look successful while the
		// keychain stayed empty.
		if err := stepShell(out, "auth", "auth"); err != nil {
			return fmt.Errorf("bootstrap: auth failed: %w", err)
		}
	}

	// Step 3: init. init refuses to overwrite without --force; in that case
	// surface the existing config and continue rather than erroring out.
	initArgs := []string{"init"}
	if bootstrapFlagGCPProject != "" {
		initArgs = append(initArgs, "--gcp-project="+bootstrapFlagGCPProject)
	}
	if bootstrapFlagGCPConfig != "" {
		initArgs = append(initArgs, "--gcp-config="+bootstrapFlagGCPConfig)
	}
	if err := stepShell(out, "init", initArgs...); err != nil {
		// init's exit code on existing-config-without-force is non-zero. If
		// .moorpost/config.yaml already exists, treat that as a soft success.
		if _, statErr := os.Stat(".moorpost/config.yaml"); statErr == nil {
			fmt.Fprintln(out, "\n→ init: project already initialized (.moorpost/config.yaml exists) — continuing")
		} else {
			return fmt.Errorf("bootstrap: init failed: %w", err)
		}
	}

	// Step 4: provision (opt-in). --wait so the bootstrap orchestrator only
	// reports success once the VM's startup script has finished and claude
	// is actually on PATH; otherwise the user might get a false "ready"
	// signal while apt is still grinding remotely.
	if bootstrapFlagProvision {
		if err := stepShell(out, "provision", "provision", "--wait"); err != nil {
			return fmt.Errorf("bootstrap: provision failed: %w", err)
		}
	}

	// Step 5: install the Anthropic Claude Code plugin wrapper. Cheap, idempotent;
	// makes the panel-UI integration work as soon as the user sets the plugin's
	// `claudeCode.claudeProcessWrapper` setting (the moorpost extension does this
	// automatically on handoff/return).
	if path, err := claudewrapper.Install(); err != nil {
		fmt.Fprintf(out, "\n→ claude-wrapper: install failed (non-fatal): %v\n", err)
	} else {
		fmt.Fprintf(out, "\n→ claude-wrapper: installed at %s\n", path)
	}

	fmt.Fprintln(out, "\n✓ Bootstrap complete.")
	if !bootstrapFlagProvision {
		fmt.Fprintln(out, "  Next: `moorpost provision` to create the VM, then `moorpost handoff` when you step away.")
	} else {
		fmt.Fprintln(out, "  Next: `moorpost handoff` when you step away.")
	}
	return nil
}

func buildSetupArgs() []string {
	args := []string{"setup"}
	if bootstrapFlagYes {
		args = append(args, "--yes")
	}
	return args
}

// authCached reports whether the default agent's OAuth token is already in
// the keychain. Errors are treated as "not cached" — auth will surface the
// real failure if the keychain itself is broken.
func authCached() bool {
	kc, err := keychain.New()
	if err != nil {
		return false
	}
	_, err = kc.Retrieve(claudecode.KeychainService, claudecode.KeychainAccount)
	return err == nil
}

// stepShell re-execs the current binary with the given args, streaming its
// stdio so prompts and progress flow through to the user. Used so each
// bootstrap step keeps the same UX as running it standalone.
func stepShell(out interface{ Write([]byte) (int, error) }, label string, args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate moorpost binary: %w", err)
	}
	fmt.Fprintf(out, "\n→ %s\n", label)
	c := exec.Command(self, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
