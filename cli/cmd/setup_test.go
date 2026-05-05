package cmd

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// stubLookup constructs a Lookup func that reports `present` as installed
// and everything else as missing.
func stubLookup(present ...string) func(string) (string, error) {
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/local/bin/" + name, nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
}

// recordRunner returns a Run func that appends each invocation to the slice
// it returns.
func recordRunner() (func(string, []string) error, *[]string) {
	var calls []string
	fn := func(name string, args []string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	return fn, &calls
}

func TestRunSetupAllPresent(t *testing.T) {
	var out bytes.Buffer
	runFn, calls := recordRunner()
	err := RunSetup(context.Background(), &out, strings.NewReader(""), SetupOptions{
		Yes:    true,
		OS:     "darwin",
		Lookup: stubLookup("node", "claude", "gcloud", "mutagen", "tmux", "rg", "rsync"),
		Run:    runFn,
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("expected 0 install calls, got %d: %v", len(*calls), *calls)
	}
	if !strings.Contains(out.String(), "All prerequisites satisfied") {
		t.Errorf("expected satisfied summary; got %q", out.String())
	}
}

func TestRunSetupOneMissingWithYes(t *testing.T) {
	var out bytes.Buffer
	runFn, calls := recordRunner()
	err := RunSetup(context.Background(), &out, strings.NewReader(""), SetupOptions{
		Yes:    true,
		OS:     "darwin",
		Lookup: stubLookup("node", "claude", "gcloud", "tmux", "rg", "rsync"), // mutagen missing
		Run:    runFn,
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 install call (for mutagen), got %d: %v", len(*calls), *calls)
	}
	if !strings.Contains((*calls)[0], "mutagen") {
		t.Errorf("install command should mention mutagen: %q", (*calls)[0])
	}
}

func TestRunSetupDryRunDoesNotInstall(t *testing.T) {
	var out bytes.Buffer
	runFn, calls := recordRunner()
	err := RunSetup(context.Background(), &out, strings.NewReader(""), SetupOptions{
		DryRun: true,
		OS:     "darwin",
		Lookup: stubLookup(), // ALL missing
		Run:    runFn,
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("dry-run made %d install calls (should be 0): %v", len(*calls), *calls)
	}
	if !strings.Contains(out.String(), "would run:") {
		t.Errorf("dry-run output should show 'would run:' lines; got %q", out.String())
	}
	if !strings.Contains(out.String(), "Dry run complete") {
		t.Error("dry-run should print completion line")
	}
}

func TestRunSetupPromptDeclined(t *testing.T) {
	var out bytes.Buffer
	runFn, calls := recordRunner()
	err := RunSetup(context.Background(), &out, strings.NewReader("n\n"), SetupOptions{
		OS:     "darwin",
		Lookup: stubLookup("node", "claude", "gcloud", "mutagen", "tmux", "rg", "rsync"),
		Run:    runFn,
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	// All present → no prompt, no calls
	if len(*calls) != 0 {
		t.Errorf("got unexpected install calls: %v", *calls)
	}
}

func TestRunSetupRejectsUnsupportedOS(t *testing.T) {
	var out bytes.Buffer
	err := RunSetup(context.Background(), &out, strings.NewReader(""), SetupOptions{
		OS: "windows",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported OS") {
		t.Errorf("err = %v, want unsupported OS", err)
	}
}

func TestRunSetupSkipsBinariesWithNoInstallCommand(t *testing.T) {
	// On Linux, gcloud has no automated install (empty Install entry).
	// Setup should print a manual hint and continue, not error.
	var out bytes.Buffer
	runFn, _ := recordRunner()
	err := RunSetup(context.Background(), &out, strings.NewReader(""), SetupOptions{
		Yes:    true,
		OS:     "linux",
		Lookup: stubLookup("node", "claude", "tmux", "rg", "rsync"), // gcloud + mutagen missing
		Run:    runFn,
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if !strings.Contains(out.String(), "no automated install") {
		t.Errorf("expected 'no automated install' for gcloud on Linux; got %q", out.String())
	}
}

func TestRunSetupInstallFailureReturnsError(t *testing.T) {
	myErr := errors.New("brew: connection refused")
	runFn := func(string, []string) error { return myErr }
	var out bytes.Buffer
	err := RunSetup(context.Background(), &out, strings.NewReader(""), SetupOptions{
		Yes:    true,
		OS:     "darwin",
		Lookup: stubLookup(), // everything missing
		Run:    runFn,
	})
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrap of %v", err, myErr)
	}
}
