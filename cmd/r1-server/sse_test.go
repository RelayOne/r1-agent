package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/session"
)

// seedStreamSession creates a running session plus n events so SSE
// tests have something to replay.
func seedStreamSession(t *testing.T, db *DB, id string, eventTypes ...string) {
	t.Helper()
	now := time.Now().UTC()
	sig := session.SignatureFile{
		InstanceID: id,
		Status:     "running",
		StartedAt:  now,
		UpdatedAt:  now,
	}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for i, typ := range eventTypes {
		raw := fmt.Sprintf(`{"type":%q,"seq":%d}`, typ, i+1)
		if err := db.InsertEvent(id, typ, []byte(raw), now.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
}

// readSSEFrames parses frames from an SSE body until the reader
// closes, the context cancels, or maxFrames is reached. Returns the
// collected frames so tests can assert id/event/data.
type sseFrame struct {
	ID    string
	Event string
	Data  string
}

func readSSEFrames(t *testing.T, ctx context.Context, body io.Reader, maxFrames int) []sseFrame {
	t.Helper()
	var (
		frames  []sseFrame
		current sseFrame
	)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	done := make(chan struct{})
	var frameErr error
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, ":"):
				// comment frame (heartbeat) — ignore
			case strings.HasPrefix(line, "id:"):
				current.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "event:"):
				current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				current.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			case strings.HasPrefix(line, "retry:"):
				// retry hint — ignore
			case line == "":
				// Blank line terminates a frame. Only flush frames that
				// have at least one data/event/id piece — the leading
				// "retry: 2000" message followed by a blank produces an
				// empty current{} which we skip.
				if current != (sseFrame{}) {
					frames = append(frames, current)
					current = sseFrame{}
					if len(frames) >= maxFrames {
						return
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			frameErr = err
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
	if frameErr != nil {
		t.Fatalf("scanner err: %v", frameErr)
	}
	return frames
}

// TestSSEEndpointStreamsBacklog opens the stream on a session with
// pre-existing events and expects those rows to arrive as SSE frames
// within a short window.
func TestSSEEndpointStreamsBacklog(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	seedStreamSession(t, db, "r1-sse-backlog", "session.start", "task.start", "ac.result")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/session/r1-sse-backlog/events/stream", nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("cache-control=%q, want no-cache", cc)
	}

	frames := readSSEFrames(t, ctx, resp.Body, 3)
	if len(frames) != 3 {
		t.Fatalf("want 3 frames, got %d (%+v)", len(frames), frames)
	}
	wantEvents := []string{"session.start", "task.start", "ac.result"}
	for i, f := range frames {
		if f.Event != wantEvents[i] {
			t.Errorf("frame %d event=%q, want %q", i, f.Event, wantEvents[i])
		}
		if f.ID == "" {
			t.Errorf("frame %d missing id", i)
		}
		if !strings.Contains(f.Data, wantEvents[i]) {
			t.Errorf("frame %d data=%q should embed event type", i, f.Data)
		}
	}
}

// TestSSEEndpointResumesFromLastEventID verifies cursor negotiation:
// when the client sends Last-Event-ID=N, only events with id > N are
// replayed. Backlog drain respects the cursor, so clients can
// reconnect and resume without reading the whole history.
func TestSSEEndpointResumesFromLastEventID(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	seedStreamSession(t, db, "r1-sse-resume", "a", "b", "c", "d")

	// Find the id of event "b" so we resume after it and expect c + d.
	rows, err := db.ListEvents("r1-sse-resume", 0, 0)
	if err != nil || len(rows) != 4 {
		t.Fatalf("seed events: rows=%d err=%v", len(rows), err)
	}
	cursor := rows[1].ID

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/session/r1-sse-resume/events/stream", nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", cursor))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	frames := readSSEFrames(t, ctx, resp.Body, 2)
	if len(frames) != 2 {
		t.Fatalf("want 2 frames after cursor, got %d (%+v)", len(frames), frames)
	}
	if frames[0].Event != "c" || frames[1].Event != "d" {
		t.Errorf("frames=%+v, want c,d", frames)
	}
}

// TestSSEEndpointLiveTailsNewEvents opens the stream first, then
// inserts events, and expects the tick loop to surface them within a
// few seconds. Proves the polling path works, not just the initial
// drain.
func TestSSEEndpointLiveTailsNewEvents(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	seedStreamSession(t, db, "r1-sse-live")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/session/r1-sse-live/events/stream", nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed on line 205
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	framesCh := make(chan []sseFrame, 1)
	go func() {
		framesCh <- readSSEFrames(t, ctx, resp.Body, 1)
	}()

	// Let the handler get past its initial drain, then insert.
	time.Sleep(100 * time.Millisecond)
	if err := db.InsertEvent("r1-sse-live", "late.arrival", []byte(`{"type":"late.arrival"}`), time.Now()); err != nil {
		t.Fatalf("insert late: %v", err)
	}

	select {
	case frames := <-framesCh:
		if len(frames) == 0 {
			t.Fatalf("no frames received")
		}
		if frames[0].Event != "late.arrival" {
			t.Errorf("event=%q, want late.arrival", frames[0].Event)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for live event")
	}
}

// TestSSEEndpointUnknownSession404s — an EventSource against a
// non-existent session should fail fast rather than hang forever.
func TestSSEEndpointUnknownSession404s(t *testing.T) {
	db := newTestDB(t)
	s := newTestServer(t, db)
	resp, err := http.Get(s.URL + "/api/session/nope/events/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

// TestFlushNewEventsHonorsCursor is a unit test on the internal helper
// — proves the cursor math without spinning an HTTP server.
func TestFlushNewEventsHonorsCursor(t *testing.T) {
	db := newTestDB(t)
	seedStreamSession(t, db, "r1-flush", "one", "two", "three")

	// Fake ResponseWriter via httptest.ResponseRecorder — also
	// implements http.Flusher through the noopFlusher wrapper below.
	rec := httptest.NewRecorder()
	fl := &noopFlusher{ResponseRecorder: rec}

	cursor, err := flushNewEvents(fl, fl, db, "r1-flush", 0, 0)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if cursor == 0 {
		t.Fatalf("cursor not advanced: %d", cursor)
	}

	body := rec.Body.String()
	for _, want := range []string{"event: one", "event: two", "event: three"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q, got:\n%s", want, body)
		}
	}

	// Second flush with the same cursor should be a no-op.
	rec2 := httptest.NewRecorder()
	fl2 := &noopFlusher{ResponseRecorder: rec2}
	newCursor, err := flushNewEvents(fl2, fl2, db, "r1-flush", cursor, 0)
	if err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if newCursor != cursor {
		t.Errorf("cursor advanced on empty drain: %d -> %d", cursor, newCursor)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("expected empty body on no-op flush, got %q", rec2.Body.String())
	}
}

// noopFlusher lets httptest.ResponseRecorder satisfy http.Flusher for
// the unit test above; Flush is a no-op because the recorder buffers.
type noopFlusher struct {
	*httptest.ResponseRecorder
}

func (n *noopFlusher) Flush() {}

// TestBuildMuxRegistersStreamRoute is a quick smoke check to ensure
// the SSE route is reachable through the real mux wiring, not just
// the handler factory.
func TestBuildMuxRegistersStreamRoute(t *testing.T) {
	db := newTestDB(t)
	mux := buildMux(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Prime the DB so the handler gets past GetSession.
	seedStreamSession(t, db, "r1-mux-check", "ping")

	req := httptest.NewRequest(http.MethodGet, "/api/session/r1-mux-check/events/stream", nil)
	rec := httptest.NewRecorder()
	// Run the handler in a goroutine with a tight deadline — the
	// stream would otherwise block until its ticker fires.
	ctx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(rec, req)
		close(done)
	}()
	<-done
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type=%q", ct)
	}
	if !strings.Contains(rec.Body.String(), "event: ping") {
		t.Errorf("expected initial drain frame, body=%q", rec.Body.String())
	}
}
