// Package audit logs CLI invocations to ~/.moorpost/logs/<date>.jsonl
// (one JSON object per line) and reads them back for `moorpost audit`.
//
// Per PLUGIN.md §10 #13: local CLI logs, rotated daily by filename,
// 30-day retention (sweep deferred to a follow-up iteration).
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Entry is one logged CLI invocation.
type Entry struct {
	Timestamp  time.Time `json:"timestamp"`
	Command    string    `json:"command"`
	Args       []string  `json:"args"`
	ExitCode   int       `json:"exit_code"`
	DurationMS int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

// Logger appends Entries to a daily log file. It's safe for sequential use
// from one moorpost invocation; multiple concurrent moorpost processes use
// O_APPEND so writes don't interleave at the byte level.
type Logger struct {
	// Dir is the logs directory (e.g. ~/.moorpost/logs/). Created on demand.
	Dir string

	// RetentionDays is the maximum age of log files to keep. 0 disables the
	// sweep. Sweep runs as a side effect of each Append.
	RetentionDays int

	// Now overrides time.Now for deterministic tests.
	Now func() time.Time
}

// NewLogger returns a Logger rooted at dir.
func NewLogger(dir string) *Logger { return &Logger{Dir: dir} }

func (l *Logger) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Append serializes e to JSONL and appends to today's log file. Creates
// the directory and file as needed.
func (l *Logger) Append(e Entry) error {
	if l.Dir == "" {
		return errors.New("audit: Logger.Dir is empty")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = l.now()
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return fmt.Errorf("audit: mkdir %s: %w", l.Dir, err)
	}
	path := filepath.Join(l.Dir, dateFilename(e.Timestamp))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", path, err)
	}
	defer f.Close()
	line, err := json.Marshal(sanitize(e))
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	// Best-effort retention sweep. Failure here never propagates — logging
	// shouldn't lose the just-appended entry just because we couldn't prune
	// old ones.
	_ = l.sweepOld(l.now())
	return nil
}

// dateFilenameRE matches our daily log filename format. Used by the sweep
// to avoid deleting unrelated files in the dir.
var dateFilenameRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})\.jsonl$`)

// sweepOld removes log files older than RetentionDays. Best-effort: returns
// nil on any directory-read failure (caller doesn't care).
func (l *Logger) sweepOld(now time.Time) error {
	if l.RetentionDays <= 0 {
		return nil
	}
	cutoff := now.UTC().AddDate(0, 0, -l.RetentionDays).Truncate(24 * time.Hour)
	entries, err := os.ReadDir(l.Dir)
	if err != nil {
		return nil // dir might not exist yet; not fatal
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		m := dateFilenameRE.FindStringSubmatch(ent.Name())
		if m == nil {
			continue // not our file
		}
		fileDate, err := time.Parse("2006-01-02", m[1]+"-"+m[2]+"-"+m[3])
		if err != nil {
			continue
		}
		if fileDate.Before(cutoff) {
			_ = os.Remove(filepath.Join(l.Dir, ent.Name()))
		}
	}
	return nil
}

// Read returns entries from the last `daysBack` days (inclusive of today),
// sorted chronologically (oldest first). A missing log file yields zero
// entries, not an error.
func (l *Logger) Read(daysBack int) ([]Entry, error) {
	if daysBack < 0 {
		return nil, errors.New("audit: daysBack must be non-negative")
	}
	now := l.now()
	var all []Entry
	for i := 0; i <= daysBack; i++ {
		day := now.AddDate(0, 0, -i)
		path := filepath.Join(l.Dir, dateFilename(day))
		entries, err := readJSONLFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})
	return all, nil
}

// readJSONLFile parses one log file (one JSON object per non-empty line).
// Malformed lines are skipped with no error — defensive against partial
// writes from a crash.
func readJSONLFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []Entry
	sc := bufio.NewScanner(f)
	// Default scanner buffer is too small for some log lines (long arg lists).
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip malformed
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("audit: scan %s: %w", path, err)
	}
	return entries, nil
}

// dateFilename returns "YYYY-MM-DD.jsonl" for t in UTC.
func dateFilename(t time.Time) string {
	return t.UTC().Format("2006-01-02") + ".jsonl"
}

// sensitivePatterns lists arg-prefix patterns that should be redacted.
// Conservative list; expand as new args carrying secrets are added.
var sensitivePatterns = []string{
	"--ssh-key=",
	"--token=",
	"--api-key=",
	"--password=",
	"ANTHROPIC_API_KEY=",
	"CLAUDE_CODE_OAUTH_TOKEN=",
	"HCLOUD_TOKEN=",
}

// sanitize returns a copy of e with arg values matching sensitive patterns
// replaced by "<redacted>". The argument key is preserved so the audit log
// remains useful for debugging.
func sanitize(e Entry) Entry {
	out := e
	if len(e.Args) == 0 {
		return out
	}
	clean := make([]string, len(e.Args))
	for i, arg := range e.Args {
		clean[i] = arg
		for _, pat := range sensitivePatterns {
			if strings.HasPrefix(arg, pat) {
				clean[i] = pat + "<redacted>"
				break
			}
		}
	}
	out.Args = clean
	return out
}
