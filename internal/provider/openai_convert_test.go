package provider

import (
	"encoding/json"
	"testing"
)

// TestConvertResponse_TextOnly verifies OpenAI → Anthropic shape mapping
// for a plain text completion: text content preserved, stop="end_turn",
// token usage populated.
func TestConvertResponse_TextOnly(t *testing.T) {
	p := NewOpenAICompatProvider("openai", "k", "https://api.openai.com")

	raw := []byte(`{
	  "id": "chatcmpl-1",
	  "model": "gpt-4o",
	  "choices": [{
	    "message": {"content": "hello world", "tool_calls": null},
	    "finish_reason": "stop"
	  }],
	  "usage": {"prompt_tokens": 12, "completion_tokens": 34}
	}`)
	var resp openAIChatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := p.convertResponse(resp)
	if got.ID != "chatcmpl-1" {
		t.Errorf("ID = %q, want chatcmpl-1", got.ID)
	}
	if got.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", got.Model)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn (stop→end_turn mapping)", got.StopReason)
	}
	if len(got.Content) != 1 {
		t.Fatalf("Content len = %d, want 1", len(got.Content))
	}
	if got.Content[0].Type != "text" || got.Content[0].Text != "hello world" {
		t.Errorf("Content[0] = %+v, want {text, 'hello world'}", got.Content[0])
	}
	if got.Usage.Input != 12 {
		t.Errorf("Usage.Input = %d, want 12", got.Usage.Input)
	}
	if got.Usage.Output != 34 {
		t.Errorf("Usage.Output = %d, want 34", got.Usage.Output)
	}
}

// TestConvertResponse_ToolCalls verifies that OpenAI tool_calls are
// mapped to Anthropic-shape tool_use blocks and stop_reason is remapped
// from tool_calls → tool_use.
func TestConvertResponse_ToolCalls(t *testing.T) {
	p := NewOpenAICompatProvider("openrouter", "k", "https://openrouter.ai/api")

	raw := []byte(`{
	  "id": "x", "model": "m",
	  "choices": [{
	    "message": {
	      "content": "",
	      "tool_calls": [{
	        "index": 0,
	        "id": "call_abc",
	        "type": "function",
	        "function": {"name": "search", "arguments": "{\"q\":\"golang\"}"}
	      }]
	    },
	    "finish_reason": "tool_calls"
	  }]
	}`)
	var resp openAIChatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := p.convertResponse(resp)
	if got.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use (tool_calls→tool_use mapping)", got.StopReason)
	}
	if len(got.Content) != 1 {
		t.Fatalf("Content len = %d, want 1 tool_use block (no text when content empty)", len(got.Content))
	}
	c := got.Content[0]
	if c.Type != "tool_use" {
		t.Errorf("Content[0].Type = %q, want tool_use", c.Type)
	}
	if c.ID != "call_abc" {
		t.Errorf("Content[0].ID = %q, want call_abc", c.ID)
	}
	if c.Name != "search" {
		t.Errorf("Content[0].Name = %q, want search", c.Name)
	}
	if c.Input["q"] != "golang" {
		t.Errorf("Content[0].Input[\"q\"] = %v, want golang", c.Input["q"])
	}
}

// TestConvertResponse_UnknownFinishReason passes through any non-stop,
// non-tool_calls finish_reason unchanged (e.g. length, content_filter).
func TestConvertResponse_UnknownFinishReason(t *testing.T) {
	p := NewOpenAICompatProvider("openai", "k", "https://api.openai.com")
	raw := []byte(`{"id":"x","model":"m","choices":[{"message":{"content":"truncated"},"finish_reason":"length"}]}`)
	var resp openAIChatResponse
	_ = json.Unmarshal(raw, &resp)
	got := p.convertResponse(resp)
	if got.StopReason != "length" {
		t.Errorf("StopReason = %q, want length (pass-through for unknown)", got.StopReason)
	}
}

// TestConvertToolDefs verifies Anthropic-style ToolDef is rewrapped in
// OpenAI's {type:function, function:{...}} envelope with name,
// description and parameters preserved.
func TestConvertToolDefs(t *testing.T) {
	p := NewOpenAICompatProvider("openai", "k", "https://api.openai.com")
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	tools := []ToolDef{
		{Name: "search", Description: "run a search", InputSchema: schema},
		{Name: "lookup", Description: "", InputSchema: json.RawMessage(`{}`)},
	}
	got := p.convertToolDefs(tools)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	// First tool
	if got[0]["type"] != "function" {
		t.Errorf("tool0.type = %v, want function", got[0]["type"])
	}
	fn0, ok := got[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool0.function not map[string]interface{}: %T", got[0]["function"])
	}
	if fn0["name"] != "search" {
		t.Errorf("tool0.name = %v, want search", fn0["name"])
	}
	if fn0["description"] != "run a search" {
		t.Errorf("tool0.description = %v, want %q", fn0["description"], "run a search")
	}
	// parameters is the raw schema, serialize back to compare
	paramsRaw, err := json.Marshal(fn0["parameters"])
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if string(paramsRaw) != `{"type":"object","properties":{"q":{"type":"string"}}}` {
		t.Errorf("tool0.parameters = %s, want the exact InputSchema", paramsRaw)
	}

	// Second tool's description is empty — still emitted (field present,
	// empty string) so OpenAI sees a well-formed function definition.
	fn1 := got[1]["function"].(map[string]interface{})
	if fn1["name"] != "lookup" {
		t.Errorf("tool1.name = %v, want lookup", fn1["name"])
	}
}

// TestConvertOneMessage_ToolResult verifies that a user message carrying
// Anthropic tool_result blocks becomes a sequence of OpenAI-shape
// "tool" role messages keyed by tool_call_id.
func TestConvertOneMessage_ToolResult(t *testing.T) {
	p := NewOpenAICompatProvider("openai", "k", "https://api.openai.com")
	content := json.RawMessage(`[
	  {"type":"tool_result","tool_use_id":"call_a","content":"result A"},
	  {"type":"tool_result","tool_use_id":"call_b","content":"result B"}
	]`)
	out := p.convertOneMessage(ChatMessage{Role: "user", Content: content})

	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 tool messages", len(out))
	}
	if out[0]["role"] != "tool" {
		t.Errorf("out[0].role = %v, want tool", out[0]["role"])
	}
	if out[0]["tool_call_id"] != "call_a" {
		t.Errorf("out[0].tool_call_id = %v, want call_a", out[0]["tool_call_id"])
	}
	if out[0]["content"] != "result A" {
		t.Errorf("out[0].content = %v, want 'result A'", out[0]["content"])
	}
	if out[1]["tool_call_id"] != "call_b" {
		t.Errorf("out[1].tool_call_id = %v, want call_b", out[1]["tool_call_id"])
	}
}

// TestConvertOneMessage_AssistantToolUse verifies that an assistant
// message with tool_use blocks collapses into a single OpenAI-shape
// assistant message with a tool_calls array.
func TestConvertOneMessage_AssistantToolUse(t *testing.T) {
	p := NewOpenAICompatProvider("openai", "k", "https://api.openai.com")
	content := json.RawMessage(`[
	  {"type":"text","text":"thinking..."},
	  {"type":"tool_use","id":"call_1","name":"calc","input":{"expr":"2+2"}}
	]`)
	out := p.convertOneMessage(ChatMessage{Role: "assistant", Content: content})

	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (text + tool_use → single message)", len(out))
	}
	msg := out[0]
	if msg["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", msg["role"])
	}
	if msg["content"] != "thinking..." {
		t.Errorf("content = %v, want 'thinking...'", msg["content"])
	}
	toolCalls, ok := msg["tool_calls"].([]map[string]interface{})
	if !ok {
		t.Fatalf("tool_calls wrong type: %T", msg["tool_calls"])
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc["id"] != "call_1" {
		t.Errorf("tc.id = %v, want call_1", tc["id"])
	}
	if tc["type"] != "function" {
		t.Errorf("tc.type = %v, want function", tc["type"])
	}
	fn := tc["function"].(map[string]interface{})
	if fn["name"] != "calc" {
		t.Errorf("fn.name = %v, want calc", fn["name"])
	}
	if fn["arguments"] != `{"expr":"2+2"}` {
		t.Errorf("fn.arguments = %v, want %q", fn["arguments"], `{"expr":"2+2"}`)
	}
}

// TestBuildRequestBody_ToolsWithCache verifies that when CacheEnabled is
// true and Tools are provided, the last tool definition carries
// cache_control (the "cache tool definitions" pattern).
func TestBuildRequestBody_ToolsWithCache(t *testing.T) {
	p := NewAnthropicProvider("key", "https://api.anthropic.com")
	req := ChatRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		Tools: []ToolDef{
			{Name: "first", InputSchema: json.RawMessage(`{}`)},
			{Name: "last", InputSchema: json.RawMessage(`{}`)},
		},
		CacheEnabled: true,
	}
	body := p.buildRequestBody(req, false)

	tools, ok := body["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools type = %T, want []interface{} (cache-enabled should wrap)", body["tools"])
	}
	if len(tools) != 2 {
		t.Fatalf("tools len = %d, want 2", len(tools))
	}

	// First tool: no cache_control
	first, _ := tools[0].(map[string]interface{})
	if _, has := first["cache_control"]; has {
		t.Errorf("first tool must NOT have cache_control: %+v", first)
	}
	// Last tool: has cache_control (implemented as map[string]string)
	last, _ := tools[1].(map[string]interface{})
	cc, has := last["cache_control"]
	if !has {
		t.Fatalf("last tool must have cache_control: %+v", last)
	}
	m, ok := cc.(map[string]string)
	if !ok {
		t.Fatalf("last tool cache_control type = %T, want map[string]string", cc)
	}
	if m["type"] != "ephemeral" {
		t.Errorf("last tool cache_control[type] = %q, want ephemeral", m["type"])
	}
	if last["name"] != "last" {
		t.Errorf("last tool name = %v, want 'last'", last["name"])
	}
}

// TestBuildRequestBody_ToolsNoCache confirms that without CacheEnabled
// tools pass through as the original ToolDef slice (no rewrapping, no
// cache_control injected).
func TestBuildRequestBody_ToolsNoCache(t *testing.T) {
	p := NewAnthropicProvider("key", "https://api.anthropic.com")
	req := ChatRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		Tools: []ToolDef{
			{Name: "a", InputSchema: json.RawMessage(`{}`)},
		},
		CacheEnabled: false,
	}
	body := p.buildRequestBody(req, false)
	// Pass-through path stores the []ToolDef directly (different type
	// than the cache-enabled []interface{}).
	tools, ok := body["tools"].([]ToolDef)
	if !ok {
		t.Fatalf("tools type = %T, want []ToolDef (no-cache path)", body["tools"])
	}
	if len(tools) != 1 || tools[0].Name != "a" {
		t.Errorf("tools = %+v, want single {Name:a}", tools)
	}
}

// TestBuildRequestBody_TemperatureAndStream ensures temperature is
// propagated when non-nil and that stream: true appears only when
// streaming=true.
func TestBuildRequestBody_TemperatureAndStream(t *testing.T) {
	p := NewAnthropicProvider("key", "https://api.anthropic.com")
	temp := 0.3
	req := ChatRequest{
		Model:       "claude-sonnet-4-6",
		MaxTokens:   100,
		Temperature: &temp,
	}

	// Streaming path
	stream := p.buildRequestBody(req, true)
	if v, ok := stream["stream"].(bool); !ok || !v {
		t.Errorf("streaming body.stream = %v, want true", stream["stream"])
	}
	if v, ok := stream["temperature"].(float64); !ok || v != 0.3 {
		t.Errorf("streaming body.temperature = %v, want 0.3", stream["temperature"])
	}

	// Non-streaming path
	nonstream := p.buildRequestBody(req, false)
	if _, has := nonstream["stream"]; has {
		t.Errorf("non-streaming body should NOT have stream key: %v", nonstream["stream"])
	}

	// Nil Temperature omits the field entirely
	req.Temperature = nil
	noTemp := p.buildRequestBody(req, false)
	if _, has := noTemp["temperature"]; has {
		t.Error("body should NOT have temperature when req.Temperature is nil")
	}
}
