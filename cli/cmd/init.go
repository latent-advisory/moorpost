package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold .moorpost/config.yaml in the current project",
	Long: `Creates a default Moorpost config in the current directory's
.moorpost/config.yaml. Uses GCP as the cloud provider and Claude Code as the
agent. After running init, run ` + "`moorpost auth`" + ` to sign in, then
` + "`moorpost provision`" + ` to create the VM.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := InitOptions{
			Dir:                 initFlagDir,
			Slug:                initFlagSlug,
			GCPProject:          initFlagGCPProject,
			Region:              initFlagRegion,
			Zone:                initFlagZone,
			MachineType:         initFlagMachineType,
			Force:               initFlagForce,
			GCloudConfiguration: initFlagGCPConfig,
		}
		// Interactive picker when no --gcp-project flag was passed:
		// gives users with multiple gcloud accounts a clear choice
		// (and lets first-time users start a fresh login). Skipped
		// when --gcp-project is set so CI / scripted runs don't hang
		// on a TTY-less stdin.
		if opts.GCPProject == "" {
			cfgName, project, err := promptForGCPConfiguration(cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			opts.GCloudConfiguration = cfgName
			opts.GCPProject = project
		}
		return RunInit(cmd.OutOrStdout(), opts)
	},
}

var (
	initFlagDir         string
	initFlagSlug        string
	initFlagGCPProject  string
	initFlagGCPConfig   string
	initFlagRegion      string
	initFlagZone        string
	initFlagMachineType string
	initFlagForce       bool
)

func init() {
	initCmd.Flags().StringVar(&initFlagDir, "dir", "", "directory to scaffold (default: cwd)")
	initCmd.Flags().StringVar(&initFlagSlug, "slug", "", "project slug (default: directory basename)")
	initCmd.Flags().StringVar(&initFlagGCPProject, "gcp-project", "", "GCP project ID (skip the interactive picker; required for non-interactive runs)")
	initCmd.Flags().StringVar(&initFlagGCPConfig, "gcp-config", "", "gcloud configuration name to pin moorpost to (default: prompt)")
	initCmd.Flags().StringVar(&initFlagRegion, "region", "us-central1", "GCP region")
	initCmd.Flags().StringVar(&initFlagZone, "zone", "", "GCP zone (default: <region>-a)")
	initCmd.Flags().StringVar(&initFlagMachineType, "machine-type", "e2-standard-2", "GCP machine type")
	initCmd.Flags().BoolVar(&initFlagForce, "force", false, "overwrite existing config")
	rootCmd.AddCommand(initCmd)
}

// InitOptions controls RunInit. All optional except as documented.
type InitOptions struct {
	Dir         string // default: cwd
	Slug        string // default: derived from Dir basename
	GCPProject  string // empty allowed; user can edit later
	Region      string // default: us-central1
	Zone        string // default: <region>-a
	MachineType string // default: e2-standard-2
	Force       bool
	// GCloudConfiguration is the gcloud configuration name to pin moorpost
	// to (passed via --configuration on every gcloud call). Empty = use
	// whichever gcloud config is currently active. Populated from the
	// interactive picker in the cobra layer when not provided as a flag.
	GCloudConfiguration string
}

// detectGCPProject returns the active project from gcloud config, or empty
// if not set. Exposed as a var so tests can stub.
var detectGCPProject = func() string {
	out, err := exec.Command("gcloud", "config", "get-value", "project", "--quiet").Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	// gcloud prints "(unset)" or empty when no active project.
	if s == "" || s == "(unset)" {
		return ""
	}
	return s
}

// RunInit creates .moorpost/config.yaml in opts.Dir. Exposed for testing.
func RunInit(out io.Writer, opts InitOptions) error {
	dir := opts.Dir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}
	slug := opts.Slug
	if slug == "" {
		slug = deriveSlug(filepath.Base(dir))
	}

	// Auto-detect GCP project from gcloud config if not provided.
	if opts.GCPProject == "" {
		if detected := detectGCPProject(); detected != "" {
			fmt.Fprintf(out, "Auto-detected GCP project from gcloud config: %s\n", detected)
			opts.GCPProject = detected
		}
	}

	target := filepath.Join(dir, ".moorpost", "config.yaml")
	if _, err := os.Stat(target); err == nil && !opts.Force {
		return fmt.Errorf("moorpost init: %s already exists (use --force to overwrite)", target)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", target, err)
	}

	cfg := config.Default()
	cfg.ProjectSlug = slug

	region := opts.Region
	if region == "" {
		region = "us-central1"
	}
	zone := opts.Zone
	if zone == "" {
		zone = region + "-a"
	}
	machine := opts.MachineType
	if machine == "" {
		machine = "e2-standard-2"
	}

	// Provider sub-config: nested under provider.gcp per the §8.1 schema.
	gcpSub := map[string]any{
		"region":       region,
		"zone":         zone,
		"machine_type": machine,
		"disk_size_gb": 100,
		"disk_type":    "pd-standard",
		"static_ip":    true,
		"network_tags": []string{"moorpost"},
	}
	if opts.GCPProject != "" {
		gcpSub["project"] = opts.GCPProject
	}
	if opts.GCloudConfiguration != "" {
		gcpSub["configuration"] = opts.GCloudConfiguration
	}
	cfg.Provider.Raw = map[string]any{"gcp": gcpSub}

	// Agent sub-config under agent.claude-code.
	cfg.Agent.Raw = map[string]any{
		"claude-code": map[string]any{
			"auth_method": "oauth-subscription",
		},
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("derived config invalid: %w", err)
	}
	if err := cfg.Save(target); err != nil {
		return err
	}
	fmt.Fprintf(out, "Created %s\n", target)
	if opts.GCPProject == "" {
		fmt.Fprintln(out, "Next: edit provider.gcp.project in the file (no GCP project was provided),")
		fmt.Fprintln(out, "      then run `moorpost auth` and `moorpost provision`.")
	} else {
		fmt.Fprintln(out, "Next: run `moorpost auth` to sign in, then `moorpost provision`.")
	}
	return nil
}

// slugSanitizeRE matches characters not allowed in a v1 project slug.
var slugSanitizeRE = regexp.MustCompile(`[^a-z0-9-]`)

// deriveSlug coerces a directory name into a valid project slug per
// config.go's regex. Lowercase; non-[a-z0-9-] → '-'; trim hyphens; ensure
// it starts with a letter.
func deriveSlug(s string) string {
	s = strings.ToLower(s)
	s = slugSanitizeRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	// Must start with a letter; if not, prefix `p-`.
	if s == "" {
		return "moorpost-project"
	}
	if !isLowerLetter(s[0]) {
		s = "p-" + s
	}
	if len(s) > 63 {
		s = s[:63]
	}
	s = strings.TrimRight(s, "-")
	return s
}

func isLowerLetter(b byte) bool { return b >= 'a' && b <= 'z' }

// silence import lint in some Go versions
var _ = errors.Is
