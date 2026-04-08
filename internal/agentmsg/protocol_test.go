package agentmsg

import (
	"testing"
)

func TestSendAndReceive(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Register("bob", nil)

	bus.Send("alice", "bob", MsgHandoff, "task.complete", map[string]any{"task": "t1"})

	msgs := bus.Receive("bob")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].From != "alice" || msgs[0].Topic != "task.complete" {
		t.Errorf("unexpected message: %+v", msgs[0])
	}

	// Inbox should be empty after receive
	msgs = bus.Receive("bob")
	if len(msgs) != 0 {
		t.Error("inbox should be empty after receive")
	}
}

func TestBroadcast(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Register("bob", nil)
	bus.Register("charlie", nil)

	bus.Broadcast("alice", "status", map[string]any{"progress": 50})

	// Bob and Charlie should get it, Alice should not
	if bus.PendingCount("alice") != 0 {
		t.Error("sender should not receive own broadcast")
	}
	if bus.PendingCount("bob") != 1 {
		t.Error("bob should receive broadcast")
	}
	if bus.PendingCount("charlie") != 1 {
		t.Error("charlie should receive broadcast")
	}
}

func TestBroadcastTopicFilter(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Register("bob", nil, "status") // only subscribes to "status"

	bus.Broadcast("alice", "conflict", map[string]any{"file": "x.go"})

	if bus.PendingCount("bob") != 0 {
		t.Error("bob should not receive non-subscribed topic")
	}

	bus.Broadcast("alice", "status", map[string]any{"progress": 100})
	if bus.PendingCount("bob") != 1 {
		t.Error("bob should receive subscribed topic")
	}
}

func TestRequestResponse(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Register("bob", func(msg Msg) map[string]any {
		return map[string]any{"answer": "42"}
	})

	resp, err := bus.Request("alice", "bob", "question", map[string]any{"q": "meaning of life"})
	if err != nil {
		t.Fatal(err)
	}
	if resp["answer"] != "42" {
		t.Errorf("expected 42, got %v", resp["answer"])
	}
}

func TestRequestNotFound(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)

	_, err := bus.Request("alice", "unknown", "topic", nil)
	if err == nil {
		t.Error("should error on unknown agent")
	}
}

func TestRequestNoHandler(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Register("bob", nil) // no handler

	_, err := bus.Request("alice", "bob", "topic", nil)
	if err == nil {
		t.Error("should error when no handler")
	}
}

func TestPeek(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Send("bob", "alice", MsgStatus, "update", nil)

	msgs := bus.Peek("alice")
	if len(msgs) != 1 {
		t.Error("peek should return messages")
	}

	// Still there after peek
	if bus.PendingCount("alice") != 1 {
		t.Error("peek should not clear inbox")
	}
}

func TestUnregister(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Unregister("alice")

	agents := bus.Agents()
	for _, a := range agents {
		if a == "alice" {
			t.Error("alice should be unregistered")
		}
	}
}

func TestHistory(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Register("bob", nil)

	bus.Send("alice", "bob", MsgHandoff, "task", nil)
	bus.Send("bob", "alice", MsgStatus, "status", nil)

	all := bus.History("")
	if len(all) != 2 {
		t.Errorf("expected 2 total, got %d", len(all))
	}

	tasks := bus.History("task")
	if len(tasks) != 1 {
		t.Errorf("expected 1 task message, got %d", len(tasks))
	}
}

func TestMessageCount(t *testing.T) {
	bus := NewBus()
	bus.Register("a", nil)
	bus.Register("b", nil)

	bus.Send("a", "b", MsgStatus, "t", nil)
	bus.Broadcast("a", "t", nil)

	if bus.MessageCount() != 2 {
		t.Errorf("expected 2, got %d", bus.MessageCount())
	}
}

func TestAgents(t *testing.T) {
	bus := NewBus()
	bus.Register("alice", nil)
	bus.Register("bob", nil)

	agents := bus.Agents()
	if len(agents) != 2 {
		t.Errorf("expected 2, got %d", len(agents))
	}
}
