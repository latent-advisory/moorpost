// RemoteManifest computes the same content-stable hash as LocalManifest,
// but for a directory on a remote VM via SSH. The bash pipeline produces
// byte-identical output to LocalManifest's format:
//
//   {relpath}\t{size}\t{mtime_seconds}\n   (one line per file or symlink)
//
// joined and SHA-256-hashed. Both produce a 64-char hex string.
//
// Caveat: the remote shell pipeline assumes GNU find (`-printf '%T@'`),
// which Ubuntu has by default. macOS BSD find lacks this — but the
// remote in v1 is always Ubuntu, so this is safe.

package session

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	mpexec "github.com/latent-advisory/moorpost/cli/internal/exec"
)

// SSHBinary is the ssh executable name. Var so tests can override.
var SSHBinary = "ssh"

// RemoteManifest runs the manifest pipeline over SSH and returns the
// resulting hex hash. A missing remote dir is treated as an empty
// manifest (returns the SHA-256 of "" — same as LocalManifest's fallback).
func RemoteManifest(ctx context.Context, exec mpexec.Executor, sshHost, remoteRoot string) (string, error) {
	if exec == nil {
		return "", errors.New("manifest: exec is nil")
	}
	if sshHost == "" {
		return "", errors.New("manifest: sshHost is required")
	}
	if remoteRoot == "" {
		return "", errors.New("manifest: remoteRoot is required")
	}

	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		sshHost,
		"bash", "-s",
	}
	stdin := buildRemoteScript(remoteRoot)

	stdout, stderr, code, err := exec.Run(ctx, SSHBinary, args, []byte(stdin))
	if err != nil {
		return "", fmt.Errorf("manifest: ssh exec: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("manifest: ssh exit %d: %s", code, strings.TrimSpace(string(stderr)))
	}

	hash := strings.TrimSpace(string(stdout))
	if !hexHashRE.MatchString(hash) {
		return "", fmt.Errorf("manifest: unparseable remote output: %q", hash)
	}
	return hash, nil
}

// hexHashRE matches a 64-character SHA-256 hex hash.
var hexHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// buildRemoteScript returns the bash script piped via stdin to bash -s on
// the remote. It produces the manifest hash for remoteRoot to stdout.
//
// The grouped command + `[ -d "$DIR" ]` guard means a missing remote dir
// produces no manifest input → sha256sum of empty stream → the same hash
// as LocalManifest's hash-of-empty fallback.
//
// %P is the path relative to the find root (no leading "./"). %T@ is
// "epoch_seconds.fractional"; awk's split + a[1] keeps just the integer
// seconds portion to match LocalManifest's UnixSecond format.
func buildRemoteScript(remoteRoot string) string {
	return fmt.Sprintf(`set +e
DIR=%s
{ [ -d "$DIR" ] && cd "$DIR" && find . \( -type f -o -type l \) -printf '%%P\t%%s\t%%T@\n' 2>/dev/null | awk -F'\t' '{split($3, a, "."); print $1"\t"$2"\t"a[1]}' | sort; } | sha256sum | cut -d' ' -f1
`, shellQuote(remoteRoot))
}

// shellQuote produces a single-quoted bash string, escaping any embedded
// single quotes via the standard '\'' trick.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
