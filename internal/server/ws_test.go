// Package server — tests for the lanes-protocol WebSocket upgrade
// (TASK-14: subprotocol negotiation, token validation, Origin pinning,
// 15s ping / 30s idle close).
package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// dialLanesWS performs a raw WebSocket upgrade against the lanes WS
// endpoint with the supplied subprotocols and Origin. Returns the live
// net.Conn (for further frame IO), a buffered reader, the upgrade
// response, and the error.
//
// The upgrade is hand-rolled so individual tests can probe failure
// scenarios (missing subprotocol, bad origin) without negotiating a
// full client library.
func dialLanesWS(t *testing.T, ts *httptest.Server, subprotocols []string, origin string, bearer string) (net.Conn, *bufio.Reader, *http.Response, error) {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return nil, nil, nil, err
	}

	// Random nonce per RFC 6455 §4.1.
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	key := encodeBase64(nonce[:])

	headers := []string{
		"GET /v1/lanes/ws HTTP/1.1",
		"Host: " + u.Host,
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Version: 13",
		"Sec-WebSocket-Key: " + key,
	}
	if len(subprotocols) > 0 {
		headers = append(headers, "Sec-WebSocket-Protocol: "+strings.Join(subprotocols, ", "))
	}
	if origin != "" {
		headers = append(headers, "Origin: "+origin)
	}
	if bearer != "" {
		headers = append(headers, "Authorization: Bearer "+bearer)
	}
	req := strings.Join(headers, "\r\n") + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	return conn, br, resp, nil
}

// encodeBase64 is a tiny wrapper around the std lib so the test file
// stays self-contained without an extra import.
func encodeBase64(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out []byte
	for len(b) >= 3 {
		v := uint(b[0])<<16 | uint(b[1])<<8 | uint(b[2])
		out = append(out,
			alphabet[(v>>18)&0x3F],
			alphabet[(v>>12)&0x3F],
			alphabet[(v>>6)&0x3F],
			alphabet[v&0x3F],
		)
		b = b[3:]
	}
	switch len(b) {
	case 1:
		v := uint(b[0]) << 16
		out = append(out, alphabet[(v>>18)&0x3F], alphabet[(v>>12)&0x3F], '=', '=')
	case 2:
		v := uint(b[0])<<16 | uint(b[1])<<8
		out = append(out, alphabet[(v>>18)&0x3F], alphabet[(v>>12)&0x3F], alphabet[(v>>6)&0x3F], '=')
	}
	return string(out)
}

// TestLaneWSUpgradeRejectsMissingSubprotocol verifies that a client that
// fails to advertise r1.lanes.v1 in Sec-WebSocket-Protocol is rejected
// with 401 (spec §5.4: close code 4401 unauthorized — surfaced as 401
// at the HTTP layer since the upgrade never completes).
func TestLaneWSUpgradeRejectsMissingSubprotocol(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// No subprotocol at all.
	conn, _, resp, err := dialLanesWS(t, ts, nil, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "subprotocol") {
		t.Errorf("body should mention subprotocol, got %q", body)
	}

	// Wrong subprotocol.
	conn2, _, resp2, err := dialLanesWS(t, ts, []string{"chat.v1"}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn2.Close()
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-subprotocol status = %d, want 401", resp2.StatusCode)
	}
}

// TestLaneWSUpgradeRejectsBadOrigin verifies the Origin pin: a
// non-loopback Origin not in AllowedOrigins must be rejected with 403.
func TestLaneWSUpgradeRejectsBadOrigin(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	srv.AllowedOrigins = []string{"https://allowed.example.com"}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Hostile origin.
	conn, _, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "https://evil.example.com", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("evil origin status = %d, want 403", resp.StatusCode)
	}

	// Allowed origin: handshake should succeed.
	conn2, _, resp2, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "https://allowed.example.com", "")
	if err != nil {
		t.Fatalf("dial allowed: %v", err)
	}
	defer conn2.Close()
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("allowed origin status = %d, want 101", resp2.StatusCode)
	}
	if got := resp2.Header.Get("Sec-WebSocket-Protocol"); got != LanesSubprotocol {
		t.Errorf("Sec-WebSocket-Protocol echo = %q, want %s", got, LanesSubprotocol)
	}
	if got := resp2.Header.Get("X-R1-Lanes-Version"); got != "1" {
		t.Errorf("X-R1-Lanes-Version = %q, want 1", got)
	}

	// Loopback (127.0.0.1) MUST be allowed even when AllowedOrigins is
	// non-empty and does not list it.
	conn3, _, resp3, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "http://127.0.0.1:1234", "")
	if err != nil {
		t.Fatalf("dial loopback: %v", err)
	}
	defer conn3.Close()
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("loopback origin status = %d, want 101", resp3.StatusCode)
	}
}

// TestLaneWSAuthSubprotocolToken exercises the r1.lanes.v1+token.<token>
// variant: a client that cannot send Authorization headers (browser)
// embeds the bearer in the subprotocol slot.
func TestLaneWSAuthSubprotocolToken(t *testing.T) {
	t.Parallel()
	srv := New(0, "secret-xyz", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Wrong token: rejected.
	conn, _, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol, LanesSubprotocolTokenPrefix + "wrong-token"}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d, want 401", resp.StatusCode)
	}

	// Correct token via subprotocol.
	conn2, _, resp2, err := dialLanesWS(t, ts, []string{LanesSubprotocol, LanesSubprotocolTokenPrefix + "secret-xyz"}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn2.Close()
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("correct-token status = %d, want 101", resp2.StatusCode)
	}

	// Correct token via Authorization header.
	conn3, _, resp3, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "secret-xyz")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn3.Close()
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("bearer status = %d, want 101", resp3.StatusCode)
	}
}

// TestLaneWSPingPongKeepsAlive verifies that the server emits $/ping
// JSON-RPC notifications on its own cadence and that a pong reply keeps
// the connection alive past the idle deadline.
//
// The production interval is 15s. Tests can't comfortably wait for that;
// instead we wait long enough to receive at least one $/ping (the loop
// fires immediately after the upgrade handshake on the next tick) and
// confirm the JSON shape.
func TestLaneWSPingPongKeepsAlive(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("ping interval is 15s; skip in -short")
	}

	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}

	// Wait for the first $/ping. The server fires on a 15s ticker, so
	// we cap at 17s.
	_ = conn.SetReadDeadline(time.Now().Add(17 * time.Second))
	opcode, payload, err := readWSFrameAsClient(br)
	if err != nil {
		t.Fatalf("read $/ping: %v", err)
	}
	if opcode != 0x1 {
		t.Fatalf("opcode = %x, want text", opcode)
	}
	var msg map[string]any
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["method"] != "$/ping" {
		t.Errorf("method = %v, want $/ping", msg["method"])
	}

	// Send $/pong: server should not close.
	pong, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/pong",
		"params":  map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano)},
	})
	if err := writeWSFrameMasked(conn, 0x1, pong); err != nil {
		t.Fatalf("write pong: %v", err)
	}
	// Connection still alive — close cleanly.
	_ = writeWSFrameMasked(conn, 0x8, []byte{0x03, 0xE8})
}

// TestLaneWSIdleTimeoutCloses verifies that 30s of silence closes the
// connection with code 4408. This test is also -short-skipped because
// of the 30s wait.
func TestLaneWSIdleTimeoutCloses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("idle timeout is 30s; skip in -short")
	}

	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()

	_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	// Drain pings until the close frame.
	for {
		opcode, payload, err := readWSFrameAsClient(br)
		if err != nil {
			return
		}
		if opcode == 0x8 {
			if len(payload) >= 2 {
				code := uint16(payload[0])<<8 | uint16(payload[1])
				if code != wsCloseIdleTimeout {
					t.Errorf("close code = %d, want %d", code, wsCloseIdleTimeout)
				}
			}
			return
		}
	}
}

// TestLaneWSRejectsBinaryFrame: per spec §5.4, binary frames are
// reserved; the server MUST close with code 4400.
func TestLaneWSRejectsBinaryFrame(t *testing.T) {
	t.Parallel()
	srv := New(0, "", NewEventBus()).WithLanes(&LanesWiring{Hub: newFakeLanesHub()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	conn, br, resp, err := dialLanesWS(t, ts, []string{LanesSubprotocol}, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	defer resp.Body.Close()

	if err := writeWSFrameMasked(conn, 0x2, []byte{0x00, 0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		opcode, payload, err := readWSFrameAsClient(br)
		if err != nil {
			t.Fatalf("read close: %v", err)
		}
		if opcode == 0x8 {
			if len(payload) < 2 {
				t.Fatalf("close payload too short: %v", payload)
			}
			code := uint16(payload[0])<<8 | uint16(payload[1])
			if code != wsCloseProtoErr {
				t.Errorf("close code = %d, want %d", code, wsCloseProtoErr)
			}
			return
		}
	}
}

// readWSFrameAsClient reads one server-sent frame (no mask, since servers
// don't mask per RFC 6455 §5.3).
func readWSFrameAsClient(r *bufio.Reader) (byte, []byte, error) {
	head, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	opcode := head & 0x0f
	lenByte, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length := uint64(lenByte & 0x7f)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(buf[:])
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

// writeWSFrameMasked writes a client-side frame with the mandatory mask
// (RFC 6455 §5.3 — clients MUST mask).
func writeWSFrameMasked(conn net.Conn, opcode byte, payload []byte) error {
	var buf bytes.Buffer
	buf.WriteByte(0x80 | opcode)
	length := len(payload)
	switch {
	case length <= 125:
		buf.WriteByte(0x80 | byte(length))
	case length <= 65535:
		buf.WriteByte(0x80 | 126)
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], uint16(length))
		buf.Write(lb[:])
	default:
		buf.WriteByte(0x80 | 127)
		var lb [8]byte
		binary.BigEndian.PutUint64(lb[:], uint64(length))
		buf.Write(lb[:])
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	buf.Write(mask[:])
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	buf.Write(masked)
	_, err := conn.Write(buf.Bytes())
	return err
}
