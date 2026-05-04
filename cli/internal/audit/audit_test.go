package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestLogger(t *testing.T, fixedNow time.Time) *Logger {
	t.Helper()
	return &Logger{
		Dir: t.TempDir(),
		Now: func() time.Time { return fixedNow },
	}
}

func TestAppendCreatesFileAndWritesJSONL(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	l := newTestLogger(t, now)
	e := Entry{Command: "provision", Args: []string{"--start"}, ExitCode: 0, DurationMS: 1234}
	if err := l.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := filepath.Join(l.Dir, "2026-05-05.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var got Entry
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Command != "provision" || got.ExitCode != 0 || got.DurationMS != 1234 {
		t.Errorf("got %+v", got)
	}
}

func TestAppendTwiceProducesTwoLines(t *testing.T) {
	l := newTestLogger(t, time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC))
	for _, cmd := range []string{"init", "auth"} {
		if err := l.Append(Entry{Command: cmd}); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(l.Dir, "2026-05-05.jsonl"))
	if strings.Count(string(data), "\n") != 2 {
		t.Errorf("expected 2 lines, got %d", strings.Count(string(data), "\n"))
	}
}

func TestAppendFillsTimestampWhenZero(t *testing.T) {
	now := time.Date(2026, 5, 5, 14, 0, 0, 0, time.UTC)
	l := newTestLogger(t, now)
	if err := l.Append(Entry{Command: "x"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := l.Read(0)
	if len(entries) != 1 {
		t.Fatalf("expected 1, got %d", len(entries))
	}
	if !entries[0].Timestamp.Equal(now) {
		t.Errorf("timestamp = %v, want %v", entries[0].Timestamp, now)
	}
}

func TestReadEmptyDir(t *testing.T) {
	l := newTestLogger(t, time.Now())
	got, err := l.Read(0)
	if err != nil {
		t.Errorf("Read on empty dir = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 entries, got %d", len(got))
	}
}

func TestReadAcrossMultipleDays(t *testing.T) {
	dir := t.TempDir()
	day1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)

	l1 := &Logger{Dir: dir, Now: func() time.Time { return day1 }}
	l2 := &Logger{Dir: dir, Now: func() time.Time { return day2 }}

	if err := l1.Append(Entry{Command: "init"}); err != nil {
		t.Fatal(err)
	}
	if err := l2.Append(Entry{Command: "provision"}); err != nil {
		t.Fatal(err)
	}

	// Read covering both days.
	reader := &Logger{Dir: dir, Now: func() time.Time { return day2 }}
	all, err := reader.Read(7)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	// Chronological: day1 first.
	if all[0].Command != "init" || all[1].Command != "provision" {
		t.Errorf("order wrong: %+v", all)
	}
}

func TestReadIgnoresMalformedLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "2026-05-05.jsonl")
	contents := `{"command":"good","timestamp":"2026-05-05T12:00:00Z"}
not-json
{"command":"also-good","timestamp":"2026-05-05T13:00:00Z"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &Logger{Dir: dir, Now: func() time.Time { return now }}
	got, err := l.Read(0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 valid entries, got %d", len(got))
	}
}

func TestSanitizeRedactsSensitive(t *testing.T) {
	in := Entry{
		Command: "auth",
		Args: []string{
			"--gcp-project=latent-advisory",
			"--ssh-key=/home/me/.ssh/id_ed25519",
			"normal-arg",
			"ANTHROPIC_API_KEY=sk-ant-xxxxx",
		},
	}
	out := sanitize(in)
	if out.Args[0] != "--gcp-project=latent-advisory" {
		t.Errorf("non-sensitive arg lost: %q", out.Args[0])
	}
	if !strings.Contains(out.Args[1], "<redacted>") {
		t.Errorf("--ssh-key value not redacted: %q", out.Args[1])
	}
	if !strings.HasPrefix(out.Args[1], "--ssh-key=") {
		t.Errorf("--ssh-key key not preserved: %q", out.Args[1])
	}
	if out.Args[2] != "normal-arg" {
		t.Errorf("normal arg modified: %q", out.Args[2])
	}
	if !strings.Contains(out.Args[3], "<redacted>") {
		t.Errorf("ANTHROPIC_API_KEY value not redacted: %q", out.Args[3])
	}
	// Original entry must not be mutated.
	if in.Args[1] == out.Args[1] {
		t.Error("sanitize mutated input slice")
	}
}

func TestAppendRequiresDir(t *testing.T) {
	l := &Logger{}
	if err := l.Append(Entry{Command: "x"}); err == nil {
		t.Error("Append with empty Dir accepted")
	}
}

func TestReadRejectsNegativeDays(t *testing.T) {
	l := newTestLogger(t, time.Now())
	if _, err := l.Read(-1); err == nil {
		t.Error("Read(-1) accepted")
	}
}

func TestFilePermissions(t *testing.T) {
	now := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	l := newTestLogger(t, now)
	if err := l.Append(Entry{Command: "x"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(l.Dir, "2026-05-05.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
}
