package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeAgent struct{ id string }

func (f *fakeAgent) ID() string                                   { return f.id }
func (f *fakeAgent) InstallScript(OSFamily) string                { return ":" }
func (f *fakeAgent) AuthenticateLocal(context.Context) (Credential, error) { return Credential{}, nil }
func (f *fakeAgent) InjectCredential(context.Context, SSHTarget, Credential) error { return nil }
func (f *fakeAgent) SessionStatePath(string) string               { return "" }
func (f *fakeAgent) Pause(context.Context, SSHTarget, SessionRef) error { return nil }
func (f *fakeAgent) Resume(context.Context, SSHTarget, SessionRef) error { return nil }
func (f *fakeAgent) IsActive(context.Context, SSHTarget, SessionRef) (bool, error) { return false, nil }

func newFake(id string) Constructor {
	return func(map[string]any) (Agent, error) { return &fakeAgent{id: id}, nil }
}

func TestRegistryHappyPath(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("claude-code", newFake("claude-code"))
	r.register("aider", newFake("aider"))

	want := []string{"aider", "claude-code"}
	got := r.list()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("list() = %v, want %v", got, want)
	}

	a, err := r.get("claude-code", nil)
	if err != nil || a.ID() != "claude-code" {
		t.Fatalf("get(claude-code) returned (%v, %v)", a, err)
	}
}

func TestRegistryUnknownID(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("claude-code", newFake("claude-code"))

	_, err := r.get("cursor-cli", nil)
	if err == nil {
		t.Fatal("get(cursor-cli) = nil err, want unknown")
	}
	if !strings.Contains(err.Error(), "cursor-cli") || !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("error %q should mention both unknown and registered ids", err.Error())
	}
}

func TestRegistryDoubleRegisterPanics(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("claude-code", newFake("claude-code"))
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on double-register")
		}
	}()
	r.register("claude-code", newFake("claude-code"))
}

func TestRegistryRejectsEmptyOrNil(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	t.Run("empty id", func(t *testing.T) {
		defer func() {
			if rec := recover(); rec == nil {
				t.Fatal("expected panic")
			}
		}()
		r.register("", newFake("x"))
	})
	t.Run("nil ctor", func(t *testing.T) {
		defer func() {
			if rec := recover(); rec == nil {
				t.Fatal("expected panic")
			}
		}()
		r.register("aider", nil)
	})
}

func TestConstructorErrorPropagates(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	myErr := errors.New("missing model field")
	r.register("claude-code", func(map[string]any) (Agent, error) { return nil, myErr })

	_, err := r.get("claude-code", nil)
	if !errors.Is(err, myErr) {
		t.Errorf("get() = %v, want errors.Is(...) of %v", err, myErr)
	}
}

func TestSentinelErrorsExist(t *testing.T) {
	// These two errors are part of the v1 contract — keep them locked down so
	// downstream callers can switch on them.
	if ErrAgentNotInstalled == nil {
		t.Error("ErrAgentNotInstalled is nil")
	}
	if ErrAuthRequired == nil {
		t.Error("ErrAuthRequired is nil")
	}
}

func TestCredentialPrintingIncludesFieldLabels(t *testing.T) {
	// Sanity check: nobody has added a Stringer method that masks the secret
	// in a way that would silently break logging assumptions elsewhere.
	// Default %+v keeps the struct shape; if someone adds a String() method
	// that omits field names, this test catches it.
	c := Credential{EnvVar: "X", Value: "secret-shhhh", Kind: "api-key"}
	got := fmt.Sprintf("%+v", c)
	if !strings.Contains(got, "EnvVar:") || !strings.Contains(got, "Kind:") {
		t.Errorf("Credential %%+v output %q lost its struct-field shape; check for Stringer impl", got)
	}
}
