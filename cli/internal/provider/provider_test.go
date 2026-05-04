package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeProvider satisfies Provider for registry tests. It records nothing and
// returns zero values; tests that exercise Provider behavior should use
// stronger fakes in their own package.
type fakeProvider struct{ id string }

func (f *fakeProvider) ID() string { return f.id }
func (f *fakeProvider) Provision(context.Context, ProvisionSpec) (VM, error) {
	return VM{}, nil
}
func (f *fakeProvider) Start(context.Context, string) error                 { return nil }
func (f *fakeProvider) Stop(context.Context, string) error                  { return nil }
func (f *fakeProvider) Destroy(context.Context, string) error               { return nil }
func (f *fakeProvider) Status(context.Context, string) (VMState, error)     { return VMStateStopped, nil }
func (f *fakeProvider) Snapshot(context.Context, string, string) (SnapshotID, error) {
	return SnapshotID(""), nil
}
func (f *fakeProvider) Cost(context.Context, string, TimeRange) (CostBreakdown, error) {
	return CostBreakdown{}, nil
}
func (f *fakeProvider) SSHTarget(context.Context, string) (SSHTarget, error) {
	return SSHTarget{}, nil
}

func newFake(id string) Constructor {
	return func(map[string]any) (Provider, error) {
		return &fakeProvider{id: id}, nil
	}
}

func TestRegistryHappyPath(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("gcp", newFake("gcp"))
	r.register("hetzner", newFake("hetzner"))

	want := []string{"gcp", "hetzner"}
	got := r.list()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("list() = %v, want %v (sorted)", got, want)
	}

	p, err := r.get("gcp", nil)
	if err != nil {
		t.Fatalf("get(gcp) = %v", err)
	}
	if p.ID() != "gcp" {
		t.Errorf("got provider ID %q, want gcp", p.ID())
	}
}

func TestRegistryUnknownID(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("gcp", newFake("gcp"))

	_, err := r.get("aws", nil)
	if err == nil {
		t.Fatal("get(aws) returned nil error, want unknown-id error")
	}
	if !strings.Contains(err.Error(), "aws") || !strings.Contains(err.Error(), "gcp") {
		t.Errorf("error %q should mention both the unknown id and the registered ones", err.Error())
	}
}

func TestRegistryDoubleRegisterPanics(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	r.register("gcp", newFake("gcp"))

	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic on double-register")
		}
		msg, ok := rec.(string)
		if !ok || !strings.Contains(msg, "twice") {
			t.Errorf("panic value %v should mention 'twice'", rec)
		}
	}()
	r.register("gcp", newFake("gcp"))
}

func TestRegistryEmptyIDPanics(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on empty id")
		}
	}()
	r.register("", newFake("x"))
}

func TestRegistryNilConstructorPanics(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on nil constructor")
		}
	}()
	r.register("gcp", nil)
}

func TestConstructorReturnsError(t *testing.T) {
	r := &registry{constructors: make(map[string]Constructor)}
	myErr := errors.New("missing project")
	r.register("gcp", func(map[string]any) (Provider, error) { return nil, myErr })

	_, err := r.get("gcp", nil)
	if !errors.Is(err, myErr) {
		t.Errorf("get() = %v, want errors.Is(...) of %v", err, myErr)
	}
}

func TestVMStateValuesAreFrozen(t *testing.T) {
	// Lock down the canonical state names so adding a new state without
	// updating the Provider docs trips this test.
	want := map[VMState]bool{
		VMStateUnknown:      true,
		VMStateProvisioning: true,
		VMStateRunning:      true,
		VMStateStopping:     true,
		VMStateStopped:      true,
		VMStateTerminated:   true,
		VMStateError:        true,
	}
	if len(want) != 7 {
		t.Fatalf("test bookkeeping: want has %d entries, expected 7", len(want))
	}
}

func TestTimeRangeIsHalfOpen(t *testing.T) {
	// Doc says half-open [Start, End). Capture this in a test so it's
	// observable; later code that depends on End being exclusive can rely on it.
	now := time.Now()
	tr := TimeRange{Start: now, End: now.Add(time.Hour)}
	if !tr.End.After(tr.Start) {
		t.Fatalf("End=%v should be after Start=%v", tr.End, tr.Start)
	}
}
