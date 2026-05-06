package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/latent-advisory/moorpost/cli/internal/state"
	mpsync "github.com/latent-advisory/moorpost/cli/internal/sync"
)

// makeConflictsContext wires a Context with a configurable cmdFakeSync and
// a project that has a sync session id. Mirrors makeLifecycleContext but
// with the Sync field populated.
func makeConflictsContext(t *testing.T, fs *cmdFakeSync, syncSessionID string) *Context {
	t.Helper()
	c, dir := makeLifecycleContext(t, &fakeProvider{}, true)
	c.Sync = fs
	// Update the project's sync session id so RunConflicts has something
	// to look up.
	st, err := state.Open(c.StatePath)
	if err != nil {
		t.Fatalf("Open state: %v", err)
	}
	ps := st.Projects[dir]
	ps.SyncSessionID = syncSessionID
	st.Projects[dir] = ps
	if err := st.Save(c.StatePath); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	c.State = st
	return c
}

func TestRunConflicts_NoSession_PrintsHint(t *testing.T) {
	c := makeConflictsContext(t, &cmdFakeSync{}, "") // empty session id
	var out bytes.Buffer
	if err := RunConflicts(context.Background(), &out, c, false); err != nil {
		t.Fatalf("RunConflicts: %v", err)
	}
	if !strings.Contains(out.String(), "No active sync session") {
		t.Errorf("expected hint about no session; got:\n%s", out.String())
	}
}

func TestRunConflicts_Clean_PrintsCheckmark(t *testing.T) {
	fs := &cmdFakeSync{conflicts: nil}
	c := makeConflictsContext(t, fs, "webapp-sync")
	var out bytes.Buffer
	if err := RunConflicts(context.Background(), &out, c, false); err != nil {
		t.Fatalf("clean session should succeed; got %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "No conflicts") {
		t.Errorf("clean output missing expected text; got:\n%s", got)
	}
	if !strings.Contains(got, "webapp-sync") {
		t.Errorf("output missing session id; got:\n%s", got)
	}
}

func TestRunConflicts_WithConflicts_ReturnsErrConflictsPresent(t *testing.T) {
	fs := &cmdFakeSync{conflicts: []mpsync.Conflict{
		{
			Path:            "src/main.go",
			AlphaKind:       mpsync.ChangeKindModified,
			BetaKind:        mpsync.ChangeKindModified,
			AlphaModifiedAt: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
			BetaModifiedAt:  time.Date(2026, 5, 4, 10, 30, 0, 0, time.UTC),
		},
	}}
	c := makeConflictsContext(t, fs, "webapp-sync")
	var out bytes.Buffer
	err := RunConflicts(context.Background(), &out, c, false)
	if !errors.Is(err, ErrConflictsPresent) {
		t.Fatalf("err = %v, want ErrConflictsPresent", err)
	}
	got := out.String()
	for _, want := range []string{"webapp-sync", "src/main.go", "modified", "1 unresolved"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
}

func TestRunConflicts_SessionNotFound_FriendlyMessage(t *testing.T) {
	fs := &cmdFakeSync{listConflictsErr: mpsync.ErrSessionNotFound}
	c := makeConflictsContext(t, fs, "ghost-session")
	var out bytes.Buffer
	if err := RunConflicts(context.Background(), &out, c, false); err != nil {
		t.Fatalf("ErrSessionNotFound should be handled gracefully; got %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "ghost-session") || !strings.Contains(got, "not found") {
		t.Errorf("output missing friendly message; got:\n%s", got)
	}
}

func TestRunConflicts_GenericError_Propagates(t *testing.T) {
	fs := &cmdFakeSync{listConflictsErr: errors.New("connection lost")}
	c := makeConflictsContext(t, fs, "webapp-sync")
	var out bytes.Buffer
	err := RunConflicts(context.Background(), &out, c, false)
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if errors.Is(err, ErrConflictsPresent) {
		t.Errorf("generic error misclassified as ErrConflictsPresent: %v", err)
	}
}

func TestRunConflicts_JSON_Clean(t *testing.T) {
	fs := &cmdFakeSync{conflicts: nil}
	c := makeConflictsContext(t, fs, "webapp-sync")
	var out bytes.Buffer
	if err := RunConflicts(context.Background(), &out, c, true); err != nil {
		t.Fatalf("err = %v", err)
	}
	var got conflictsReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v\nbody: %s", err, out.String())
	}
	if got.Session != "webapp-sync" {
		t.Errorf("session = %q, want webapp-sync", got.Session)
	}
	if len(got.Conflicts) != 0 {
		t.Errorf("expected 0 conflicts in JSON, got %d", len(got.Conflicts))
	}
	if got.NotFound {
		t.Error("NotFound = true, want false on clean session")
	}
}

func TestRunConflicts_JSON_WithConflicts_ReturnsErr(t *testing.T) {
	fs := &cmdFakeSync{conflicts: []mpsync.Conflict{{Path: "f.txt", AlphaKind: mpsync.ChangeKindModified}}}
	c := makeConflictsContext(t, fs, "webapp-sync")
	var out bytes.Buffer
	err := RunConflicts(context.Background(), &out, c, true)
	// Both human and JSON paths return ErrConflictsPresent when conflicts
	// exist — exit code matches state regardless of format. Pipeline
	// callers who don't want the non-zero exit can use `|| true`.
	if !errors.Is(err, ErrConflictsPresent) {
		t.Fatalf("err = %v, want ErrConflictsPresent", err)
	}
	var got conflictsReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v\nbody: %s", err, out.String())
	}
	if len(got.Conflicts) != 1 {
		t.Errorf("expected 1 conflict in JSON, got %d", len(got.Conflicts))
	}
}

func TestRunConflicts_JSON_NotFound_FlagSet(t *testing.T) {
	fs := &cmdFakeSync{listConflictsErr: mpsync.ErrSessionNotFound}
	c := makeConflictsContext(t, fs, "ghost")
	var out bytes.Buffer
	if err := RunConflicts(context.Background(), &out, c, true); err != nil {
		t.Fatalf("err = %v", err)
	}
	var got conflictsReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if !got.NotFound {
		t.Errorf("expected not_found=true for ghost session; got %+v", got)
	}
}
