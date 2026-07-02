// Package main — sse_v2_test.go
//
// Spec 4 §10 T18 + T19: SSE polish tests.
//   T18 — last_event_id query fallback when the Last-Event-ID header is empty
//   T19 — event:resync frame when cursor < oldest_available
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// T18 — query-string cursor.
func TestSSE_LastEventIDFromQuery(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	seedStreamSession(t, db, "r1-sse-q", "a", "b", "c", "d")

	rows, err := db.ListEvents("r1-sse-q", 0, 0)
	if err != nil || len(rows) != 4 {
		t.Fatalf("seed events: rows=%d err=%v", len(rows), err)
	}
	cursor := rows[1].ID

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/api/session/r1-sse-q/events/stream?last_event_id=%d", s.URL, cursor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	frames := readSSEFrames(t, ctx, resp.Body, 2)
	if len(frames) != 2 {
		t.Fatalf("want 2 frames after cursor=%d, got %d (%+v)", cursor, len(frames), frames)
	}
	if frames[0].Event != "c" || frames[1].Event != "d" {
		t.Errorf("frames=%+v, want c,d", frames)
	}
}

// T19 — pruned-cursor resync.
func TestSSE_ResyncOnPrunedCursor(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	seedStreamSession(t, db, "r1-sse-resync", "a", "b", "c", "d")

	rows, err := db.ListEvents("r1-sse-resync", 0, 0)
	if err != nil || len(rows) != 4 {
		t.Fatalf("seed events: rows=%d err=%v", len(rows), err)
	}

	// Prune the lowest-id row directly, modeling retention deleting old
	// events. AUTOINCREMENT never reuses ids, so the remaining oldest id
	// is strictly greater than the deleted one — a below-floor cursor is
	// then always constructible.
	if _, err := db.sql.Exec(
		`DELETE FROM session_events WHERE instance_id = ? AND id = ?`,
		"r1-sse-resync", rows[0].ID,
	); err != nil {
		t.Fatalf("prune oldest event: %v", err)
	}
	rows, err = db.ListEvents("r1-sse-resync", 0, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("re-list after prune: rows=%d err=%v", len(rows), err)
	}
	oldest := rows[0].ID

	// Request with a cursor BELOW the oldest available row.
	prunedCursor := oldest - 1
	if prunedCursor <= 0 {
		t.Fatalf("fixture broken: oldest=%d after pruning; cursor must be >= 1", oldest)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/api/session/r1-sse-resync/events/stream?last_event_id=%d", s.URL, prunedCursor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, "event: resync") {
		t.Errorf("body should contain `event: resync` frame; got:\n%s", bs)
	}
	if !strings.Contains(bs, `"reason":"cursor_pruned"`) {
		t.Errorf("body should contain reason cursor_pruned; got:\n%s", bs)
	}
	if !strings.Contains(bs, fmt.Sprintf(`"oldest_available":%d`, oldest)) {
		t.Errorf("body should contain oldest_available=%d; got:\n%s", oldest, bs)
	}
}

func TestSSE_NoResyncWhenCursorAtOrAboveOldest(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	seedStreamSession(t, db, "r1-sse-no-resync", "a", "b", "c")

	rows, _ := db.ListEvents("r1-sse-no-resync", 0, 0)
	oldest := rows[0].ID

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/api/session/r1-sse-no-resync/events/stream?last_event_id=%d", s.URL, oldest)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	// Read just the first frame burst — should be event-stream
	// content (b, c) NOT a resync frame.
	frames := readSSEFrames(t, ctx, resp.Body, 2)
	for _, f := range frames {
		if f.Event == "resync" {
			t.Errorf("got resync frame for non-pruned cursor; frames=%+v", frames)
			break
		}
	}
}
