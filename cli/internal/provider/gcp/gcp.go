// Package gcp implements the Provider interface using the gcloud CLI.
//
// We shell out to gcloud rather than using cloud.google.com/go/compute
// because it reuses the user's existing gcloud ADC, keeps the dep surface
// small, and is what most prosumer scripts already do.
package gcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
)

// ProviderID is the registry identifier.
const ProviderID = "gcp"

// Default Ubuntu image used when ProvisionSpec.Image is empty.
const (
	defaultImageFamily  = "ubuntu-2404-lts-amd64"
	defaultImageProject = "ubuntu-os-cloud"
	defaultMachineType  = "e2-standard-2"
	defaultDiskGB       = 100
	defaultDiskType     = "pd-standard"
	defaultUser         = "" // empty = let gcloud derive from os-login or current user
)

// Options controls runtime behavior.
type Options struct {
	// Executor wraps os/exec; defaults to mpexec.New() if nil.
	Executor mpexec.Executor

	// Binary overrides the gcloud executable. Default "gcloud".
	Binary string

	// Project is the GCP project ID (taken from config if empty here).
	Project string

	// Region defaults to "us-central1" if empty.
	Region string

	// Zone defaults to "<region>-a" if empty.
	Zone string

	// SSHUser is the OS login user on provisioned VMs (used by SSHTarget).
	// Defaults to the current user if empty.
	SSHUser string
}

// engine is the concrete Provider.
type engine struct {
	exec    mpexec.Executor
	binary  string
	project string
	region  string
	zone    string
	sshUser string
}

// New constructs a GCP provider from a config map. Recognized keys:
//
//	project: string (required)
//	region:  string (default "us-central1")
//	zone:    string (default "<region>-a")
//	ssh_user: string
func New(config map[string]any) (provider.Provider, error) {
	opts := Options{}
	if v, ok := config["project"].(string); ok {
		opts.Project = v
	}
	if v, ok := config["region"].(string); ok {
		opts.Region = v
	}
	if v, ok := config["zone"].(string); ok {
		opts.Zone = v
	}
	if v, ok := config["ssh_user"].(string); ok {
		opts.SSHUser = v
	}
	return NewWithOptions(opts)
}

// NewWithOptions allows tests to inject fakes.
func NewWithOptions(opts Options) (provider.Provider, error) {
	if opts.Project == "" {
		return nil, errors.New("gcp: project is required (set provider.gcp.project in config)")
	}
	e := &engine{
		exec:    opts.Executor,
		binary:  opts.Binary,
		project: opts.Project,
		region:  opts.Region,
		zone:    opts.Zone,
		sshUser: opts.SSHUser,
	}
	if e.exec == nil {
		e.exec = mpexec.New()
	}
	if e.binary == "" {
		e.binary = "gcloud"
	}
	if e.region == "" {
		e.region = "us-central1"
	}
	if e.zone == "" {
		e.zone = e.region + "-a"
	}
	return e, nil
}

func init() {
	provider.Register(ProviderID, New)
}

func (e *engine) ID() string { return ProviderID }

// Preflight validates auth + API enablement before any provisioning call.
// Returns nil if ready; otherwise a multi-line error listing each problem
// with the remediation command.
func (e *engine) Preflight(ctx context.Context) error {
	var problems []string

	// 1. Active gcloud auth.
	stdout, _, code, err := e.exec.Run(ctx, e.binary,
		[]string{"auth", "list", "--filter=status:ACTIVE", "--format=value(account)"},
		nil)
	if err != nil || code != 0 || strings.TrimSpace(string(stdout)) == "" {
		problems = append(problems,
			"  - no active gcloud account; run: gcloud auth login")
	}

	// 2. Compute Engine API enabled on the configured project.
	stdout, _, code, err = e.exec.Run(ctx, e.binary,
		[]string{"services", "list", "--enabled",
			"--project=" + e.project,
			"--filter=name:compute.googleapis.com",
			"--format=value(name)"},
		nil)
	if err != nil {
		problems = append(problems,
			fmt.Sprintf("  - cannot check API enablement on project %q: %v", e.project, err))
	} else if code != 0 || !strings.Contains(string(stdout), "compute.googleapis.com") {
		problems = append(problems, fmt.Sprintf(
			"  - Compute Engine API not enabled on project %q\n    fix: gcloud services enable compute.googleapis.com --project=%s",
			e.project, e.project))
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("gcp preflight failed:\n%s", strings.Join(problems, "\n"))
}

// gcloudArgs prepends the project + format flags every command needs.
func (e *engine) gcloudArgs(args ...string) []string {
	out := []string{"--project", e.project, "--quiet"}
	out = append(out, args...)
	return out
}

func (e *engine) Provision(ctx context.Context, spec provider.ProvisionSpec) (provider.VM, error) {
	if spec.Name == "" {
		return provider.VM{}, errors.New("gcp.Provision: spec.Name is required")
	}
	if spec.SSHKeyPub == "" {
		return provider.VM{}, errors.New("gcp.Provision: spec.SSHKeyPub is required")
	}
	zone := spec.Zone
	if zone == "" {
		zone = e.zone
	}
	machine := spec.MachineType
	if machine == "" {
		machine = defaultMachineType
	}
	diskGB := spec.DiskGB
	if diskGB == 0 {
		diskGB = defaultDiskGB
	}
	diskType := spec.DiskType
	if diskType == "" {
		diskType = defaultDiskType
	}

	args := []string{
		"compute", "instances", "create", spec.Name,
		"--zone", zone,
		"--machine-type", machine,
		"--boot-disk-size", fmt.Sprintf("%dGB", diskGB),
		"--boot-disk-type", diskType,
	}
	if spec.Image != "" {
		// Allow either family or full image name. If no slash, treat as family.
		if strings.Contains(spec.Image, "/") {
			args = append(args, "--image", spec.Image)
		} else {
			args = append(args, "--image-family", spec.Image, "--image-project", defaultImageProject)
		}
	} else {
		args = append(args, "--image-family", defaultImageFamily, "--image-project", defaultImageProject)
	}

	if len(spec.Tags) > 0 {
		args = append(args, "--tags", strings.Join(spec.Tags, ","))
	}

	// Inject the SSH key via metadata. The user's gcloud account becomes the
	// OS login if not explicitly set.
	user := e.sshUser
	if user == "" {
		user = "moorpost"
	}
	sshKeysMeta := user + ":" + strings.TrimRight(spec.SSHKeyPub, "\n")
	args = append(args, "--metadata", "ssh-keys="+sshKeysMeta)

	// Bootstrap script via startup-script metadata. gcloud's
	// --metadata-from-file requires a real file path (not "-"); write to a
	// temp file and clean up after.
	var tmpScriptPath string
	if spec.BootstrapScript != "" {
		f, err := os.CreateTemp("", "moorpost-bootstrap-*.sh")
		if err != nil {
			return provider.VM{}, fmt.Errorf("gcp.Provision: write bootstrap temp: %w", err)
		}
		tmpScriptPath = f.Name()
		if _, err := f.WriteString(spec.BootstrapScript); err != nil {
			f.Close()
			os.Remove(tmpScriptPath)
			return provider.VM{}, fmt.Errorf("gcp.Provision: write bootstrap content: %w", err)
		}
		if err := f.Close(); err != nil {
			os.Remove(tmpScriptPath)
			return provider.VM{}, fmt.Errorf("gcp.Provision: close bootstrap temp: %w", err)
		}
		defer os.Remove(tmpScriptPath)
		args = append(args, "--metadata-from-file", "startup-script="+tmpScriptPath)
	}

	out, stderr, code, err := e.exec.Run(ctx, e.binary, e.gcloudArgs(args...), nil)
	if err != nil {
		return provider.VM{}, fmt.Errorf("gcloud compute instances create: %w", err)
	}
	if code != 0 {
		return provider.VM{}, fmt.Errorf("gcloud compute instances create exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}

	vm := provider.VM{
		ID:        spec.Name,
		Name:      spec.Name,
		Provider:  ProviderID,
		Zone:      zone,
		Region:    deriveRegion(zone),
		CreatedAt: time.Now().UTC(),
	}

	// Best-effort IP extraction from the create stdout.
	if ip := extractIPFromCreateOutput(string(out)); ip != "" {
		vm.ExternalIP = ip
	}

	// If the spec wants the VM stopped after creation, issue stop now.
	if !spec.StartImmediately {
		if err := e.Stop(ctx, vm.ID); err != nil {
			return vm, fmt.Errorf("gcp.Provision: post-create stop: %w", err)
		}
	}

	// Apply SSH firewall restriction by source IP if requested. We use a
	// network-tagged firewall rule (only created if not already present).
	// Skipped when SourceIPRanges is empty (default = open).
	// (For v0.1 we rely on GCP's default-allow-ssh; firewall provisioning
	// is deferred to a later iteration to avoid race-conditions on first run.)
	_ = spec.SourceIPRanges
	_ = spec.StaticIP

	return vm, nil
}

func (e *engine) Start(ctx context.Context, vmID string) error {
	if vmID == "" {
		return errors.New("gcp.Start: vmID required")
	}
	args := e.gcloudArgs("compute", "instances", "start", vmID, "--zone", e.zone)
	_, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return fmt.Errorf("gcloud start: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		// "is already running" → idempotent
		if strings.Contains(stderrStr, "is already") || strings.Contains(stderrStr, "already running") {
			return nil
		}
		if strings.Contains(stderrStr, "was not found") {
			return provider.ErrNotFound
		}
		return fmt.Errorf("gcloud start exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	return nil
}

func (e *engine) Stop(ctx context.Context, vmID string) error {
	if vmID == "" {
		return errors.New("gcp.Stop: vmID required")
	}
	args := e.gcloudArgs("compute", "instances", "stop", vmID, "--zone", e.zone)
	_, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return fmt.Errorf("gcloud stop: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "is already") || strings.Contains(stderrStr, "TERMINATED") {
			return nil
		}
		if strings.Contains(stderrStr, "was not found") {
			return provider.ErrNotFound
		}
		return fmt.Errorf("gcloud stop exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	return nil
}

func (e *engine) Destroy(ctx context.Context, vmID string) error {
	if vmID == "" {
		return errors.New("gcp.Destroy: vmID required")
	}
	args := e.gcloudArgs("compute", "instances", "delete", vmID, "--zone", e.zone)
	_, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return fmt.Errorf("gcloud delete: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "was not found") {
			return nil // idempotent
		}
		return fmt.Errorf("gcloud delete exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	return nil
}

func (e *engine) Status(ctx context.Context, vmID string) (provider.VMState, error) {
	if vmID == "" {
		return provider.VMStateUnknown, errors.New("gcp.Status: vmID required")
	}
	args := e.gcloudArgs("compute", "instances", "describe", vmID,
		"--zone", e.zone,
		"--format", "value(status)")
	stdout, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return provider.VMStateUnknown, fmt.Errorf("gcloud describe: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "was not found") {
			return provider.VMStateUnknown, provider.ErrNotFound
		}
		return provider.VMStateUnknown, fmt.Errorf("gcloud describe exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	return parseGCPState(strings.TrimSpace(string(stdout))), nil
}

func parseGCPState(s string) provider.VMState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "PROVISIONING", "STAGING":
		return provider.VMStateProvisioning
	case "RUNNING":
		return provider.VMStateRunning
	case "STOPPING", "SUSPENDING":
		return provider.VMStateStopping
	case "TERMINATED", "STOPPED", "SUSPENDED":
		return provider.VMStateStopped
	case "REPAIRING":
		return provider.VMStateError
	case "":
		return provider.VMStateUnknown
	default:
		return provider.VMStateUnknown
	}
}

func (e *engine) SSHTarget(ctx context.Context, vmID string) (provider.SSHTarget, error) {
	if vmID == "" {
		return provider.SSHTarget{}, errors.New("gcp.SSHTarget: vmID required")
	}
	args := e.gcloudArgs("compute", "instances", "describe", vmID,
		"--zone", e.zone,
		"--format", "value(networkInterfaces[0].accessConfigs[0].natIP)")
	stdout, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return provider.SSHTarget{}, fmt.Errorf("gcloud describe: %w", err)
	}
	if code != 0 {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "was not found") {
			return provider.SSHTarget{}, provider.ErrNotFound
		}
		return provider.SSHTarget{}, fmt.Errorf("gcloud describe exit %d: %s", code, strings.TrimSpace(stderrStr))
	}
	ip := strings.TrimSpace(string(stdout))
	if ip == "" {
		return provider.SSHTarget{}, fmt.Errorf("gcp.SSHTarget: VM %s has no external IP", vmID)
	}
	user := e.sshUser
	if user == "" {
		user = "moorpost"
	}
	return provider.SSHTarget{Host: ip, Port: 22, User: user}, nil
}

// snapshotLabelRE filters chars not allowed in GCE snapshot names. Names must
// match `[a-z]([-a-z0-9]*[a-z0-9])?` and be 1–63 chars.
var snapshotLabelRE = regexp.MustCompile(`[^a-z0-9-]`)

func sanitizeSnapshotLabel(label string) string {
	s := strings.ToLower(label)
	s = snapshotLabelRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func (e *engine) Snapshot(ctx context.Context, vmID string, label string) (provider.SnapshotID, error) {
	if vmID == "" {
		return "", errors.New("gcp.Snapshot: vmID required")
	}
	clean := sanitizeSnapshotLabel(label)
	if clean == "" {
		clean = "moorpost"
	}
	// GCE snapshot naming: <vm>-<label>-<timestamp>
	stamp := time.Now().UTC().Format("20060102-150405")
	snapName := vmID + "-" + clean + "-" + stamp
	if len(snapName) > 63 {
		// Truncate from the front-end label, not the timestamp.
		excess := len(snapName) - 63
		// Trim the clean label from the right
		if len(clean) > excess {
			clean = clean[:len(clean)-excess]
			snapName = vmID + "-" + clean + "-" + stamp
		}
	}
	args := e.gcloudArgs("compute", "disks", "snapshot", vmID,
		"--zone", e.zone,
		"--snapshot-names", snapName)
	_, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return "", fmt.Errorf("gcloud disks snapshot: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("gcloud disks snapshot exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	return provider.SnapshotID(snapName), nil
}

// listPriceTable is a small lookup of approximate hourly USD rates for common
// machine types in us-central1 as of 2026-Q2. Used only as a fallback estimate
// when the Cloud Billing API is unavailable. The real cost surface lands in
// v0.3 (PLUGIN.md §9).
var listPriceTable = map[string]float64{
	"e2-micro":      0.0084,
	"e2-small":      0.0168,
	"e2-medium":     0.0335,
	"e2-standard-2": 0.0670,
	"e2-standard-4": 0.1340,
	"e2-standard-8": 0.2680,
	"n2-standard-2": 0.0971,
	"n2-standard-4": 0.1942,
	"t2d-standard-2": 0.0827,
	"t2d-standard-4": 0.1654,
}

func (e *engine) Cost(ctx context.Context, vmID string, period provider.TimeRange) (provider.CostBreakdown, error) {
	// v0.1 uses a list-price estimate only; real billing API is v0.3.
	// Caller passes in the time range; we look up the VM's machine type and
	// multiply by hours-in-period. Disk/egress not yet modeled — caller can
	// inspect IsEstimate=true to decide whether to display.
	machineType, err := e.lookupMachineType(ctx, vmID)
	if err != nil {
		return provider.CostBreakdown{}, fmt.Errorf("gcp.Cost: lookup machine type: %w", err)
	}
	rate, ok := listPriceTable[machineType]
	if !ok {
		return provider.CostBreakdown{IsEstimate: true, Period: period},
			fmt.Errorf("%w: machine type %q has no list-price entry", provider.ErrCostUnavailable, machineType)
	}
	hours := period.End.Sub(period.Start).Hours()
	if hours < 0 {
		hours = 0
	}
	return provider.CostBreakdown{
		Compute:    rate * hours,
		Total:      rate * hours,
		Period:     period,
		IsEstimate: true,
	}, nil
}

// lookupMachineType describes the VM and returns its machine type name (last
// path segment of the GCE URI).
func (e *engine) lookupMachineType(ctx context.Context, vmID string) (string, error) {
	args := e.gcloudArgs("compute", "instances", "describe", vmID,
		"--zone", e.zone,
		"--format", "value(machineType)")
	stdout, stderr, code, err := e.exec.Run(ctx, e.binary, args, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("describe exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	full := strings.TrimSpace(string(stdout))
	// gcloud returns either the bare name or a full URL ending in `/machineTypes/<name>`.
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[i+1:], nil
	}
	return full, nil
}

// deriveRegion strips the trailing `-<letter>` from a zone (e.g. "us-central1-a" → "us-central1").
func deriveRegion(zone string) string {
	if zone == "" {
		return ""
	}
	if i := strings.LastIndex(zone, "-"); i >= 0 {
		// Last component is likely a single letter zone suffix.
		suffix := zone[i+1:]
		if len(suffix) <= 2 {
			return zone[:i]
		}
	}
	return zone
}

// extractIPFromCreateOutput pulls the EXTERNAL_IP column from gcloud's
// instances-create stdout (which prints a small table on success). Best
// effort; returns empty if not found.
var ipRE = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)

func extractIPFromCreateOutput(out string) string {
	// gcloud's create output prints a table like:
	//   NAME       ZONE           MACHINE_TYPE   ... INTERNAL_IP  EXTERNAL_IP   STATUS
	//   argus-vm   us-central1-a  e2-standard-2  ... 10.128.0.2   35.x.y.z      RUNNING
	// We just extract the LAST IP-shaped token, which is the external on a
	// public-IP create. This is a best-effort hint; SSHTarget() is the
	// authoritative source.
	matches := ipRE.FindAllString(out, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// silence unused-import check (referenced via init's panic on unknown ID)
var _ = strconv.Itoa
