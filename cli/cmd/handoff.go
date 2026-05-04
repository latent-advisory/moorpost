package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
	"github.com/spf13/cobra"
)

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Hand off the active Claude session from local to the remote VM",
	Long: `Pauses local agent (caller's responsibility — be at a turn
boundary), starts the VM if stopped, syncs the project + agent session
state to the remote, and resumes the agent there. After this, you can close
the laptop and the work continues on the VM.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunHandoff(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), c, HandoffOptions{
			SkipPrompt: handoffFlagYes,
		})
	},
}

var handoffFlagYes bool

func init() {
	handoffCmd.Flags().BoolVarP(&handoffFlagYes, "yes", "y", false, "skip the confirmation prompt")
	rootCmd.AddCommand(handoffCmd)
}

// HandoffOptions controls RunHandoff.
type HandoffOptions struct {
	SkipPrompt bool
	// Now overrides time.Now for deterministic testing of timestamps.
	Now func() time.Time
	// IPWaitTimeout overrides the default 60s.
	IPWaitTimeout time.Duration
}

// RunHandoff executes the full local→remote handoff flow.
func RunHandoff(ctx context.Context, out io.Writer, in io.Reader, c *Context, opts HandoffOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.Provider == nil || c.Agent == nil || c.Sync == nil {
		return errors.New("handoff: incomplete context (need provider, agent, sync)")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ipWaitTimeout := opts.IPWaitTimeout
	if ipWaitTimeout == 0 {
		ipWaitTimeout = 60 * time.Second
	}

	ps, ok := c.State.GetProject(projectKey(c))
	if !ok || ps.VMID == "" {
		return errors.New("handoff: project not provisioned (run `moorpost provision` first)")
	}
	if ps.ActiveSide == state.SideRemote {
		return errors.New("handoff: active side is already remote — nothing to do (use `moorpost return` to bring it back)")
	}

	if !opts.SkipPrompt {
		fmt.Fprintf(out, "Pause local Claude (be at a turn boundary), then continue? [y/N]: ")
		reader := bufio.NewReader(in)
		line, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			return errors.New("handoff: aborted")
		}
	}

	// Start the VM (idempotent on already-running).
	fmt.Fprintf(out, "Starting %s...\n", ps.VMID)
	if err := c.Provider.Start(ctx, ps.VMID); err != nil {
		return fmt.Errorf("handoff: start VM: %w", err)
	}

	// Poll for IP readiness.
	tgt, err := waitForIP(ctx, c.Provider, ps.VMID, ipWaitTimeout)
	if err != nil {
		return fmt.Errorf("handoff: %w", err)
	}
	fmt.Fprintf(out, "VM running at %s\n", tgt.Host)

	// Inject the cached credential into /etc/moorpost/env (or wherever the
	// agent puts it).
	cred, err := c.Agent.AuthenticateLocal(ctx) // hits keychain cache
	if err != nil {
		return fmt.Errorf("handoff: load credential: %w", err)
	}
	agentTarget := agent.SSHTarget{Host: tgt.Host, Port: tgt.Port, User: tgt.User}
	if err := c.Agent.InjectCredential(ctx, agentTarget, cred); err != nil {
		return fmt.Errorf("handoff: inject credential: %w", err)
	}

	// One-shot push: agent session state directory. This must happen
	// BEFORE Resume so the remote claude --resume sees the latest state.
	// (Project files are handled by the continuous sync below.)
	localState := c.Agent.SessionStatePath(c.ProjectDir)
	if localState != "" {
		remoteState := remoteSessionStateFor(c)
		fmt.Fprintf(out, "Syncing agent session state → %s ...\n", tgt.Host)
		if err := c.Sync.OneShot(ctx,
			mpsync.Endpoint{Path: localState + "/"},
			mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remoteState + "/"},
			mpsync.DirectionLocalToRemote,
		); err != nil {
			// Session-state may not exist yet (first run); warn but don't fail.
			fmt.Fprintf(out, "warning: session state sync failed (this may be the first run): %v\n", err)
		}
	}

	// Start the continuous bidirectional project-file sync. Per
	// PLUGIN.md §6.5, project files are continuously synced (so the
	// desktop app's .docx edits at the root propagate to the remote
	// while Claude Code is running there).
	remoteProjectPath := remoteProjectPathFor(c)
	fmt.Fprintf(out, "Starting continuous sync %s ↔ %s:%s ...\n", c.ProjectDir, tgt.Host, remoteProjectPath)
	syncID, err := c.Sync.StartSession(ctx, mpsync.SyncSpec{
		Label:          c.Config.ProjectSlug + "-handoff",
		Alpha:          mpsync.Endpoint{Path: c.ProjectDir},
		Beta:           mpsync.Endpoint{SSHHost: hostFromTarget(tgt), Path: remoteProjectPath},
		ConflictPolicy: c.Config.Sync.ConflictPolicy,
		IgnorePatterns: c.Config.Sync.Ignore,
	})
	if err != nil {
		return fmt.Errorf("handoff: start sync session: %w", err)
	}

	// Resume the agent on the remote.
	ref := agent.SessionRef{
		ProjectSlug:   c.Config.ProjectSlug,
		ProjectAbsDir: c.ProjectDir,
		SessionID:     ps.AgentSessionID,
	}
	fmt.Fprintf(out, "Resuming claude on remote (slug=%s)...\n", ref.ProjectSlug)
	if err := c.Agent.Resume(ctx, agentTarget, ref); err != nil {
		return fmt.Errorf("handoff: resume on remote: %w", err)
	}

	// Update state with handoff metadata + new continuous sync session ID.
	if err := withProjectState(c, func(p *state.ProjectState) error {
		p.LastHandoff = now()
		p.ActiveSide = state.SideRemote
		p.SyncSessionID = string(syncID)
		return nil
	}); err != nil {
		return err
	}
	if err := withVM(c, ps.VMID, func(rec *state.VMRecord) error {
		rec.StateCache = "running"
		rec.ExternalIP = tgt.Host
		return nil
	}); err != nil {
		return err
	}

	fmt.Fprintln(out, "Done. Local Claude is now inactive. Use `moorpost attach` to view, `moorpost return` to bring back.")
	return nil
}

// waitForIP polls Provider.SSHTarget until the VM has an external IP, up to
// timeout. Returns the resolved target.
func waitForIP(ctx context.Context, p provider.Provider, vmID string, timeout time.Duration) (provider.SSHTarget, error) {
	deadline := time.Now().Add(timeout)
	delay := 250 * time.Millisecond
	for {
		tgt, err := p.SSHTarget(ctx, vmID)
		if err == nil && tgt.Host != "" {
			return tgt, nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return provider.SSHTarget{}, fmt.Errorf("VM %s not reachable within %s: %w", vmID, timeout, err)
			}
			return provider.SSHTarget{}, fmt.Errorf("VM %s has no external IP within %s", vmID, timeout)
		}
		select {
		case <-ctx.Done():
			return provider.SSHTarget{}, ctx.Err()
		case <-time.After(delay):
		}
		// Modest exponential backoff capped at 2s.
		if delay < 2*time.Second {
			delay = delay * 2
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
		}
	}
}

// hostFromTarget returns the SSH host string used by Sync engines. We prefer
// `<user>@<host>` so rsync/mutagen log into the right account.
func hostFromTarget(tgt provider.SSHTarget) string {
	if tgt.User == "" {
		return tgt.Host
	}
	return tgt.User + "@" + tgt.Host
}

// remoteProjectPathFor returns the conventional remote path for the
// project's working tree: ~/moorpost/<slug>.
func remoteProjectPathFor(c *Context) string {
	if c == nil || c.Config == nil {
		return ""
	}
	return "~/moorpost/" + c.Config.ProjectSlug
}

// remoteSessionStateFor returns the remote path for the agent's session
// state directory. Uses the agent's encoder against the project's local
// absolute path; the iter 14 bootstrap symlink keeps the encoded form
// matching on both sides.
func remoteSessionStateFor(c *Context) string {
	if c == nil || c.Agent == nil {
		return ""
	}
	local := c.Agent.SessionStatePath(c.ProjectDir)
	if local == "" {
		return ""
	}
	// Take only the last path component (the encoded slug).
	if i := strings.LastIndex(local, "/"); i >= 0 {
		return "~/.claude/projects/" + local[i+1:]
	}
	return "~/.claude/projects/" + local
}
