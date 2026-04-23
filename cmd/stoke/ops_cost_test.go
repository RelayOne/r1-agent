package main

// ops_cost_test.go — OPSUX-tail: tests for `stoke cost`. Covers the
// two contribution paths (type contains "cost", payload.cost_usd
// present) plus the malformed-row robustness requirement.

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/bus"
)

func TestCost_DBNotFound(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runCostCmd([]string{"--db", "/no/such/path/events.db"}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errBuf.String())
	}
}

func TestCost_EmptyDB(t *testing.T) {
	dbPath := seedLog(t, nil)
	var out, errBuf bytes.Buffer
	code := runCostCmd([]string{"--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no cost events") {
		t.Errorf("stdout=%q; want 'no cost events'", out.String())
	}
}

func TestCost_SumsPayloadCostUSD(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.complete", "M1", "T1", "L1", map[string]any{"cost_usd": 0.25}),
		mkEvent("task.complete", "M1", "T2", "L1", map[string]any{"cost_usd": 1.75}),
		mkEvent("task.complete", "M1", "T3", "L1", map[string]any{"other": "data"}), // not counted
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runCostCmd([]string{"--db", dbPath, "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	var agg map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &agg); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", out.String(), err)
	}
	if math.Abs(agg["total_usd"].(float64)-2.0) > 1e-9 {
		t.Errorf("total_usd=%v, want 2.0", agg["total_usd"])
	}
	if agg["events"].(float64) != 2 {
		t.Errorf("events=%v, want 2", agg["events"])
	}
}

func TestCost_TypeContainsCost_ContributesEvenWithoutCostUSD(t *testing.T) {
	// A "worker.cost" event with no cost_usd field still counts as an
	// event (at zero). Matches spec ("type contains 'cost' or payload
	// has cost_usd").
	events := []bus.Event{
		mkEvent("worker.cost", "M1", "T1", "L1", map[string]any{"note": "snapshot"}),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runCostCmd([]string{"--db", dbPath, "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	var agg map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &agg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if agg["events"].(float64) != 1 {
		t.Errorf("events=%v, want 1", agg["events"])
	}
	if agg["total_usd"].(float64) != 0.0 {
		t.Errorf("total_usd=%v, want 0", agg["total_usd"])
	}
}

func TestCost_TypeContainsCost_PicksUpCostUSD(t *testing.T) {
	events := []bus.Event{
		mkEvent("stoke.cost.update", "M1", "T1", "L1", map[string]any{"cost_usd": 3.5}),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runCostCmd([]string{"--db", dbPath, "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	var agg map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &agg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if math.Abs(agg["total_usd"].(float64)-3.5) > 1e-9 {
		t.Errorf("total_usd=%v, want 3.5", agg["total_usd"])
	}
}

func TestCost_TableRender(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.complete", "M1", "T1", "L1", map[string]any{"cost_usd": 1.2345}),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runCostCmd([]string{"--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{"TOTAL_USD", "EVENTS", "1.2345", "FIRST", "LAST"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
}

func TestCost_SessionFilter(t *testing.T) {
	events := []bus.Event{
		mkEvent("task.complete", "M1", "T1", "L1", map[string]any{"cost_usd": 1.0}),
		mkEvent("task.complete", "M2", "T2", "L2", map[string]any{"cost_usd": 9.0}),
	}
	dbPath := seedLog(t, events)

	var out, errBuf bytes.Buffer
	code := runCostCmd([]string{"--db", dbPath, "--session", "M1", "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	var agg map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &agg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Only the M1 row's $1 should count.
	if math.Abs(agg["total_usd"].(float64)-1.0) > 1e-9 {
		t.Errorf("total_usd=%v, want 1.0 under session=M1", agg["total_usd"])
	}
	if agg["events"].(float64) != 1 {
		t.Errorf("events=%v, want 1", agg["events"])
	}
}

func TestEventCostUSD_Helper(t *testing.T) {
	// Unit-level sanity: exercise the helper directly across the
	// contribution matrix. Inlined (no t.Run subtests) so assertions
	// live directly in this function body.

	// Case 1: cost type + no payload → counted at zero.
	got, ok := eventCostUSD(bus.Event{Type: bus.EventType("worker.cost")})
	if !ok {
		t.Errorf("cost-type no-payload: ok=false, want true")
	}
	if got != 0 {
		t.Errorf("cost-type no-payload: usd=%v, want 0", got)
	}

	// Case 2: plain type + payload without cost_usd → not counted.
	raw2, _ := json.Marshal(map[string]any{"x": 1})
	got, ok = eventCostUSD(bus.Event{Type: bus.EventType("task.dispatch"), Payload: raw2})
	if ok {
		t.Errorf("plain-type no-cost: ok=true, want false")
	}
	if got != 0 {
		t.Errorf("plain-type no-cost: usd=%v, want 0", got)
	}

	// Case 3: plain type + cost_usd in payload → counted at value.
	raw3, _ := json.Marshal(map[string]any{"cost_usd": 0.42})
	got, ok = eventCostUSD(bus.Event{Type: bus.EventType("task.complete"), Payload: raw3})
	if !ok {
		t.Errorf("plain-type with-cost: ok=false, want true")
	}
	if math.Abs(got-0.42) > 1e-9 {
		t.Errorf("plain-type with-cost: usd=%v, want 0.42", got)
	}

	// Case 4: cost type + cost_usd in payload → counted at value.
	raw4, _ := json.Marshal(map[string]any{"cost_usd": 2.5})
	got, ok = eventCostUSD(bus.Event{Type: bus.EventType("worker.cost"), Payload: raw4})
	if !ok {
		t.Errorf("cost-type with-cost: ok=false, want true")
	}
	if math.Abs(got-2.5) > 1e-9 {
		t.Errorf("cost-type with-cost: usd=%v, want 2.5", got)
	}

	// Case 5: cost type + non-object (array) payload → still counted.
	got, ok = eventCostUSD(bus.Event{Type: bus.EventType("worker.cost"), Payload: []byte(`[1,2,3]`)})
	if !ok {
		t.Errorf("cost-type non-object: ok=false, want true")
	}
	if got != 0 {
		t.Errorf("cost-type non-object: usd=%v, want 0", got)
	}
}
