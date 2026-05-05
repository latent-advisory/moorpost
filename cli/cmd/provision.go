package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/bootstrap"
	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	"github.com/spf13/cobra"
)

var provisionCmd = &cobra.Command{
	Use:   "provision",
	Short: "Create the project's remote VM (left stopped by default)",
	Long: `Provisions a fresh VM via the configured cloud Provider, runs
the bootstrap script (if any), and leaves it stopped (local-first mode) or
running (if --start is given). Idempotent: re-running on an existing
project simply re-confirms the recorded VM id.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		// --wait implies --start: we can only SSH into a running VM. Surface
		// the implication to the user instead of silently flipping a flag.
		start := provisionFlagStart
		if provisionFlagWait && !start {
			start = true
			fmt.Fprintln(cmd.OutOrStdout(), "Note: --wait implies --start (the VM must be running to be SSHed for readiness check).")
		}
		opts := ProvisionOptions{
			SSHKeyPath:  provisionFlagSSHKey,
			Start:       start,
			Tags:        provisionFlagTags,
			OverrideCap: provisionFlagOverrideCap,
			Wait:        provisionFlagWait,
		}
		return RunProvision(cmd.Context(), cmd.OutOrStdout(), c, opts)
	},
}

var (
	provisionFlagSSHKey      string
	provisionFlagStart       bool
	provisionFlagTags        []string
	provisionFlagOverrideCap bool
	provisionFlagWait        bool
)

func init() {
	provisionCmd.Flags().StringVar(&provisionFlagSSHKey, "ssh-key", "", "path to SSH public key (default: ~/.ssh/google_compute_engine.pub)")
	provisionCmd.Flags().BoolVar(&provisionFlagStart, "start", false, "start the VM immediately after creation (skip the local-first stopped default)")
	provisionCmd.Flags().StringSliceVar(&provisionFlagTags, "tag", nil, "extra GCP network tag (repeatable)")
	provisionCmd.Flags().BoolVar(&provisionFlagOverrideCap, "override-cap", false, "bypass cost.monthly_cap_usd")
	provisionCmd.Flags().BoolVar(&provisionFlagWait, "wait", false, "after creation, poll the VM via SSH until claude is on PATH (implies --start)")
	rootCmd.AddCommand(provisionCmd)
}

// ProvisionOptions are the runtime knobs for RunProvision.
type ProvisionOptions struct {
	SSHKeyPath  string   // path to .pub file
	Start       bool     // start immediately after create
	Tags        []string // extra tags
	OverrideCap bool     // bypass cost.monthly_cap_usd check
	Wait        bool     // SSH-poll for bootstrap completion before returning
}

// RunProvision is the testable provision entrypoint.
func RunProvision(ctx context.Context, out io.Writer, c *Context, opts ProvisionOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Config == nil || c.Provider == nil {
		return errors.New("provision: incomplete context (missing config or provider)")
	}
	if c.Config.ProjectSlug == "" {
		return errors.New("provision: project slug missing — run `moorpost init`")
	}

	keyPath := opts.SSHKeyPath
	if keyPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("provision: resolve home: %w", err)
		}
		keyPath = filepath.Join(home, ".ssh", "google_compute_engine.pub")
	}
	pub, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("provision: read ssh key %s: %w", keyPath, err)
	}

	// Pull provider-specific knobs from the config map.
	gcpCfg := pickSubsection(c.Config.Provider.Raw, c.Config.Provider.Type)
	machineType, _ := gcpCfg["machine_type"].(string)
	diskGB := pickInt(gcpCfg, "disk_size_gb")
	diskType, _ := gcpCfg["disk_type"].(string)
	zone, _ := gcpCfg["zone"].(string)
	tags := append([]string{"moorpost"}, opts.Tags...)

	// Render the bootstrap script. The remote user is whatever the provider
	// established as the OS-login user; for v0.1 GCP we default to "moorpost"
	// (matching gcp.engine.SSHUser default) but read from gcp config if set.
	remoteUser, _ := gcpCfg["ssh_user"].(string)
	if remoteUser == "" {
		remoteUser = "moorpost"
	}
	idleAutoStopMin := 0
	if c.Config.Mode == config.ModePersistent {
		idleAutoStopMin = c.Config.Persistent.AutoStopMinutes
	}
	bootScript, err := bootstrap.Render(bootstrap.BootstrapVars{
		ProjectSlug:         c.Config.ProjectSlug,
		LocalAbsPath:        c.ProjectDir,
		RemoteUser:          remoteUser,
		IdleAutoStopMinutes: idleAutoStopMin,
	})
	if err != nil {
		return fmt.Errorf("provision: render bootstrap: %w", err)
	}

	spec := provider.ProvisionSpec{
		Name:             vmName(c),
		Zone:             zone,
		MachineType:      machineType,
		DiskGB:           diskGB,
		DiskType:         diskType,
		SSHKeyPub:        string(pub),
		Tags:             tags,
		StartImmediately: opts.Start,
		BootstrapScript:  bootScript,
	}

	// Cost cap: refuse if MTD spend already over user-set cap.
	if err := enforceCostCap(ctx, c, opts.OverrideCap); err != nil {
		return fmt.Errorf("provision: %w", err)
	}

	// Preflight: catch missing API/auth before the create call so the user
	// sees an actionable hint instead of a raw gcloud error mid-create.
	if err := c.Provider.Preflight(ctx); err != nil {
		return fmt.Errorf("provision: %w", err)
	}

	fmt.Fprintf(out, "Provisioning %s in %s...\n", spec.Name, spec.Zone)
	vm, err := c.Provider.Provision(ctx, spec)
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}

	// Persist the project + VM record under lock.
	err = withProjectState(c, func(ps *state.ProjectState) error {
		ps.VMID = vm.ID
		ps.VMZone = vm.Zone
		if ps.ActiveSide == "" {
			ps.ActiveSide = state.SideLocal
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := withVM(c, vm.ID, func(rec *state.VMRecord) error {
		rec.Provider = vm.Provider
		rec.ExternalIP = vm.ExternalIP
		if opts.Start {
			rec.StateCache = "running"
		} else {
			rec.StateCache = "stopped"
		}
		return nil
	}); err != nil {
		return err
	}

	// A fresh VM has a fresh host key. If we ever provisioned at this IP
	// before, our private known_hosts file holds a stale entry that would
	// fail later SSH calls with "Host key verification failed". Clear it.
	clearStaleKnownHostsEntry(out, vm.ExternalIP)

	fmt.Fprintf(out, "Done. VM %s (%s).\n", vm.ID, statusLabel(opts.Start))
	if !opts.Start {
		fmt.Fprintln(out, "VM is stopped. Run `moorpost handoff` when stepping away, or `moorpost up` for always-on.")
	}

	if opts.Wait {
		gcpProject, _ := gcpCfg["project"].(string)
		if err := waitForBootstrapReady(ctx, out, gcpProject, vm); err != nil {
			return err
		}
	}
	return nil
}

// waitForBootstrapReady polls the VM via `gcloud compute ssh` until `claude`
// is on PATH (the canonical "bootstrap finished" signal), or the deadline
// elapses. GCP-specific for v1.0 — the gcloud handle subsumes OS Login,
// IAP, key sync, etc., which a raw ssh.Runner would have to reimplement.
// When v1.x adds Hetzner/etc. providers, this should move behind a
// Provider.WaitForBootstrap method.
func waitForBootstrapReady(ctx context.Context, out io.Writer, project string, vm provider.VM) error {
	if project == "" {
		fmt.Fprintln(out, "⚠ --wait: GCP project not in config; skipping readiness probe.")
		return nil
	}
	fmt.Fprintln(out, "Waiting for bootstrap to finish on the VM (Node + Claude install)...")
	deadline := time.Now().Add(10 * time.Minute)
	probe := "command -v claude >/dev/null && echo READY || echo PENDING"
	for time.Now().Before(deadline) {
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		c := exec.CommandContext(attemptCtx, // #nosec G204 — args are static + provider data
			"gcloud", "compute", "ssh", vm.Name,
			"--project="+project, "--zone="+vm.Zone, "--quiet",
			"--command="+probe)
		var buf bytes.Buffer
		c.Stdout = &buf
		c.Stderr = io.Discard
		_ = c.Run()
		cancel()
		if strings.Contains(buf.String(), "READY") {
			fmt.Fprintln(out, "✓ Bootstrap complete — VM is ready for handoff.")
			return nil
		}
		fmt.Fprintln(out, "  ...still bootstrapping (apt + Node tarball + claude npm install)...")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
	fmt.Fprintln(out, "⚠ Bootstrap did not finish within 10 min. The VM is created; check progress with `gcloud compute ssh "+vm.Name+" --command='sudo tail /var/log/moorpost-bootstrap.log'`.")
	return nil
}

func statusLabel(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}

// clearStaleKnownHostsEntry removes any prior host-key entry for ip from
// moorpost's private known_hosts file (~/.moorpost/known_hosts). Best
// effort: silently no-ops on missing file, missing ssh-keygen, or any
// other error — provisioning should never fail because of a known_hosts
// cleanup quirk.
func clearStaleKnownHostsEntry(out io.Writer, ip string) {
	if ip == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	khPath := filepath.Join(home, ".moorpost", "known_hosts")
	if _, err := os.Stat(khPath); errors.Is(err, os.ErrNotExist) {
		return // never used, nothing to clear
	}
	cmd := exec.Command("ssh-keygen", "-R", ip, "-f", khPath) // #nosec G204
	if err := cmd.Run(); err != nil {
		// Don't surface as an error — the worst case is a stale entry
		// that the next SSH attempt will fail on, at which point the
		// user can manually `ssh-keygen -R <ip> -f ~/.moorpost/known_hosts`.
		fmt.Fprintf(out, "  (note: could not refresh ~/.moorpost/known_hosts for %s: %v)\n", ip, err)
	}
}

// pickInt extracts an int-ish value from a config map. yaml.v3 unmarshals
// numbers into int when no type hint is given.
func pickInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
