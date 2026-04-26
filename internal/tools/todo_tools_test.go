package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTodoWriteAndRead(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	_, err := reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{
		"todos": []map[string]interface{}{
			{"id": "t1", "content": "Write tests", "status": "in_progress", "priority": "high"},
			{"id": "t2", "content": "Ship PR", "status": "pending", "priority": "medium"},
		},
	}))
	if err != nil {
		t.Fatalf("todo_write error: %v", err)
	}

	result, err := reg.Handle(context.Background(), "todo_read", toJSON(map[string]interface{}{}))
	if err != nil {
		t.Fatalf("todo_read error: %v", err)
	}
	if !strings.Contains(result, "Write tests") {
		t.Errorf("expected 'Write tests' in result, got: %s", result)
	}
	if !strings.Contains(result, "Ship PR") {
		t.Errorf("expected 'Ship PR' in result, got: %s", result)
	}
	if !strings.Contains(result, "IN PROGRESS") {
		t.Errorf("expected 'IN PROGRESS' section in result, got: %s", result)
	}
}

func TestTodoWriteReplacesAll(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{ //nolint:errcheck
		"todos": []map[string]interface{}{
			{"content": "old task"},
		},
	}))

	reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{ //nolint:errcheck
		"todos": []map[string]interface{}{
			{"content": "new task"},
		},
	}))

	result, _ := reg.Handle(context.Background(), "todo_read", toJSON(map[string]interface{}{}))
	if strings.Contains(result, "old task") {
		t.Error("todo_write should replace old items, but found 'old task' in result")
	}
	if !strings.Contains(result, "new task") {
		t.Errorf("expected 'new task' after replacement, got: %s", result)
	}
}

func TestTodoReadEmpty(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "todo_read", toJSON(map[string]interface{}{}))
	if err != nil {
		t.Fatalf("todo_read on empty list error: %v", err)
	}
	if !strings.Contains(result, "no todos") {
		t.Errorf("expected '(no todos)' for empty list, got: %s", result)
	}
}

func TestTodoReadStatusFilter(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{ //nolint:errcheck
		"todos": []map[string]interface{}{
			{"id": "a", "content": "Task A", "status": "done"},
			{"id": "b", "content": "Task B", "status": "pending"},
		},
	}))

	result, err := reg.Handle(context.Background(), "todo_read", toJSON(map[string]string{"status": "done"}))
	if err != nil {
		t.Fatalf("todo_read with filter error: %v", err)
	}
	if !strings.Contains(result, "Task A") {
		t.Errorf("expected 'Task A' (done) in filtered result, got: %s", result)
	}
	if strings.Contains(result, "Task B") {
		t.Errorf("did not expect 'Task B' (pending) in done-only result, got: %s", result)
	}
}

func TestTodoUpdate(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{ //nolint:errcheck
		"todos": []map[string]interface{}{
			{"id": "x1", "content": "Do thing", "status": "pending"},
		},
	}))

	result, err := reg.Handle(context.Background(), "todo_update", toJSON(map[string]string{
		"id":     "x1",
		"status": "done",
	}))
	if err != nil {
		t.Fatalf("todo_update error: %v", err)
	}
	if !strings.Contains(result, "done") {
		t.Errorf("expected updated status 'done' in result, got: %s", result)
	}

	readResult, _ := reg.Handle(context.Background(), "todo_read", toJSON(map[string]string{"status": "done"}))
	if !strings.Contains(readResult, "Do thing") {
		t.Errorf("expected 'Do thing' in done section after update, got: %s", readResult)
	}
}

func TestTodoUpdateNotFound(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "todo_update", toJSON(map[string]string{
		"id":     "nonexistent",
		"status": "done",
	}))
	if err == nil {
		t.Error("expected error for nonexistent todo id")
	}
}

func TestTodoWriteEmptyContent(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{
		"todos": []map[string]interface{}{
			{"id": "bad", "content": ""},
		},
	}))
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestTodoWriteMissingArray(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{}))
	if err == nil {
		t.Error("expected error when todos array is missing")
	}
}

func TestTodoDefaultStatusAndPriority(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{ //nolint:errcheck
		"todos": []map[string]interface{}{
			{"content": "Default item"},
		},
	}))

	result, _ := reg.Handle(context.Background(), "todo_read", toJSON(map[string]interface{}{}))
	// Default status is pending, default priority is medium
	if !strings.Contains(result, "PENDING") {
		t.Errorf("expected default status 'pending' to appear as PENDING, got: %s", result)
	}
}

func TestTodoPersistence(t *testing.T) {
	dir := t.TempDir()

	reg1 := NewRegistry(dir)
	reg1.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{ //nolint:errcheck
		"todos": []map[string]interface{}{
			{"id": "p1", "content": "Persist me", "status": "pending"},
		},
	}))

	// New registry instance on same dir — should load from disk.
	reg2 := NewRegistry(dir)
	result, err := reg2.Handle(context.Background(), "todo_read", toJSON(map[string]interface{}{}))
	if err != nil {
		t.Fatalf("todo_read on second registry: %v", err)
	}
	if !strings.Contains(result, "Persist me") {
		t.Errorf("expected persisted todo in new registry, got: %s", result)
	}
}

func TestTodoUpdateInvalidStatus(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	reg.Handle(context.Background(), "todo_write", toJSON(map[string]interface{}{ //nolint:errcheck
		"todos": []map[string]interface{}{
			{"id": "q1", "content": "Some task"},
		},
	}))
	_, err := reg.Handle(context.Background(), "todo_update", toJSON(map[string]string{
		"id":     "q1",
		"status": "invalid_status",
	}))
	if err == nil {
		t.Error("expected error for invalid status value")
	}
}
