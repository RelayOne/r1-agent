package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/session"
)

// helper: insert a session row + N event rows for diff tests. The
// SignatureFile shape matches what the production daemon writes when
// it registers; we only populate what the FK + event-list path needs.
func seedDiffSession(t *testing.T, db *DB, id string, events []eventSeed) {
	t.Helper()
	now := time.Now().UTC()
	sig := session.SignatureFile{
		InstanceID: id,
		Status:     "running",
		StartedAt:  now,
		UpdatedAt:  now,
	}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("UpsertSession(%s): %v", id, err)
	}
	for i, ev := range events {
		ts := now.Add(time.Duration(i) * time.Millisecond)
		if err := db.InsertEvent(id, ev.typ, []byte(ev.data), ts); err != nil {
			t.Fatalf("InsertEvent(%s, %s): %v", id, ev.typ, err)
		}
	}
}

type eventSeed struct {
	typ  string
	data string
}

// newDiffTestServer wires a minimal mux with the diff route mounted
// against R1_SERVER_UI_V2-mode behavior. We construct the mux exactly
// like mountUI does for the diff route so we exercise the production
// PathValue dispatch.
func newDiffTestServer(t *testing.T, db *DB) *httptest.Server {
	t.Helper()
	mux := buildMux(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux.HandleFunc("GET /diff/{a}/{b}", db.serveDiff)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDiffSessions_AddedRemovedAndStatusChange exercises the three
// classifier branches against a tightly-controlled event stream.
func TestDiffSessions_AddedRemovedAndStatusChange(t *testing.T) {
	db := newTestDB(t)
	seedDiffSession(t, db, "sess-a", []eventSeed{
		{"task.start", `{"id":"t1","status":"running"}`},
		{"ledger.append", `{"id":"n1"}`},
	})
	seedDiffSession(t, db, "sess-b", []eventSeed{
		{"task.start", `{"id":"t1","status":"done"}`},
		{"task.start", `{"id":"t2","status":"running"}`},
	})

	rows, err := diffSessions(db, "sess-a", "sess-b")
	if err != nil {
		t.Fatalf("diffSessions: %v", err)
	}

	// After the deterministic sort (event_type, key, kind):
	//   ledger.append n1 → removed (only in A)
	//   task.start t1   → changed_status (running→done)
	//   task.start t2   → added (only in B)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	want := []DiffRow{
		{Kind: "removed", EventType: "ledger.append", Key: "n1", IndexA: 2},
		{Kind: "changed_status", EventType: "task.start", Key: "t1",
			StatusA: "running", StatusB: "done", IndexA: 1, IndexB: 1},
		{Kind: "added", EventType: "task.start", Key: "t2",
			StatusB: "running", IndexB: 2},
	}
	for i, w := range want {
		if rows[i].Kind != w.Kind || rows[i].EventType != w.EventType || rows[i].Key != w.Key {
			t.Errorf("rows[%d]: kind/type/key mismatch want=%+v got=%+v", i, w, rows[i])
		}
		if rows[i].StatusA != w.StatusA || rows[i].StatusB != w.StatusB {
			t.Errorf("rows[%d]: status mismatch want %s→%s got %s→%s",
				i, w.StatusA, w.StatusB, rows[i].StatusA, rows[i].StatusB)
		}
	}
}

// TestDiffSessions_NoDifferences pins the empty-diff case so future
// changes to the classifier don't accidentally introduce false
// positives on identical streams.
func TestDiffSessions_NoDifferences(t *testing.T) {
	db := newTestDB(t)
	seedDiffSession(t, db, "sess-1", []eventSeed{
		{"x.y", `{"id":"k1","status":"ok"}`},
	})
	seedDiffSession(t, db, "sess-2", []eventSeed{
		{"x.y", `{"id":"k1","status":"ok"}`},
	})
	rows, err := diffSessions(db, "sess-1", "sess-2")
	if err != nil {
		t.Fatalf("diffSessions: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("want 0 rows, got %d: %+v", len(rows), rows)
	}
}

// TestServeDiff_JSON exercises the JSON content-negotiation branch.
func TestServeDiff_JSON(t *testing.T) {
	db := newTestDB(t)
	seedDiffSession(t, db, "x", []eventSeed{{"task.done", `{"id":"t1","status":"ok"}`}})
	seedDiffSession(t, db, "y", []eventSeed{{"task.done", `{"id":"t1","status":"err"}`}})

	srv := newDiffTestServer(t, db)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", srv.URL+"/diff/x/y", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q want application/json", ct)
	}
	var got struct {
		A    string    `json:"a"`
		B    string    `json:"b"`
		Rows []DiffRow `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.A != "x" || got.B != "y" {
		t.Errorf("a=%q b=%q want x,y", got.A, got.B)
	}
	if len(got.Rows) != 1 || got.Rows[0].Kind != "changed_status" {
		t.Errorf("rows=%+v want one changed_status", got.Rows)
	}
}

// TestServeDiff_HTMLDefault verifies the fallback HTML page renders
// with the spec-mandated "content-diff out of scope" footer.
func TestServeDiff_HTMLDefault(t *testing.T) {
	db := newTestDB(t)
	seedDiffSession(t, db, "p", []eventSeed{{"task.start", `{"id":"t1"}`}})
	seedDiffSession(t, db, "q", nil)

	srv := newDiffTestServer(t, db)
	resp, err := srv.Client().Get(srv.URL + "/diff/p/q")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type=%q want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	if !strings.Contains(bs, "Diff <code>p</code> vs <code>q</code>") {
		t.Errorf("body missing header; got %q", bs[:min(200, len(bs))])
	}
	if !strings.Contains(bs, "removed") {
		t.Errorf("body missing removed row; body=%q", bs[:min(400, len(bs))])
	}
	if !strings.Contains(bs, "Content-diff") {
		t.Errorf("footer missing Content-diff out-of-scope note")
	}
}

// TestKeyOf_FallbackPriority pins the field-priority order so future
// changes don't accidentally re-rank id-bearing fields and produce
// different fingerprints (which would break diff stability).
func TestKeyOf_FallbackPriority(t *testing.T) {
	cases := []struct {
		data string
		want string
	}{
		{`{"id":"a","node_id":"b"}`, "a"},
		{`{"node_id":"b","task_id":"c"}`, "b"},
		{`{"task_id":"c","name":"d"}`, "c"},
		{`{"name":"d","key":"e"}`, "d"},
		{`{"key":"e"}`, "e"},
		{`{"unrelated":"f"}`, ""},
		{``, ""},
		{`{not-json}`, ""},
	}
	for _, tc := range cases {
		got := keyOf(EventRow{Data: json.RawMessage(tc.data)})
		if got != tc.want {
			t.Errorf("keyOf(%q) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

// TestStatusOf_FallbackPriority — status, then state, then phase.
func TestStatusOf_FallbackPriority(t *testing.T) {
	cases := []struct {
		data string
		want string
	}{
		{`{"status":"running","state":"queued"}`, "running"},
		{`{"state":"queued","phase":"plan"}`, "queued"},
		{`{"phase":"plan"}`, "plan"},
		{`{"unrelated":"x"}`, ""},
	}
	for _, tc := range cases {
		got := statusOf(EventRow{Data: json.RawMessage(tc.data)})
		if got != tc.want {
			t.Errorf("statusOf(%q) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

// TestAccepts covers the content-negotiation matcher used by the
// JSON branch of serveDiff.
func TestAccepts(t *testing.T) {
	mk := func(h string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			r.Header.Set("Accept", h)
		}
		return r
	}
	cases := []struct {
		header string
		ct     string
		want   bool
	}{
		{"application/json", "application/json", true},
		{"*/*", "application/json", true},
		{"application/json,text/html", "application/json", true},
		{"text/html,application/xhtml+xml", "application/json", false},
		{"", "application/json", false},
	}
	for _, tc := range cases {
		got := accepts(mk(tc.header), tc.ct)
		if got != tc.want {
			t.Errorf("accepts(%q, %q) = %v, want %v", tc.header, tc.ct, got, tc.want)
		}
	}
}
