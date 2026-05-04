package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderSubstitutesVars(t *testing.T) {
	out, err := Render(BootstrapVars{
		ProjectSlug:       "argus",
		LocalAbsPath:      "/Users/landytang/argus",
		RemoteUser:        "landytang",
		NodeVersion:       "20",
		ClaudeCodeVersion: "2.0.0",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"project=argus",
		"/Users/landytang/argus",
		"landytang",
		"setup_20.x",
		"@anthropic-ai/claude-code@2.0.0",
		"chmod 0600 /etc/moorpost/env",
		"ln -sfn",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestRenderDefaults(t *testing.T) {
	out, err := Render(BootstrapVars{
		ProjectSlug:  "x",
		LocalAbsPath: "/x",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "setup_20.x") {
		t.Error("default Node version 20 should be applied")
	}
	if !strings.Contains(out, "moorpost") {
		t.Error("default RemoteUser 'moorpost' should appear")
	}
	if strings.Contains(out, "@anthropic-ai/claude-code@") {
		t.Error("with no ClaudeCodeVersion, @ pin should be absent")
	}
}

func TestRenderRejectsMissingRequired(t *testing.T) {
	_, err := Render(BootstrapVars{ProjectSlug: ""})
	if err == nil {
		t.Error("Render accepted empty ProjectSlug")
	}
	_, err = Render(BootstrapVars{ProjectSlug: "x", LocalAbsPath: ""})
	if err == nil {
		t.Error("Render accepted empty LocalAbsPath")
	}
}

func TestRenderedScriptIsBashSyntactic(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	out, err := Render(BootstrapVars{
		ProjectSlug:  "argus",
		LocalAbsPath: "/Users/x/argus",
	})
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "bootstrap.sh")
	if err := os.WriteFile(tmp, []byte(out), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-n", tmp)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("bash -n failed:\n%s\n--- script ---\n%s", combined, out)
	}
}

func TestRenderHandlesPathsWithSpaces(t *testing.T) {
	out, err := Render(BootstrapVars{
		ProjectSlug:  "x",
		LocalAbsPath: "/Users/x/AI M&A/code/argus",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/Users/x/AI M&A/code/argus") {
		t.Error("path with spaces lost in render")
	}
	// And should still pass bash -n.
	if bash, err := exec.LookPath("bash"); err == nil {
		tmp := filepath.Join(t.TempDir(), "bootstrap.sh")
		if err := os.WriteFile(tmp, []byte(out), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bash, "-n", tmp)
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("bash -n failed for path-with-spaces:\n%s", combined)
		}
	}
}
