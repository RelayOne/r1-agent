// remoteTransport.KillAll wire-behavior test.
//
// The lanes protocol ships NO r1.lanes.killAll RPC (MCP LaneToolNames:
// list/subscribe/get/kill/pin only), so KillAll must be an iterated
// r1.lanes.kill over the non-terminal lanes returned by r1.lanes.list —
// matching localTransport.KillAll semantics (audit A040/A073 killAll
// decision). This test drives the real frame codec (writeFrame /
// readFrame / callRPC / readLoop) over a net.Pipe against a minimal
// in-process JSON-RPC peer and asserts the wire methods issued.
package lanes

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// readClientWSFrame parses one client→server WebSocket frame (client
// frames are masked per RFC 6455 §5.3) and returns the opcode and the
// unmasked payload.
func readClientWSFrame(br *bufio.Reader) (byte, []byte, error) {
	head, err := br.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	opcode := head & 0x0f
	lenByte, err := br.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	masked := lenByte&0x80 != 0
	length := uint64(lenByte & 0x7f)
	switch length {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

// writeServerWSFrame writes one unmasked server→client text frame.
func writeServerWSFrame(w io.Writer, payload []byte) error {
	header := []byte{0x81} // FIN | text
	switch {
	case len(payload) <= 125:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		var b [3]byte
		b[0] = 126
		binary.BigEndian.PutUint16(b[1:], uint16(len(payload)))
		header = append(header, b[:]...)
	default:
		var b [9]byte
		b[0] = 127
		binary.BigEndian.PutUint64(b[1:], uint64(len(payload)))
		header = append(header, b[:]...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// TestRemoteTransport_KillAllIteratesPerLaneKill asserts KillAll issues
// r1.lanes.list, then one r1.lanes.kill per NON-terminal lane, and
// never a r1.lanes.killAll method.
func TestRemoteTransport_KillAllIteratesPerLaneKill(t *testing.T) {
	srvConn, cliConn := net.Pipe()
	defer srvConn.Close()
	defer cliConn.Close()

	tr := &remoteTransport{
		addr:    "pipe",
		pending: make(map[int64]chan rpcReply),
		conn:    cliConn,
		bufrw:   bufio.NewReadWriter(bufio.NewReader(cliConn), bufio.NewWriter(cliConn)),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out := make(chan LaneEvent, 8)
	go func() { _ = tr.readLoop(ctx, out) }()

	// Fake peer: replies to r1.lanes.list with one running + one done
	// lane; acks every r1.lanes.kill; records every method + params.
	var (
		mu      sync.Mutex
		methods []string
		killed  []string
	)
	go func() {
		br := bufio.NewReader(srvConn)
		for {
			_, payload, err := readClientWSFrame(br)
			if err != nil {
				return
			}
			var req struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				return
			}
			mu.Lock()
			methods = append(methods, req.Method)
			if req.Method == "r1.lanes.kill" {
				if id, ok := req.Params["lane_id"].(string); ok {
					killed = append(killed, id)
				}
			}
			mu.Unlock()

			var result string
			switch req.Method {
			case "r1.lanes.list":
				result = `{"lanes":[` +
					`{"lane_id":"lane_a","label":"worker a","kind":"tool","status":"running"},` +
					`{"lane_id":"lane_b","label":"worker b","kind":"tool","status":"done"}]}`
			case "r1.lanes.kill":
				result = `{"status":"cancelled"}`
			default:
				// Unknown method — reply -32601 like the real server.
				resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"method not found"}}`, req.ID)
				if err := writeServerWSFrame(srvConn, []byte(resp)); err != nil {
					return
				}
				continue
			}
			resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
			if err := writeServerWSFrame(srvConn, []byte(resp)); err != nil {
				return
			}
		}
	}()

	if err := tr.KillAll(ctx); err != nil {
		t.Fatalf("KillAll: %v", err)
	}

	mu.Lock()
	gotMethods := append([]string(nil), methods...)
	gotKilled := append([]string(nil), killed...)
	mu.Unlock()

	if len(gotMethods) != 2 || gotMethods[0] != "r1.lanes.list" || gotMethods[1] != "r1.lanes.kill" {
		t.Fatalf("wire methods = %v, want [r1.lanes.list r1.lanes.kill]", gotMethods)
	}
	for _, mth := range gotMethods {
		if mth == "r1.lanes.killAll" {
			t.Fatalf("client issued r1.lanes.killAll — no such wire method exists")
		}
	}
	if len(gotKilled) != 1 || gotKilled[0] != "lane_a" {
		t.Fatalf("killed lanes = %v, want [lane_a] (lane_b is terminal and must be skipped)", gotKilled)
	}
}
