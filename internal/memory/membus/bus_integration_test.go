package membus

// Integration-flavored tests covering the membus scoped write/read
// visibility contract on top of real SQLite. These exercise the
// end-to-end path — WriteMemory / Remember → writer goroutine →
// BEGIN IMMEDIATE commit → Recall SELECT — and assert on observable
// state (row counts, content filtering by scope_target, ordering).

import (
	"context"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestIntegration_ScopeSessionIsolation verifies that two concurrent
// sessions writing ScopeSession rows cannot see each other's memories.
// Session isolation is implemented via the scope_target column:
// ScopeSession + scope_target="s-A" scopes to session A only.
//
// The test writes three rows — one for s-A, one for s-B, one "global"
// ScopeGlobal row shared across all sessions — and verifies that a
// Recall filtered to (ScopeSession, ScopeTarget="s-A") returns only
// s-A's row, with s-B's row invisible.
func TestIntegration_ScopeSessionIsolation(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)

	// Session-A memory.
	if err := b.Remember(ctx, RememberRequest{
		Scope:       ScopeSession,
		ScopeTarget: "s-A",
		SessionID:   "s-A",
		Author:      "worker:a1",
		Key:         "plan",
		Content:     "session-A plan notes",
	}); err != nil {
		t.Fatalf("Remember s-A: %v", err)
	}

	// Session-B memory — same Key but different scope_target, so both
	// rows coexist (the UPSERT conflict key is (scope, scope_target, key)).
	if err := b.Remember(ctx, RememberRequest{
		Scope:       ScopeSession,
		ScopeTarget: "s-B",
		SessionID:   "s-B",
		Author:      "worker:b1",
		Key:         "plan",
		Content:     "session-B plan notes",
	}); err != nil {
		t.Fatalf("Remember s-B: %v", err)
	}

	// Recall only session-A memories.
	mems, err := b.Recall(ctx, RecallRequest{
		Scope:       ScopeSession,
		ScopeTarget: "s-A",
	})
	if err != nil {
		t.Fatalf("Recall s-A: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("s-A recall returned %d rows, want 1; rows=%+v", len(mems), mems)
	}
	if mems[0].SessionID != "s-A" {
		t.Errorf("s-A recall returned session_id=%q, want s-A", mems[0].SessionID)
	}
	if mems[0].Content != "session-A plan notes" {
		t.Errorf("s-A recall content=%q, want session-A plan notes", mems[0].Content)
	}

	// Recall only session-B — must see s-B's row, not s-A's.
	mems, err = b.Recall(ctx, RecallRequest{
		Scope:       ScopeSession,
		ScopeTarget: "s-B",
	})
	if err != nil {
		t.Fatalf("Recall s-B: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("s-B recall returned %d rows, want 1", len(mems))
	}
	if mems[0].SessionID != "s-B" {
		t.Errorf("s-B recall session_id=%q, want s-B", mems[0].SessionID)
	}
	if mems[0].Content != "session-B plan notes" {
		t.Errorf("s-B content=%q, want session-B plan notes", mems[0].Content)
	}

	// Direct SQL cross-check: there really are two rows in the
	// backing table, they just don't leak between sessions via Recall.
	var total int
	if err := b.DB().QueryRow(
		`SELECT COUNT(*) FROM stoke_memory_bus WHERE scope='session'`).Scan(&total); err != nil {
		t.Fatalf("count session rows: %v", err)
	}
	if total != 2 {
		t.Errorf("stoke_memory_bus has %d session rows, want 2 (both sessions' writes persisted)", total)
	}
}

// TestIntegration_GlobalScopeVisibleAcrossSessions verifies that a
// ScopeGlobal row written from session A is readable from session B
// (and from a recall that passes no scope_target). ScopeGlobal is the
// cross-R1-instance tier and must not participate in session
// partitioning.
func TestIntegration_GlobalScopeVisibleAcrossSessions(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)

	// Session-A writes a global memory. The session_id attribute is
	// recorded on the row for provenance, but it MUST NOT narrow
	// visibility — global is global.
	if err := b.Remember(ctx, RememberRequest{
		Scope:     ScopeGlobal,
		SessionID: "s-A",
		Author:    "worker:a1",
		Key:       "global-learning",
		Content:   "pnpm install needs --frozen-lockfile in CI",
	}); err != nil {
		t.Fatalf("Remember global: %v", err)
	}

	// Session-B performs a global recall without supplying ScopeTarget
	// — it must receive the row written by session A. This is the
	// observable distinction from ScopeSession.
	mems, err := b.Recall(ctx, RecallRequest{Scope: ScopeGlobal})
	if err != nil {
		t.Fatalf("Recall global: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("global recall returned %d rows, want 1", len(mems))
	}
	if mems[0].Scope != ScopeGlobal {
		t.Errorf("recalled scope=%q, want %q", mems[0].Scope, ScopeGlobal)
	}
	if mems[0].Content != "pnpm install needs --frozen-lockfile in CI" {
		t.Errorf("global recall content=%q", mems[0].Content)
	}
	if mems[0].SessionID != "s-A" {
		t.Errorf("provenance session_id=%q, want s-A (the writer)", mems[0].SessionID)
	}

	// Counter-check: a ScopeSession recall from any session_id must
	// NOT surface global rows. This pins down the scope filter so a
	// regression that collapses "global" into "session" fails loudly.
	sessionView, err := b.Recall(ctx, RecallRequest{
		Scope:       ScopeSession,
		ScopeTarget: "s-B",
	})
	if err != nil {
		t.Fatalf("Recall session s-B: %v", err)
	}
	if len(sessionView) != 0 {
		t.Errorf("ScopeSession recall for s-B returned %d rows, want 0 (global must not leak into session view)",
			len(sessionView))
	}

	// A second global write from session-B must also be visible to any
	// caller recalling ScopeGlobal. This nails down bidirectional
	// cross-session visibility: A reads B's writes AND B reads A's.
	if err := b.Remember(ctx, RememberRequest{
		Scope:     ScopeGlobal,
		SessionID: "s-B",
		Author:    "worker:b1",
		Key:       "global-from-b",
		Content:   "from-b",
	}); err != nil {
		t.Fatalf("Remember global from s-B: %v", err)
	}
	combined, err := b.Recall(ctx, RecallRequest{Scope: ScopeGlobal})
	if err != nil {
		t.Fatalf("Recall global combined: %v", err)
	}
	if len(combined) != 2 {
		t.Fatalf("combined global recall returned %d rows, want 2", len(combined))
	}
	// Must contain both writers' rows regardless of which session recalls.
	writers := map[string]bool{}
	for _, m := range combined {
		writers[m.SessionID] = true
	}
	if !writers["s-A"] || !writers["s-B"] {
		t.Errorf("global recall missing a writer: got writers=%v, want both s-A and s-B", writers)
	}
}

