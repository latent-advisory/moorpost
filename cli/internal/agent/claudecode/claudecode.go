// Package claudecode implements the Agent interface for Anthropic's Claude
// Code CLI.
//
// It registers itself with the agent package via init() and is the v1
// default agent.
package claudecode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
	"github.com/latent-advisory/moorpost/cli/internal/keychain"
	mpssh "github.com/latent-advisory/moorpost/cli/internal/ssh"
	"github.com/latent-advisory/moorpost/cli/internal/tmux"
)

const (
	// AgentID is the registry identifier.
	AgentID = "claude-code"

	// CredentialEnvVar is the env var Claude Code reads for its OAuth token.
	CredentialEnvVar = "CLAUDE_CODE_OAUTH_TOKEN"

	// KeychainService is the keychain service name for the cached token.
	KeychainService = "moorpost.claude-code.token"

	// KeychainAccount is the keychain account name (single per machine for v1).
	KeychainAccount = "default"

	// DefaultRemoteEnvPath is the file the systemd unit / tmux env reads on
	// the remote VM. Mode 0600.
	DefaultRemoteEnvPath = "/etc/moorpost/env"

	// PauseDefaultTimeout is the default timeout if the caller doesn't set one
	// on the context. Pause polls until the pane shows the ready prompt.
	PauseDefaultTimeout = 30 * time.Second

	// PausePollInterval is how often Pause polls the remote pane.
	PausePollInterval = 200 * time.Millisecond
)

// Claude Code's session-state directory layout: ~/.claude/projects/<encoded>
// where <encoded> is the absolute project path with every non-[a-zA-Z0-9-]
// character replaced by '-'. Verified empirically (2026-05-04) by inspecting
// ~/.claude/projects/ on a populated machine; rule confirmed against several
// real-world paths including those with spaces, ampersands, dots, and slashes.
var nonSlugRE = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// EncodeSessionPath returns Claude Code's encoded directory name for a given
// absolute path. Returns the empty string if abs is empty.
func EncodeSessionPath(abs string) string {
	if abs == "" {
		return ""
	}
	return nonSlugRE.ReplaceAllString(abs, "-")
}

// Options controls behaviour of New.
type Options struct {
	// Executor wraps os/exec; defaults to mpexec.New() if nil.
	Executor mpexec.Executor

	// Keychain stores cached OAuth tokens; defaults to keychain.New() result
	// if nil. Constructor returns an error if the default cannot be created.
	Keychain keychain.Keychain

	// SSHRunner runs commands on remote hosts; defaults to ssh.NewRunner over
	// Executor if nil.
	SSHRunner mpssh.Runner

	// HomeDir overrides the user's home directory (used by SessionStatePath).
	// If empty, defaults to os.UserHomeDir().
	HomeDir string

	// RemoteEnvPath overrides DefaultRemoteEnvPath for InjectCredential.
	RemoteEnvPath string

	// TmuxFactory constructs a Tmux for a given remote host. Lets tests
	// inject a fake. Defaults to tmux.NewRemote over SSHRunner.
	TmuxFactory func(host, identityFile string) tmux.Tmux

	// ReadyPromptRegex is what Pause looks for to consider the agent idle.
	// Defaults to readyPromptDefault.
	ReadyPromptRegex *regexp.Regexp

	// PausePollInterval overrides the default 200ms poll cadence.
	PausePollInterval time.Duration
}

// claudeCode is the concrete Agent.
type claudeCode struct {
	exec          mpexec.Executor
	kc            keychain.Keychain
	ssh           mpssh.Runner
	home          string
	remoteEnvPath string
	tmuxFactory   func(host, identityFile string) tmux.Tmux
	readyRE       *regexp.Regexp
	pauseInterval time.Duration
}

// New constructs a Claude Code agent. config is currently unused; reserved
// for future per-agent options (model preference, alt token paths, etc.).
func New(config map[string]any) (agent.Agent, error) {
	return NewWithOptions(Options{})
}

// NewWithOptions allows tests to inject fakes.
func NewWithOptions(opts Options) (agent.Agent, error) {
	c := &claudeCode{
		exec:          opts.Executor,
		kc:            opts.Keychain,
		ssh:           opts.SSHRunner,
		home:          opts.HomeDir,
		remoteEnvPath: opts.RemoteEnvPath,
		tmuxFactory:   opts.TmuxFactory,
		readyRE:       opts.ReadyPromptRegex,
		pauseInterval: opts.PausePollInterval,
	}
	if c.exec == nil {
		c.exec = mpexec.New()
	}
	if c.kc == nil {
		kc, err := keychain.New()
		if err != nil {
			return nil, fmt.Errorf("claudecode: init keychain: %w", err)
		}
		c.kc = kc
	}
	if c.ssh == nil {
		c.ssh = mpssh.NewRunner(c.exec)
	}
	if c.home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("claudecode: resolve home: %w", err)
		}
		c.home = h
	}
	if c.remoteEnvPath == "" {
		c.remoteEnvPath = DefaultRemoteEnvPath
	}
	if c.tmuxFactory == nil {
		c.tmuxFactory = func(host, identityFile string) tmux.Tmux {
			return tmux.NewRemote(c.ssh.WithIdentity(identityFile), host)
		}
	}
	if c.readyRE == nil {
		c.readyRE = readyPromptDefault
	}
	if c.pauseInterval == 0 {
		c.pauseInterval = PausePollInterval
	}
	return c, nil
}

// readyPromptDefault matches Claude Code's idle/ready states. The CLI shows
// either a `>` prompt awaiting input or "Press Esc to interrupt" lines while
// running. We treat the absence of "Esc to interrupt" as ready, but also
// look explicitly for the trailing `>` to be more robust to upstream UI
// changes — match either.
var readyPromptDefault = regexp.MustCompile(`(?m)(^\s*>\s*$|^\s*>\s+\S|Welcome to Claude Code|Type your message)`)

func init() {
	agent.Register(AgentID, New)
}

func (c *claudeCode) ID() string { return AgentID }

// InstallScript returns a shell snippet that installs Claude Code on the
// remote VM. Idempotent: re-running once installed is a no-op.
//
// The bootstrap relies on Node 20 already being present (installed by the
// VM bootstrap script), so this is just `npm install -g`.
func (c *claudeCode) InstallScript(os agent.OSFamily) string {
	switch os {
	case agent.OSUbuntu, agent.OSDebian:
		return ubuntuDebianInstall
	case agent.OSAmazonLinux:
		return amazonLinuxInstall
	default:
		// Unknown OS: emit a script that fails fast with a clear message.
		return `echo "claudecode: unsupported OS family for InstallScript" >&2; exit 1`
	}
}

const ubuntuDebianInstall = `set -euo pipefail
if command -v claude >/dev/null 2>&1; then
  echo "claude already installed: $(claude --version)"
  exit 0
fi
if ! command -v npm >/dev/null 2>&1; then
  echo "claudecode install: npm not found; bootstrap should install Node 20 first" >&2
  exit 1
fi
sudo npm install -g @anthropic-ai/claude-code
claude --version
`

const amazonLinuxInstall = ubuntuDebianInstall // identical for now

// SessionStatePath returns the local-disk directory where Claude Code keeps
// per-project session state (conversation logs, cached tool calls, etc).
//
// Format: <home>/.claude/projects/<encoded-abs-path>
func (c *claudeCode) SessionStatePath(projectAbsDir string) string {
	if projectAbsDir == "" {
		return ""
	}
	return filepath.Join(c.home, ".claude", "projects", EncodeSessionPath(projectAbsDir))
}

// LoadCachedCredential returns the cached credential without triggering an
// OAuth flow. Returns ErrNotAuthenticated when no token is cached. Use this
// in passive code paths (handoff preflight, status reports) where the user
// hasn't asked for an interactive auth flow.
func (c *claudeCode) LoadCachedCredential() (agent.Credential, error) {
	existing, err := c.kc.Retrieve(KeychainService, KeychainAccount)
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			return agent.Credential{}, agent.ErrNotAuthenticated
		}
		return agent.Credential{}, fmt.Errorf("claudecode: keychain retrieve: %w", err)
	}
	return agent.Credential{
		EnvVar: CredentialEnvVar,
		Value:  string(existing),
		Kind:   "oauth-subscription",
	}, nil
}

// AuthenticateLocal runs `claude setup-token` on the local machine, parses
// the printed long-lived OAuth token, and stashes it in the keychain. If a
// token is already cached, returns it without re-running setup-token.
//
// If ANTHROPIC_API_KEY is set in the environment, prefer that (no OAuth
// browser flow, no parsing) — useful for CI or when the OAuth-token
// regex fails on a newer Claude Code release.
func (c *claudeCode) AuthenticateLocal(ctx context.Context) (agent.Credential, error) {
	// Cache hit?
	if cred, err := c.LoadCachedCredential(); err == nil {
		return cred, nil
	} else if !errors.Is(err, agent.ErrNotAuthenticated) {
		return agent.Credential{}, err
	}

	// Env-var bypass: if user set ANTHROPIC_API_KEY, store it directly.
	if envToken := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); envToken != "" {
		if err := c.kc.Store(KeychainService, KeychainAccount, []byte(envToken)); err != nil {
			return agent.Credential{}, fmt.Errorf("claudecode: keychain store: %w", err)
		}
		return agent.Credential{
			EnvVar: CredentialEnvVar,
			Value:  envToken,
			Kind:   "api-key",
		}, nil
	}

	if _, err := c.exec.LookPath("claude"); err != nil {
		return agent.Credential{}, fmt.Errorf("%w: claude CLI not found on PATH (install with `npm install -g @anthropic-ai/claude-code`)", agent.ErrAgentNotInstalled)
	}

	stdout, stderr, code, err := c.exec.Run(ctx, "claude", []string{"setup-token"}, nil)
	if err != nil {
		return agent.Credential{}, fmt.Errorf("claudecode: run setup-token: %w", err)
	}
	if code != 0 {
		return agent.Credential{}, fmt.Errorf("claudecode: setup-token exited %d: %s", code, strings.TrimSpace(string(stderr)))
	}
	token, err := parseOAuthToken(string(stdout))
	if err != nil {
		return agent.Credential{}, fmt.Errorf("%w (the OAuth completed in the browser but the token wasn't visible to moorpost — your Claude Code may store it in its own keychain entry; workaround: re-run `moorpost auth --token <paste>` or set ANTHROPIC_API_KEY and re-run)", err)
	}
	if err := c.kc.Store(KeychainService, KeychainAccount, []byte(token)); err != nil {
		return agent.Credential{}, fmt.Errorf("claudecode: keychain store: %w", err)
	}
	return agent.Credential{
		EnvVar: CredentialEnvVar,
		Value:  token,
		Kind:   "oauth-subscription",
	}, nil
}

// tokenRE matches the long-lived OAuth token (sk-ant-oat01-*) or the API
// key format (sk-ant-api03-*) that `claude setup-token` may print. Kept
// permissive on the prefix segment so future Claude Code releases that
// change the sub-prefix don't silently fail our parser.
var tokenRE = regexp.MustCompile(`(sk-ant-[a-z0-9]+-[A-Za-z0-9_\-]+)`)

func parseOAuthToken(out string) (string, error) {
	m := tokenRE.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("claudecode: no sk-ant-oat01-* token found in `claude setup-token` output (got %d bytes)", len(out))
	}
	return strings.TrimSpace(m[1]), nil
}

// sshHostFor renders an SSHTarget into the user@host string the ssh.Runner
// expects. Without the User prefix, ssh defaults to the local OS user —
// which fails on GCE VMs whose SSH key is bound to a specific provisioned
// user (e.g. "moorpost"). Falls back to bare host when no user is set, to
// preserve callers that intentionally rely on the local user.
func sshHostFor(target agent.SSHTarget) string {
	if target.User == "" {
		return target.Host
	}
	return target.User + "@" + target.Host
}

// InjectCredential writes the credential to the remote VM's env file
// (mode 0600). The remote bootstrap script's systemd unit and tmux session
// load this file at agent startup.
func (c *claudeCode) InjectCredential(ctx context.Context, target agent.SSHTarget, cred agent.Credential) error {
	if target.Host == "" {
		return errors.New("claudecode.InjectCredential: requires a non-empty SSHTarget.Host")
	}
	if cred.Value == "" {
		return errors.New("claudecode.InjectCredential: refusing to inject empty credential")
	}
	if strings.ContainsAny(cred.Value, "'\n\r") {
		return errors.New("claudecode.InjectCredential: credential contains forbidden chars (single quote, newline)")
	}
	envVar := cred.EnvVar
	if envVar == "" {
		envVar = CredentialEnvVar
	}
	// Single-quoted shell value so any other special chars are inert.
	content := []byte(envVar + "='" + cred.Value + "'\n")
	runner := c.ssh.WithIdentity(target.IdentityFile)
	if err := runner.WriteRemoteFile(ctx, sshHostFor(target), c.remoteEnvPath, content, 0o600); err != nil {
		return fmt.Errorf("claudecode.InjectCredential: %w", err)
	}
	return nil
}

// Resume starts (or attaches to) a tmux session running `claude --resume <id>`
// on target. If the session already exists, no-op (caller's contract is
// "make sure claude is running there"; if it already is, we're done).
//
// If ref.SessionID is empty, runs `claude` (a fresh session); otherwise
// `claude --resume <id>`. Token is sourced from the keychain and injected
// as an env var on the new tmux session.
func (c *claudeCode) Resume(ctx context.Context, target agent.SSHTarget, ref agent.SessionRef) error {
	if target.Host == "" {
		return errors.New("claudecode.Resume: requires a non-empty SSHTarget.Host")
	}
	if ref.ProjectSlug == "" {
		return errors.New("claudecode.Resume: requires a non-empty ProjectSlug")
	}
	tx := c.tmuxFactory(sshHostFor(target), target.IdentityFile)
	exists, err := tx.HasSession(ctx, ref.ProjectSlug)
	if err != nil {
		return fmt.Errorf("claudecode.Resume: %w", err)
	}
	if exists {
		return nil
	}
	// Launch claude with cwd set to the LOCAL absolute path
	// (`/Users/.../<project>`), which on the VM resolves through the
	// bootstrap-created symlink to the actual project tree at
	// `/home/moorpost/moorpost/<slug>`. claude encodes its session-state
	// directory by literal cwd (`~/.claude/projects/<encoded-path>/`),
	// not by realpath — so launching from /Users/.../<project> keeps
	// the encoding aligned with the local side, where session JSONLs
	// are stored as `~/.claude/projects/-Users-...-<project>/<sid>.jsonl`.
	//
	// Without this `cd`, tmux inherits the SSH session's $HOME as cwd
	// (`/home/moorpost`) and any new-session data gets written to a
	// second encoding dir (e.g. `-home-moorpost-moorpost-<slug>`),
	// which `moorpost return` then doesn't sync back — silently losing
	// any messages the user typed on the remote side.
	claudeCmd := "claude"
	if ref.SessionID != "" {
		claudeCmd = "claude --resume " + ref.SessionID
	}
	cmd := claudeCmd
	if ref.ProjectAbsDir != "" {
		cmd = "cd " + shellSingleQuote(ref.ProjectAbsDir) + " && " + claudeCmd
	}
	// Pull the cached token if available; the remote agent will *also* read
	// from /etc/moorpost/env (set by InjectCredential), so passing the env
	// here is a belt-and-braces optimization for environments that haven't
	// installed the env file yet.
	env := map[string]string{}
	if tok, err := c.kc.Retrieve(KeychainService, KeychainAccount); err == nil {
		env[CredentialEnvVar] = string(tok)
	}
	if err := tx.NewSession(ctx, ref.ProjectSlug, cmd, env); err != nil {
		return fmt.Errorf("claudecode.Resume: %w", err)
	}
	return nil
}

// IsActive reports whether a tmux session for the project exists on target.
// A more rigorous check could capture the pane and verify Claude's UI is
// up; v0.1 keeps it simple.
func (c *claudeCode) IsActive(ctx context.Context, target agent.SSHTarget, ref agent.SessionRef) (bool, error) {
	if target.Host == "" {
		return false, errors.New("claudecode.IsActive: requires a non-empty SSHTarget.Host")
	}
	if ref.ProjectSlug == "" {
		return false, errors.New("claudecode.IsActive: requires a non-empty ProjectSlug")
	}
	tx := c.tmuxFactory(sshHostFor(target), target.IdentityFile)
	return tx.HasSession(ctx, ref.ProjectSlug)
}

// Pause polls the remote pane until it shows the ready prompt, indicating
// Claude has finished its current turn and is waiting for input. Honors
// context cancellation; uses PauseDefaultTimeout if no deadline is set.
//
// No-op if no session exists (already paused).
func (c *claudeCode) Pause(ctx context.Context, target agent.SSHTarget, ref agent.SessionRef) error {
	if target.Host == "" {
		return errors.New("claudecode.Pause: requires a non-empty SSHTarget.Host")
	}
	if ref.ProjectSlug == "" {
		return errors.New("claudecode.Pause: requires a non-empty ProjectSlug")
	}
	tx := c.tmuxFactory(sshHostFor(target), target.IdentityFile)
	exists, err := tx.HasSession(ctx, ref.ProjectSlug)
	if err != nil {
		return fmt.Errorf("claudecode.Pause: %w", err)
	}
	if !exists {
		return nil
	}

	// Apply default timeout if caller hasn't set one.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, PauseDefaultTimeout)
		defer cancel()
	}

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("claudecode.Pause: %w", err)
		}
		buf, err := tx.CapturePane(ctx, ref.ProjectSlug, 50)
		if err != nil {
			return fmt.Errorf("claudecode.Pause: %w", err)
		}
		if c.readyRE.MatchString(buf) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("claudecode.Pause: %w", ctx.Err())
		case <-time.After(c.pauseInterval):
		}
	}
}

// shellSingleQuote wraps s in single quotes for safe inclusion in a
// POSIX-shell command line. Embedded single quotes are escaped via the
// classic `'\''` close-reopen idiom. Used so paths with spaces or shell
// metacharacters (e.g. `&`) survive the trip through tmux → ssh → remote
// shell.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// silence unused-import warnings on some Go versions
var _ = exec.ErrNotFound
var _ = filepath.Join
