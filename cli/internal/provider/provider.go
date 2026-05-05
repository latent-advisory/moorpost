// Package provider defines the cloud-provider abstraction used by Moorpost.
//
// Each provider (gcp, hetzner, aws, ...) implements the Provider interface
// in its own subpackage and registers a constructor with Register at init time.
// CLI commands look up providers by ID via Get and never depend on concrete impls.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Provider abstracts a cloud-VM lifecycle. Implementations must be safe for
// concurrent use by multiple goroutines.
type Provider interface {
	// ID returns a stable identifier (e.g. "gcp", "hetzner") matching the
	// constructor name used in Register.
	ID() string

	// Provision creates a new VM per spec and returns its identifying info.
	// The VM should be left in the state requested by spec.StartImmediately.
	Provision(ctx context.Context, spec ProvisionSpec) (VM, error)

	// Start transitions a stopped VM to running. Idempotent if already running.
	Start(ctx context.Context, vmID string) error

	// Stop transitions a running VM to stopped. Idempotent if already stopped.
	// Disk and configuration are preserved.
	Stop(ctx context.Context, vmID string) error

	// Destroy permanently deletes the VM and its boot disk. Irreversible.
	Destroy(ctx context.Context, vmID string) error

	// Status returns the current state of the VM, freshly fetched from the
	// provider's API (no caching at this layer).
	Status(ctx context.Context, vmID string) (VMState, error)

	// Snapshot creates a point-in-time snapshot of the VM's boot disk. The
	// label is provider-namespaced and used in the snapshot name when supported.
	Snapshot(ctx context.Context, vmID string, label string) (SnapshotID, error)

	// Cost returns a breakdown of charges incurred by the VM during the period.
	// Implementations may return ErrCostUnavailable if the provider's billing
	// API is not enabled or the time range is too recent for billing data.
	Cost(ctx context.Context, vmID string, period TimeRange) (CostBreakdown, error)

	// SSHTarget returns the network coordinates and OS user for SSH access.
	SSHTarget(ctx context.Context, vmID string) (SSHTarget, error)

	// Preflight validates that the provider is ready to provision: auth is
	// configured, required APIs are enabled, and other one-time setup is
	// in place. Returns nil if ready; otherwise a multi-line error with
	// remediation hints. Cheap to call (one or two API list-style requests).
	//
	// Used by `moorpost provision` before any cloud-creating call, and by
	// `moorpost doctor` when a project config is present.
	Preflight(ctx context.Context) error
}

// ProvisionSpec describes a VM to create. Fields not used by a given provider
// should be ignored rather than rejected, so the same spec works across clouds.
type ProvisionSpec struct {
	// Name is the desired VM name. Providers may sanitize it for their
	// naming rules but should preserve it where possible.
	Name string

	// Region/Zone are provider-specific location strings. At least one must
	// be set; providers requiring a zone will derive it if only Region is set.
	Region string
	Zone   string

	// MachineType is the provider-specific instance class (e.g. "e2-standard-2",
	// "ccx23"). It is intentionally opaque at this layer.
	MachineType string

	// Disk configures the boot disk.
	DiskGB   int    // size in GiB
	DiskType string // provider-specific (e.g. "pd-standard", "ssd")

	// Image is the OS image identifier. Empty means "use the provider's
	// Moorpost-recommended default" (Ubuntu 24.04 in v1).
	Image string

	// SSHKeyPub is the OpenSSH public key to install for the default user.
	// Required: providers must reject an empty value rather than silently
	// using a metadata-only key, to keep behavior predictable.
	SSHKeyPub string

	// StaticIP requests a reserved external IP. Cost-positive on most
	// providers; default false.
	StaticIP bool

	// Tags are provider-specific labels (e.g. firewall tags on GCP).
	// Moorpost reserves "moorpost" and "moorpost-test".
	Tags []string

	// SourceIPRanges restricts the SSH firewall (CIDR list). Empty means
	// "any" — providers should warn but not error.
	SourceIPRanges []string

	// StartImmediately controls whether the VM should be left running after
	// Provision returns. The local-first mode passes false (provision-then-stop).
	StartImmediately bool

	// BootstrapScript is an optional script to run on first boot via the
	// provider's user-data / cloud-init mechanism.
	BootstrapScript string
}

// VM is the provider's identifying handle for a created instance. Returned
// by Provision and stored in ~/.moorpost/state.json.
type VM struct {
	ID         string    // provider-internal identifier (used by all later calls)
	Name       string    // human-readable name (may differ from ID)
	Provider   string    // provider ID, e.g. "gcp"
	Region     string    // provider-specific
	Zone       string    // provider-specific
	ExternalIP string    // empty until first Start completes
	CreatedAt  time.Time // wall-clock time at provider, may differ from local
}

// VMState is the runtime state of a VM. Providers should map their native
// states into these values; values outside this set are not allowed.
type VMState string

const (
	VMStateUnknown      VMState = "unknown"
	VMStateProvisioning VMState = "provisioning"
	VMStateRunning      VMState = "running"
	VMStateStopping     VMState = "stopping"
	VMStateStopped      VMState = "stopped"
	VMStateTerminated   VMState = "terminated"
	VMStateError        VMState = "error"
)

// SnapshotID is an opaque identifier for a snapshot, scoped to its provider.
type SnapshotID string

// TimeRange is a half-open interval [Start, End).
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// CostBreakdown is the cost incurred over a TimeRange. Currency is always USD
// at the provider layer; any conversion is the caller's job.
type CostBreakdown struct {
	Compute    float64 // VM compute hours
	Disk       float64 // boot disk + snapshots
	Network    float64 // egress (ingress is typically free)
	Other      float64 // misc (e.g. static IP, IAM)
	Total      float64 // sum of the above (provider may compute separately)
	Period     TimeRange
	IsEstimate bool // true if derived from list price rather than billed amounts
}

// SSHTarget is the network coordinates needed to SSH into a VM.
type SSHTarget struct {
	Host string // hostname or IP
	Port int    // typically 22
	User string // OS login user (e.g. "landytang")
	// IdentityFile is the path to the SSH private key the user should
	// authenticate with. Empty means "rely on ssh's default identity
	// resolution" (~/.ssh/id_*, ssh-agent). Set by providers that
	// installed a specific key during Provision (e.g. GCP uses
	// ~/.ssh/google_compute_engine).
	IdentityFile string
}

// ErrCostUnavailable indicates the provider's billing data is not available
// for the requested vmID/period. Callers should fall back to a list-price
// estimate or display "—" in UI.
var ErrCostUnavailable = errors.New("cost data unavailable")

// ErrNotFound indicates a VM with the given ID does not exist (or no longer
// exists, e.g. after Destroy).
var ErrNotFound = errors.New("vm not found")

// Constructor builds a Provider from a config map. Implementations validate
// required keys and return a clear error for missing/invalid config.
type Constructor func(config map[string]any) (Provider, error)

// registry holds the registered provider constructors.
type registry struct {
	mu           sync.RWMutex
	constructors map[string]Constructor
}

var defaultRegistry = &registry{constructors: make(map[string]Constructor)}

// Register associates an ID with a Constructor. It panics if id is empty or
// already registered — duplicate registration is a programmer error, not a
// runtime condition. Call this from an init() function in a provider package.
func Register(id string, c Constructor) {
	defaultRegistry.register(id, c)
}

func (r *registry) register(id string, c Constructor) {
	if id == "" {
		panic("provider: Register called with empty id")
	}
	if c == nil {
		panic("provider: Register called with nil Constructor for id " + id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.constructors[id]; dup {
		panic("provider: Register called twice for id " + id)
	}
	r.constructors[id] = c
}

// Get builds a provider from its registered constructor. It returns an error
// if the id is not registered or the constructor rejects the config.
func Get(id string, config map[string]any) (Provider, error) {
	return defaultRegistry.get(id, config)
}

func (r *registry) get(id string, config map[string]any) (Provider, error) {
	r.mu.RLock()
	c, ok := r.constructors[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider: unknown id %q (registered: %v)", id, r.list())
	}
	return c(config)
}

// List returns the IDs of all registered providers, in stable lexical order.
func List() []string {
	return defaultRegistry.list()
}

func (r *registry) list() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.constructors))
	for id := range r.constructors {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// resetForTest clears the registry. Tests in this package only.
func (r *registry) resetForTest() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.constructors = make(map[string]Constructor)
}
