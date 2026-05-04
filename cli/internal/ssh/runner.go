package ssh

import (
	"context"
	"fmt"
	"strings"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
)

// Runner executes commands on remote hosts via the local `ssh` client.
type Runner interface {
	// Run executes cmd on host and returns its outputs and exit code.
	// A non-zero exit code is NOT an error; the caller decides.
	Run(ctx context.Context, host, cmd string) (stdout, stderr []byte, exitCode int, err error)

	// RunWithStdin pipes stdin to the remote command.
	RunWithStdin(ctx context.Context, host, cmd string, stdin []byte) (stdout, stderr []byte, exitCode int, err error)

	// WriteRemoteFile writes content to remotePath on host with the given
	// numeric mode (e.g. 0o600). Parent directory is created if missing.
	// Atomic via tmp+rename on the remote.
	WriteRemoteFile(ctx context.Context, host, remotePath string, content []byte, mode int) error
}

// runner is the concrete implementation, parameterized by an Executor so
// tests can drive it without touching the network.
type runner struct {
	exec mpexec.Executor

	// SSHBinary, if non-empty, overrides the default "ssh" executable.
	SSHBinary string

	// ExtraArgs are inserted before the host argument on every invocation.
	// Useful for injecting `-F /path/to/config` in tests.
	ExtraArgs []string
}

// NewRunner returns a Runner backed by exec. Pass mpexec.New() in production
// or mpexec.NewFake() in tests.
func NewRunner(exec mpexec.Executor) Runner {
	return &runner{exec: exec}
}

// NewRunnerWithOptions allows tests to override the ssh binary path and/or
// inject extra args (e.g. -F /tmp/test-config) without touching the user's
// real ~/.ssh/config.
func NewRunnerWithOptions(exec mpexec.Executor, sshBin string, extraArgs []string) Runner {
	return &runner{exec: exec, SSHBinary: sshBin, ExtraArgs: extraArgs}
}

func (r *runner) sshBinary() string {
	if r.SSHBinary != "" {
		return r.SSHBinary
	}
	return "ssh"
}

// baseArgs assembles the boilerplate args used for every Run call. We force
// BatchMode=yes so SSH never prompts; if the user's keys/agent aren't set up,
// the call fails fast with a clear error rather than hanging forever.
func (r *runner) baseArgs(host string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
	}
	args = append(args, r.ExtraArgs...)
	args = append(args, host)
	return args
}

func (r *runner) Run(ctx context.Context, host, cmd string) ([]byte, []byte, int, error) {
	return r.RunWithStdin(ctx, host, cmd, nil)
}

func (r *runner) RunWithStdin(ctx context.Context, host, cmd string, stdin []byte) ([]byte, []byte, int, error) {
	if host == "" {
		return nil, nil, 0, fmt.Errorf("ssh: Run requires a non-empty host")
	}
	if cmd == "" {
		return nil, nil, 0, fmt.Errorf("ssh: Run requires a non-empty command")
	}
	args := append(r.baseArgs(host), "--", cmd)
	stdout, stderr, code, err := r.exec.Run(ctx, r.sshBinary(), args, stdin)
	if err != nil {
		return stdout, stderr, code, fmt.Errorf("ssh run %q: %w", host, err)
	}
	return stdout, stderr, code, nil
}

// WriteRemoteFile uploads content to remotePath atomically, with the given
// mode. Implemented as a single ssh invocation that creates the parent
// directory, writes a tmp file from stdin, chmods it, and renames into place.
//
// Quoting: the remote command is fixed shell text with the file path embedded
// as a single-quoted literal. We refuse paths containing single quotes since
// that's the only character the shell escape can't easily handle here.
func (r *runner) WriteRemoteFile(ctx context.Context, host, remotePath string, content []byte, mode int) error {
	if host == "" || remotePath == "" {
		return fmt.Errorf("ssh: WriteRemoteFile requires non-empty host and path")
	}
	if strings.Contains(remotePath, "'") {
		return fmt.Errorf("ssh: WriteRemoteFile path contains single quote, refusing for safety: %q", remotePath)
	}
	if mode <= 0 || mode > 0o777 {
		return fmt.Errorf("ssh: WriteRemoteFile mode %o out of range", mode)
	}
	// Build the remote shell snippet. Use bash -c to keep it portable across
	// distros; mktemp is widely available. The chmod runs on the temp before
	// the rename so the visible file appears with correct mode atomically.
	remoteCmd := fmt.Sprintf(
		`bash -c 'set -e; p=%q; mkdir -p "$(dirname "$p")"; t="$(mktemp "${p}.XXXXXX")"; cat > "$t"; chmod %o "$t"; mv "$t" "$p"'`,
		remotePath, mode,
	)
	_, stderr, code, err := r.RunWithStdin(ctx, host, remoteCmd, content)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("ssh: WriteRemoteFile %s:%s exited %d: %s", host, remotePath, code, strings.TrimSpace(string(stderr)))
	}
	return nil
}
