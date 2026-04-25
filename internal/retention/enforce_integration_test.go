package retention

// Integration test for the membus → retention.EnforceOnSessionEnd
// ephemeral-wipe pipeline. Complements the existing unit-level tests in
// enforce_test.go by running a multi-session seed and asserting that
// EnforceOnSessionEnd targets the ending session WITHOUT touching any
// other session's rows (regardless of scope or memory_type).

import (
	"context"
	"sort"
	"testing"

	"github.com/RelayOne/r1-agent/internal/memory/membus"
)

// TestIntegration_EnforceOnSessionEndIsolatesEndingSession seeds four
// sessions with a mix of ephemeral and session memory types across the
// three in-session scopes (session, session_step, worker) and verifies:
//
//   - Ending session s-A: every ephemeral row in (session|session_step|
//     worker) scope with session_id=s-A is deleted.
//   - Ending session s-A: non-ephemeral rows owned by s-A survive.
//   - Other sessions (s-B, s-C, s-D) keep BOTH their ephemeral AND
//     non-ephemeral rows — the wipe must not leak.
//   - ScopeAllSessions rows with session_id=s-A survive (the wipe SQL
//     filters on scope IN ('session','session_step','worker') —
//     all_sessions is explicitly not in that set).
func TestIntegration_EnforceOnSessionEndIsolatesEndingSession(t *testing.T) {
	ctx := context.Background()
	b := newBusForTest(t)

	// Seed matrix. Each row documents a unique (scope, session, memory_type)
	// triple so post-wipe assertions can pin down exactly which keys survive.
	seeds := []struct {
		scope      string
		sessionID  string
		memoryType string
		key        string
		content    string
	}{
		// --- s-A (the ending session) ----------------------------------
		{"session", "s-A", "ephemeral", "A-sess-eph", "wipe me"},
		{"session", "s-A", "session", "A-sess-ses", "keep: session mem"},
		{"session_step", "s-A", "ephemeral", "A-step-eph", "wipe me"},
		{"session_step", "s-A", "session", "A-step-ses", "keep: session mem"},
		{"worker", "s-A", "ephemeral", "A-work-eph", "wipe me"},
		{"worker", "s-A", "session", "A-work-ses", "keep: session mem"},
		// ScopeAllSessions is cross-session and must NOT be wiped even
		// when owned by the ending session.
		{"all_sessions", "s-A", "ephemeral", "A-all-eph", "keep: all_sessions"},

		// --- s-B, s-C, s-D (other sessions) ---------------------------
		// Their ephemeral rows must survive even though the wipe
		// targets memory_type=ephemeral — the session filter in the
		// DELETE must bound the blast radius.
		{"session", "s-B", "ephemeral", "B-sess-eph", "other session keep"},
		{"worker", "s-C", "ephemeral", "C-work-eph", "other session keep"},
		{"session_step", "s-D", "ephemeral", "D-step-eph", "other session keep"},
	}
	for _, s := range seeds {
		insertMemory(t, b, s.scope, s.sessionID, s.memoryType, s.key, s.content, nil)
	}

	if got := countMemories(t, b); got != len(seeds) {
		t.Fatalf("precondition: got %d rows, want %d", got, len(seeds))
	}

	// End session s-A. Defaults policy has EphemeralMemories=WipeAfterSession.
	if err := EnforceOnSessionEnd(ctx, Defaults(), "s-A", b); err != nil {
		t.Fatalf("EnforceOnSessionEnd(s-A): %v", err)
	}

	// Collect surviving keys, sorted for stable comparison.
	rows, err := b.DB().Query(`SELECT key FROM stoke_memory_bus ORDER BY key`)
	if err != nil {
		t.Fatalf("select keys: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		got = append(got, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iterate: %v", err)
	}

	// Expected: 3 wiped (A-sess-eph, A-step-eph, A-work-eph) → 7 survive.
	// Every non-ephemeral s-A row stays. Every other session's row stays.
	// ScopeAllSessions with s-A stays (scope filter exempts it).
	want := []string{
		"A-all-eph",  // all_sessions scope — not in wipe set
		"A-sess-ses", // s-A non-ephemeral
		"A-step-ses", // s-A non-ephemeral
		"A-work-ses", // s-A non-ephemeral
		"B-sess-eph", // other session
		"C-work-eph", // other session
		"D-step-eph", // other session
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("surviving keys = %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("surviving key[%d] = %q, want %q (full got=%v want=%v)",
				i, got[i], want[i], got, want)
		}
	}

	// Targeted sanity: none of the wiped s-A ephemerals should be
	// queryable via membus.Recall either.
	wiped := []string{"A-sess-eph", "A-step-eph", "A-work-eph"}
	for _, k := range wiped {
		mems, err := b.Recall(ctx, membus.RecallRequest{Key: k})
		if err != nil {
			t.Errorf("Recall(%s): %v", k, err)
			continue
		}
		if len(mems) != 0 {
			t.Errorf("wiped key %q still recallable via membus: %+v", k, mems)
		}
	}

	// And confirm the other-session ephemerals remain recallable.
	for _, k := range []string{"B-sess-eph", "C-work-eph", "D-step-eph"} {
		mems, err := b.Recall(ctx, membus.RecallRequest{Key: k})
		if err != nil {
			t.Errorf("Recall(%s): %v", k, err)
			continue
		}
		if len(mems) != 1 {
			t.Errorf("expected other-session key %q to survive, got %d rows", k, len(mems))
		}
	}
}
