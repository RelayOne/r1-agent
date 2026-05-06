package ledger

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newRedactionTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"nodes", "chain", "content", "edges", "index"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestRedactionLog_RecordAndRoundtrip(t *testing.T) {
	store := newRedactionTestStore(t)
	ev := SignedRedactionEvent{
		NodeID:     "n-1",
		RedactedAt: "2026-05-05T12:00:00Z",
		Reason:     "retention_policy",
		Signer:     "policy-engine",
	}
	if err := store.RecordRedaction(ev); err != nil {
		t.Fatalf("RecordRedaction: %v", err)
	}
	got, err := store.RedactionsFor("n-1")
	if err != nil {
		t.Fatalf("RedactionsFor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0] != ev {
		t.Errorf("event roundtrip mismatch:\n got: %+v\nwant: %+v", got[0], ev)
	}
}

func TestRedactionLog_MultipleEventsAreChronological(t *testing.T) {
	store := newRedactionTestStore(t)
	// Insert in reverse order to verify chronological sort.
	events := []SignedRedactionEvent{
		{NodeID: "n-1", RedactedAt: "2026-05-05T12:00:00Z", Reason: "gdpr_erasure"},
		{NodeID: "n-1", RedactedAt: "2026-05-04T10:00:00Z", Reason: "retention_policy"},
		{NodeID: "n-1", RedactedAt: "2026-05-06T08:00:00Z", Reason: "operator_request"},
	}
	for _, ev := range events {
		if err := store.RecordRedaction(ev); err != nil {
			t.Fatalf("RecordRedaction: %v", err)
		}
	}
	got, err := store.RedactionsFor("n-1")
	if err != nil {
		t.Fatalf("RedactionsFor: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	wantOrder := []string{"retention_policy", "gdpr_erasure", "operator_request"}
	for i, want := range wantOrder {
		if got[i].Reason != want {
			t.Errorf("event[%d].Reason = %q, want %q", i, got[i].Reason, want)
		}
	}
}

func TestRedactionLog_NoLogReturnsNil(t *testing.T) {
	store := newRedactionTestStore(t)
	got, err := store.RedactionsFor("never-redacted")
	if err != nil {
		t.Errorf("RedactionsFor on missing log: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("RedactionsFor on missing log: got %v, want nil", got)
	}
}

func TestRedactionLog_RejectsEmptyFields(t *testing.T) {
	store := newRedactionTestStore(t)
	cases := []SignedRedactionEvent{
		{NodeID: "", RedactedAt: "2026-05-05T12:00:00Z", Reason: "retention"},
		{NodeID: "n-1", RedactedAt: "", Reason: "retention"},
		{NodeID: "n-1", RedactedAt: "2026-05-05T12:00:00Z", Reason: ""},
	}
	for i, ev := range cases {
		if err := store.RecordRedaction(ev); err == nil {
			t.Errorf("case %d: expected validation error, got nil", i)
		}
	}
}

func TestRedactionLog_DifferentNodesAreIndependent(t *testing.T) {
	store := newRedactionTestStore(t)
	for _, nodeID := range []string{"n-1", "n-2", "n-3"} {
		ev := SignedRedactionEvent{
			NodeID:     nodeID,
			RedactedAt: "2026-05-05T12:00:00Z",
			Reason:     "retention_policy",
		}
		if err := store.RecordRedaction(ev); err != nil {
			t.Fatalf("RecordRedaction(%s): %v", nodeID, err)
		}
	}
	for _, nodeID := range []string{"n-1", "n-2", "n-3"} {
		got, err := store.RedactionsFor(nodeID)
		if err != nil {
			t.Errorf("RedactionsFor(%s): %v", nodeID, err)
			continue
		}
		if len(got) != 1 {
			t.Errorf("RedactionsFor(%s): got %d events, want 1", nodeID, len(got))
		}
	}
}

func TestRedactionLog_RedactAndLog(t *testing.T) {
	store := newRedactionTestStore(t)
	// Seed a chain entry so Redact has something to operate against.
	chainPath := filepath.Join(store.chainDirFor(), "n-test.json")
	if err := os.WriteFile(chainPath, []byte(`{"id":"n-test"}`), 0o644); err != nil {
		t.Fatalf("seed chain: %v", err)
	}
	contentPath := filepath.Join(store.contentDirFor(), "n-test.json")
	if err := os.WriteFile(contentPath, []byte(`{"content":"sensitive"}`), 0o644); err != nil {
		t.Fatalf("seed content: %v", err)
	}

	ev, err := store.RedactAndLog(context.Background(), "n-test", "retention_policy")
	if err != nil {
		t.Fatalf("RedactAndLog: %v", err)
	}
	if ev.NodeID != "n-test" {
		t.Errorf("NodeID = %q, want n-test", ev.NodeID)
	}
	if ev.Reason != "retention_policy" {
		t.Errorf("Reason = %q, want retention_policy", ev.Reason)
	}
	if !strings.Contains(ev.RedactedAt, "Z") {
		t.Errorf("RedactedAt = %q, want UTC RFC3339", ev.RedactedAt)
	}

	// Content blob is gone (Redact crypto-shred).
	if _, err := os.Stat(contentPath); !os.IsNotExist(err) {
		t.Errorf("content blob should be gone after RedactAndLog, stat err=%v", err)
	}
	// Log captured the event.
	got, err := store.RedactionsFor("n-test")
	if err != nil {
		t.Fatalf("RedactionsFor: %v", err)
	}
	if len(got) != 1 || got[0].Reason != "retention_policy" {
		t.Errorf("log entry = %+v, want one retention_policy event", got)
	}
}

func TestRedactionLog_RecordAfterContextCancelStillSucceeds(t *testing.T) {
	// RecordRedaction is a pure file-append; ctx cancel of the
	// caller's request shouldn't lose audit data already in flight.
	// The function takes no ctx argument (deliberate per spec).
	store := newRedactionTestStore(t)
	if err := store.RecordRedaction(SignedRedactionEvent{
		NodeID:     "n-async",
		RedactedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Reason:     "retention_policy",
	}); err != nil {
		t.Fatalf("RecordRedaction: %v", err)
	}
	got, err := store.RedactionsFor("n-async")
	if err != nil {
		t.Fatalf("RedactionsFor: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d events, want 1", len(got))
	}
}

func TestRedactionLog_CorruptLineSurfacesErrorPreservesGoodEntries(t *testing.T) {
	store := newRedactionTestStore(t)
	// Seed two valid entries, then a corrupt line.
	for _, ts := range []string{"2026-05-05T10:00:00Z", "2026-05-05T11:00:00Z"} {
		if err := store.RecordRedaction(SignedRedactionEvent{
			NodeID:     "n-corrupt",
			RedactedAt: ts,
			Reason:     "retention_policy",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	logPath := filepath.Join(store.redactionLogDirFor(), "n-corrupt.ndjson")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString("{not json\n"); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	f.Close()

	got, err := store.RedactionsFor("n-corrupt")
	if err == nil {
		t.Errorf("expected parse error on corrupt line, got nil")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 valid entries returned alongside the parse error, got %d", len(got))
	}
}
