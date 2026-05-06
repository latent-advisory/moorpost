package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/state"
	"github.com/spf13/cobra"
)

// sessionUUIDRE matches the UUID-shaped JSONL filenames that Claude Code
// writes per session. We use this to skip directories or stray files
// (lockfiles, .DS_Store, "<sid>" companion subdirectories alongside the
// "<sid>.jsonl") under ~/.claude/projects/<encoded>/.
var sessionUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)

// sessionsCmd is the parent. It does nothing on its own; users hit
// subcommands like `sessions list`.
var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Inspect Claude Code sessions Moorpost knows about",
	Long: `Sessions surface what claude has on disk for the current project and
which of those are currently routed to the remote VM. Useful when the
extension's panel-list and the CLI's idea of "what's where" disagree.`,
}

var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Claude Code sessions for the current project",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := loadProjectContext(ContextOptions{
			RequireConfig: true,
			Stdout:        cmd.OutOrStdout(),
			Stderr:        cmd.ErrOrStderr(),
		})
		if err != nil {
			return err
		}
		return RunSessionsList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), c, SessionsListOptions{
			JSON:   sessionsListFlagJSON,
			NoLive: sessionsListFlagNoLive,
		})
	},
}

var (
	sessionsListFlagJSON   bool
	sessionsListFlagNoLive bool
)

func init() {
	sessionsListCmd.Flags().BoolVar(&sessionsListFlagJSON, "json", false, "emit machine-readable JSON")
	sessionsListCmd.Flags().BoolVar(&sessionsListFlagNoLive, "no-live", false, "skip the per-session ssh check for a live remote claude process (faster)")
	sessionsCmd.AddCommand(sessionsListCmd)
	rootCmd.AddCommand(sessionsCmd)
}

// SessionsListOptions controls RunSessionsList.
type SessionsListOptions struct {
	JSON   bool
	NoLive bool
}

// SessionInfo is one row in the sessions list — both human and JSON output
// pivot from this type.
type SessionInfo struct {
	SessionID     string    `json:"session_id"`
	Location      string    `json:"location"` // "local" | "remote"
	LiveOnRemote  bool      `json:"live_on_remote"`
	MTime         time.Time `json:"mtime"`
	SizeBytes     int64     `json:"size_bytes"`
	FirstUserText string    `json:"first_user_text,omitempty"`
}

// SessionsReport is the JSON envelope.
type SessionsReport struct {
	ProjectDir string        `json:"project_dir"`
	Sessions   []SessionInfo `json:"sessions"`
}

// RunSessionsList enumerates Claude Code session JSONLs for c.ProjectDir,
// classifies each as local/remote against state.json's RemoteSIDs, and
// (unless opts.NoLive) checks which remote SIDs have a live claude
// process on the VM. Errors talking to the VM are non-fatal: we mark all
// remote sessions as not-live and log to stderr.
func RunSessionsList(ctx context.Context, out, errOut io.Writer, c *Context, opts SessionsListOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return fmt.Errorf("sessions: no project context loaded")
	}
	if c.Agent == nil {
		return fmt.Errorf("sessions: no agent in context")
	}
	if errOut == nil {
		errOut = os.Stderr
	}

	dir := c.Agent.SessionStatePath(c.ProjectDir)
	if dir == "" {
		return fmt.Errorf("sessions: agent did not return a session-state path for %q", c.ProjectDir)
	}

	// Determine remote SIDs from state.
	var remoteSet map[string]bool
	var ps state.ProjectState
	if c.State != nil {
		if got, ok := c.State.GetProject(projectKey(c)); ok {
			ps = got
		}
	}
	remoteSet = make(map[string]bool, len(ps.RemoteSIDs))
	for _, s := range ps.RemoteSIDs {
		remoteSet[s] = true
	}

	sessions, err := scanSessionsDir(dir)
	if err != nil {
		// A missing session dir is an empty list, not an error.
		if os.IsNotExist(err) {
			sessions = nil
		} else {
			return fmt.Errorf("sessions: scan %s: %w", dir, err)
		}
	}

	for i := range sessions {
		if remoteSet[sessions[i].SessionID] {
			sessions[i].Location = "remote"
		} else {
			sessions[i].Location = "local"
		}
	}

	// Live check: ssh to the VM, run pgrep -af claude once, see which of
	// our remote SIDs appear. Optional (--no-live) and best-effort.
	if !opts.NoLive && len(remoteSet) > 0 {
		live, err := liveRemoteSIDsFunc(ctx, c, &ps)
		if err != nil {
			fmt.Fprintf(errOut, "sessions: live-check failed (%v); marking all remote sessions as not-live\n", err)
		} else {
			for i := range sessions {
				if sessions[i].Location == "remote" && live[sessions[i].SessionID] {
					sessions[i].LiveOnRemote = true
				}
			}
		}
	}

	report := SessionsReport{
		ProjectDir: c.ProjectDir,
		Sessions:   sessions,
	}

	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	return printSessionsHuman(out, report)
}

// liveRemoteSIDsFunc is overridable in tests.
var liveRemoteSIDsFunc = liveRemoteSIDs

// liveRemoteSIDs ssh's to the project's VM, runs `pgrep -af claude`, and
// returns the set of SIDs from ps.RemoteSIDs that appear in any matching
// process line. Returns nil + error if the VM isn't provisioned, has no
// IP, or ssh fails. The caller treats nil as "couldn't check; assume
// not-live."
func liveRemoteSIDs(ctx context.Context, c *Context, ps *state.ProjectState) (map[string]bool, error) {
	if c == nil || c.Provider == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	if ps == nil || ps.VMID == "" {
		return nil, fmt.Errorf("project not provisioned")
	}

	tgt, err := c.Provider.SSHTarget(ctx, ps.VMID)
	if err != nil {
		return nil, fmt.Errorf("ssh target: %w", err)
	}
	if tgt.Host == "" {
		return nil, fmt.Errorf("VM has no host")
	}

	out, err := runRemotePgrep(ctx, tgt)
	if err != nil {
		return nil, err
	}

	live := make(map[string]bool)
	for _, sid := range ps.RemoteSIDs {
		if sid == "" {
			continue
		}
		if strings.Contains(out, sid) {
			live[sid] = true
		}
	}
	return live, nil
}

// runRemotePgrep runs `pgrep -af claude` on the remote and returns its
// stdout. Empty output is fine (nobody running claude). Non-zero exit
// from pgrep when nothing matches is also fine — we squash it. A real
// ssh failure (network, auth) returns an error.
//
// Overridable in tests via runRemotePgrepFunc.
var runRemotePgrepFunc = runRemotePgrepReal

func runRemotePgrep(ctx context.Context, tgt provider.SSHTarget) (string, error) {
	return runRemotePgrepFunc(ctx, tgt)
}

func runRemotePgrepReal(ctx context.Context, tgt provider.SSHTarget) (string, error) {
	host := tgt.Host
	if tgt.User != "" {
		host = tgt.User + "@" + tgt.Host
	}
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if tgt.IdentityFile != "" {
		args = append(args, "-i", tgt.IdentityFile)
	}
	args = append(args, host, "--", "pgrep", "-af", "claude")
	cmd := exec.CommandContext(ctx, "ssh", args...) // #nosec G204 — args are static + provider data
	out, err := cmd.Output()
	if err != nil {
		// pgrep exits 1 when no process matches. exec returns *ExitError;
		// stdout will simply be empty. Treat that as success.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("ssh pgrep: %w", err)
	}
	return string(out), nil
}

// scanSessionsDir reads dir, filters to UUID-named .jsonl files, and
// returns one SessionInfo per file (sorted by mtime desc — most recent
// first). Location/LiveOnRemote are not set here (caller fills them in).
func scanSessionsDir(dir string) ([]SessionInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var infos []SessionInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !sessionUUIDRE.MatchString(name) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		sid := strings.TrimSuffix(name, ".jsonl")
		first, _ := readFirstUserText(filepath.Join(dir, name))
		infos = append(infos, SessionInfo{
			SessionID:     sid,
			MTime:         fi.ModTime().UTC(),
			SizeBytes:     fi.Size(),
			FirstUserText: first,
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].MTime.After(infos[j].MTime)
	})
	return infos, nil
}

// firstUserScanLines caps how many JSONL lines we'll read while looking
// for the first user message. 200 is generous: typical sessions emit
// the first user turn in the first 5-20 lines (after queue ops + the
// SessionStart hook payload), but plugins have been known to write more.
const firstUserScanLines = 200

// firstUserTextCap caps the snippet returned to the caller. 60 chars is
// short enough to fit on one terminal row alongside the other columns.
const firstUserTextCap = 60

// readFirstUserText scans the first ~firstUserScanLines lines of path,
// returning the first user message's text content (capped). Tolerates
// malformed/non-JSON lines by skipping them. Returns "" when no user
// message is found.
func readFirstUserText(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 — path is from os.ReadDir of a fixed dir
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Some JSONL records (especially those including hookSpecificOutput
	// dumps) can be very long — bump bufio's max from the default 64 KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for line := 0; line < firstUserScanLines && sc.Scan(); line++ {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		if rec.Type != "user" || len(rec.Message) == 0 {
			continue
		}
		text := extractUserText(rec.Message)
		if text == "" || isSyntheticUserMessage(text) {
			continue
		}
		return capRunes(text, firstUserTextCap), nil
	}
	return "", sc.Err()
}

// isSyntheticUserMessage filters out Claude Code's auto-injected
// "user" messages that aren't actual prompts the human typed:
//   - <ide_opened_file>: IDE selection-context dump fired on file open
//   - <ide_selection>: IDE selection-context dump on highlight change
//   - <local-command-caveat>: warning prepended when a /command was used
//   - <command-name>, <command-message>, <command-args>: slash-command metadata
//   - <system-reminder>: harness reminders to the agent
//
// We skip these so the picker labels reflect what the user actually
// typed first ("review the migration", "fix the bug") rather than
// IDE noise ("The user opened the file ..."). Implementation: any
// text whose first non-whitespace char is `<` and starts with one of
// the known synthetic tags is skipped.
func isSyntheticUserMessage(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "<") {
		return false
	}
	for _, tag := range syntheticUserTags {
		if strings.HasPrefix(t, tag) {
			return true
		}
	}
	return false
}

var syntheticUserTags = []string{
	"<ide_opened_file>",
	"<ide_selection>",
	"<local-command-caveat>",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<local-command-output>",
	"<command-name>",
	"<command-message>",
	"<command-args>",
	"<system-reminder>",
	"<user-prompt-submit-hook>",
	"<bash-stdout>",
	"<bash-stderr>",
}

// extractUserText handles the two shapes Claude Code writes for a user
// message's content: a bare string, or an array of content blocks (the
// `[{type:text,text:...}, ...]` form). Returns "" for tool_result-only
// turns, image-only turns, or anything we don't recognize.
func extractUserText(message json.RawMessage) string {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return ""
	}
	if len(msg.Content) == 0 {
		return ""
	}
	// String form.
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return s
	}
	// Array form.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// capRunes returns s truncated to at most n runes, with an ellipsis when
// truncated. Avoids splitting a multi-byte rune mid-sequence.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		// Strip newlines/tabs that would break a one-line table.
		return strings.Map(sanitizeRune, s)
	}
	return strings.Map(sanitizeRune, string(r[:n])) + "..."
}

func sanitizeRune(r rune) rune {
	if r == '\n' || r == '\r' || r == '\t' {
		return ' '
	}
	return r
}

// printSessionsHuman emits a tabular human-readable view. We keep it
// simple (text/tabwriter would be nicer but adds a dep, and the existing
// commands all use plain Fprintf).
func printSessionsHuman(out io.Writer, r SessionsReport) error {
	fmt.Fprintf(out, "Project: %s\n", r.ProjectDir)
	if len(r.Sessions) == 0 {
		fmt.Fprintln(out, "(no sessions yet)")
		return nil
	}
	fmt.Fprintf(out, "%-36s  %-7s  %-5s  %-19s  %10s  %s\n",
		"SESSION ID", "WHERE", "LIVE", "MODIFIED", "SIZE", "FIRST USER MESSAGE")
	for _, s := range r.Sessions {
		live := ""
		if s.Location == "remote" {
			if s.LiveOnRemote {
				live = "yes"
			} else {
				live = "no"
			}
		} else {
			live = "-"
		}
		fmt.Fprintf(out, "%-36s  %-7s  %-5s  %-19s  %10d  %s\n",
			s.SessionID,
			s.Location,
			live,
			s.MTime.Format("2006-01-02 15:04:05"),
			s.SizeBytes,
			s.FirstUserText,
		)
	}
	return nil
}
