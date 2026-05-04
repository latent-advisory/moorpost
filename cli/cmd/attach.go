package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:   "attach",
	Short: "Open an SSH session and attach to the remote tmux running Claude",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunAttach(cmd.Context(), cmd.OutOrStdout(), c)
	},
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

// AttachPlan captures the argv the attach command will exec. Tests inspect
// this; the real cobra command turns around and exec's it.
type AttachPlan struct {
	SSHBin string
	Args   []string // includes user@host and tmux args
}

// RunAttach resolves SSH target + tmux session name, then exec's `ssh` to
// replace the current process. Returns only on error before exec.
func RunAttach(ctx context.Context, out io.Writer, c *Context) error {
	plan, err := planAttach(ctx, c)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Attaching to %s...\n", plan.Args[len(plan.Args)-1])
	// Find the ssh binary on PATH. We don't have the Executor here because
	// exec'ing into ssh replaces our process — Executor is for child procs.
	bin, err := lookPathSSH(plan.SSHBin)
	if err != nil {
		return fmt.Errorf("attach: locate ssh: %w", err)
	}
	argv := append([]string{plan.SSHBin}, plan.Args...)
	env := os.Environ()
	if err := syscall.Exec(bin, argv, env); err != nil {
		return fmt.Errorf("attach: exec ssh: %w", err)
	}
	return nil // unreachable
}

// planAttach is the testable half — builds the argv without exec'ing.
func planAttach(ctx context.Context, c *Context) (AttachPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil || c.Config == nil {
		return AttachPlan{}, errors.New("attach: incomplete context")
	}
	if c.State == nil {
		return AttachPlan{}, errors.New("attach: no state available")
	}
	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return AttachPlan{}, errors.New("attach: project not provisioned")
	}
	tgt, err := c.Provider.SSHTarget(ctx, ps.VMID)
	if err != nil {
		return AttachPlan{}, fmt.Errorf("attach: %w", err)
	}
	if tgt.Host == "" {
		return AttachPlan{}, errors.New("attach: VM has no external IP (is it stopped?)")
	}
	hostSpec := tgt.User + "@" + tgt.Host
	if tgt.User == "" {
		hostSpec = tgt.Host
	}
	args := []string{
		"-t",
		"-o", "BatchMode=no", // attach can prompt for first-time host key
		"-o", "ServerAliveInterval=30",
	}
	if tgt.Port != 0 && tgt.Port != 22 {
		args = append(args, "-p", strconv.Itoa(tgt.Port))
	}
	args = append(args, hostSpec, "tmux", "attach-session", "-t", c.Config.ProjectSlug)
	return AttachPlan{
		SSHBin: "ssh",
		Args:   args,
	}, nil
}

// lookPathSSH is a var so tests could swap it. Defaults to os/exec.LookPath.
var lookPathSSH = exec.LookPath
