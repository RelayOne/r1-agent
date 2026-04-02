package stream

import (
	"strings"
	"testing"
	"time"
)

func collect(t *testing.T, input string) []Event {
	t.Helper()
	p := &Parser{StreamIdleTimeout: 2 * time.Second, PostResultTimeout: 1 * time.Second, GlobalTimeout: 5 * time.Second}
	done := make(chan struct{})
	ch := p.Parse(strings.NewReader(input), done)
	var out []Event
	for ev := range ch { out = append(out, ev) }
	<-done
	return out
}

func TestSystem(t *testing.T) {
	evs := collect(t, `{"type":"system","subtype":"init","session_id":"abc"}`)
	if len(evs) == 0 || evs[0].Type != "system" { t.Fatal("no system event") }
	if evs[0].Subtype != "init" { t.Errorf("subtype=%q", evs[0].Subtype) }
}

func TestAssistantToolUse(t *testing.T) {
	evs := collect(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"a.go"}}],"usage":{"input_tokens":100,"output_tokens":50}}}`)
	if len(evs) == 0 { t.Fatal("no events") }
	if len(evs[0].ToolUses) != 1 { t.Fatalf("tools=%d", len(evs[0].ToolUses)) }
	if evs[0].ToolUses[0].Name != "Write" { t.Errorf("name=%q", evs[0].ToolUses[0].Name) }
	if evs[0].Tokens.Input != 100 { t.Errorf("tokens=%d", evs[0].Tokens.Input) }
}

func TestResult(t *testing.T) {
	evs := collect(t, `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.42,"num_turns":5,"usage":{"input_tokens":10,"output_tokens":20}}`)
	if len(evs) == 0 { t.Fatal("no events") }
	if evs[0].CostUSD != 0.42 { t.Errorf("cost=%f", evs[0].CostUSD) }
	if evs[0].NumTurns != 5 { t.Errorf("turns=%d", evs[0].NumTurns) }
}

func TestResultMaxTurns(t *testing.T) {
	evs := collect(t, `{"type":"result","subtype":"error_max_turns","is_error":false}`)
	if evs[0].Subtype != "error_max_turns" { t.Errorf("subtype=%q", evs[0].Subtype) }
	if evs[0].IsError { t.Error("is_error should be false for max_turns") }
}

func TestRateLimitEvent(t *testing.T) {
	evs := collect(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`)
	if !evs[0].IsError { t.Error("rate_limit_event should be error") }
}

func TestStreamEvent(t *testing.T) {
	evs := collect(t, `{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"Hello"}}}`)
	if evs[0].DeltaText != "Hello" { t.Errorf("delta=%q", evs[0].DeltaText) }
}

func TestSkipsNonJSON(t *testing.T) {
	input := "[SandboxDebug] init\n" + `{"type":"result","subtype":"success"}` + "\ngarbage\n"
	evs := collect(t, input)
	found := false
	for _, e := range evs { if e.Type == "result" { found = true } }
	if !found { t.Error("expected result event") }
}

func TestMultipleEvents(t *testing.T) {
	input := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}
{"type":"result","subtype":"success","total_cost_usd":0.01}`
	evs := collect(t, input)
	if len(evs) != 3 { t.Fatalf("events=%d, want 3", len(evs)) }
}

func TestDrainBufferedLinesOnEOF(t *testing.T) {
	// All lines arrive at once, then EOF. Parser should drain all before returning.
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"line"}]}}`)
	}
	lines = append(lines, `{"type":"result","subtype":"success","total_cost_usd":0.50}`)
	input := strings.Join(lines, "\n")

	evs := collect(t, input)

	// Count: 20 assistant + 1 result = 21
	if len(evs) != 21 {
		t.Errorf("events=%d, want 21 (20 assistant + 1 result). Drain may have missed buffered lines.", len(evs))
	}

	// Last event should be result
	last := evs[len(evs)-1]
	if last.Type != "result" {
		t.Errorf("last event type=%q, want result", last.Type)
	}
	if last.CostUSD != 0.50 {
		t.Errorf("cost=%f, want 0.50", last.CostUSD)
	}
}

func TestErrorEventOnBrokenJSON(t *testing.T) {
	input := `{"type":"system"}
{broken json here
{"type":"result","subtype":"success"}`
	evs := collect(t, input)
	// broken line should be skipped, we should still get system + result
	types := map[string]bool{}
	for _, e := range evs { types[e.Type] = true }
	if !types["system"] { t.Error("missing system") }
	if !types["result"] { t.Error("missing result") }
}
