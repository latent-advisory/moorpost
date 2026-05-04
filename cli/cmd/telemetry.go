package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/latent-advisory/moorpost/cli/internal/state"
	"github.com/spf13/cobra"
)

var telemetryCmd = &cobra.Command{
	Use:   "telemetry [on|off|status]",
	Short: "Show or set the telemetry opt-in (default: off)",
	Long: `Moorpost telemetry is strictly opt-in. When enabled, anonymous
usage data may be sent: command name, exit code, duration, OS, CLI
version. NEVER: project names, file paths, GCP project IDs, error
messages (those can leak paths).

The opt-in flag is per-machine, stored in ~/.moorpost/state.json.
` + "`moorpost telemetry`" + ` with no argument is equivalent to ` + "`status`" + `.

Note: as of v1.0, the opt-in flag exists but no telemetry sender ships
yet. Setting opt-in true today does NOT result in any data being sent;
the gate is simply set so the future sender (if/when added) honors it.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		op := "status"
		if len(args) > 0 {
			op = args[0]
		}
		statePath, err := defaultStatePath()
		if err != nil {
			return err
		}
		return RunTelemetry(cmd.OutOrStdout(), statePath, op)
	},
}

func init() {
	rootCmd.AddCommand(telemetryCmd)
}

// defaultStatePath returns the canonical ~/.moorpost/state.json path.
// Exposed as a var so tests can stub.
var defaultStatePath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".moorpost", "state.json"), nil
}

// RunTelemetry handles `moorpost telemetry on|off|status`.
func RunTelemetry(out io.Writer, statePath, op string) error {
	switch op {
	case "status", "":
		return printTelemetryStatus(out, statePath)
	case "on":
		return setTelemetryOptIn(out, statePath, true)
	case "off":
		return setTelemetryOptIn(out, statePath, false)
	default:
		return fmt.Errorf("telemetry: unknown subcommand %q (want on|off|status)", op)
	}
}

func printTelemetryStatus(out io.Writer, statePath string) error {
	st, err := state.Open(statePath)
	if err != nil {
		return fmt.Errorf("telemetry: read state: %w", err)
	}
	if st.TelemetryOptIn {
		fmt.Fprintln(out, "Telemetry: ON (set via `moorpost telemetry on`).")
		fmt.Fprintln(out, "Note: as of v1.0 no sender is implemented; the opt-in flag is the consent gate for future telemetry.")
	} else {
		fmt.Fprintln(out, "Telemetry: OFF (default).")
		fmt.Fprintln(out, "Run `moorpost telemetry on` to opt in.")
	}
	return nil
}

func setTelemetryOptIn(out io.Writer, statePath string, optIn bool) error {
	if err := state.WithLock(statePath, func(s *state.State) error {
		s.TelemetryOptIn = optIn
		return nil
	}); err != nil {
		return fmt.Errorf("telemetry: write state: %w", err)
	}
	if optIn {
		fmt.Fprintln(out, "Telemetry opt-in: ON.")
		fmt.Fprintln(out, "Data that may be sent (when sender ships): command name, exit code, duration, OS, CLI version.")
		fmt.Fprintln(out, "Never sent: project names, file paths, GCP project IDs, error messages.")
		fmt.Fprintln(out, "Run `moorpost telemetry off` to opt out at any time.")
	} else {
		fmt.Fprintln(out, "Telemetry opt-in: OFF.")
	}
	return nil
}

// silence import check
var _ = errors.New
