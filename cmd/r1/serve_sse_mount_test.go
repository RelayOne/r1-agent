// serve_sse_mount_test.go — integration test for the read-only SSE
// bridge mounted by runServeLoop (audit A069; specs/r1d-server.md
// Phase E item 33). Uses the SAME constructor the serve loop uses
// (buildSessionSSEHandler) so the middleware chain — ?token= auth via
// sse.AttachAuthFromQuery BEFORE RequireBearer — plus journal replay
// and live fanout are proven end-to-end over a real HTTP server.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// sseRecord is one parsed SSE record.
type sseRecord struct {
	ID    string
	Event string
	Data  string
}

// readSSERecords streams records from body into ch until read error.
func readSSERecords(body *bufio.Reader, ch chan<- sseRecord) {
	var cur sseRecord
	for {
		line, err := body.ReadString('\n')
		if err != nil {
			close(ch)
			return
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if cur.Event != "" || cur.Data != "" {
				ch <- cur
			}
			cur = sseRecord{}
		case strings.HasPrefix(line, "id: "):
			cur.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data += strings.TrimPrefix(line, "data: ")
		}
	}
}

// TestServeSSEMount_TokenAuthReplayAndLive is the A069 activation
// proof at the route level: GET /v1/sessions/{id}/sse with ?token=
// authenticates via AttachAuthFromQuery→RequireBearer, replays the
// journal, then streams live fanout events.
func TestServeSSEMount_TokenAuthReplayAndLive(t *testing.T) {
	rpcHandler, sess, sid := newWebTestDaemon(t)

	// Two events BEFORE any subscriber — journal-only.
	ctx := context.Background()
	if err := sess.DispatchEvent(ctx, testLaneEvent(sid, "alpha")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := sess.DispatchEvent(ctx, testLaneEvent(sid, "beta")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /v1/sessions/{id}/sse", buildSessionSSEHandler(rpcHandler, testBearer))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// No token → 401 (requireBearer holds).
	resp401, err := ts.Client().Get(ts.URL + "/v1/sessions/" + sid + "/sse")
	if err != nil {
		t.Fatalf("unauth get: %v", err)
	}
	resp401.Body.Close()
	if resp401.StatusCode != 401 {
		t.Fatalf("no-token status = %d, want 401", resp401.StatusCode)
	}

	// ?token= flows through AttachAuthFromQuery into the bearer gate.
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, "GET",
		ts.URL+"/v1/sessions/"+sid+"/sse?token="+testBearer+"&since_seq=0", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("sse get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sse status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if xb := resp.Header.Get("X-Accel-Buffering"); xb != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", xb)
	}

	ch := make(chan sseRecord, 16)
	go readSSERecords(bufio.NewReader(resp.Body), ch)

	readRecord := func(what string) sseRecord {
		t.Helper()
		select {
		case rec, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed waiting for %s", what)
			}
			return rec
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for %s", what)
		}
		return sseRecord{}
	}

	// Replay: the two journaled events, in order, ids 1 and 2.
	first := readRecord("replay record 1")
	if first.Event != "lane.delta" || first.ID != "1" {
		t.Fatalf("record 1 = %+v, want lane.delta id=1", first)
	}
	var payload hub.Event
	if err := json.Unmarshal([]byte(first.Data), &payload); err != nil {
		t.Fatalf("record 1 data: %v", err)
	}
	if payload.Lane == nil || payload.Lane.Block == nil || payload.Lane.Block.Text != "alpha" {
		t.Errorf("record 1 payload = %s", first.Data)
	}
	second := readRecord("replay record 2")
	if second.ID != "2" {
		t.Fatalf("record 2 = %+v, want id=2", second)
	}

	// Live: a dispatch AFTER subscribe streams without reconnect.
	if err := sess.DispatchEvent(ctx, testLaneEvent(sid, "gamma")); err != nil {
		t.Fatalf("live dispatch: %v", err)
	}
	live := readRecord("live record")
	if live.Event != "lane.delta" || live.ID != "3" {
		t.Fatalf("live record = %+v, want lane.delta id=3", live)
	}
}

