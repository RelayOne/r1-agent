// desktop_rpc_localhandler_test.go — end-to-end proof that the read-only
// desktop verbs return REAL backend data through the actual JSON-RPC
// dispatch (audit A011). Each test seeds a real ledger store, memory bus,
// and cost tracker, wires them into desktopapi.LocalHandler, drives NDJSON
// requests through desktopRPCServer.serve, and asserts the decoded
// responses carry the seeded values.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/desktopapi"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/memory/membus"
)

// dispatchThrough runs reqs through a real desktopRPCServer wired to
// handler and returns every response object that carries an "id".
func dispatchThrough(t *testing.T, handler desktopapi.Handler, reqs ...map[string]any) map[string]map[string]any {
	t.Helper()
	var lines []string
	for _, r := range reqs {
		r["jsonrpc"] = "2.0"
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		lines = append(lines, string(b))
	}

	var stdout strings.Builder
	srv := &desktopRPCServer{
		sessionID: "e2e",
		handler:   handler,
		stdout:    &stdout,
		stderr:    &strings.Builder{},
	}
	srv.serve(strings.NewReader(strings.Join(lines, "\n") + "\n"))

	out := map[string]map[string]any{}
	sc := bufio.NewScanner(strings.NewReader(stdout.String()))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		var obj map[string]any
		if err := json.Unmarshal([]byte(sc.Text()), &obj); err != nil {
			continue
		}
		id, ok := obj["id"].(string)
		if !ok {
			continue
		}
		out[id] = obj
	}
	return out
}

func mustResult(t *testing.T, resp map[string]map[string]any, id string) map[string]any {
	t.Helper()
	obj, ok := resp[id]
	if !ok {
		t.Fatalf("no response for id %q (got %v)", id, resp)
	}
	if obj["error"] != nil {
		t.Fatalf("id %q returned error: %v", id, obj["error"])
	}
	result, ok := obj["result"].(map[string]any)
	if !ok {
		t.Fatalf("id %q result is not an object: %v", id, obj["result"])
	}
	return result
}

func TestDesktopRPC_LocalHandler_EndToEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// --- Seed a real ledger ---
	root := filepath.Join(t.TempDir(), "ledger")
	lg, err := ledger.New(root)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer lg.Close()
	base := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	idA, err := lg.AddNode(ctx, ledger.Node{
		Type: "task", SchemaVersion: 1, MissionID: "sess-e2e", CreatedBy: "seed",
		CreatedAt: base, Content: json.RawMessage(`{"title":"root task"}`),
	})
	if err != nil {
		t.Fatalf("AddNode A: %v", err)
	}
	idB, err := lg.AddNode(ctx, ledger.Node{
		Type: "decision", SchemaVersion: 1, MissionID: "sess-e2e", CreatedBy: "seed",
		CreatedAt: base.Add(time.Minute), Content: json.RawMessage(`{"choice":"ship"}`),
	})
	if err != nil {
		t.Fatalf("AddNode B: %v", err)
	}
	if err := lg.AddEdge(ctx, ledger.Edge{From: idB, To: idA, Type: ledger.EdgeReferences}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	store, err := ledger.NewStore(root)
	if err != nil {
		t.Fatalf("ledger.NewStore: %v", err)
	}

	// --- Seed a real memory bus ---
	memPath := filepath.Join(t.TempDir(), "memory.db")
	bus, db, err := openMemoryBus(memPath, true)
	if err != nil {
		t.Fatalf("openMemoryBus: %v", err)
	}
	defer func() { _ = bus.Close(); _ = db.Close() }()
	if err := bus.Remember(ctx, membus.RememberRequest{
		Scope: membus.ScopeSession, Author: "system", Key: "greeting", Content: "hello-world",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// --- Seed a real cost tracker ---
	tr := costtrack.NewTracker(0, nil)
	tr.Record("claude-sonnet-4", "t1", 120, 30, 0, 0)

	handler := &desktopapi.LocalHandler{Ledger: store, Memory: bus, Cost: tr}

	resp := dispatchThrough(t, handler,
		map[string]any{"id": "get", "method": "ledger.get_node", "params": map[string]any{"hash": idB}},
		map[string]any{"id": "list", "method": "ledger.list_events", "params": map[string]any{"session_id": "sess-e2e"}},
		map[string]any{"id": "mem", "method": "memory.query", "params": map[string]any{"scope": "Session"}},
		map[string]any{"id": "cost", "method": "cost.get_current", "params": map[string]any{}},
	)

	// ledger.get_node — real payload + edges over the wire.
	get := mustResult(t, resp, "get")
	if get["hash"] != idB {
		t.Errorf("get_node hash = %v, want %v", get["hash"], idB)
	}
	if get["type"] != "decision" {
		t.Errorf("get_node type = %v, want decision", get["type"])
	}
	if payload, _ := get["payload"].(map[string]any); payload["choice"] != "ship" {
		t.Errorf("get_node payload.choice = %v, want ship", get["payload"])
	}
	edges, _ := get["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("get_node edges = %v, want 1", get["edges"])
	}
	if edge0, _ := edges[0].(map[string]any); edge0["to"] != idA || edge0["kind"] != "references" {
		t.Errorf("get_node edge = %v, want {to:%s, kind:references}", edges[0], idA)
	}

	// ledger.list_events — real events, newest-first.
	list := mustResult(t, resp, "list")
	events, _ := list["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("list_events = %v, want 2 events", list["events"])
	}
	if first, _ := events[0].(map[string]any); first["hash"] != idB {
		t.Errorf("list_events[0].hash = %v, want %v (newest-first)", events[0], idB)
	}

	// memory.query — real row over the wire.
	mem := mustResult(t, resp, "mem")
	entries, _ := mem["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("memory.query entries = %v, want 1", mem["entries"])
	}
	if e0, _ := entries[0].(map[string]any); e0["key"] != "greeting" || e0["value"] != "hello-world" {
		t.Errorf("memory.query entry = %v, want {key:greeting, value:hello-world}", entries[0])
	}

	// cost.get_current — real token totals over the wire.
	cost := mustResult(t, resp, "cost")
	if got := jsonNum(cost["tokens_in"]); got != 120 {
		t.Errorf("cost.tokens_in = %v, want 120", cost["tokens_in"])
	}
	if got := jsonNum(cost["tokens_out"]); got != 30 {
		t.Errorf("cost.tokens_out = %v, want 30", cost["tokens_out"])
	}
}

// TestDesktopRPC_BuildHandler_WiresRealBackends proves the production
// wiring path (runDesktopRPCCmd → buildDesktopHandler) actually serves
// real data when a session workdir already contains a ledger: seed a
// ledger under <workdir>/.stoke/ledger, then drive ledger.get_node over
// the full command entry point and assert the seeded node comes back.
func TestDesktopRPC_BuildHandler_WiresRealBackends(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()

	lg, err := ledger.New(filepath.Join(workdir, ".stoke", "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	id, err := lg.AddNode(ctx, ledger.Node{
		Type: "artifact", SchemaVersion: 1, MissionID: "sess-x", CreatedBy: "seed",
		CreatedAt: time.Now().UTC(), Content: json.RawMessage(`{"path":"main.go"}`),
	})
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	_ = lg.Close()

	req := map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": "ledger.get_node",
		"params": map[string]any{"hash": id},
	}
	line, _ := json.Marshal(req)

	var stdout strings.Builder
	code := runDesktopRPCCmd(
		[]string{"--session-id", "build-wire", "--workdir", workdir},
		strings.NewReader(string(line)+"\n"),
		&stdout,
		&strings.Builder{},
	)
	if code != 0 {
		t.Fatalf("runDesktopRPCCmd exit = %d", code)
	}

	var found map[string]any
	sc := bufio.NewScanner(strings.NewReader(stdout.String()))
	for sc.Scan() {
		var obj map[string]any
		if err := json.Unmarshal([]byte(sc.Text()), &obj); err != nil {
			continue
		}
		if obj["id"] == "1" {
			found = obj
			break
		}
	}
	if found == nil {
		t.Fatalf("no response for the seeded node; stdout:\n%s", stdout.String())
	}
	if found["error"] != nil {
		t.Fatalf("expected the seeded node, got error: %v", found["error"])
	}
	result, _ := found["result"].(map[string]any)
	if result["type"] != "artifact" {
		t.Errorf("type = %v, want artifact (real backend not wired?)", result["type"])
	}
}

// jsonNum coerces a decoded JSON number (float64) to int for assertions.
func jsonNum(v any) int {
	f, _ := v.(float64)
	return int(f)
}
