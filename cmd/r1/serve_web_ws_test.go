// serve_web_ws_test.go — integration test for the web chat surface
// mounted by runServeLoop (audit A008): POST /auth/ws-ticket mints a
// short-lived token the /ws typed-frame bridge accepts as the
// subprotocol slot, and typed frames land on the REAL HubHandler.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/server"
	"github.com/RelayOne/r1/internal/server/jsonrpc"
	"github.com/RelayOne/r1/internal/server/sessionhub"
	"github.com/RelayOne/r1/internal/server/ws"
)

const testBearer = "sekret-bearer-token"

// newWebTestDaemon builds the rpc handler + one journaled session,
// mirroring the serve loop's wiring (SetJournalPathFn before any
// session.start).
func newWebTestDaemon(t *testing.T) (*jsonrpc.HubHandler, *sessionhub.Session, string) {
	t.Helper()
	t.Setenv("R1_HOME", t.TempDir())
	shub, err := sessionhub.NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	rpcHandler := jsonrpc.NewHubHandler(shub)
	jd := t.TempDir()
	rpcHandler.SetJournalPathFn(func(sessionID string) string {
		return filepath.Join(jd, sessionID+".jsonl")
	})
	resp, err := rpcHandler.DaemonSessionStart(context.Background(), jsonrpc.DaemonSessionStartRequest{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("session.start: %v", err)
	}
	sess, err := shub.Get(resp.SessionID)
	if err != nil {
		t.Fatalf("hub.Get: %v", err)
	}
	return rpcHandler, sess, resp.SessionID
}

func testLaneEvent(sessionID, text string) *hub.Event {
	return &hub.Event{
		Type:      hub.EventLaneDelta,
		Timestamp: time.Now(),
		Lane: &hub.LaneEvent{
			LaneID:    "lane-1",
			SessionID: sessionID,
			Block:     &hub.LaneContentBlock{Type: "text", Text: text},
		},
	}
}

// TestServeWebMounts_TicketThenWS is the A008 activation proof at the
// route level: POST /auth/ws-ticket (bearer-authed) mints a ticket the
// /ws typed-frame bridge accepts as the subprotocol token, and a chat
// frame lands on the real HubHandler (surfacing the honest
// "not running" validation error, since no agent loop is driving the
// session in this test).
func TestServeWebMounts_TicketThenWS(t *testing.T) {
	rpcHandler, _, sid := newWebTestDaemon(t)

	tickets := server.NewWSTicketStore(server.DefaultWSTicketTTL)
	mux := http.NewServeMux()
	mux.Handle("/auth/ws-ticket", server.RequireBearer(testBearer)(tickets.Handler()))
	mux.Handle("/ws", &ws.WebHandler{
		API: rpcHandler,
		ValidateToken: func(tok string) bool {
			return tok == testBearer || tickets.Validate(tok)
		},
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Mint without bearer → 401.
	respNoAuth, err := ts.Client().Post(ts.URL+"/auth/ws-ticket", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	respNoAuth.Body.Close()
	if respNoAuth.StatusCode != 401 {
		t.Fatalf("unauth mint status = %d, want 401", respNoAuth.StatusCode)
	}

	// Mint with bearer → ticket.
	req, _ := http.NewRequest("POST", ts.URL+"/auth/ws-ticket", nil)
	req.Header.Set("Authorization", "Bearer "+testBearer)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	var ticket struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ticket); err != nil {
		t.Fatalf("decode ticket: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || ticket.Token == "" {
		t.Fatalf("mint status=%d token=%q", resp.StatusCode, ticket.Token)
	}

	// Dial /ws with the TICKET (not the bearer) in the subprotocol
	// slot — the browser path.
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"r1.bearer", ticket.Token},
		HTTPClient:   ts.Client(),
	})
	if err != nil {
		t.Fatalf("ws dial with ticket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// A random token is rejected pre-upgrade.
	_, badResp, badErr := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"r1.bearer", "wt-forged"},
		HTTPClient:   ts.Client(),
	})
	if badErr == nil {
		t.Fatal("forged ticket accepted")
	}
	if badResp != nil && badResp.StatusCode != 401 {
		t.Errorf("forged ticket status = %d, want 401", badResp.StatusCode)
	}

	// Typed chat frame reaches DaemonSessionSend on the REAL handler:
	// the session exists but is not running, so the contract's honest
	// reply is an error envelope with INVALID_INPUT (inbox closed).
	chat, _ := json.Marshal(map[string]any{"type": "chat", "sessionId": sid, "content": "hello"})
	if err := conn.Write(dialCtx, websocket.MessageText, chat); err != nil {
		t.Fatalf("write chat: %v", err)
	}
	_, data, err := conn.Read(dialCtx)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if env["type"] != "error" || env["code"] != "INVALID_INPUT" {
		t.Fatalf("envelope = %v, want error/INVALID_INPUT (session not running)", env)
	}
	if env["sessionId"] != sid {
		t.Errorf("error sessionId = %v, want %v", env["sessionId"], sid)
	}

	// Unknown session → NOT_FOUND mapping over the same socket.
	chat2, _ := json.Marshal(map[string]any{"type": "chat", "sessionId": "s-nope", "content": "x"})
	if err := conn.Write(dialCtx, websocket.MessageText, chat2); err != nil {
		t.Fatalf("write chat2: %v", err)
	}
	_, data2, err := conn.Read(dialCtx)
	if err != nil {
		t.Fatalf("read reply2: %v", err)
	}
	var env2 map[string]any
	_ = json.Unmarshal(data2, &env2)
	if env2["code"] != "NOT_FOUND" {
		t.Errorf("envelope2 code = %v, want NOT_FOUND", env2["code"])
	}
}
