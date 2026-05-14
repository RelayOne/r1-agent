package lifecycle

import (
	"context"
	"path/filepath"
	"testing"
)

// Spec: specs/customerio-lifecycle.md §6.1 flagstore_test.go.

func newStore(t *testing.T) *FlagStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFlagStore_OpenCreatesSchema(t *testing.T) {
	s := newStore(t)
	if s.Path() == "" {
		t.Errorf("Path() empty")
	}
	n, err := s.CountAll(context.Background())
	if err != nil {
		t.Fatalf("CountAll: %v", err)
	}
	if n != 0 {
		t.Errorf("CountAll on fresh store = %d, want 0", n)
	}
}

func TestFlagStore_IsFirstTime_FiresOnceThenSuppresses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first, err := s.IsFirstTime(ctx, "acme", EventSignup, "u1")
	if err != nil {
		t.Fatalf("IsFirstTime: %v", err)
	}
	if !first {
		t.Errorf("first call should return true")
	}
	second, err := s.IsFirstTime(ctx, "acme", EventSignup, "u1")
	if err != nil {
		t.Fatalf("IsFirstTime#2: %v", err)
	}
	if second {
		t.Errorf("second call should return false (already fired)")
	}
}

func TestFlagStore_IsFirstTime_PerTripleIndependence(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	// Different tenant → different triple → fires.
	if first, _ := s.IsFirstTime(ctx, "acme", EventSignup, "u1"); !first {
		t.Errorf("acme/signup/u1 first should be true")
	}
	if first, _ := s.IsFirstTime(ctx, "beta", EventSignup, "u1"); !first {
		t.Errorf("beta/signup/u1 first should be true (different tenant)")
	}
	if first, _ := s.IsFirstTime(ctx, "acme", EventActivation, "u1"); !first {
		t.Errorf("acme/activation/u1 first should be true (different event)")
	}
	if first, _ := s.IsFirstTime(ctx, "acme", EventSignup, "u2"); !first {
		t.Errorf("acme/signup/u2 first should be true (different user)")
	}
}

func TestFlagStore_IsFirstTime_UnknownEventErrors(t *testing.T) {
	s := newStore(t)
	_, err := s.IsFirstTime(context.Background(), "acme", "not_a_valid_event", "u1")
	if err == nil {
		t.Errorf("unknown event should error")
	}
}

func TestFlagStore_IsFirstTime_EmptyUserIDErrors(t *testing.T) {
	s := newStore(t)
	_, err := s.IsFirstTime(context.Background(), "acme", EventSignup, "")
	if err == nil {
		t.Errorf("empty userID should error")
	}
}

func TestFlagStore_HasFired_ReadOnly(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	// Initially false.
	fired, err := s.HasFired(ctx, "acme", EventSignup, "u1")
	if err != nil {
		t.Fatalf("HasFired: %v", err)
	}
	if fired {
		t.Errorf("HasFired on empty store should be false")
	}
	// HasFired does NOT insert — IsFirstTime should still return true.
	if first, _ := s.IsFirstTime(ctx, "acme", EventSignup, "u1"); !first {
		t.Errorf("HasFired should not have inserted; IsFirstTime should still fire")
	}
	// Now HasFired returns true.
	if fired, _ := s.HasFired(ctx, "acme", EventSignup, "u1"); !fired {
		t.Errorf("after IsFirstTime, HasFired should be true")
	}
}

func TestFlagStore_DeleteUser_RemovesAllRowsForUser(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	// Seed 3 milestones for u1, 1 for u2.
	_, _ = s.IsFirstTime(ctx, "acme", EventSignup, "u1")
	_, _ = s.IsFirstTime(ctx, "acme", EventActivation, "u1")
	_, _ = s.IsFirstTime(ctx, "beta", EventFirstMission, "u1")
	_, _ = s.IsFirstTime(ctx, "acme", EventSignup, "u2")

	deleted, err := s.DeleteUser(ctx, "u1")
	if err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}
	// u2 still has a row.
	if fired, _ := s.HasFired(ctx, "acme", EventSignup, "u2"); !fired {
		t.Errorf("u2's row was incorrectly deleted")
	}
	// u1 is fully gone — IsFirstTime fires again.
	if first, _ := s.IsFirstTime(ctx, "acme", EventSignup, "u1"); !first {
		t.Errorf("after DSAR delete, u1 should refire signup")
	}
}

func TestFlagStore_DeleteUser_NoRowsForUserReturnsZeroNotError(t *testing.T) {
	s := newStore(t)
	n, err := s.DeleteUser(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if n != 0 {
		t.Errorf("DeleteUser on missing user = %d, want 0", n)
	}
}

func TestFlagStore_DeleteUser_EmptyIDErrors(t *testing.T) {
	s := newStore(t)
	_, err := s.DeleteUser(context.Background(), "  ")
	if err == nil {
		t.Errorf("empty userID should error")
	}
}

func TestFlagStore_SeedLegacyUsers(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	users := []string{"legacy1", "legacy2", "legacy3", "  ", ""} // empties skipped
	n, err := s.SeedLegacyUsers(ctx, "acme", users)
	if err != nil {
		t.Fatalf("SeedLegacyUsers: %v", err)
	}
	if n != 3 {
		t.Errorf("seeded = %d, want 3 (empty IDs skipped)", n)
	}
	// Re-running is idempotent (INSERT OR IGNORE).
	n2, err := s.SeedLegacyUsers(ctx, "acme", users)
	if err != nil {
		t.Fatalf("SeedLegacyUsers#2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second seed = %d, want 0 (idempotent)", n2)
	}
	// Legacy user does NOT refire signup.
	if first, _ := s.IsFirstTime(ctx, "acme", EventSignup, "legacy1"); first {
		t.Errorf("legacy seeded user should not refire signup")
	}
}

func TestFlagStore_NilReceiverSafe(t *testing.T) {
	var s *FlagStore
	if got, err := s.IsFirstTime(context.Background(), "", EventSignup, "u"); got || err == nil {
		t.Errorf("nil IsFirstTime: got (%v, %v); want (false, error)", got, err)
	}
	if got, err := s.HasFired(context.Background(), "", EventSignup, "u"); got || err == nil {
		t.Errorf("nil HasFired: got (%v, %v); want (false, error)", got, err)
	}
	if n, err := s.DeleteUser(context.Background(), "u"); n != 0 || err == nil {
		t.Errorf("nil DeleteUser: got (%d, %v); want (0, error)", n, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("nil Close returned %v", err)
	}
	if p := s.Path(); p != "" {
		t.Errorf("nil Path = %q, want empty", p)
	}
}

func TestFlagStore_ConcurrentFiringSerialisedByMutex(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const N = 20
	done := make(chan bool, N)
	for i := 0; i < N; i++ {
		go func() {
			first, err := s.IsFirstTime(ctx, "acme", EventSignup, "race-user")
			if err != nil {
				t.Errorf("IsFirstTime: %v", err)
				done <- false
				return
			}
			done <- first
		}()
	}
	var trueCount, falseCount int
	for i := 0; i < N; i++ {
		if <-done {
			trueCount++
		} else {
			falseCount++
		}
	}
	if trueCount != 1 {
		t.Errorf("trueCount = %d, want exactly 1 (the FlagStore guards against double-fire)", trueCount)
	}
	if falseCount != N-1 {
		t.Errorf("falseCount = %d, want %d", falseCount, N-1)
	}
}

func TestFirstTimeEvents_ReturnsClosedSet(t *testing.T) {
	got := FirstTimeEvents()
	want := []string{EventSignup, EventActivation, EventFirstMission, EventFirstCompletion}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
	// Defensive-copy assertion.
	got[0] = "MUTATED"
	again := FirstTimeEvents()
	if again[0] == "MUTATED" {
		t.Errorf("FirstTimeEvents should return a defensive copy")
	}
}

func TestOpen_EmptyPathErrors(t *testing.T) {
	_, err := Open("   ")
	if err == nil {
		t.Errorf("Open(\"\") should error")
	}
}
