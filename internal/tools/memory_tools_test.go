package tools

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryStoreAndRecall(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	storeResult, err := reg.Handle(context.Background(), "memory_store", toJSON(map[string]interface{}{
		"content":  "Always use t.TempDir() not os.TempDir() in Go tests",
		"category": "gotcha",
		"tags":     []string{"go", "testing"},
	}))
	if err != nil {
		t.Fatalf("memory_store error: %v", err)
	}
	if !strings.Contains(storeResult, "Stored memory") {
		t.Errorf("expected 'Stored memory' in result, got: %s", storeResult)
	}

	recallResult, err := reg.Handle(context.Background(), "memory_recall", toJSON(map[string]string{"query": "go testing tempdir"}))
	if err != nil {
		t.Fatalf("memory_recall error: %v", err)
	}
	if !strings.Contains(recallResult, "TempDir") {
		t.Errorf("expected recalled content in result, got: %s", recallResult)
	}
}

func TestMemoryRecallEmpty(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "memory_recall", toJSON(map[string]string{"query": "nothing matches this query xyz123"}))
	if err != nil {
		t.Fatalf("memory_recall error: %v", err)
	}
	if !strings.Contains(result, "no relevant memories") {
		t.Errorf("expected no-memories message, got: %s", result)
	}
}

func TestMemoryRecallEmptyQuery(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "memory_recall", toJSON(map[string]string{"query": ""}))
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestMemoryForget(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	storeResult, _ := reg.Handle(context.Background(), "memory_store", toJSON(map[string]interface{}{
		"content":  "to be forgotten",
		"category": "fact",
	}))

	// Extract the memory ID from the store result, e.g. "Stored memory mem-1 [fact]"
	var memID string
	parts := strings.Fields(storeResult)
	for i, p := range parts {
		if p == "memory" && i+1 < len(parts) {
			memID = parts[i+1]
			break
		}
	}
	if memID == "" {
		t.Fatalf("could not parse memory ID from store result: %s", storeResult)
	}

	forgetResult, err := reg.Handle(context.Background(), "memory_forget", toJSON(map[string]string{"id": memID}))
	if err != nil {
		t.Fatalf("memory_forget error: %v", err)
	}
	if !strings.Contains(forgetResult, "Deleted memory") {
		t.Errorf("expected 'Deleted memory' in result, got: %s", forgetResult)
	}

	// Verify it's gone.
	recallResult, _ := reg.Handle(context.Background(), "memory_recall", toJSON(map[string]string{"query": "to be forgotten"}))
	if strings.Contains(recallResult, memID) {
		t.Errorf("memory should be forgotten but still found in recall: %s", recallResult)
	}
}

func TestMemoryForgetNotFound(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "memory_forget", toJSON(map[string]string{"id": "mem-999"}))
	if err == nil {
		t.Error("expected error for nonexistent memory id")
	}
}

func TestMemoryForgetMissingID(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "memory_forget", toJSON(map[string]string{}))
	if err == nil {
		t.Error("expected error when id is missing")
	}
}

func TestMemoryStoreEmptyContent(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "memory_store", toJSON(map[string]interface{}{
		"content": "",
	}))
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestMemoryDefaultCategory(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "memory_store", toJSON(map[string]interface{}{
		"content": "A plain fact about the codebase",
	}))
	if err != nil {
		t.Fatalf("memory_store error: %v", err)
	}
	if !strings.Contains(result, "fact") {
		t.Errorf("expected default category 'fact' in result, got: %s", result)
	}
}

func TestMemoryPersistence(t *testing.T) {
	dir := t.TempDir()

	reg1 := NewRegistry(dir)
	reg1.Handle(context.Background(), "memory_store", toJSON(map[string]interface{}{ //nolint:errcheck
		"content":  "persist this across sessions",
		"category": "pattern",
	}))

	reg2 := NewRegistry(dir)
	result, err := reg2.Handle(context.Background(), "memory_recall", toJSON(map[string]string{"query": "persist across sessions"}))
	if err != nil {
		t.Fatalf("memory_recall on second registry: %v", err)
	}
	if !strings.Contains(result, "persist this across sessions") {
		t.Errorf("expected persisted memory in new registry instance, got: %s", result)
	}
}

func TestMemoryRecallMaxResults(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	for i := 0; i < 15; i++ {
		reg.Handle(context.Background(), "memory_store", toJSON(map[string]interface{}{ //nolint:errcheck
			"content":  "golang test fact number entry",
			"category": "fact",
		}))
	}

	result, err := reg.Handle(context.Background(), "memory_recall", toJSON(map[string]interface{}{
		"query": "golang test fact",
		"limit": 5,
	}))
	if err != nil {
		t.Fatalf("memory_recall with limit: %v", err)
	}
	// Count occurrences of "[mem-" in the result — each entry starts with "[mem-<id>]".
	count := strings.Count(result, "[mem-")
	if count > 5 {
		t.Errorf("expected at most 5 results with limit=5, got %d in:\n%s", count, result)
	}
	if count == 0 {
		t.Errorf("expected at least 1 result matching 'golang test fact', got 0 in:\n%s", result)
	}
}
