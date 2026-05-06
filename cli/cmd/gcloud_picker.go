package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// gcloudConfig is a single `gcloud config configurations` entry. We parse
// the tab-separated output of `gcloud config configurations list` so we
// don't depend on a Go gcloud SDK.
type gcloudConfig struct {
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
	Account  string `json:"account"`
	Project  string `json:"project"`
}

// listGcloudConfigurations runs `gcloud config configurations list` and
// returns the parsed entries. Empty slice if gcloud isn't installed.
// Exposed via package-level var so tests can stub.
var listGcloudConfigurations = func() ([]gcloudConfig, error) {
	out, err := exec.Command("gcloud", "config", "configurations", "list",
		"--format=value(name,is_active,properties.core.account,properties.core.project)").Output()
	if err != nil {
		return nil, err
	}
	var configs []gcloudConfig
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// `value()` formatter emits tab-separated columns.
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			continue
		}
		configs = append(configs, gcloudConfig{
			Name:     fields[0],
			IsActive: strings.EqualFold(fields[1], "True"),
			Account:  fields[2],
			Project:  fields[3],
		})
	}
	return configs, nil
}

// promptForGCPConfiguration asks the user to pick which gcloud
// configuration moorpost should use for this project. Returns the
// chosen configuration name and the GCP project ID, or an error.
//
// Behavior:
//   - 0 existing configs: triggers a fresh login (gcloud auth login + new
//     configuration) and returns its name+project.
//   - 1 existing config: uses it without prompting (no point asking).
//   - 2+ existing configs: shows a picker with all configs + an "Add new
//     account" entry; returns whichever the user selects.
func promptForGCPConfiguration(in io.Reader, out io.Writer) (configName, project string, err error) {
	configs, err := listGcloudConfigurations()
	if err != nil {
		return "", "", fmt.Errorf("gcloud config configurations list: %w (is gcloud installed?)", err)
	}

	if len(configs) == 0 {
		fmt.Fprintln(out, "No gcloud configurations found. Setting up a new one...")
		return addNewGCloudConfiguration(in, out)
	}

	if len(configs) == 1 {
		c := configs[0]
		fmt.Fprintf(out, "Using gcloud configuration: %s (account=%s, project=%s)\n",
			c.Name, c.Account, c.Project)
		return c.Name, c.Project, nil
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Choose a gcloud configuration for this moorpost project:")
	for i, c := range configs {
		marker := " "
		if c.IsActive {
			marker = "*"
		}
		fmt.Fprintf(out, "  [%d]%s %-20s account=%s  project=%s\n",
			i+1, marker, c.Name, c.Account, c.Project)
	}
	addIdx := len(configs) + 1
	fmt.Fprintf(out, "  [%d]  add a new gcloud account (browser OAuth)\n", addIdx)
	fmt.Fprint(out, "Pick a number: ")

	reader := bufio.NewReader(in)
	line, _ := reader.ReadString('\n')
	choice, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || choice < 1 || choice > addIdx {
		return "", "", fmt.Errorf("invalid choice: %q", strings.TrimSpace(line))
	}
	if choice == addIdx {
		return addNewGCloudConfiguration(in, out)
	}
	c := configs[choice-1]
	return c.Name, c.Project, nil
}

// addNewGCloudConfiguration walks the user through:
//  1. Naming a new gcloud configuration
//  2. Activating it
//  3. `gcloud auth login` (browser OAuth)
//  4. Choosing/setting the GCP project
//
// All gcloud calls are interactive so the user sees gcloud's own
// prompts/output. Returns the new configuration name + project ID.
func addNewGCloudConfiguration(in io.Reader, out io.Writer) (string, string, error) {
	reader := bufio.NewReader(in)

	fmt.Fprint(out, "Name for the new gcloud configuration (e.g., 'work' or 'personal'): ")
	nameLine, _ := reader.ReadString('\n')
	name := strings.TrimSpace(nameLine)
	if name == "" {
		return "", "", errors.New("configuration name required")
	}

	// Configurations create activates the new one as a side effect.
	fmt.Fprintf(out, "→ gcloud config configurations create %s\n", name)
	cmd := exec.Command("gcloud", "config", "configurations", "create", name, "--activate")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	if err := cmd.Run(); err != nil {
		// Activate-existing fallback: name may already exist (idempotent).
		fmt.Fprintln(out, "  (configuration may already exist; activating it)")
		cmd = exec.Command("gcloud", "config", "configurations", "activate", name)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
		if err2 := cmd.Run(); err2 != nil {
			return "", "", fmt.Errorf("create-or-activate %s: create=%v, activate=%w", name, err, err2)
		}
	}

	fmt.Fprintln(out, "→ gcloud auth login (browser OAuth flow)")
	cmd = exec.Command("gcloud", "auth", "login", "--update-adc")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("gcloud auth login: %w", err)
	}

	fmt.Fprint(out, "GCP project ID for this moorpost project: ")
	projLine, _ := reader.ReadString('\n')
	project := strings.TrimSpace(projLine)
	if project == "" {
		return "", "", errors.New("GCP project required")
	}

	fmt.Fprintf(out, "→ gcloud config set project %s\n", project)
	cmd = exec.Command("gcloud", "config", "set", "project", project)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("gcloud config set project: %w", err)
	}

	fmt.Fprintf(out, "✓ gcloud configuration %q ready (account+project set)\n", name)
	return name, project, nil
}
