package ws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/server/jsonrpc"
	"github.com/RelayOne/r1/internal/stokerr"
)

// fakeWebAPI records the daemon calls the bridge translates frames into.
type fakeWebAPI struct {
	mu         sync.Mutex
	sends      []jsonrpc.SessionSendRequest
	interrupts []jsonrpc.SessionInterruptRequest
	subscribes []string
	sink       jsonrpc.EventSink
	cancelled  bool
}

func (f *fakeWebAPI) DaemonSessionSend(_ context.Context, req jsonrpc.SessionSendRequest) (jsonrpc.SessionSendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Text == "boom" {
		return jsonrpc.SessionSendResponse{}, stokerr.NotFoundf("session not found: %s", req.SessionID)
	}
	f.sends = append(f.sends, req)
	return jsonrpc.SessionSendResponse{DeliveredAt: "2026-07-01T00:00:00Z"}, nil
}

func (f *fakeWebAPI) DaemonSessionInterrupt(_ context.Context, req jsonrpc.SessionInterruptRequest) (jsonrpc.SessionInterruptResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupts = append(f.interrupts, req)
	return jsonrpc.SessionInterruptResponse{InterruptedAt: "2026-07-01T00:00:01Z", WasRunning: true}, nil
}

func (f *fakeWebAPI) SubscribeSessionWithSink(_ context.Context, sessionID string, _ uint64, _ []string, sink jsonrpc.EventSink) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribes = append(f.subscribes, sessionID)
	f.sink = sink
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cancelled = true
	}, nil
}

func (f *fakeWebAPI) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func (f *fakeWebAPI) interruptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.interrupts)
}

func (f *fakeWebAPI) getSink() jsonrpc.EventSink {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink
}

func dialWeb(t *testing.T, ts *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{Subprotocol, token},
		HTTPClient:   ts.Client(),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func readEnvelope(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal %q: %v", data, err)
	}
	return env
}

func writeFrame(t *testing.T, conn *websocket.Conn, frame map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, _ := json.Marshal(frame)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestWebHandler_RejectsBadToken: wrong subprotocol token → 401 before
// upgrade.
func TestWebHandler_RejectsBadToken(t *testing.T) {
	h := &WebHandler{
		API:           &fakeWebAPI{},
		ValidateToken: func(tok string) bool { return tok == "good" },
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{Subprotocol, "bad"},
		HTTPClient:   ts.Client(),
	})
	if err == nil {
		t.Fatal("dial with bad token succeeded, want 401")
	}
	if resp != nil && resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	// Missing r1.bearer subprotocol entirely → also 401.
	_, resp2, err2 := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: ts.Client()})
	if err2 == nil {
		t.Fatal("dial without subprotocol succeeded, want 401")
	}
	if resp2 != nil && resp2.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp2.StatusCode)
	}
}

// TestWebHandler_TypedFrameRoundTrip drives the full typed-frame wire
// contract: ping→pong, chat→DaemonSessionSend, interrupt→drop-partial,
// subscribe→lane.* envelopes from the fanout sink, error mapping.
func TestWebHandler_TypedFrameRoundTrip(t *testing.T) {
	api := &fakeWebAPI{}
	h := &WebHandler{
		API:           api,
		ValidateToken: func(tok string) bool { return tok == "good" },
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	conn := dialWeb(t, ts, "good")
	defer conn.Close(websocket.StatusNormalClosure, "done")
	if got := conn.Subprotocol(); got != Subprotocol {
		t.Fatalf("negotiated subprotocol = %q, want %q", got, Subprotocol)
	}

	// ping → pong
	writeFrame(t, conn, map[string]any{"type": "ping"})
	pong := readEnvelope(t, conn)
	if pong["type"] != "pong" {
		t.Fatalf("reply type = %v, want pong", pong["type"])
	}
	if _, ok := pong["ts"].(string); !ok {
		t.Error("pong missing ts")
	}

	// chat → DaemonSessionSend
	writeFrame(t, conn, map[string]any{"type": "chat", "sessionId": "s-1", "content": "hello"})
	deadline := time.Now().Add(2 * time.Second)
	for api.sendCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if api.sendCount() != 1 {
		t.Fatal("chat frame did not reach DaemonSessionSend")
	}
	api.mu.Lock()
	sent := api.sends[0]
	api.mu.Unlock()
	if sent.SessionID != "s-1" || sent.Text != "hello" {
		t.Errorf("send req = %+v", sent)
	}

	// interrupt → DaemonSessionInterrupt with DropPartial
	writeFrame(t, conn, map[string]any{"type": "interrupt", "sessionId": "s-1"})
	deadline = time.Now().Add(2 * time.Second)
	for api.interruptCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if api.interruptCount() != 1 {
		t.Fatal("interrupt frame did not reach DaemonSessionInterrupt")
	}
	api.mu.Lock()
	intr := api.interrupts[0]
	api.mu.Unlock()
	if intr.SessionID != "s-1" || !intr.DropPartial {
		t.Errorf("interrupt req = %+v, want drop_partial", intr)
	}

	// subscribe → sink captured; fanout event surfaces as lane.delta.
	writeFrame(t, conn, map[string]any{"type": "subscribe", "sessionId": "s-1"})
	deadline = time.Now().Add(2 * time.Second)
	for api.getSink() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	sink := api.getSink()
	if sink == nil {
		t.Fatal("subscribe frame did not reach SubscribeSessionWithSink")
	}
	startedAt := time.Now().UTC()
	if err := sink(context.Background(), &jsonrpc.SubscriptionEvent{
		SubID: "sub-x", Seq: 7, Type: string(hub.EventLaneDelta),
		Data: &hub.Event{
			Type:      hub.EventLaneDelta,
			Timestamp: startedAt,
			Lane: &hub.LaneEvent{
				LaneID:    "lane-9",
				SessionID: "s-1",
				Block:     &hub.LaneContentBlock{Type: "text", Text: "chunk"},
			},
		},
	}); err != nil {
		t.Fatalf("sink: %v", err)
	}
	delta := readEnvelope(t, conn)
	if delta["type"] != "lane.delta" {
		t.Fatalf("envelope type = %v, want lane.delta", delta["type"])
	}
	if delta["sessionId"] != "s-1" || delta["laneId"] != "lane-9" {
		t.Errorf("envelope routing fields = %v / %v", delta["sessionId"], delta["laneId"])
	}
	if delta["data"] != "chunk" {
		t.Errorf("data = %v, want chunk", delta["data"])
	}
	if seq, _ := delta["seq"].(float64); seq != 7 {
		t.Errorf("seq = %v, want 7", delta["seq"])
	}

	// lane.status envelope with state mapping.
	if err := sink(context.Background(), &jsonrpc.SubscriptionEvent{
		SubID: "sub-x", Seq: 8, Type: string(hub.EventLaneStatus),
		Data: &hub.Event{
			Type:      hub.EventLaneStatus,
			Timestamp: startedAt,
			Lane:      &hub.LaneEvent{LaneID: "lane-9", SessionID: "s-1", Status: hub.LaneStatusDone},
		},
	}); err != nil {
		t.Fatalf("sink status: %v", err)
	}
	status := readEnvelope(t, conn)
	if status["type"] != "lane.status" || status["state"] != "completed" {
		t.Errorf("status envelope = %v", status)
	}

	// chat error → error envelope with mapped code.
	writeFrame(t, conn, map[string]any{"type": "chat", "sessionId": "s-1", "content": "boom"})
	errEnv := readEnvelope(t, conn)
	if errEnv["type"] != "error" {
		t.Fatalf("envelope type = %v, want error", errEnv["type"])
	}
	if errEnv["code"] != "NOT_FOUND" {
		t.Errorf("error code = %v, want NOT_FOUND", errEnv["code"])
	}
	if errEnv["retryable"] != false {
		t.Errorf("retryable = %v, want false", errEnv["retryable"])
	}

	// unsubscribe cancels the subscription.
	writeFrame(t, conn, map[string]any{"type": "unsubscribe", "sessionId": "s-1"})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		api.mu.Lock()
		c := api.cancelled
		api.mu.Unlock()
		if c {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	api.mu.Lock()
	cancelled := api.cancelled
	api.mu.Unlock()
	if !cancelled {
		t.Error("unsubscribe did not cancel the subscription")
	}
}

// TestWebEnvelopeFor_ReplayPayload proves the journal-replay payload
// shape (json.RawMessage) translates identically to the live path.
func TestWebEnvelopeFor_ReplayPayload(t *testing.T) {
	raw, _ := json.Marshal(&hub.Event{
		Type:      hub.EventLaneKilled,
		Timestamp: time.Now(),
		Lane:      &hub.LaneEvent{LaneID: "lane-2", SessionID: "s-1", Reason: "operator"},
	})
	env, ok := webEnvelopeFor("s-1", &jsonrpc.SubscriptionEvent{
		Seq: 3, Type: string(hub.EventLaneKilled), Data: json.RawMessage(raw),
	})
	if !ok {
		t.Fatal("replay payload not translated")
	}
	if env["type"] != "lane.killed" || env["laneId"] != "lane-2" || env["reason"] != "operator" {
		t.Errorf("envelope = %v", env)
	}

	// Non-web event types are skipped, not errored.
	if _, ok := webEnvelopeFor("s-1", &jsonrpc.SubscriptionEvent{
		Seq: 4, Type: "tool.post_use", Data: json.RawMessage(`{"type":"tool.post_use"}`),
	}); ok {
		t.Error("non-lane event should be skipped")
	}
}
