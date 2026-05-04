// Manifest computes a content-stable hash of a session-state directory
// suitable for detecting "did anything change since the last sync."
//
// The hash covers every regular file and symlink under root, identified by
// the tuple {relpath, size, mtime_ns}. We deliberately don't read file
// contents — agent session-state directories can be large and a stat-based
// hash is fast + cheap. mtime_ns gives us nanosecond granularity (where
// the filesystem supports it) so adjacent edits within the same second
// still produce different hashes.
//
// Used by `moorpost handoff` / `moorpost return` to detect the case the
// PLUGIN.md spec (line 261) calls fatal: both local and remote have
// modified session state since the last sync.

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LocalManifest returns a SHA-256 hex hash of the directory's content.
// Two invocations with the same logical state yield the same hash.
//
// A missing root is treated as "no session state yet" — returns the hash
// of an empty manifest (deterministic) and a nil error. This is the
// correct behavior for a freshly-provisioned project that has never
// invoked claude.
//
// Walks symlinks via Lstat (does not follow), so a symlink contributes
// its own mtime to the hash, not the target's. Robust against the iter 14
// absolute-path-symlink trick where ~/.claude/projects/<encoded> may be
// a symlink on the remote side.
func LocalManifest(root string) (string, error) {
	if root == "" {
		return hashEmpty(), nil
	}
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return hashEmpty(), nil
	}
	if err != nil {
		return "", fmt.Errorf("manifest: lstat %s: %w", root, err)
	}
	// Allow root to be a symlink to a directory; in that case we walk the
	// target. (filepath.Walk follows the root if it's a symlink, but does
	// not follow nested symlinks.)
	if info.Mode()&os.ModeSymlink != 0 {
		// Resolve once to walk the target's contents.
		target, err := os.Readlink(root)
		if err != nil {
			return "", fmt.Errorf("manifest: readlink %s: %w", root, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(root), target)
		}
		root = target
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return hashEmpty(), nil
	}

	var lines []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Skip directories themselves — only files + symlinks contribute.
		// (Empty dirs are invisible; this matches mutagen's "only files
		// matter for a sync" model.)
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			// Stale dirent (e.g., file just deleted between WalkDir and
			// Info). Skip it — we'll catch it on the next manifest run.
			return nil
		}
		size := fi.Size()
		mtimeNs := fi.ModTime().UnixNano()
		lines = append(lines, fmt.Sprintf("%s\t%d\t%d", rel, size, mtimeNs))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("manifest: walk %s: %w", root, err)
	}

	sort.Strings(lines)
	h := sha256.New()
	h.Write([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashEmpty is the manifest of an empty directory. Cached at init so
// callers can compare against it without re-hashing.
func hashEmpty() string {
	h := sha256.New()
	return hex.EncodeToString(h.Sum(nil))
}
