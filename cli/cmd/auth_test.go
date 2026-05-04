package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
)

// fakeAuthAgent is a stand-in that returns a canned credential.
type fakeAuthAgent struct {
	id   string
	cred agent.Credential
	err  error
}

func (f *fakeAuthAgent) ID() string                                { return f.id }
func (f *fakeAuthAgent) InstallScript(agent.OSFamily) string       { return ":" }
func (f *fakeAuthAgent) AuthenticateLocal(context.Context) (agent.Credential, error) {
	return f.cred, f.err
}
func (f *fakeAuthAgent) InjectCredential(context.Context, agent.SSHTarget, agent.Credential) error {
	return nil
}
func (f *fakeAuthAgent) SessionStatePath(string) string                                       { return "" }
func (f *fakeAuthAgent) Pause(context.Context, agent.SSHTarget, agent.SessionRef) error       { return nil }
func (f *fakeAuthAgent) Resume(context.Context, agent.SSHTarget, agent.SessionRef) error      { return nil }
func (f *fakeAuthAgent) IsActive(context.Context, agent.SSHTarget, agent.SessionRef) (bool, error) {
	return false, nil
}

func TestRunAuthHappyPath(t *testing.T) {
	a := &fakeAuthAgent{id: "claude-code", cred: agent.Credential{Kind: "oauth-subscription"}}
	var out bytes.Buffer
	if err := RunAuth(context.Background(), &out, a); err != nil {
		t.Fatalf("RunAuth: %v", err)
	}
	if !strings.Contains(out.String(), "claude-code") {
		t.Errorf("output missing agent id: %q", out.String())
	}
	if !strings.Contains(out.String(), "oauth-subscription") {
		t.Errorf("output missing credential kind: %q", out.String())
	}
}

func TestRunAuthAgentError(t *testing.T) {
	myErr := errors.New("setup-token failed")
	a := &fakeAuthAgent{id: "claude-code", err: myErr}
	var out bytes.Buffer
	err := RunAuth(context.Background(), &out, a)
	if !errors.Is(err, myErr) {
		t.Errorf("err = %v, want wrapping %v", err, myErr)
	}
}

func TestRunAuthRequiresAgent(t *testing.T) {
	var out bytes.Buffer
	if err := RunAuth(context.Background(), &out, nil); err == nil {
		t.Error("RunAuth accepted nil agent")
	}
}
