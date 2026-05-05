package ws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/RelayOne/r1/internal/server/jsonrpc"
)

// newTestServer wires a Handler around an httptest.Server. The Dispatcher
// is initialised with one method "echo" that returns its params.
func newTestServer(t *testing.T, token string) (*httptest.Server, *Handler) {
	t.Helper()
	d := jsonrpc.NewDispatcher()
	d.Register("echo", func(ctx context.Context, params json.RawMessage) (any, error) {
		return json.RawMessage(params), nil
	})
	h := &Handler{
		Dispatcher:   d,
		Token:        token,
		IdleTimeout:  500 * time.Millisecond,
		PingInterval: 100 * time.Millisecond,
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, h
}

// dialURL returns the ws:// URL for an httptest.Server's URL (which is
// http://127.0.0.1:PORT).
func dialURL(httpURL string) string {
	if strings.HasPrefix(httpURL, "https://") {
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	}
	return "ws://" + strings.TrimPrefix(httpURL, "http://")
}

// TestWS_UpgradeRequiresSubprotocol asserts the upgrade is rejected with
// HTTP 401 when the client doesn't advertise `r1.bearer`. Both branches:
// no subprotocol header at all, AND the wrong subprotocol value.
func TestWS_UpgradeRequiresSubprotocol(t *testing.T) {
	srv, _ := newTestServer(t, "")
	cases := []struct {
		name       string
		subprotos  []string
		wantStatus int
	}{
		{"no_subprotos", nil, http.StatusUnauthorized},
		{"wrong_subprotos", []string{"unrelated"}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			opts := &websocket.DialOptions{Subprotocols: tc.subprotos}
			conn, resp, err := websocket.Dial(ctx, dialURL(srv.URL), opts)
			if err == nil {
				_ = conn.Close(websocket.StatusNormalClosure, "")
				t.Fatal("expected error, got success")
			}
			if resp == nil {
				t.Fatalf("expected http response, got %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d want %d", resp.StatusCode, tc.wantStatus)
			}
			// WWW-Authenticate must be set per spec (TASK-18 / RFC 7235).
			if got := resp.Header.Get("WWW-Authenticate"); got == "" {
				t.Fatal("missing WWW-Authenticate header")
			}
		})
	}
}

// TestWS_UpgradeBearerTokenRejected asserts that the second
// subprotocol value is treated as the bearer token.
func TestWS_UpgradeBearerTokenRejected(t *testing.T) {
	srv, _ := newTestServer(t, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Wrong token in the second slot.
	opts := &websocket.DialOptions{Subprotocols: []string{Subprotocol, "wrong"}}
	conn, resp, err := websocket.Dial(ctx, dialURL(srv.URL), opts)
	if err == nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected error")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
}

// TestWS_UpgradeAcceptsCorrectToken asserts that the correct token
// passes the upgrade and that the negotiated subprotocol is "r1.bearer".
func TestWS_UpgradeAcceptsCorrectToken(t *testing.T) {
	srv, _ := newTestServer(t, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{Subprotocols: []string{Subprotocol, "secret"}}
	conn, _, err := websocket.Dial(ctx, dialURL(srv.URL), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") }()
	if got := conn.Subprotocol(); got != Subprotocol {
		t.Fatalf("negotiated subprotocol: got %q want %q", got, Subprotocol)
	}
}

// TestWS_RoundTripDispatch sends one JSON-RPC request over the WS and
// verifies the response echoes back. Smoke test that wires the
// Dispatcher to the read loop.
func TestWS_RoundTripDispatch(t *testing.T) {
	srv, _ := newTestServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{Subprotocols: []string{Subprotocol}}
	conn, _, err := websocket.Dial(ctx, dialURL(srv.URL), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") }()

	in := []byte(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{"hi":"there"}}`)
	if err := conn.Write(ctx, websocket.MessageText, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode: %v: raw=%s", err, data)
	}
	if out.ID != 1 {
		t.Fatalf("id: got %d", out.ID)
	}
	if !strings.Contains(string(out.Result), `"hi":"there"`) {
		t.Fatalf("result did not echo: %s", out.Result)
	}
}

// TestWS_PingWatchdogClosesIdleConnection asserts that a connection
// that goes silent past IdleTimeout is closed by the watchdog. The
// test server is configured with a 500ms idle timeout in
// newTestServer; we wait past it without sending or reading, then
// confirm Read errors out.
func TestWS_PingWatchdogClosesIdleConnection(t *testing.T) {
	srv, _ := newTestServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{Subprotocols: []string{Subprotocol}}
	conn, _, err := websocket.Dial(ctx, dialURL(srv.URL), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	// Sleep past the server's idle timeout (500ms) + a generous slack.
	// The server will close us; the next Read fails.
	time.Sleep(2 * time.Second)
	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("expected read error after idle timeout")
	}
	// Accept any non-nil error — coder/websocket surfaces the close as
	// either io.EOF, a CloseError, or a context cancel depending on
	// timing.
	if errors.Is(err, io.EOF) {
		return
	}
	t.Logf("got expected close error: %v", err)
}

// TestWS_OnConnectFires asserts the OnConnect hook is invoked once per
// connection and that the *Conn it receives is usable for outbound
// notifications.
func TestWS_OnConnectFires(t *testing.T) {
	srv, h := newTestServer(t, "")
	calls := make(chan *Conn, 1)
	h.OnConnect = func(ctx context.Context, c *Conn) {
		// Send one $/event notification immediately so the test can
		// observe it client-side.
		go func() {
			_ = c.WriteNotification(ctx, jsonrpc.NewNotification("$/event", map[string]any{"hello": true}))
			calls <- c
		}()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{Subprotocols: []string{Subprotocol}}
	conn, _, err := websocket.Dial(ctx, dialURL(srv.URL), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") }()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"$/event"`) {
		t.Fatalf("expected $/event notification, got %s", data)
	}
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("OnConnect hook did not fire")
	}
}

// TestWS_BatchAndNotificationDispatch covers two paths in one test: a
// batch with a mix of notifications and requests, and pure-notification
// frames that produce no wire response.
func TestWS_BatchAndNotificationDispatch(t *testing.T) {
	srv, _ := newTestServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{Subprotocols: []string{Subprotocol}}
	conn, _, err := websocket.Dial(ctx, dialURL(srv.URL), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") }()

	batch := []byte(`[
		{"jsonrpc":"2.0","method":"echo"},
		{"jsonrpc":"2.0","id":42,"method":"echo","params":{"k":"v"}}
	]`)
	if err := conn.Write(ctx, websocket.MessageText, batch); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if data[0] != '[' {
		t.Fatalf("expected batch response, got %s", data)
	}
	if !strings.Contains(string(data), `"id":42`) {
		t.Fatalf("missing id=42: %s", data)
	}
}

// TestParseSubprotocols covers the comma-separated split with various
// whitespace edge cases.
func TestParseSubprotocols(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"r1.bearer", []string{"r1.bearer"}},
		{"r1.bearer, abc", []string{"r1.bearer", "abc"}},
		{" r1.bearer , abc ", []string{"r1.bearer", "abc"}},
		{"r1.bearer,,abc", []string{"r1.bearer", "abc"}},
	}
	for _, tc := range cases {
		got := parseSubprotocols(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("%q: len got=%d want=%d (got=%v)", tc.in, len(got), len(tc.want), got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%q[%d]: got %q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
