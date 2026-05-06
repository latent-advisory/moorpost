package session

import (
	"context"
	"testing"

	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/sync"
)

// minimal stubs to construct a Session in tests; behavior covered in each
// package's own test file.

type stubProvider struct{}

func (stubProvider) ID() string                                            { return "stub" }
func (stubProvider) Provision(context.Context, provider.ProvisionSpec) (provider.VM, error) {
	return provider.VM{}, nil
}
func (stubProvider) Start(context.Context, string) error                              { return nil }
func (stubProvider) Stop(context.Context, string) error                               { return nil }
func (stubProvider) Destroy(context.Context, string) error                            { return nil }
func (stubProvider) Status(context.Context, string) (provider.VMState, error)         { return provider.VMStateStopped, nil }
func (stubProvider) Snapshot(context.Context, string, string) (provider.SnapshotID, error) {
	return "", nil
}
func (stubProvider) Cost(context.Context, string, provider.TimeRange) (provider.CostBreakdown, error) {
	return provider.CostBreakdown{}, nil
}
func (stubProvider) SSHTarget(context.Context, string) (provider.SSHTarget, error) {
	return provider.SSHTarget{}, nil
}
func (stubProvider) Preflight(context.Context) error { return nil }

type stubAgent struct{}

func (stubAgent) ID() string                                              { return "stub" }
func (stubAgent) InstallScript(agent.OSFamily) string                     { return ":" }
func (stubAgent) AuthenticateLocal(context.Context) (agent.Credential, error) {
	return agent.Credential{}, nil
}
func (stubAgent) LoadCachedCredential() (agent.Credential, error) {
	return agent.Credential{}, nil
}
func (stubAgent) InjectCredential(context.Context, agent.SSHTarget, agent.Credential) error {
	return nil
}
func (stubAgent) SessionStatePath(string) string                                       { return "" }
func (stubAgent) Pause(context.Context, agent.SSHTarget, agent.SessionRef) error       { return nil }
func (stubAgent) Resume(context.Context, agent.SSHTarget, agent.SessionRef) error      { return nil }
func (stubAgent) IsActive(context.Context, agent.SSHTarget, agent.SessionRef) (bool, error) {
	return false, nil
}

type stubSync struct{}

func (stubSync) ID() string                                                   { return "stub" }
func (stubSync) StartSession(context.Context, sync.SyncSpec) (sync.SyncSessionID, error) {
	return "", nil
}
func (stubSync) Pause(context.Context, sync.SyncSessionID) error              { return nil }
func (stubSync) Resume(context.Context, sync.SyncSessionID) error             { return nil }
func (stubSync) OneShot(context.Context, sync.Endpoint, sync.Endpoint, sync.Direction) error {
	return nil
}
func (stubSync) Status(context.Context, sync.SyncSessionID) (sync.SyncStatus, error) {
	return sync.SyncStatus{}, nil
}
func (stubSync) Stop(context.Context, sync.SyncSessionID) error { return nil }
func (stubSync) ListConflicts(context.Context, sync.SyncSessionID) ([]sync.Conflict, error) {
	return nil, nil
}

func TestNew(t *testing.T) {
	s := New(stubProvider{}, stubAgent{}, stubSync{}, "webapp", "/Users/x/webapp")
	if s.Provider == nil || s.Agent == nil || s.Sync == nil {
		t.Fatal("Session has nil interface fields after New()")
	}
	if s.ProjectSlug != "webapp" {
		t.Errorf("ProjectSlug = %q, want webapp", s.ProjectSlug)
	}
	if s.ProjectAbsDir != "/Users/x/webapp" {
		t.Errorf("ProjectAbsDir = %q, want /Users/x/webapp", s.ProjectAbsDir)
	}
}
