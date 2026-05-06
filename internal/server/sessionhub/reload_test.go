package sessionhub

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/journal"
)

// TestDaemonStart_ReplaysSessions exercises the spec §11.27 happy
// path:
//
//   1. Build a sessions-index.json with three entries (one deleted).
//   2. Build per-session journals with several records each.
//   3. Spin up a fresh SessionHub, call Reload.
//   4. Assert: two non-deleted sessions land in the hub at state
//      paused-reattachable; the deleted session is skipped; LastSeq
//      and RecordCount match the seeded journals.
//   5. Assert EmitDaemonReloaded delivers the event to a subscriber
//      with Custom["sessions"] containing the reloaded ids.
func TestDaemonStart_ReplaysSessions(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	tmp := t.TempDir()
	idxPath := filepath.Join(tmp, "sessions-index.json")
	journalDir := filepath.Join(tmp, "sessions")

	// Seed the index + journals.
	si, _ := NewSessionsIndexAt(idxPath)
	for _, id := range []string{"s-1", "s-2", "s-3"} {
		jp := filepath.Join(journalDir, id+".jsonl")
		if err := si.Append(IndexEntry{
			ID: id, Workdir: tmp, JournalPath: jp, Model: "m",
		}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
		// Three records per journal for the live ones; skip s-3 to
		// also exercise the no-journal-file branch.
		if id == "s-3" {
			continue
		}
		w, err := journal.OpenWriter(jp, journal.WriterOptions{})
		if err != nil {
			t.Fatalf("OpenWriter %s: %v", id, err)
		}
		for i := 0; i < 3; i++ {
			if _, err := w.Append("hub.event", map[string]int{"i": i}); err != nil {
				t.Fatalf("Append journal %s: %v", id, err)
			}
		}
		_ = w.Close()
	}
	// Mark s-2 deleted.
	if err := si.MarkDeleted("s-2"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	// Fresh hub, configure index and reload.
	hub2, _ := NewHub()
	hub2.SetSessionsIndex(si)
	hub2.SetJournalDir(journalDir)

	results, err := hub2.Reload(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len: got %d, want 2 (s-1 + s-3, s-2 deleted)", len(results))
	}
	// s-1: 3 records, LastSeq=3.
	r1 := findResult(results, "s-1")
	if r1 == nil {
		t.Fatalf("missing s-1 in results")
	}
	if r1.RecordCount != 3 || r1.LastSeq != 3 || r1.Err != nil {
		t.Errorf("s-1: got count=%d seq=%d err=%v, want 3/3/nil", r1.RecordCount, r1.LastSeq, r1.Err)
	}
	// s-3: no journal file -> 0 records, no error (absent journal is
	// "empty", per journal.Replay spec).
	r3 := findResult(results, "s-3")
	if r3 == nil {
		t.Fatalf("missing s-3 in results")
	}
	if r3.RecordCount != 0 || r3.LastSeq != 0 || r3.Err != nil {
		t.Errorf("s-3: got count=%d seq=%d err=%v, want 0/0/nil", r3.RecordCount, r3.LastSeq, r3.Err)
	}
	// Hub now has s-1 and s-3 in paused-reattachable state.
	for _, id := range []string{"s-1", "s-3"} {
		s, gerr := hub2.Get(id)
		if gerr != nil {
			t.Errorf("Get(%s): %v", id, gerr)
			continue
		}
		if s.State != SessionStatePausedReattachable {
			t.Errorf("%s state: got %q, want %q", id, s.State, SessionStatePausedReattachable)
		}
	}
	// s-2 must NOT be in the hub.
	if _, err := hub2.Get("s-2"); err == nil {
		t.Errorf("Get(s-2): unexpected hit; deleted entry should not reload")
	}

	// EmitDaemonReloaded — subscribe a wildcard observer and
	// verify it receives the event with Custom["sessions"].
	bus := hub.New()
	var got hub.Event
	var seen atomic.Int32
	var mu sync.Mutex
	bus.Register(hub.Subscriber{
		ID:     "test-listener",
		Events: []hub.EventType{hub.EventDaemonReloaded},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			mu.Lock()
			got = *ev
			mu.Unlock()
			seen.Add(1)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
	ids := []string{"s-1", "s-3"}
	EmitDaemonReloaded(bus, ids)
	// EmitAsync returns immediately; spin briefly waiting for the
	// observe goroutine to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && seen.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if seen.Load() == 0 {
		t.Fatalf("daemon.reloaded never delivered")
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Type != hub.EventDaemonReloaded {
		t.Errorf("event type: got %q, want %q", got.Type, hub.EventDaemonReloaded)
	}
	if got.Custom == nil {
		t.Fatalf("event Custom: nil")
	}
	gotIDs, _ := got.Custom["sessions"].([]string)
	if len(gotIDs) != 2 || gotIDs[0] != "s-1" || gotIDs[1] != "s-3" {
		t.Errorf("custom.sessions: got %v, want [s-1 s-3]", gotIDs)
	}
}

// TestReload_BumpsIDCounter asserts that after Reload, a freshly
// minted id from Create skips past every reloaded `s-N` id. Without
// this, a daemon restart could mint `s-1` again and collide.
func TestReload_BumpsIDCounter(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	tmp := t.TempDir()
	idxPath := filepath.Join(tmp, "sessions-index.json")
	si, _ := NewSessionsIndexAt(idxPath)
	for _, id := range []string{"s-1", "s-7", "s-3"} {
		jp := filepath.Join(tmp, id+".jsonl")
		_ = si.Append(IndexEntry{ID: id, Workdir: tmp, JournalPath: jp})
	}
	hub2, _ := NewHub()
	hub2.SetSessionsIndex(si)
	hub2.SetJournalDir(tmp)
	if _, err := hub2.Reload(context.Background(), nil); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// First fresh Create should mint s-8, NOT s-1 / s-2.
	wd := t.TempDir()
	s, err := hub2.Create(CreateOptions{Workdir: wd})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID != "s-8" {
		t.Fatalf("minted ID: got %q, want s-8", s.ID)
	}
}

// TestReload_NoIndexConfigured returns nil with no error — the
// first-run path.
func TestReload_NoIndexConfigured(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub2, _ := NewHub()
	results, err := hub2.Reload(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results: got %d, want 0", len(results))
	}
}

// TestReload_HandlerInvoked asserts the optional ReplayHandler fires
// per-record with the session id.
func TestReload_HandlerInvoked(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	tmp := t.TempDir()
	idxPath := filepath.Join(tmp, "sessions-index.json")
	si, _ := NewSessionsIndexAt(idxPath)
	jp := filepath.Join(tmp, "s-1.jsonl")
	_ = si.Append(IndexEntry{ID: "s-1", Workdir: tmp, JournalPath: jp})
	w, _ := journal.OpenWriter(jp, journal.WriterOptions{})
	for i := 0; i < 4; i++ {
		_, _ = w.Append("e", i)
	}
	_ = w.Close()

	hub2, _ := NewHub()
	hub2.SetSessionsIndex(si)
	hub2.SetJournalDir(tmp)

	type call struct {
		id  string
		seq uint64
	}
	var calls []call
	handler := func(id string, rec journal.Record) error {
		calls = append(calls, call{id: id, seq: rec.Seq})
		return nil
	}
	if _, err := hub2.Reload(context.Background(), handler); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("handler calls: got %d, want 4", len(calls))
	}
	for i, c := range calls {
		if c.id != "s-1" {
			t.Errorf("calls[%d].id: got %q", i, c.id)
		}
		if c.seq != uint64(i+1) {
			t.Errorf("calls[%d].seq: got %d, want %d", i, c.seq, i+1)
		}
	}
}

// findResult is a tiny linear lookup used by assertion code.
func findResult(rs []ReloadResult, id string) *ReloadResult {
	for i := range rs {
		if rs[i].ID == id {
			return &rs[i]
		}
	}
	return nil
}
