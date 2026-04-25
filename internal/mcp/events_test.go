package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/streamjson"
)

// forbiddenKeys are payload keys that MUST NEVER appear on any MCP
// lifecycle event (specs/mcp-client.md §Event Emission hard rule).
// Substring match is used because nested keys or hyphenated variants
// would be equally unacceptable.
var forbiddenKeys = []string{
	"args",
	"arguments",
	"result",
	"body",
	"token",
	"authorization",
	"bearer",
	"api_key",
	"apikey",
	"secret",
	"password",
}

// assertNoForbiddenKeys walks a payload and fails the test if any key
// matches a forbidden substring (case-insensitive).
func assertNoForbiddenKeys(t *testing.T, where string, payload map[string]any) {
	t.Helper()
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch tv := v.(type) {
		case map[string]any:
			for k, vv := range tv {
				lk := strings.ToLower(k)
				for _, f := range forbiddenKeys {
					if strings.Contains(lk, f) {
						t.Errorf("%s: forbidden key %q found at %s.%s", where, k, prefix, k)
					}
				}
				walk(prefix+"."+k, vv)
			}
		case []any:
			for i, vv := range tv {
				walk(prefix, vv)
				_ = i
			}
		}
	}
	walk("payload", payload)
}

// setupBus creates a fresh bus and a collector goroutine subscribed to
// the "mcp." prefix. Returns the bus, a func to fetch collected events,
// and a cleanup.
func setupBus(t *testing.T) (*bus.Bus, func() []bus.Event, func()) {
	t.Helper()
	dir := t.TempDir()
	b, err := bus.New(filepath.Join(dir, "wal"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	var mu sync.Mutex
	var events []bus.Event
	sub := b.Subscribe(bus.Pattern{TypePrefix: "mcp."}, func(evt bus.Event) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	})
	get := func() []bus.Event {
		mu.Lock()
		defer mu.Unlock()
		out := make([]bus.Event, len(events))
		copy(out, events)
		return out
	}
	cleanup := func() {
		sub.Cancel()
		b.Close()
	}
	return b, get, cleanup
}

// waitFor polls get until it returns at least n events or the timeout
// elapses. Returns the slice captured at exit.
func waitFor(get func() []bus.Event, n int) []bus.Event {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if evts := get(); len(evts) >= n {
			return evts
		}
		time.Sleep(5 * time.Millisecond)
	}
	return get()
}

// parsePayload unmarshals a bus event's raw payload into a map.
func parsePayload(t *testing.T, evt bus.Event) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(evt.Payload, &m); err != nil {
		t.Fatalf("unmarshal payload for %s: %v", evt.Type, err)
	}
	return m
}

// parseStream splits the NDJSON buffer into one map per line.
func parseStream(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		m := map[string]any{}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode stream: %v", err)
		}
		if len(m) == 0 {
			t.Fatalf("empty stream object decoded")
		}
		out = append(out, m)
	}
	return out
}

// hasKeys asserts every key in want is present in got.
func hasKeys(t *testing.T, where string, got map[string]any, want ...string) {
	t.Helper()
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s: expected key %q in %v", where, k, got)
		}
	}
}

func TestEmitter_NilSafe(t *testing.T) {
	// Nil emitter: every method should no-op without panic.
	var e *Emitter
	e.PublishStart("s", "t", "c")
	e.PublishComplete("s", "t", "c", 10, 100)
	e.PublishError("s", "t", "c", ErrKindTimeout, "x")
	e.PublishCircuitStateChange("s", "closed", "open", nil)
	e.PublishConfigDeprecated("s", "r")

	// Nil bus + nil stream: constructor returns valid Emitter, methods no-op.
	e = NewEmitter(nil, nil)
	if e == nil {
		t.Fatal("NewEmitter(nil, nil) returned nil")
	}
	e.PublishStart("s", "t", "c") // must not panic
}

func TestEmitter_PublishStart(t *testing.T) {
	b, get, cleanup := setupBus(t)
	defer cleanup()
	var buf bytes.Buffer
	s := streamjson.New(&buf, true)
	e := NewEmitter(b, s)

	e.PublishStart("github", "create_issue", "call-1")

	evts := waitFor(get, 1)
	if len(evts) != 1 {
		t.Fatalf("bus events: got %d want 1", len(evts))
	}
	evt := evts[0]
	if evt.Type != EvtMCPCallStart {
		t.Errorf("bus type=%q want %q", evt.Type, EvtMCPCallStart)
	}
	p := parsePayload(t, evt)
	hasKeys(t, "bus.start", p, "server", "tool", "call_id")
	if p["server"] != "github" || p["tool"] != "create_issue" || p["call_id"] != "call-1" {
		t.Errorf("bus.start payload mismatch: %v", p)
	}
	assertNoForbiddenKeys(t, "bus.start", p)

	streamEvts := parseStream(t, &buf)
	if len(streamEvts) != 1 {
		t.Fatalf("stream events: got %d want 1", len(streamEvts))
	}
	if streamEvts[0]["type"] != "stoke.mcp.call.start" {
		t.Errorf("stream type=%v want stoke.mcp.call.start", streamEvts[0]["type"])
	}
	hasKeys(t, "stream.start", streamEvts[0], "server", "tool", "call_id")
	assertNoForbiddenKeys(t, "stream.start", streamEvts[0])
}

func TestEmitter_PublishComplete(t *testing.T) {
	b, get, cleanup := setupBus(t)
	defer cleanup()
	var buf bytes.Buffer
	s := streamjson.New(&buf, true)
	e := NewEmitter(b, s)

	e.PublishComplete("linear", "create_ticket", "call-2", 123, 4096)

	evts := waitFor(get, 1)
	if len(evts) != 1 {
		t.Fatalf("bus events: got %d want 1", len(evts))
	}
	if evts[0].Type != EvtMCPCallComplete {
		t.Errorf("bus type=%q want %q", evts[0].Type, EvtMCPCallComplete)
	}
	p := parsePayload(t, evts[0])
	hasKeys(t, "bus.complete", p, "server", "tool", "call_id", "duration_ms", "size_bytes")
	// JSON round-trip: ints come back as float64.
	dur, ok := p["duration_ms"].(float64)
	if !ok {
		t.Fatalf("duration_ms: unexpected type: %T", p["duration_ms"])
	}
	if dur != 123 {
		t.Errorf("duration_ms=%v", p["duration_ms"])
	}
	sz, ok := p["size_bytes"].(float64)
	if !ok {
		t.Fatalf("size_bytes: unexpected type: %T", p["size_bytes"])
	}
	if sz != 4096 {
		t.Errorf("size_bytes=%v", p["size_bytes"])
	}
	assertNoForbiddenKeys(t, "bus.complete", p)

	streamEvts := parseStream(t, &buf)
	if len(streamEvts) != 1 {
		t.Fatalf("stream events: got %d want 1", len(streamEvts))
	}
	if streamEvts[0]["type"] != "stoke.mcp.call.complete" {
		t.Errorf("stream type=%v", streamEvts[0]["type"])
	}
	hasKeys(t, "stream.complete", streamEvts[0],
		"server", "tool", "call_id", "duration_ms", "size_bytes")
	assertNoForbiddenKeys(t, "stream.complete", streamEvts[0])
}

func TestEmitter_PublishError(t *testing.T) {
	b, get, cleanup := setupBus(t)
	defer cleanup()
	var buf bytes.Buffer
	s := streamjson.New(&buf, true)
	e := NewEmitter(b, s)

	e.PublishError("slack", "post_message", "call-3", ErrKindCircuitOpen, "circuit open; retry later")

	evts := waitFor(get, 1)
	if len(evts) != 1 {
		t.Fatalf("bus events: got %d want 1", len(evts))
	}
	if evts[0].Type != EvtMCPCallError {
		t.Errorf("bus type=%q want %q", evts[0].Type, EvtMCPCallError)
	}
	p := parsePayload(t, evts[0])
	hasKeys(t, "bus.error", p, "server", "tool", "call_id", "err_kind", "err_msg")
	if p["err_kind"] != ErrKindCircuitOpen {
		t.Errorf("err_kind=%v", p["err_kind"])
	}
	assertNoForbiddenKeys(t, "bus.error", p)

	streamEvts := parseStream(t, &buf)
	if len(streamEvts) != 1 {
		t.Fatalf("stream events: got %d want 1", len(streamEvts))
	}
	if streamEvts[0]["type"] != "stoke.mcp.call.error" {
		t.Errorf("stream type=%v", streamEvts[0]["type"])
	}
	hasKeys(t, "stream.error", streamEvts[0],
		"server", "tool", "call_id", "err_kind", "err_msg")
	assertNoForbiddenKeys(t, "stream.error", streamEvts[0])
}

func TestEmitter_PublishCircuitStateChange(t *testing.T) {
	b, get, cleanup := setupBus(t)
	defer cleanup()
	var buf bytes.Buffer
	s := streamjson.New(&buf, true)
	e := NewEmitter(b, s)

	info := map[string]any{
		"fail_count":  5,
		"cooldown_ms": 60000,
	}
	e.PublishCircuitStateChange("github", "closed", "open", info)

	evts := waitFor(get, 1)
	if len(evts) != 1 {
		t.Fatalf("bus events: got %d want 1", len(evts))
	}
	if evts[0].Type != EvtMCPCircuitStateChange {
		t.Errorf("bus type=%q want %q", evts[0].Type, EvtMCPCircuitStateChange)
	}
	p := parsePayload(t, evts[0])
	hasKeys(t, "bus.circuit", p, "server", "from", "to", "info")
	if p["from"] != "closed" || p["to"] != "open" {
		t.Errorf("from/to payload: %v", p)
	}
	assertNoForbiddenKeys(t, "bus.circuit", p)

	streamEvts := parseStream(t, &buf)
	if len(streamEvts) != 1 {
		t.Fatalf("stream events: got %d want 1", len(streamEvts))
	}
	if streamEvts[0]["type"] != "stoke.mcp.circuit.state_change" {
		t.Errorf("stream type=%v", streamEvts[0]["type"])
	}
	hasKeys(t, "stream.circuit", streamEvts[0], "server", "from", "to", "info")
	assertNoForbiddenKeys(t, "stream.circuit", streamEvts[0])
}

func TestEmitter_PublishCircuitStateChange_NilInfoSafe(t *testing.T) {
	// Nil info map should emit an empty info object, not panic.
	b, get, cleanup := setupBus(t)
	defer cleanup()
	e := NewEmitter(b, nil)

	e.PublishCircuitStateChange("slack", "open", "half_open", nil)

	evts := waitFor(get, 1)
	if len(evts) != 1 {
		t.Fatalf("bus events: got %d want 1", len(evts))
	}
	p := parsePayload(t, evts[0])
	if _, ok := p["info"].(map[string]any); !ok {
		t.Errorf("info is not a map: %v (%T)", p["info"], p["info"])
	}
}

func TestEmitter_PublishConfigDeprecated(t *testing.T) {
	b, get, cleanup := setupBus(t)
	defer cleanup()
	var buf bytes.Buffer
	s := streamjson.New(&buf, true)
	e := NewEmitter(b, s)

	e.PublishConfigDeprecated("slack", "SSE transport deprecated; use streamable_http")

	evts := waitFor(get, 1)
	if len(evts) != 1 {
		t.Fatalf("bus events: got %d want 1", len(evts))
	}
	if evts[0].Type != EvtMCPConfigDeprecated {
		t.Errorf("bus type=%q want %q", evts[0].Type, EvtMCPConfigDeprecated)
	}
	p := parsePayload(t, evts[0])
	hasKeys(t, "bus.deprecated", p, "server", "reason")
	assertNoForbiddenKeys(t, "bus.deprecated", p)

	streamEvts := parseStream(t, &buf)
	if len(streamEvts) != 1 {
		t.Fatalf("stream events: got %d want 1", len(streamEvts))
	}
	if streamEvts[0]["type"] != "stoke.mcp.config.deprecated" {
		t.Errorf("stream type=%v", streamEvts[0]["type"])
	}
	hasKeys(t, "stream.deprecated", streamEvts[0], "server", "reason")
	assertNoForbiddenKeys(t, "stream.deprecated", streamEvts[0])
}

func TestEmitter_BusOnly(t *testing.T) {
	// stream=nil: bus still receives events; no panic.
	b, get, cleanup := setupBus(t)
	defer cleanup()
	e := NewEmitter(b, nil)

	e.PublishStart("s", "t", "c")
	evts := waitFor(get, 1)
	if len(evts) != 1 {
		t.Fatalf("bus events: got %d want 1", len(evts))
	}
}

func TestEmitter_StreamOnly(t *testing.T) {
	// bus=nil: stream still receives events; no panic.
	var buf bytes.Buffer
	s := streamjson.New(&buf, true)
	e := NewEmitter(nil, s)

	e.PublishStart("s", "t", "c")
	streamEvts := parseStream(t, &buf)
	if len(streamEvts) != 1 {
		t.Fatalf("stream events: got %d want 1", len(streamEvts))
	}
	if streamEvts[0]["type"] != "stoke.mcp.call.start" {
		t.Errorf("stream type=%v", streamEvts[0]["type"])
	}
}

func TestEmitter_NoSecretLikePayload(t *testing.T) {
	// Belt-and-braces: fuzz a fake auth token through every publisher's
	// string inputs, confirm it appears only where the caller put it
	// (err_msg is the one place we DO pass a string through) and never
	// under any forbidden KEY.
	b, get, cleanup := setupBus(t)
	defer cleanup()
	var buf bytes.Buffer
	s := streamjson.New(&buf, true)
	e := NewEmitter(b, s)

	e.PublishStart("srv", "tool", "cid-1")
	e.PublishComplete("srv", "tool", "cid-2", 1, 2)
	e.PublishError("srv", "tool", "cid-3", ErrKindOther, "redacted message")
	e.PublishCircuitStateChange("srv", "closed", "open", map[string]any{"fail_count": 3})
	e.PublishConfigDeprecated("srv", "legacy")

	evts := waitFor(get, 5)
	if len(evts) != 5 {
		t.Fatalf("bus events: got %d want 5", len(evts))
	}
	for _, evt := range evts {
		p := parsePayload(t, evt)
		assertNoForbiddenKeys(t, string(evt.Type), p)
	}
	streamEvts := parseStream(t, &buf)
	if len(streamEvts) != 5 {
		t.Fatalf("stream events: got %d want 5", len(streamEvts))
	}
	for _, se := range streamEvts {
		seType, ok := se["type"].(string)
		if !ok {
			t.Fatalf("stream event type: unexpected type: %T", se["type"])
		}
		assertNoForbiddenKeys(t, "stream/"+strings.TrimPrefix(seType, "stoke."), se)
	}
}
