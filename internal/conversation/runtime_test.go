package conversation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeBasic(t *testing.T) {
	rt := NewRuntime("You are a helpful assistant.", 200000)

	rt.AddMessage(TextMessage(RoleUser, "Hello"))
	rt.AddMessage(TextMessage(RoleAssistant, "Hi there!"))

	if rt.TurnCount() != 2 {
		t.Errorf("expected 2 turns, got %d", rt.TurnCount())
	}
	if rt.SystemPrompt() != "You are a helpful assistant." {
		t.Error("wrong system prompt")
	}
}

func TestRuntimePendingToolUses(t *testing.T) {
	rt := NewRuntime("test", 200000)

	// Add assistant message with tool use
	rt.AddMessage(Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Type: "text", Text: "Let me read that file."},
			{Type: "tool_use", ID: "tu_1", Name: "Read", Input: map[string]interface{}{"path": "/tmp/test"}},
			{Type: "tool_use", ID: "tu_2", Name: "Grep", Input: map[string]interface{}{"pattern": "TODO"}},
		},
	})

	pending := rt.PendingToolUses()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}

	// Respond to one
	rt.AddMessage(ToolResultMessage("tu_1", "file contents here", false))

	pending = rt.PendingToolUses()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after response, got %d", len(pending))
	}
	if pending[0].ID != "tu_2" {
		t.Errorf("expected tu_2, got %s", pending[0].ID)
	}
}

func TestRuntimeCompact(t *testing.T) {
	rt := NewRuntime("test", 200000)

	for i := 0; i < 10; i++ {
		rt.AddMessage(TextMessage(RoleUser, "message"))
		rt.AddMessage(TextMessage(RoleAssistant, "response"))
	}

	if rt.TurnCount() != 20 {
		t.Fatalf("expected 20 turns, got %d", rt.TurnCount())
	}

	rt.Compact(4)

	// Should have: 1 summary + 4 kept = 5
	if rt.TurnCount() != 5 {
		t.Errorf("expected 5 turns after compaction, got %d", rt.TurnCount())
	}

	// First message should be the summary
	msgs := rt.Messages()
	if msgs[0].Content[0].Type != "text" {
		t.Error("expected text summary")
	}
}

func TestRuntimeSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conv.json")

	rt := NewRuntime("system prompt", 200000)
	rt.AddMessage(TextMessage(RoleUser, "Hello"))
	rt.AddMessage(TextMessage(RoleAssistant, "World"))

	if err := rt.SaveTo(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	rt2 := NewRuntime("", 0)
	if err := rt2.LoadFrom(path); err != nil {
		t.Fatalf("load: %v", err)
	}

	if rt2.SystemPrompt() != "system prompt" {
		t.Error("system prompt not restored")
	}
	if rt2.TurnCount() != 2 {
		t.Errorf("expected 2 turns, got %d", rt2.TurnCount())
	}

	// Cleanup
	os.Remove(path)
}

func TestRuntimeEstimatedTokens(t *testing.T) {
	rt := NewRuntime("short", 200000)
	rt.AddMessage(TextMessage(RoleUser, "four"))

	tokens := rt.EstimatedTokens()
	if tokens <= 0 {
		t.Errorf("expected positive token estimate, got %d", tokens)
	}
}

func TestRuntimeCompactNoOp(t *testing.T) {
	rt := NewRuntime("test", 200000)
	rt.AddMessage(TextMessage(RoleUser, "hello"))

	rt.Compact(10) // keepLast > len, should be no-op
	if rt.TurnCount() != 1 {
		t.Errorf("expected 1 turn, got %d", rt.TurnCount())
	}
}
