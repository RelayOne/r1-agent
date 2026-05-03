package sse

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/server/jsonrpc"
)

// fakeSubscribe wires Subscribe so the test can drive replay + live
// events deterministically. The replayed events fire from journalSeqs
// (in order) using the replay-time sink; after replayDone is closed,
// any liveEvents are pushed.
//
// We do NOT actually use the *Subscription type here — the bridge
// just calls the supplied EventSink. That's the contract: the daemon
// owns the Subscription and bridges events through Subscribe.
type fakeSubscribe struct {
	replayEvents []*jsonrpc.SubscriptionEvent
	liveEvents   []*jsonrpc.SubscriptionEvent
	captureSince *atomic.Uint64
	captureFiltr *atomic.Pointer[[]string]
	subErr       error

	cancelOnce sync.Once
	cancelled  chan struct{}
}

func (f *fakeSubscribe) start(ctx context.Context, sessionID string, sinceSeq uint64, filter []string, sink jsonrpc.EventSink) (func(), error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	if f.captureSince != nil {
		f.captureSince.Store(sinceSeq)
	}
	if f.captureFiltr != nil {
		f.captureFiltr.Store(&filter)
	}
	if f.cancelled == nil {
		f.cancelled = make(chan struct{})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Replay first.
		for _, ev := range f.replayEvents {
			select {
			case <-ctx.Done():
				return
			case <-f.cancelled:
				return
			default:
			}
			if err := sink(ctx, ev); err != nil {
				return
			}
		}
		// Live next.
		for _, ev := range f.liveEvents {
			select {
			case <-ctx.Done():
				return
			case <-f.cancelled:
				return
			default:
			}
			if err := sink(ctx, ev); err != nil {
				return
			}
		}
	}()
	cancel := func() {
		f.cancelOnce.Do(func() { close(f.cancelled) })
		// Wait for the goroutine to drain so we don't race with the
		// http response writer's finalisation. Production daemons get
		// the same behaviour from *jsonrpc.Subscription.Close +
		// teardown of the bus subscriber.
		<-done
	}
	return cancel, nil
}

// readSSERecords pulls SSE records off the response body until either
// `count` records have been received OR the deadline fires.
func readSSERecords(t *testing.T, r io.Reader, count int, deadline time.Duration) []map[string]string {
	t.Helper()
	type line struct {
		s   string
		err error
	}
	lineCh := make(chan line, 64)
	go func() {
		defer close(lineCh)
		bs := bufio.NewScanner(r)
		for bs.Scan() {
			lineCh <- line{s: bs.Text()}
		}
		if err := bs.Err(); err != nil {
			lineCh <- line{err: err}
		}
	}()

	var out []map[string]string
	cur := map[string]string{}
	timer := time.NewTimer(deadline)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return out
		case ln, ok := <-lineCh:
			if !ok {
				return out
			}
			if ln.err != nil {
				return out
			}
			s := ln.s
			if s == "" {
				if len(cur) > 0 {
					out = append(out, cur)
					cur = map[string]string{}
					if len(out) >= count {
						return out
					}
				}
				continue
			}
			if i := strings.Index(s, ":"); i >= 0 {
				k := strings.TrimSpace(s[:i])
				v := strings.TrimSpace(s[i+1:])
				// SSE: multi-line `data:` concatenates.
				if existing, ok := cur[k]; ok {
					cur[k] = existing + "\n" + v
				} else {
					cur[k] = v
				}
			}
		}
	}
}

// makeHandler wires up a Handler with a fakeSubscribe.
func makeHandler(fs *fakeSubscribe) *Handler {
	return &Handler{
		Subscribe: fs.start,
		SessionIDFromRequest: func(r *http.Request) string {
			return r.URL.Query().Get("session_id")
		},
	}
}

// TestSSEBridge_BasicWriteFlush asserts the bridge emits an SSE record
// per delivered event with id/event/data fields and the
// X-Accel-Buffering header is set.
func TestSSEBridge_BasicWriteFlush(t *testing.T) {
	fs := &fakeSubscribe{
		liveEvents: []*jsonrpc.SubscriptionEvent{
			{SubID: "sub-1", Seq: 1, Type: "session.delta", Data: map[string]string{"text": "hello"}},
			{SubID: "sub-1", Seq: 2, Type: "lane.delta", Data: map[string]string{"text": "world"}},
		},
	}
	srv := httptest.NewServer(makeHandler(fs))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"?session_id=s-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get(HeaderXAccelBuffering) != "no" {
		t.Fatalf("X-Accel-Buffering: got %q want \"no\"", resp.Header.Get(HeaderXAccelBuffering))
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type: %q", resp.Header.Get("Content-Type"))
	}

	records := readSSERecords(t, resp.Body, 2, 2*time.Second)
	if len(records) < 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0]["id"] != "1" || records[0]["event"] != "session.delta" {
		t.Fatalf("record 0: %+v", records[0])
	}
	if records[1]["id"] != "2" || records[1]["event"] != "lane.delta" {
		t.Fatalf("record 1: %+v", records[1])
	}
	if !strings.Contains(records[0]["data"], "hello") {
		t.Fatalf("record 0 data missing 'hello': %q", records[0]["data"])
	}
}

// TestSSEBridge_LastEventIDResume asserts that the Last-Event-ID
// header is plumbed through to the Subscribe call as since_seq.
func TestSSEBridge_LastEventIDResume(t *testing.T) {
	var captured atomic.Uint64
	fs := &fakeSubscribe{captureSince: &captured}
	srv := httptest.NewServer(makeHandler(fs))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"?session_id=s-1", nil)
	req.Header.Set(HeaderLastEventID, "42")

	// Use a context with timeout so we don't block forever.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get: %v", err)
	}
	if resp != nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if got := captured.Load(); got != 42 {
		t.Fatalf("since_seq: got %d want 42", got)
	}
}

// TestSSEBridge_LastEventIDOverridesQuerySinceSeq asserts the header
// takes precedence over the query param when both are present.
func TestSSEBridge_LastEventIDOverridesQuerySinceSeq(t *testing.T) {
	var captured atomic.Uint64
	fs := &fakeSubscribe{captureSince: &captured}
	srv := httptest.NewServer(makeHandler(fs))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"?session_id=s-1&since_seq=99", nil)
	req.Header.Set(HeaderLastEventID, "42")

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if got := captured.Load(); got != 42 {
		t.Fatalf("captured: got %d want 42", got)
	}
}

// TestSSEBridge_QuerySinceSeqFallback asserts the query param is used
// when no Last-Event-ID header is present.
func TestSSEBridge_QuerySinceSeqFallback(t *testing.T) {
	var captured atomic.Uint64
	fs := &fakeSubscribe{captureSince: &captured}
	srv := httptest.NewServer(makeHandler(fs))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"?session_id=s-1&since_seq=88", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if got := captured.Load(); got != 88 {
		t.Fatalf("captured: got %d want 88", got)
	}
}

// TestSSEBridge_TokenQueryParam asserts the AttachAuthFromQuery
// middleware promotes ?token=<t> into the Authorization header before
// downstream handlers see it.
func TestSSEBridge_TokenQueryParam(t *testing.T) {
	var sawAuth string
	check := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(AttachAuthFromQuery(check))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?token=secret")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if sawAuth != "Bearer secret" {
		t.Fatalf("authorization: got %q want \"Bearer secret\"", sawAuth)
	}

	// Existing Authorization header is NOT overwritten.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"?token=fromquery", nil)
	req.Header.Set("Authorization", "Bearer fromheader")
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatalf("do: %v", err)
	}
	if sawAuth != "Bearer fromheader" {
		t.Fatalf("authorization: got %q want \"Bearer fromheader\" (header takes precedence)", sawAuth)
	}
}

// TestSSEBridge_MissingSessionID returns 400.
func TestSSEBridge_MissingSessionID(t *testing.T) {
	fs := &fakeSubscribe{}
	srv := httptest.NewServer(makeHandler(fs))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// TestSSEBridge_SubscribeErrorEmitsErrorRecord asserts a subscribe
// failure produces an `event: error` record before the connection
// closes.
func TestSSEBridge_SubscribeErrorEmitsErrorRecord(t *testing.T) {
	fs := &fakeSubscribe{subErr: errors.New("nope")}
	srv := httptest.NewServer(makeHandler(fs))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?session_id=s-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "event: error") {
		t.Fatalf("missing error event: %s", body)
	}
	if !strings.Contains(string(body), "nope") {
		t.Fatalf("missing error message: %s", body)
	}
}

// TestSSEBridge_FilterFromRequest covers the QueryFilter helper +
// FilterFromRequest plumbing.
func TestSSEBridge_FilterFromRequest(t *testing.T) {
	var captured atomic.Pointer[[]string]
	fs := &fakeSubscribe{captureFiltr: &captured}
	h := makeHandler(fs)
	h.FilterFromRequest = QueryFilter
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"?session_id=s-1&filter=lane.delta,cost.tick", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	got := captured.Load()
	if got == nil || len(*got) != 2 || (*got)[0] != "lane.delta" {
		t.Fatalf("filter: %+v", got)
	}
}

// TestPathSessionID covers the helper.
func TestPathSessionID(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?session_id=qp", nil)
	if got := PathSessionID(r); got != "qp" {
		t.Fatalf("query fallback: %q", got)
	}
}

// TestSSEBridge_MalformedLastEventIDDegrades silently degrades to 0
// rather than 400.
func TestSSEBridge_MalformedLastEventIDDegrades(t *testing.T) {
	var captured atomic.Uint64
	captured.Store(999) // sentinel — must be reset to 0 by handler
	fs := &fakeSubscribe{captureSince: &captured}
	srv := httptest.NewServer(makeHandler(fs))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"?session_id=s-1", nil)
	req.Header.Set(HeaderLastEventID, "not-a-number")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if got := captured.Load(); got != 0 {
		t.Fatalf("captured: got %d want 0 (silent degrade)", got)
	}
}
