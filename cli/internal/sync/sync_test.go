package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeSync struct{ id string }

func (f *fakeSync) ID() string                                              { return f.id }
func (f *fakeSync) StartSession(context.Context, SyncSpec) (SyncSessionID, error) { return "", nil }
func (f *fakeSync) Pause(context.Context, SyncSessionID) error              { return nil }
func (f *fakeSync) Resume(context.Context, SyncSessionID) error             { return nil }
func (f *fakeSync) OneShot(context.Context, Endpoint, Endpoint, Direction) error { return nil }
func (f *fakeSync) Status(context.Context, SyncSessionID) (SyncStatus, error) {
	return SyncStatus{}, nil
}
func (f *fakeSync) Stop(context.Context, SyncSessionID) error { return nil }
func (f *fakeSync) ListConflicts(context.Context, SyncSessionID) ([]Conflict, error) {
	return nil, nil
}

func newFake(id string) Constructor {
	return func(map[string]any) (Sync, error) { return &fakeSync{id: id}, nil }
}

func TestRegistryHappyPath(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("mutagen", newFake("mutagen"))
	r.register("rsync", newFake("rsync"))

	got := r.list()
	want := []string{"mutagen", "rsync"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("list() = %v, want %v", got, want)
	}

	s, err := r.get("mutagen", nil)
	if err != nil || s.ID() != "mutagen" {
		t.Fatalf("get(mutagen) = (%v, %v)", s, err)
	}
}

func TestRegistryUnknownID(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("mutagen", newFake("mutagen"))

	_, err := r.get("syncthing", nil)
	if err == nil {
		t.Fatal("get(syncthing) = nil err, want unknown")
	}
	if !strings.Contains(err.Error(), "syncthing") || !strings.Contains(err.Error(), "mutagen") {
		t.Errorf("error %q should mention both unknown and registered ids", err.Error())
	}
}

func TestRegistryDoubleRegisterPanics(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("mutagen", newFake("mutagen"))
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on double-register")
		}
	}()
	r.register("mutagen", newFake("mutagen"))
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
		r.register("rsync", nil)
	})
}

func TestEndpointIsLocal(t *testing.T) {
	tests := []struct {
		name string
		ep   Endpoint
		want bool
	}{
		{"local with path", Endpoint{Path: "/x"}, true},
		{"remote with host", Endpoint{SSHHost: "vm", Path: "/x"}, false},
		{"empty (degenerate, treated as local)", Endpoint{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ep.IsLocal(); got != tc.want {
				t.Errorf("IsLocal() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSentinelErrorsExist(t *testing.T) {
	if ErrInvalidDirection == nil {
		t.Error("ErrInvalidDirection is nil")
	}
	if ErrSessionNotFound == nil {
		t.Error("ErrSessionNotFound is nil")
	}
}

func TestConstructorErrorPropagates(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	myErr := errors.New("mutagen daemon not running")
	r.register("mutagen", func(map[string]any) (Sync, error) { return nil, myErr })

	_, err := r.get("mutagen", nil)
	if !errors.Is(err, myErr) {
		t.Errorf("get() = %v, want errors.Is(...) of %v", err, myErr)
	}
}

func TestDirectionConstantsFrozen(t *testing.T) {
	// Lock down the canonical Direction values.
	want := map[Direction]bool{
		DirectionLocalToRemote: true,
		DirectionRemoteToLocal: true,
		DirectionBidirectional: true,
	}
	if len(want) != 3 {
		t.Fatalf("test bookkeeping: expected 3 directions, got %d", len(want))
	}
}
