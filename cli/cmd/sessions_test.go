package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/config"
	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// sessionsFakeAgent is a minimal fakeAgent good enough for RunSessionsList:
// only SessionStatePath needs real behaviour. Returning the directory we
// create in the test gives us full control over what gets scanned.
type sessionsFakeAgent struct {
	cmdFakeAgent
	sessionDir string
}

func (f *sessionsFakeAgent) SessionStatePath(string) string {
	return f.sessionDir
}

// sessionsBaseContext sets up a Context whose Agent.SessionStatePath
// points at sessionDir. Pass remoteSIDs to seed the project's RemoteSIDs.
func sessionsBaseContext(t *testing.T, sessionDir string, remoteSIDs []string) *Context {
	t.Helper()
	cfg := config.Default()
	cfg.ProjectSlug = "webapp"
	cfg.Provider.Type = "gcp"
	cfg.Agent.Type = "claude-code"
	cfg.Sync.Engine = "mutagen"
	st := state.New()
	st.SetProject("/abs/webapp", state.ProjectState{
		Slug:       "webapp",
		VMID:       "webapp-vm",
		ActiveSide: state.SideLocal,
		RemoteSIDs: remoteSIDs,
	})
	return &Context{
		Config:     cfg,
		State:      st,
		ProjectDir: "/abs/webapp",
		Agent:      &sessionsFakeAgent{sessionDir: sessionDir},
	}
}

// writeJSONL drops a session JSONL with a known first user message at
// the given path. Tests can pass userText="" to skip the user record.
func writeJSONL(t *testing.T, path, userText string) {
	t.Helper()
	var lines []string
	// First a noise line (queue-operation) — exercises the "skip
	// non-user records" path in readFirstUserText.
	lines = append(lines, `{"type":"queue-operation","operation":"enqueue"}`)
	if userText != "" {
		// Array-form content.
		rec := map[string]any{
			"type": "user",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": userText},
				},
			},
		}
		b, _ := json.Marshal(rec)
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRunSessionsList_FiltersToUUIDJSONLs(t *testing.T) {
	dir := t.TempDir()
	// Two valid session JSONLs.
	sid1 := "11111111-1111-4111-8111-111111111111"
	sid2 := "22222222-2222-4222-8222-222222222222"
	writeJSONL(t, filepath.Join(dir, sid1+".jsonl"), "first user message for session 1")
	writeJSONL(t, filepath.Join(dir, sid2+".jsonl"), "second")
	// Stagger mtimes so we can assert ordering.
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, sid2+".jsonl"), past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// Decoy files that must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "not-a-uuid.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid1+".txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A subdirectory named like a sid (the wrapper-companion case from PLUGIN.md).
	if err := os.Mkdir(filepath.Join(dir, sid1), 0o755); err != nil {
		t.Fatal(err)
	}

	c := sessionsBaseContext(t, dir, nil)
	var out, errOut bytes.Buffer
	if err := RunSessionsList(context.Background(), &out, &errOut, c, SessionsListOptions{NoLive: true}); err != nil {
		t.Fatalf("RunSessionsList: %v", err)
	}

	got := out.String()
	for _, want := range []string{sid1, sid2, "first user message for session 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "not-a-uuid") {
		t.Errorf("output should skip non-UUID files; got:\n%s", got)
	}
}

func TestRunSessionsList_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	c := sessionsBaseContext(t, dir, nil)
	var out, errOut bytes.Buffer
	if err := RunSessionsList(context.Background(), &out, &errOut, c, SessionsListOptions{NoLive: true}); err != nil {
		t.Fatalf("RunSessionsList on empty dir: %v", err)
	}
	if !strings.Contains(out.String(), "no sessions") {
		t.Errorf("expected 'no sessions' message; got:\n%s", out.String())
	}
}

func TestRunSessionsList_MissingDirIsEmpty(t *testing.T) {
	c := sessionsBaseContext(t, "/nonexistent/path/that/does/not/exist", nil)
	var out, errOut bytes.Buffer
	if err := RunSessionsList(context.Background(), &out, &errOut, c, SessionsListOptions{NoLive: true, JSON: true}); err != nil {
		t.Fatalf("RunSessionsList missing dir: %v", err)
	}
	var report SessionsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(report.Sessions) != 0 {
		t.Errorf("expected 0 sessions; got %+v", report.Sessions)
	}
}

func TestRunSessionsList_JSONShape(t *testing.T) {
	dir := t.TempDir()
	sid := "33333333-3333-4333-8333-333333333333"
	writeJSONL(t, filepath.Join(dir, sid+".jsonl"), "hello world")

	c := sessionsBaseContext(t, dir, nil)
	var out, errOut bytes.Buffer
	if err := RunSessionsList(context.Background(), &out, &errOut, c, SessionsListOptions{JSON: true, NoLive: true}); err != nil {
		t.Fatalf("RunSessionsList JSON: %v", err)
	}

	var report SessionsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if report.ProjectDir != "/abs/webapp" {
		t.Errorf("ProjectDir = %q, want /abs/webapp", report.ProjectDir)
	}
	if len(report.Sessions) != 1 {
		t.Fatalf("Sessions len = %d, want 1", len(report.Sessions))
	}
	s := report.Sessions[0]
	if s.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", s.SessionID, sid)
	}
	if s.Location != "local" {
		t.Errorf("Location = %q, want local", s.Location)
	}
	if s.LiveOnRemote {
		t.Error("LiveOnRemote = true; want false (no remote SIDs)")
	}
	if s.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", s.SizeBytes)
	}
	if s.FirstUserText != "hello world" {
		t.Errorf("FirstUserText = %q, want 'hello world'", s.FirstUserText)
	}
}

func TestRunSessionsList_RemoteVsLocal(t *testing.T) {
	dir := t.TempDir()
	sidLocal := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	sidRemote := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	writeJSONL(t, filepath.Join(dir, sidLocal+".jsonl"), "local one")
	writeJSONL(t, filepath.Join(dir, sidRemote+".jsonl"), "remote one")

	c := sessionsBaseContext(t, dir, []string{sidRemote})
	var out, errOut bytes.Buffer
	if err := RunSessionsList(context.Background(), &out, &errOut, c, SessionsListOptions{JSON: true, NoLive: true}); err != nil {
		t.Fatalf("RunSessionsList: %v", err)
	}
	var report SessionsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	loc := map[string]string{}
	for _, s := range report.Sessions {
		loc[s.SessionID] = s.Location
	}
	if loc[sidLocal] != "local" {
		t.Errorf("sidLocal location = %q, want local", loc[sidLocal])
	}
	if loc[sidRemote] != "remote" {
		t.Errorf("sidRemote location = %q, want remote", loc[sidRemote])
	}
}

// TestRunSessionsList_LiveCheckSuccess verifies the --no-live=false path
// via a stubbed liveRemoteSIDsFunc. We don't ssh to anything real.
func TestRunSessionsList_LiveCheckSuccess(t *testing.T) {
	dir := t.TempDir()
	sidLive := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	sidIdle := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	writeJSONL(t, filepath.Join(dir, sidLive+".jsonl"), "live one")
	writeJSONL(t, filepath.Join(dir, sidIdle+".jsonl"), "idle one")

	c := sessionsBaseContext(t, dir, []string{sidLive, sidIdle})

	// Stub the live-check.
	prev := liveRemoteSIDsFunc
	t.Cleanup(func() { liveRemoteSIDsFunc = prev })
	liveRemoteSIDsFunc = func(_ context.Context, _ *Context, _ *state.ProjectState) (map[string]bool, error) {
		return map[string]bool{sidLive: true}, nil
	}

	var out, errOut bytes.Buffer
	if err := RunSessionsList(context.Background(), &out, &errOut, c, SessionsListOptions{JSON: true}); err != nil {
		t.Fatalf("RunSessionsList: %v", err)
	}
	var report SessionsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	live := map[string]bool{}
	for _, s := range report.Sessions {
		live[s.SessionID] = s.LiveOnRemote
	}
	if !live[sidLive] {
		t.Errorf("sidLive should be live; got %+v", live)
	}
	if live[sidIdle] {
		t.Errorf("sidIdle should NOT be live; got %+v", live)
	}
}

// TestRunSessionsList_LiveCheckFailure verifies that an ssh failure is
// non-fatal: the command still succeeds with all remote sessions
// flagged not-live, and a warning lands on stderr.
func TestRunSessionsList_LiveCheckFailure(t *testing.T) {
	dir := t.TempDir()
	sid := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	writeJSONL(t, filepath.Join(dir, sid+".jsonl"), "x")

	c := sessionsBaseContext(t, dir, []string{sid})
	prev := liveRemoteSIDsFunc
	t.Cleanup(func() { liveRemoteSIDsFunc = prev })
	liveRemoteSIDsFunc = func(_ context.Context, _ *Context, _ *state.ProjectState) (map[string]bool, error) {
		return nil, os.ErrPermission
	}

	var out, errOut bytes.Buffer
	if err := RunSessionsList(context.Background(), &out, &errOut, c, SessionsListOptions{JSON: true}); err != nil {
		t.Fatalf("RunSessionsList should not fail when live-check errors: %v", err)
	}
	if !strings.Contains(errOut.String(), "live-check failed") {
		t.Errorf("expected stderr warning; got: %s", errOut.String())
	}
	var report SessionsReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(report.Sessions) != 1 || report.Sessions[0].LiveOnRemote {
		t.Errorf("expected 1 session, not-live: got %+v", report.Sessions)
	}
}

// TestReadFirstUserText_StringContent covers the string-content branch
// (Claude Code occasionally emits {message:{content:"..."}} rather than
// the array form).
func TestReadFirstUserText_StringContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	rec := `{"type":"user","message":{"role":"user","content":"hi there"}}` + "\n"
	if err := os.WriteFile(path, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFirstUserText(path)
	if err != nil {
		t.Fatalf("readFirstUserText: %v", err)
	}
	if got != "hi there" {
		t.Errorf("got %q, want 'hi there'", got)
	}
}

// TestReadFirstUserText_LongMessageTruncated verifies the 60-char cap.
func TestReadFirstUserText_LongMessageTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	long := strings.Repeat("a", 200)
	rec := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + long + `"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFirstUserText(path)
	if err != nil {
		t.Fatalf("readFirstUserText: %v", err)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	// Cap is 60 runes + 3-rune ellipsis.
	if r := []rune(got); len(r) != 63 {
		t.Errorf("expected 63 runes, got %d (%q)", len(r), got)
	}
}

// TestReadFirstUserText_SkipsSyntheticIDEContext verifies that
// auto-injected user messages (IDE selection dumps, slash-command
// metadata, system reminders) get skipped — the picker label should
// be the first ACTUAL prompt the user typed.
func TestReadFirstUserText_SkipsSyntheticIDEContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	// First 4 user events are all synthetic IDE/system context.
	// 5th is the real prompt — that's what we should pick.
	body := `{"type":"user","message":{"content":"<ide_opened_file>The user opened /foo.go"}}` + "\n" +
		`{"type":"user","message":{"content":"<ide_selection>The user selected lines 1-5"}}` + "\n" +
		`{"type":"user","message":{"content":"<system-reminder>Some harness reminder"}}` + "\n" +
		`{"type":"user","message":{"content":"<command-name>/clear"}}` + "\n" +
		`{"type":"user","message":{"content":"actually fix the bug in handoff.go"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFirstUserText(path)
	if err != nil {
		t.Fatalf("readFirstUserText: %v", err)
	}
	if got != "actually fix the bug in handoff.go" {
		t.Errorf("got %q, want 'actually fix the bug in handoff.go' "+
			"(synthetic IDE/system messages should be skipped)", got)
	}
}

// TestIsSyntheticUserMessage exercises the tag-prefix matcher. Tags
// embedded mid-text (not a synthetic message) shouldn't be filtered.
func TestIsSyntheticUserMessage(t *testing.T) {
	cases := []struct {
		text    string
		want    bool
		comment string
	}{
		{"<ide_opened_file>The user opened the file foo.go", true, "ide_opened_file"},
		{"<ide_selection>The user selected lines 1-5", true, "ide_selection"},
		{"<system-reminder>plan mode active", true, "system-reminder"},
		{"  <ide_opened_file>...", true, "leading whitespace"},
		{"hello world", false, "plain prompt"},
		{"can you parse this <ide_opened_file> tag?", false, "tag mid-sentence — not synthetic"},
		{"", false, "empty"},
	}
	for _, c := range cases {
		if got := isSyntheticUserMessage(c.text); got != c.want {
			t.Errorf("isSyntheticUserMessage(%q) = %v, want %v (%s)",
				c.text, got, c.want, c.comment)
		}
	}
}
