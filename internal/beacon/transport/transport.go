package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- RFC 6455 WebSocket handshake mandates SHA1 for Sec-WebSocket-Accept; not used for cryptographic security.
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

const (
	ChannelSessionFrame = "session_frame"
	ChannelTrustSignal  = "trust_signal"
)

type Envelope struct {
	Channel string          `json:"channel"`
	Session string          `json:"session,omitempty"`
	Body    json.RawMessage `json:"body"`
}

func (e Envelope) Validate() error {
	if e.Channel != ChannelSessionFrame && e.Channel != ChannelTrustSignal {
		return fmt.Errorf("transport: unsupported channel %q", e.Channel)
	}
	if len(e.Body) == 0 {
		return errors.New("transport: body required")
	}
	return nil
}

type Handler func(context.Context, Envelope) error

func HTTPHandler(fn Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		var env Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := env.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := fn(r.Context(), env); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

func Post(ctx context.Context, client *http.Client, url string, env Envelope) error {
	if err := env.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("transport: marshal envelope: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("transport: http %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return nil
}

func ServeWS(w http.ResponseWriter, r *http.Request, fn Handler) error {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return errors.New("transport: expected websocket upgrade")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return errors.New("transport: websocket unsupported")
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return err
	}
	defer conn.Close()

	bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	bufrw.WriteString("Upgrade: websocket\r\n")
	bufrw.WriteString("Connection: Upgrade\r\n")
	bufrw.WriteString("Sec-WebSocket-Accept: " + computeAcceptKey(r.Header.Get("Sec-WebSocket-Key")) + "\r\n")
	bufrw.WriteString("\r\n")
	if err := bufrw.Flush(); err != nil {
		return err
	}

	for {
		payload, err := readWSFrame(bufrw.Reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var env Envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			return fmt.Errorf("transport: decode ws envelope: %w", err)
		}
		if err := env.Validate(); err != nil {
			return err
		}
		if err := fn(r.Context(), env); err != nil {
			return err
		}
	}
}

func DialAndSendWS(ctx context.Context, url string, env Envelope) error {
	if err := env.Validate(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	conn, err := dialHTTP(req)
	if err != nil {
		return err
	}
	defer conn.Close()
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	if err := req.Write(bufrw); err != nil {
		return err
	}
	if err := bufrw.Flush(); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufrw.Reader, req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("transport: websocket upgrade failed: %s", resp.Status)
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := writeWSTextFrame(bufrw.Writer, data); err != nil {
		return err
	}
	return bufrw.Flush()
}

func dialHTTP(req *http.Request) (net.Conn, error) {
	host := req.URL.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	var d net.Dialer
	return d.DialContext(req.Context(), "tcp", host)
}

func computeAcceptKey(clientKey string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.New()
	_, _ = io.WriteString(h, clientKey+magic)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func writeWSTextFrame(w *bufio.Writer, payload []byte) error {
	if err := w.WriteByte(0x81); err != nil {
		return err
	}
	length := len(payload)
	switch {
	case length <= 125:
		if err := w.WriteByte(byte(length)); err != nil {
			return err
		}
	case length <= 65535:
		if err := w.WriteByte(126); err != nil {
			return err
		}
		var buf [2]byte
		binary.BigEndian.PutUint16(buf[:], uint16(length))
		if _, err := w.Write(buf[:]); err != nil {
			return err
		}
	default:
		if err := w.WriteByte(127); err != nil {
			return err
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(length))
		if _, err := w.Write(buf[:]); err != nil {
			return err
		}
	}
	_, err := w.Write(payload)
	return err
}

func readWSFrame(r *bufio.Reader) ([]byte, error) {
	head, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	opcode := head & 0x0f
	if opcode == 0x8 {
		return nil, io.EOF
	}
	if opcode != 0x1 {
		return nil, fmt.Errorf("transport: unsupported websocket opcode %d", opcode)
	}
	lenByte, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	masked := lenByte&0x80 != 0
	length := uint64(lenByte & 0x7f)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(buf[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, nil
}
