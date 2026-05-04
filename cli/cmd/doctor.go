package cmd

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"

	"github.com/latent-advisory/moorpost/cli/internal/keychain"
	_ "github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics: check that all Moorpost prerequisites are present",
	Long: `Runs a series of checks to confirm Moorpost prerequisites are
satisfied: required binaries on PATH, OS keychain reachable, and (when in
a project directory) the configured Provider's preflight.

With --fix, attempts to auto-resolve known issues (currently: enabling
the Compute Engine API on the configured GCP project).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		checks := defaultDoctorChecks()
		// If a project config is reachable from cwd, also run the configured
		// Provider's preflight (e.g. GCP auth + API enablement).
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		if c, err := loadProjectContext(ContextOptions{
			Stdout: cmd.OutOrStdout(),
			Stderr: cmd.ErrOrStderr(),
		}); err == nil && c.Provider != nil {
			checks = append(checks, checkProviderPreflight(c.Provider, c.Config.Provider.Type))
			if doctorFlagFix {
				// Run the doctor checks first; then if the gcp-preflight
				// reported API-disabled, run the fix and re-run the check.
				if err := RunDoctor(ctx, cmd.OutOrStdout(), checks); err != nil {
					if applied, ferr := tryFixComputeAPI(ctx, cmd.OutOrStdout(), c); ferr != nil {
						return ferr
					} else if applied {
						fmt.Fprintln(cmd.OutOrStdout(), "Re-running doctor after fix...")
						return RunDoctor(ctx, cmd.OutOrStdout(), checks)
					}
					return err
				}
				return nil
			}
		}
		return RunDoctor(ctx, cmd.OutOrStdout(), checks)
	},
}

var doctorFlagFix bool

func init() {
	doctorCmd.Flags().BoolVar(&doctorFlagFix, "fix", false, "attempt to auto-fix known issues (e.g., enable Compute Engine API)")
	rootCmd.AddCommand(doctorCmd)
}

// computeAPIDisabledRE detects the specific preflight failure that we know
// how to auto-fix.
var computeAPIDisabledRE = regexp.MustCompile(`Compute Engine API not enabled on project "([^"]+)"`)

// fixGCPRunner is the runner the fix code uses; swappable for tests. Defaults
// to a plain os/exec runner that streams output to stdout/stderr.
var fixGCPRunner = func(ctx context.Context, w io.Writer, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout = w
	c.Stderr = w
	return c.Run()
}

// tryFixComputeAPI inspects the most recent preflight failure and, if it's
// the API-disabled case, runs `gcloud services enable compute.googleapis.com`
// against the configured project. Returns (applied, err): applied=true
// means a fix was attempted; err is non-nil only on actual failure.
func tryFixComputeAPI(ctx context.Context, out io.Writer, c *Context) (bool, error) {
	if c == nil || c.Provider == nil || c.Config == nil {
		return false, nil
	}
	// Re-run preflight to get the current error string.
	err := c.Provider.Preflight(ctx)
	if err == nil {
		return false, nil
	}
	m := computeAPIDisabledRE.FindStringSubmatch(err.Error())
	if m == nil {
		return false, nil
	}
	project := m[1]
	fmt.Fprintf(out, "→ Auto-fix: gcloud services enable compute.googleapis.com --project=%s\n", project)
	if rerr := fixGCPRunner(ctx, out, "gcloud", "services", "enable",
		"compute.googleapis.com", "--project="+project); rerr != nil {
		return true, fmt.Errorf("doctor --fix: enable Compute API: %w", rerr)
	}
	fmt.Fprintln(out, "  done.")
	return true, nil
}

// CheckResult captures the outcome of one doctor check. Severity is one of
// "ok", "warn", "fail". Hint is shown to the user for non-ok results.
type CheckResult struct {
	Name     string
	Severity string
	Detail   string
	Hint     string
}

// Check is a single doctor probe.
type Check func(ctx context.Context) CheckResult

// RunDoctor runs each check in order and prints a status line per check.
// Returns an error if any check has Severity == "fail".
func RunDoctor(ctx context.Context, out io.Writer, checks []Check) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var anyFail bool
	for _, ch := range checks {
		r := ch(ctx)
		marker := "[OK]  "
		switch r.Severity {
		case "warn":
			marker = "[WARN]"
		case "fail":
			marker = "[FAIL]"
			anyFail = true
		}
		fmt.Fprintf(out, "%s %s", marker, r.Name)
		if r.Detail != "" {
			fmt.Fprintf(out, ": %s", r.Detail)
		}
		fmt.Fprintln(out)
		if r.Hint != "" {
			fmt.Fprintf(out, "       hint: %s\n", r.Hint)
		}
	}
	if anyFail {
		return fmt.Errorf("doctor: one or more checks failed")
	}
	return nil
}

func defaultDoctorChecks() []Check {
	return []Check{
		checkBinaryAvailable("gcloud", "install via https://cloud.google.com/sdk/docs/install"),
		checkBinaryAvailable("ssh", "OpenSSH client should be present on macOS/Linux by default"),
		checkBinaryAvailable("tmux", "install via your package manager (brew/apt/dnf install tmux)"),
		checkBinaryAvailable("mutagen", "install via brew install mutagen-io/mutagen/mutagen or see mutagen.io"),
		checkBinaryAvailable("rsync", "install via your package manager (typically pre-installed)"),
		checkBinaryAvailable("claude", "install via npm install -g @anthropic-ai/claude-code"),
		checkKeychainReachable(),
	}
}

func checkBinaryAvailable(name, hint string) Check {
	return func(_ context.Context) CheckResult {
		path, err := exec.LookPath(name)
		if err != nil {
			return CheckResult{
				Name:     fmt.Sprintf("%s on PATH", name),
				Severity: "fail",
				Detail:   "not found",
				Hint:     hint,
			}
		}
		return CheckResult{
			Name:     fmt.Sprintf("%s on PATH", name),
			Severity: "ok",
			Detail:   path,
		}
	}
}

// checkProviderPreflight runs the configured Provider's Preflight method.
// Failures are FAIL severity (the user can't provision until they're fixed);
// the Hint is the multi-line preflight error.
func checkProviderPreflight(p interface {
	Preflight(ctx context.Context) error
}, providerID string) Check {
	return func(ctx context.Context) CheckResult {
		err := p.Preflight(ctx)
		if err == nil {
			return CheckResult{
				Name:     fmt.Sprintf("%s preflight", providerID),
				Severity: "ok",
				Detail:   "auth + APIs ready",
			}
		}
		return CheckResult{
			Name:     fmt.Sprintf("%s preflight", providerID),
			Severity: "fail",
			Detail:   "not ready",
			Hint:     err.Error(),
		}
	}
}

func checkKeychainReachable() Check {
	return func(_ context.Context) CheckResult {
		kc, err := keychain.New()
		if err != nil {
			return CheckResult{
				Name:     "OS keychain",
				Severity: "warn",
				Detail:   err.Error(),
				Hint:     "Moorpost will fall back to file-based credential storage if --unsafe-token-storage is set",
			}
		}
		return CheckResult{
			Name:     "OS keychain",
			Severity: "ok",
			Detail:   kc.Backend(),
		}
	}
}
