package membus

import (
	"context"
	"testing"
	"time"
)

func TestExportDelta_EmptyBus(t *testing.T) {
	b := newTestBus(t)
	got, err := b.ExportDelta(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("ExportDelta err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d rows", len(got))
	}
}

func TestExportDelta_ReturnsRowsCreatedAfterCutoff(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	// Seed three rows with different content / keys so scanMemory
	// can distinguish them.
	for i, key := range []string{"a", "b", "c"} {
		req := RememberRequest{
			Scope:     ScopeAllSessions,
			Key:       key,
			Content:   "content-" + key,
			SessionID: "s1",
		}
		if err := b.Remember(ctx, req); err != nil {
			t.Fatalf("Remember[%d] %s: %v", i, key, err)
		}
	}

	// Every row is newer than the unix epoch, so no cutoff = all rows.
	rows, err := b.ExportDelta(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ExportDelta: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows post-seed, got %d", len(rows))
	}
	for _, m := range rows {
		if m.Key == "" {
			t.Errorf("row missing key: %+v", m)
		}
		if m.Content == "" {
			t.Errorf("row missing content: %+v", m)
		}
	}
}

func TestExportDelta_RespectsCutoff(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	// Seed two rows with a sleep between to guarantee distinct created_at.
	_ = b.Remember(ctx, RememberRequest{
		Scope: ScopeAllSessions, Key: "before", Content: "old",
	})
	time.Sleep(10 * time.Millisecond)
	cutoff := time.Now().UTC()
	time.Sleep(10 * time.Millisecond)
	_ = b.Remember(ctx, RememberRequest{
		Scope: ScopeAllSessions, Key: "after", Content: "new",
	})

	rows, err := b.ExportDelta(ctx, cutoff)
	if err != nil {
		t.Fatalf("ExportDelta: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after cutoff, got %d: %+v", len(rows), rows)
	}
	if rows[0].Key != "after" {
		t.Errorf("expected row key=after, got %q", rows[0].Key)
	}
}

func TestExportDelta_NilBusReturnsNilNilPair(t *testing.T) {
	var b *Bus
	rows, err := b.ExportDelta(context.Background(), time.Time{})
	if err != nil || rows != nil {
		t.Errorf("nil bus: got (%v, %v), want (nil, nil)", rows, err)
	}
}

func TestExportDeltaSince_NeverReturnsNilSlice(t *testing.T) {
	b := newTestBus(t)
	rows := b.ExportDeltaSince(time.Now().Add(time.Hour)) // cutoff in the future
	if rows == nil {
		t.Errorf("ExportDeltaSince should return empty slice, not nil")
	}
	if len(rows) != 0 {
		t.Errorf("cutoff in future should yield 0 rows, got %d", len(rows))
	}
}
