// Package ssh provides Moorpost's SSH machinery:
//   - A managed-block writer for ~/.ssh/config
//   - A Runner that executes commands on remote hosts via the OS `ssh` binary
//
// We deliberately use the system ssh client rather than golang.org/x/crypto/ssh
// so the user's existing agent, keys, ProxyJump rules, etc. continue to work
// without re-implementation.
package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HostEntry captures the SSH config directives Moorpost manages for one host.
// Empty fields are omitted from the rendered block. Non-Moorpost-relevant
// directives (e.g. ProxyJump) belong outside the managed block.
type HostEntry struct {
	HostName             string // host or IP
	User                 string
	Port                 int    // emitted only if non-zero
	IdentityFile         string
	ServerAliveInterval  int    // seconds; 0 = omit
	ServerAliveCountMax  int    // 0 = omit
	ControlMaster        string // e.g. "auto"
	ControlPath          string // e.g. "~/.moorpost/cm/%C"
	ControlPersist       string // e.g. "10m"
	StrictHostKeyChecking string // e.g. "accept-new"
	UserKnownHostsFile   string
}

// Render emits the SSH config directives for this entry, indented under the
// `Host <name>` line. Returns an empty string if the entry has no fields set.
func (e HostEntry) Render() string {
	var b strings.Builder
	if e.HostName != "" {
		fmt.Fprintf(&b, "  HostName %s\n", e.HostName)
	}
	if e.User != "" {
		fmt.Fprintf(&b, "  User %s\n", e.User)
	}
	if e.Port != 0 {
		fmt.Fprintf(&b, "  Port %d\n", e.Port)
	}
	if e.IdentityFile != "" {
		fmt.Fprintf(&b, "  IdentityFile %s\n", e.IdentityFile)
	}
	if e.ServerAliveInterval > 0 {
		fmt.Fprintf(&b, "  ServerAliveInterval %d\n", e.ServerAliveInterval)
	}
	if e.ServerAliveCountMax > 0 {
		fmt.Fprintf(&b, "  ServerAliveCountMax %d\n", e.ServerAliveCountMax)
	}
	if e.ControlMaster != "" {
		fmt.Fprintf(&b, "  ControlMaster %s\n", e.ControlMaster)
	}
	if e.ControlPath != "" {
		fmt.Fprintf(&b, "  ControlPath %s\n", e.ControlPath)
	}
	if e.ControlPersist != "" {
		fmt.Fprintf(&b, "  ControlPersist %s\n", e.ControlPersist)
	}
	if e.StrictHostKeyChecking != "" {
		fmt.Fprintf(&b, "  StrictHostKeyChecking %s\n", e.StrictHostKeyChecking)
	}
	if e.UserKnownHostsFile != "" {
		fmt.Fprintf(&b, "  UserKnownHostsFile %s\n", e.UserKnownHostsFile)
	}
	return b.String()
}

// Manager edits a single ~/.ssh/config-style file, preserving user content
// outside the Moorpost-managed blocks.
type Manager struct {
	Path string
}

// NewManager returns a Manager rooted at path. The file does not need to
// exist yet; Upsert will create it (mode 0600) on first write.
func NewManager(path string) *Manager { return &Manager{Path: path} }

// blockMarkers returns the begin/end marker lines for a host. The host name
// appears in both markers so multiple Moorpost blocks can coexist.
func blockMarkers(host string) (begin, end string) {
	return fmt.Sprintf("# >>> moorpost begin: %s >>>", host),
		fmt.Sprintf("# <<< moorpost end: %s <<<", host)
}

// renderBlock returns the full text of a managed block, terminating in a
// newline. The block looks like:
//
//	# >>> moorpost begin: <host> >>>
//	Host <host>
//	  HostName ...
//	  User ...
//	# <<< moorpost end: <host> <<<
func renderBlock(host string, e HostEntry) string {
	begin, end := blockMarkers(host)
	var b strings.Builder
	b.WriteString(begin)
	b.WriteString("\n")
	fmt.Fprintf(&b, "Host %s\n", host)
	b.WriteString(e.Render())
	b.WriteString(end)
	b.WriteString("\n")
	return b.String()
}

// Upsert inserts or replaces the managed block for host with entry.
// The non-Moorpost portion of the file is preserved byte-for-byte.
//
// Idempotent: upserting an unchanged entry produces an unchanged file.
func (m *Manager) Upsert(host string, entry HostEntry) error {
	if host == "" {
		return fmt.Errorf("ssh: Upsert requires a non-empty host")
	}
	contents, mode, err := readOrEmpty(m.Path)
	if err != nil {
		return err
	}
	updated, err := upsertBlock(contents, host, entry)
	if err != nil {
		return err
	}
	return writeAtomic(m.Path, updated, mode)
}

// Remove drops the managed block for host. Returns nil if the block is absent
// (idempotent). Other content is preserved.
func (m *Manager) Remove(host string) error {
	if host == "" {
		return fmt.Errorf("ssh: Remove requires a non-empty host")
	}
	contents, mode, err := readOrEmpty(m.Path)
	if err != nil {
		return err
	}
	updated, removed := removeBlock(contents, host)
	if !removed {
		return nil
	}
	return writeAtomic(m.Path, updated, mode)
}

// Has reports whether a Moorpost block for host currently exists.
func (m *Manager) Has(host string) (bool, error) {
	contents, _, err := readOrEmpty(m.Path)
	if err != nil {
		return false, err
	}
	begin, _ := blockMarkers(host)
	return strings.Contains(contents, begin), nil
}

// readOrEmpty returns ("", 0o600, nil) if the path doesn't exist, or the
// file's contents and mode otherwise. Other read errors propagate.
func readOrEmpty(path string) (string, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", 0o600, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("ssh: read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("ssh: stat %s: %w", path, err)
	}
	return string(data), info.Mode().Perm(), nil
}

// writeAtomic writes content to path with the given mode using a tmp+rename.
func writeAtomic(path, content string, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ssh: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".moorpost-ssh-config-*.tmp")
	if err != nil {
		return fmt.Errorf("ssh: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("ssh: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("ssh: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("ssh: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("ssh: chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("ssh: rename: %w", err)
	}
	return nil
}

// upsertBlock returns the contents with the host's block inserted or replaced.
func upsertBlock(contents, host string, entry HostEntry) (string, error) {
	begin, end := blockMarkers(host)
	beginIdx := strings.Index(contents, begin)
	if beginIdx < 0 {
		// No existing block — append.
		newBlock := renderBlock(host, entry)
		if contents == "" {
			return newBlock, nil
		}
		// Ensure exactly one blank line between existing content and our block.
		trimmed := strings.TrimRight(contents, "\n")
		return trimmed + "\n\n" + newBlock, nil
	}
	endIdx := strings.Index(contents[beginIdx:], end)
	if endIdx < 0 {
		return "", fmt.Errorf("ssh: malformed config — found begin marker but not end for host %q", host)
	}
	endIdx += beginIdx + len(end)
	// Include the trailing newline if present.
	if endIdx < len(contents) && contents[endIdx] == '\n' {
		endIdx++
	}
	newBlock := renderBlock(host, entry)
	return contents[:beginIdx] + newBlock + contents[endIdx:], nil
}

// removeBlock returns (newContents, true) if a block was removed.
func removeBlock(contents, host string) (string, bool) {
	begin, end := blockMarkers(host)
	beginIdx := strings.Index(contents, begin)
	if beginIdx < 0 {
		return contents, false
	}
	endIdx := strings.Index(contents[beginIdx:], end)
	if endIdx < 0 {
		return contents, false
	}
	endIdx += beginIdx + len(end)
	// Consume trailing newline.
	if endIdx < len(contents) && contents[endIdx] == '\n' {
		endIdx++
	}
	// Also collapse the blank line we may have inserted before the block, but
	// only if the previous char(s) are blank lines.
	// Walk back from beginIdx through any '\n' chars.
	leadStart := beginIdx
	for leadStart > 0 && contents[leadStart-1] == '\n' {
		leadStart--
	}
	// Keep at most one trailing newline of the leading content.
	if leadStart < beginIdx {
		// Restore exactly one '\n' between the preceding content and what
		// follows, unless the preceding content is empty.
		if leadStart == 0 {
			// Block was at top of file; just remove the leading whitespace.
			return contents[endIdx:], true
		}
		return contents[:leadStart] + "\n" + contents[endIdx:], true
	}
	return contents[:beginIdx] + contents[endIdx:], true
}
