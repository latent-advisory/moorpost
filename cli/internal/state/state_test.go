package state

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewHasMachineID(t *testing.T) {
	s := New()
	if s.MachineID == "" {
		t.Fatal("New() did not generate a machine_id")
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestOpenMissingReturnsFreshState(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.MachineID == "" {
		t.Fatal("Open of missing file did not generate machine_id")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s := New()
	s.TelemetryOptIn = true
	s.SetProject("/Users/x/argus", ProjectState{
		Slug:           "argus",
		VMID:           "argus-vm-1",
		VMZone:         "us-central1-a",
		ActiveSide:     SideLocal,
		AgentSessionID: "abc",
	})
	s.VMs["argus-vm-1"] = VMRecord{
		Provider:       "gcp",
		ExternalIP:     "35.1.2.3",
		StateCache:     "stopped",
		StateCacheAt:   time.Now().UTC().Truncate(time.Second),
		MonthToDateUSD: 1.42,
	}
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if s2.MachineID != s.MachineID {
		t.Errorf("machine_id round-trip: %q -> %q", s.MachineID, s2.MachineID)
	}
	if !s2.TelemetryOptIn {
		t.Error("TelemetryOptIn lost in round-trip")
	}
	if got, ok := s2.GetProject("/Users/x/argus"); !ok || got.Slug != "argus" {
		t.Errorf("project round-trip: ok=%v slug=%q", ok, got.Slug)
	}
	if vm := s2.VMs["argus-vm-1"]; vm.ExternalIP != "35.1.2.3" {
		t.Errorf("vm round-trip lost external_ip: %v", vm)
	}
}

func TestMachineIDStableAcrossSaves(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	originalID := s.MachineID
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatalf("Open after save: %v", err)
	}
	if s2.MachineID != originalID {
		t.Errorf("machine_id changed across reload: %q -> %q", originalID, s2.MachineID)
	}
}

func TestSaveRejectsCorruptDir(t *testing.T) {
	// Create a file where a directory is expected.
	root := t.TempDir()
	conflict := filepath.Join(root, "blocked")
	if err := os.WriteFile(conflict, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	s := New()
	if err := s.Save(filepath.Join(conflict, "state.json")); err == nil {
		t.Error("Save did not error when parent dir was a file")
	}
}

func TestUnsupportedSchemaVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	bad := []byte(`{"schema_version": 99}`)
	if err := os.WriteFile(p, bad, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Open(p)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("Open returned %v, want ErrUnsupportedSchema", err)
	}
}

func TestWithLockSerializesWriters(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")

	const N = 10
	var inCritical int32 // number of goroutines currently inside the locked region
	var maxConcurrent int32
	var totalEntered int32
	var wg sync.WaitGroup
	wg.Add(N)
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			err := WithLock(p, func(s *State) error {
				now := atomic.AddInt32(&inCritical, 1)
				// Track the maximum concurrency seen inside the critical
				// section. A correctly-locked region should keep this at 1.
				for {
					prev := atomic.LoadInt32(&maxConcurrent)
					if now <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, now) {
						break
					}
				}
				atomic.AddInt32(&totalEntered, 1)
				// Tiny sleep to widen the window so violations would be
				// detected; passes in <1s on any modern machine.
				time.Sleep(2 * time.Millisecond)
				atomic.AddInt32(&inCritical, -1)

				proj := s.Projects["/p"]
				proj.Slug = "argus"
				s.SetProject("/p", proj)
				return nil
			})
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("WithLock returned error: %v", err)
	}
	if maxConcurrent != 1 {
		t.Errorf("maxConcurrent inside critical section = %d, want 1 (lock not exclusive)", maxConcurrent)
	}
	if totalEntered != N {
		t.Errorf("totalEntered = %d, want %d", totalEntered, N)
	}
	final, err := Open(p)
	if err != nil {
		t.Fatalf("final Open: %v", err)
	}
	if proj, ok := final.GetProject("/p"); !ok || proj.Slug != "argus" {
		t.Errorf("final state missing or wrong: ok=%v slug=%q", ok, proj.Slug)
	}
}

func TestWithLockNoSaveOnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")

	// Write a baseline.
	if err := WithLock(p, func(s *State) error {
		s.SetProject("/p", ProjectState{Slug: "before"})
		return nil
	}); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	myErr := errors.New("mutation rejected")
	if err := WithLock(p, func(s *State) error {
		s.SetProject("/p", ProjectState{Slug: "after"})
		return myErr
	}); !errors.Is(err, myErr) {
		t.Fatalf("WithLock did not propagate error: %v", err)
	}
	final, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	proj, _ := final.GetProject("/p")
	if proj.Slug != "before" {
		t.Errorf("slug = %q, want 'before' (changes should not persist on error)", proj.Slug)
	}
}

func TestRecordHandoffAndReturn(t *testing.T) {
	s := New()
	now := time.Now().UTC().Truncate(time.Second)
	s.SetProject("/p", ProjectState{Slug: "argus", ActiveSide: SideLocal})
	s.RecordHandoff("/p", now)
	if got, _ := s.GetProject("/p"); got.ActiveSide != SideRemote || !got.LastHandoff.Equal(now) {
		t.Errorf("after RecordHandoff: %+v", got)
	}
	later := now.Add(time.Hour)
	s.RecordReturn("/p", later)
	if got, _ := s.GetProject("/p"); got.ActiveSide != SideLocal || !got.LastReturn.Equal(later) {
		t.Errorf("after RecordReturn: %+v", got)
	}
}

func TestSetActiveSide(t *testing.T) {
	s := New()
	s.SetActiveSide("/p", SideRemote)
	if got, _ := s.GetProject("/p"); got.ActiveSide != SideRemote {
		t.Errorf("SetActiveSide didn't stick: %+v", got)
	}
}

func TestOpenFileWithMissingMaps(t *testing.T) {
	// Hand-rolled minimal valid file: schema_version + machine_id only.
	// Open should backfill empty Projects/VMs maps so callers don't crash on
	// nil-map writes.
	p := filepath.Join(t.TempDir(), "state.json")
	minimal := []byte(`{"schema_version": 1, "machine_id": "test-uuid"}`)
	if err := os.WriteFile(p, minimal, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Projects == nil {
		t.Error("Projects map is nil after Open")
	}
	if s.VMs == nil {
		t.Error("VMs map is nil after Open")
	}
	// Smoke: should not panic on writes.
	s.SetProject("/x", ProjectState{Slug: "test"})
	if got, _ := s.GetProject("/x"); got.Slug != "test" {
		t.Errorf("set/get failed: %+v", got)
	}
}

func TestSavePermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s := New()
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("file permissions = %o, want 0600 (state may contain credential references)", mode)
	}
}

func TestCorruptStateFileIsAClearError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte("not-json{{{{"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Open(p)
	if err == nil {
		t.Fatal("Open accepted garbage JSON")
	}
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
