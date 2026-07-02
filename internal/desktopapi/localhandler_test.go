package desktopapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/memory/membus"
	"github.com/RelayOne/r1/internal/stokerr"
)

// ---------------------------------------------------------------------
// Ledger — real backend (internal/ledger) through LocalHandler
// ---------------------------------------------------------------------

func TestLocalHandler_Ledger_Real(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	root := filepath.Join(t.TempDir(), "ledger")
	lg, err := ledger.New(root)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer lg.Close()

	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	id1, err := lg.AddNode(ctx, ledger.Node{
		Type:          "task",
		SchemaVersion: 1,
		MissionID:     "sess-1",
		CreatedBy:     "tester",
		CreatedAt:     base,
		Content:       json.RawMessage(`{"title":"first"}`),
	})
	if err != nil {
		t.Fatalf("AddNode id1: %v", err)
	}
	id2, err := lg.AddNode(ctx, ledger.Node{
		Type:          "decision",
		SchemaVersion: 1,
		MissionID:     "sess-1",
		CreatedBy:     "tester",
		CreatedAt:     base.Add(time.Minute),
		Content:       json.RawMessage(`{"choice":"go"}`),
	})
	if err != nil {
		t.Fatalf("AddNode id2: %v", err)
	}
	// A node in a different session so session filtering has something to exclude.
	if _, err := lg.AddNode(ctx, ledger.Node{
		Type:          "task",
		SchemaVersion: 1,
		MissionID:     "sess-2",
		CreatedBy:     "tester",
		CreatedAt:     base.Add(2 * time.Minute),
		Content:       json.RawMessage(`{"title":"other-session"}`),
	}); err != nil {
		t.Fatalf("AddNode other: %v", err)
	}
	if err := lg.AddEdge(ctx, ledger.Edge{From: id2, To: id1, Type: ledger.EdgeReferences}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	// Read through a *ledger.Store on the same on-disk root — exactly how
	// the production handler is wired.
	store, err := ledger.NewStore(root)
	if err != nil {
		t.Fatalf("ledger.NewStore: %v", err)
	}
	h := &LocalHandler{Ledger: store}

	t.Run("get_node returns real payload + edges", func(t *testing.T) {
		node, err := h.LedgerGetNode(ctx, LedgerGetNodeRequest{Hash: id2})
		if err != nil {
			t.Fatalf("LedgerGetNode: %v", err)
		}
		if node.Hash != id2 {
			t.Errorf("hash = %q, want %q", node.Hash, id2)
		}
		if node.NodeType != "decision" {
			t.Errorf("type = %q, want decision", node.NodeType)
		}
		if got := node.Payload["choice"]; got != "go" {
			t.Errorf("payload.choice = %v, want go", got)
		}
		if len(node.Edges) != 1 {
			t.Fatalf("edges = %d, want 1 (%+v)", len(node.Edges), node.Edges)
		}
		if node.Edges[0].To != id1 || node.Edges[0].Kind != "references" {
			t.Errorf("edge = %+v, want {To:%s, Kind:references}", node.Edges[0], id1)
		}
	})

	t.Run("get_node missing hash is not_found", func(t *testing.T) {
		_, err := h.LedgerGetNode(ctx, LedgerGetNodeRequest{Hash: "deadbeef"})
		if err == nil {
			t.Fatal("expected error for missing node")
		}
		if !stokerr.HasCode(err, stokerr.ErrNotFound) {
			t.Errorf("err code = %v, want not_found", err)
		}
	})

	t.Run("list_events session-scoped, newest-first", func(t *testing.T) {
		resp, err := h.LedgerListEvents(ctx, LedgerListEventsRequest{SessionID: "sess-1"})
		if err != nil {
			t.Fatalf("LedgerListEvents: %v", err)
		}
		if len(resp.Events) != 2 {
			t.Fatalf("events = %d, want 2 (%+v)", len(resp.Events), resp.Events)
		}
		// Newest-first: id2 (base+1m) before id1 (base).
		if resp.Events[0].Hash != id2 || resp.Events[1].Hash != id1 {
			t.Errorf("order = [%s,%s], want [%s,%s]",
				resp.Events[0].Hash, resp.Events[1].Hash, id2, id1)
		}
		if resp.Events[0].NodeType != "decision" {
			t.Errorf("event[0].type = %q, want decision", resp.Events[0].NodeType)
		}
	})

	t.Run("list_events unscoped sees all sessions", func(t *testing.T) {
		resp, err := h.LedgerListEvents(ctx, LedgerListEventsRequest{})
		if err != nil {
			t.Fatalf("LedgerListEvents: %v", err)
		}
		if len(resp.Events) != 3 {
			t.Errorf("events = %d, want 3", len(resp.Events))
		}
	})

	t.Run("list_events limit sets NextCursor", func(t *testing.T) {
		resp, err := h.LedgerListEvents(ctx, LedgerListEventsRequest{Limit: 1})
		if err != nil {
			t.Fatalf("LedgerListEvents: %v", err)
		}
		if len(resp.Events) != 1 {
			t.Fatalf("events = %d, want 1", len(resp.Events))
		}
		if resp.NextCursor == "" {
			t.Error("NextCursor must be set when more events remain")
		}
	})
}

func TestLocalHandler_Ledger_NilBackend(t *testing.T) {
	t.Parallel()
	h := &LocalHandler{}
	if _, err := h.LedgerGetNode(context.Background(), LedgerGetNodeRequest{Hash: "x"}); !IsNotImplemented(err) {
		t.Errorf("nil ledger get_node err = %v, want not_implemented", err)
	}
	if _, err := h.LedgerListEvents(context.Background(), LedgerListEventsRequest{}); !IsNotImplemented(err) {
		t.Errorf("nil ledger list_events err = %v, want not_implemented", err)
	}
}

// ---------------------------------------------------------------------
// Memory — real backend (internal/memory/membus) through LocalHandler
// ---------------------------------------------------------------------

func openTestBus(t *testing.T) *membus.Bus {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	b, err := membus.NewBus(db, membus.Options{})
	if err != nil {
		t.Fatalf("membus.NewBus: %v", err)
	}
	t.Cleanup(func() { _ = b.Close(); _ = db.Close() })
	return b
}

func TestLocalHandler_Memory_Real(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bus := openTestBus(t)

	for _, kv := range []struct{ key, val string }{
		{"alpha:1", "first"},
		{"alpha:2", "second"},
		{"beta:1", "third"},
	} {
		if err := bus.Remember(ctx, membus.RememberRequest{
			Scope:   membus.ScopeSession,
			Author:  "system",
			Key:     kv.key,
			Content: kv.val,
		}); err != nil {
			t.Fatalf("Remember %s: %v", kv.key, err)
		}
	}

	h := &LocalHandler{Memory: bus}

	t.Run("list_scopes returns the canonical five", func(t *testing.T) {
		resp, err := h.MemoryListScopes(ctx)
		if err != nil {
			t.Fatalf("MemoryListScopes: %v", err)
		}
		if len(resp.Scopes) != 5 {
			t.Errorf("scopes = %d, want 5", len(resp.Scopes))
		}
	})

	t.Run("query returns real rows", func(t *testing.T) {
		resp, err := h.MemoryQuery(ctx, MemoryQueryRequest{Scope: MemoryScopeSession})
		if err != nil {
			t.Fatalf("MemoryQuery: %v", err)
		}
		if len(resp.Entries) != 3 {
			t.Fatalf("entries = %d, want 3 (%+v)", len(resp.Entries), resp.Entries)
		}
		// Value must be the stored content, keyed by the dedup key.
		byKey := map[string]string{}
		for _, e := range resp.Entries {
			byKey[e.Key] = e.Value
		}
		if byKey["alpha:1"] != "first" || byKey["beta:1"] != "third" {
			t.Errorf("entries content mismatch: %+v", byKey)
		}
	})

	t.Run("query key-prefix filters", func(t *testing.T) {
		resp, err := h.MemoryQuery(ctx, MemoryQueryRequest{Scope: MemoryScopeSession, KeyPrefix: "alpha:"})
		if err != nil {
			t.Fatalf("MemoryQuery prefix: %v", err)
		}
		if len(resp.Entries) != 2 {
			t.Errorf("prefixed entries = %d, want 2", len(resp.Entries))
		}
		for _, e := range resp.Entries {
			if e.Key[:6] != "alpha:" {
				t.Errorf("unexpected key %q for prefix alpha:", e.Key)
			}
		}
	})

	t.Run("query empty scope returns no rows", func(t *testing.T) {
		resp, err := h.MemoryQuery(ctx, MemoryQueryRequest{Scope: MemoryScopeGlobal})
		if err != nil {
			t.Fatalf("MemoryQuery global: %v", err)
		}
		if len(resp.Entries) != 0 {
			t.Errorf("global entries = %d, want 0", len(resp.Entries))
		}
	})

	t.Run("query unknown scope is validation error", func(t *testing.T) {
		_, err := h.MemoryQuery(ctx, MemoryQueryRequest{Scope: MemoryScope("Bogus")})
		if !stokerr.HasCode(err, stokerr.ErrValidation) {
			t.Errorf("err = %v, want validation", err)
		}
	})

	t.Run("query truncation flag", func(t *testing.T) {
		resp, err := h.MemoryQuery(ctx, MemoryQueryRequest{Scope: MemoryScopeSession, Limit: 2})
		if err != nil {
			t.Fatalf("MemoryQuery limit: %v", err)
		}
		if len(resp.Entries) != 2 {
			t.Errorf("entries = %d, want 2", len(resp.Entries))
		}
		if !resp.Truncated {
			t.Error("Truncated must be true when more rows exist than the limit")
		}
	})
}

func TestLocalHandler_Memory_NilBackend(t *testing.T) {
	t.Parallel()
	h := &LocalHandler{}
	// list_scopes is always answerable (fixed contract list).
	if _, err := h.MemoryListScopes(context.Background()); err != nil {
		t.Errorf("MemoryListScopes with nil backend err = %v, want nil", err)
	}
	if _, err := h.MemoryQuery(context.Background(), MemoryQueryRequest{Scope: MemoryScopeSession}); !IsNotImplemented(err) {
		t.Errorf("nil memory query err = %v, want not_implemented", err)
	}
}

// ---------------------------------------------------------------------
// Cost — real backend (internal/costtrack) through LocalHandler
// ---------------------------------------------------------------------

func TestLocalHandler_Cost_Real(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tr := costtrack.NewTracker(0, nil)
	tr.Record("claude-sonnet-4", "t1", 100, 40, 0, 0)
	tr.Record("claude-sonnet-4", "t2", 200, 60, 0, 0)

	h := &LocalHandler{
		Cost: tr,
		Now:  func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) },
	}

	t.Run("get_current aggregates tokens", func(t *testing.T) {
		snap, err := h.CostGetCurrent(ctx, CostGetCurrentRequest{})
		if err != nil {
			t.Fatalf("CostGetCurrent: %v", err)
		}
		if snap.TokensIn != 300 {
			t.Errorf("tokens_in = %d, want 300", snap.TokensIn)
		}
		if snap.TokensOut != 100 {
			t.Errorf("tokens_out = %d, want 100", snap.TokensOut)
		}
		if snap.USD < 0 {
			t.Errorf("usd = %v, want >= 0", snap.USD)
		}
		if snap.AsOf != "2026-07-01T12:00:00Z" {
			t.Errorf("as_of = %q, want injected clock value", snap.AsOf)
		}
	})

	t.Run("get_history buckets real records", func(t *testing.T) {
		resp, err := h.CostGetHistory(ctx, CostGetHistoryRequest{Bucket: "hour"})
		if err != nil {
			t.Fatalf("CostGetHistory: %v", err)
		}
		// Both records were recorded ~now → a single hour bucket summing
		// all tokens (100+40 + 200+60 = 400).
		if len(resp.Buckets) != 1 {
			t.Fatalf("buckets = %d, want 1 (%+v)", len(resp.Buckets), resp.Buckets)
		}
		if resp.Buckets[0].Tokens != 400 {
			t.Errorf("bucket tokens = %d, want 400", resp.Buckets[0].Tokens)
		}
	})

	t.Run("get_history rejects bad bucket", func(t *testing.T) {
		_, err := h.CostGetHistory(ctx, CostGetHistoryRequest{Bucket: "fortnight"})
		if !stokerr.HasCode(err, stokerr.ErrValidation) {
			t.Errorf("err = %v, want validation", err)
		}
	})
}

// fakeCost lets us pin record timestamps for multi-bucket coverage that a
// live tracker (Timestamp = time.Now) cannot exercise deterministically.
type fakeCost struct {
	total   float64
	in, out int
	records []costtrack.Usage
}

func (f *fakeCost) Total() float64 { return f.total }
func (f *fakeCost) TokenTotals() (int, int, int, int) {
	return f.in, f.out, 0, 0
}
func (f *fakeCost) Records() []costtrack.Usage { return f.records }

func TestLocalHandler_Cost_HistoryBucketing(t *testing.T) {
	t.Parallel()
	h := &LocalHandler{Cost: &fakeCost{
		records: []costtrack.Usage{
			{InputTokens: 10, OutputTokens: 5, Cost: 0.01, Timestamp: time.Date(2026, 7, 1, 9, 15, 0, 0, time.UTC)},
			{InputTokens: 20, OutputTokens: 5, Cost: 0.02, Timestamp: time.Date(2026, 7, 1, 9, 45, 0, 0, time.UTC)},
			{InputTokens: 30, OutputTokens: 5, Cost: 0.03, Timestamp: time.Date(2026, 7, 1, 11, 5, 0, 0, time.UTC)},
		},
	}}

	resp, err := h.CostGetHistory(context.Background(), CostGetHistoryRequest{Bucket: "hour"})
	if err != nil {
		t.Fatalf("CostGetHistory: %v", err)
	}
	if len(resp.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2 (9:00 and 11:00) %+v", len(resp.Buckets), resp.Buckets)
	}
	// Oldest-first.
	if resp.Buckets[0].At != "2026-07-01T09:00:00Z" {
		t.Errorf("bucket[0].At = %q, want 09:00", resp.Buckets[0].At)
	}
	// The 09:00 bucket merges the two 9:xx records: 10+5 + 20+5 = 40 tokens.
	if resp.Buckets[0].Tokens != 40 {
		t.Errorf("bucket[0].tokens = %d, want 40", resp.Buckets[0].Tokens)
	}
	if resp.Buckets[1].At != "2026-07-01T11:00:00Z" {
		t.Errorf("bucket[1].At = %q, want 11:00", resp.Buckets[1].At)
	}

	// A `since` cutoff drops the 9:xx bucket entirely.
	resp2, err := h.CostGetHistory(context.Background(), CostGetHistoryRequest{
		Bucket: "hour",
		Since:  "2026-07-01T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("CostGetHistory since: %v", err)
	}
	if len(resp2.Buckets) != 1 {
		t.Fatalf("since buckets = %d, want 1", len(resp2.Buckets))
	}
}

func TestLocalHandler_Cost_NilBackend(t *testing.T) {
	t.Parallel()
	h := &LocalHandler{}
	if _, err := h.CostGetCurrent(context.Background(), CostGetCurrentRequest{}); !IsNotImplemented(err) {
		t.Errorf("nil cost get_current err = %v, want not_implemented", err)
	}
	if _, err := h.CostGetHistory(context.Background(), CostGetHistoryRequest{}); !IsNotImplemented(err) {
		t.Errorf("nil cost get_history err = %v, want not_implemented", err)
	}
}

// ---------------------------------------------------------------------
// Unimplemented verbs stay per-verb not_implemented (audit A029), never
// -32601 method_not_found — and every message names the missing dep.
// ---------------------------------------------------------------------

func TestLocalHandler_UnimplementedVerbs_NotImplemented(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := &LocalHandler{
		Ledger: nil, // irrelevant; these verbs never touch a backend
	}

	cases := map[string]func() error{
		"SessionStart":           func() error { _, e := h.SessionStart(ctx, SessionStartRequest{Prompt: "x"}); return e },
		"SessionPause":           func() error { _, e := h.SessionPause(ctx, SessionIDRequest{SessionID: "s"}); return e },
		"SessionResume":          func() error { _, e := h.SessionResume(ctx, SessionIDRequest{SessionID: "s"}); return e },
		"DescentCurrentTier":     func() error { _, e := h.DescentCurrentTier(ctx, DescentCurrentTierRequest{SessionID: "s"}); return e },
		"DescentTierHistory":     func() error { _, e := h.DescentTierHistory(ctx, DescentTierHistoryRequest{SessionID: "s", ACID: "a"}); return e },
		"SessionLanesList":       func() error { _, e := h.SessionLanesList(ctx, SessionLanesListRequest{SessionID: "s"}); return e },
		"SessionLanesSubscribe":  func() error { _, e := h.SessionLanesSubscribe(ctx, SessionLanesSubscribeRequest{SessionID: "s"}); return e },
		"SessionLanesUnsubscribe": func() error { _, e := h.SessionLanesUnsubscribe(ctx, SessionLanesUnsubscribeRequest{SubscriptionID: "x"}); return e },
		"SessionLanesKill":       func() error { _, e := h.SessionLanesKill(ctx, SessionLanesKillRequest{SessionID: "s", LaneID: "l"}); return e },
		"SessionSetWorkdir":      func() error { _, e := h.SessionSetWorkdir(ctx, SessionSetWorkdirRequest{SessionID: "s", Workdir: "/w"}); return e },
		"DaemonStatus":           func() error { _, e := h.DaemonStatus(ctx); return e },
		"DaemonShutdown":         func() error { _, e := h.DaemonShutdown(ctx, DaemonShutdownRequest{}); return e },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !IsNotImplemented(err) {
				t.Fatalf("%s err = %v, want not_implemented (errors.Is ErrNotImplemented)", name, err)
			}
			if !stokerr.HasCode(err, errNotImplementedCode) {
				t.Errorf("%s not tagged not_implemented code", name)
			}
			// Must carry a specific, non-empty message (names the missing dep).
			if err.Error() == ErrNotImplemented.Error() {
				t.Errorf("%s returned the blanket sentinel message; want a verb-specific message", name)
			}
		})
	}
}
