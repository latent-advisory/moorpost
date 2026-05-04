package cmd

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/latent-advisory/moorpost/cli/internal/keychain"
	_ "github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics: check that all Moorpost prerequisites are present",
	RunE: func(cmd *cobra.Command, args []string) error {
		checks := defaultDoctorChecks()
		// If a project config is reachable from cwd, also run the configured
		// Provider's preflight (e.g. GCP auth + API enablement).
		if c, err := loadProjectContext(ContextOptions{
			Stdout: cmd.OutOrStdout(),
			Stderr: cmd.ErrOrStderr(),
		}); err == nil && c.Provider != nil {
			checks = append(checks, checkProviderPreflight(c.Provider, c.Config.Provider.Type))
		}
		return RunDoctor(cmd.Context(), cmd.OutOrStdout(), checks)
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
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
