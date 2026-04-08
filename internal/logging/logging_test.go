package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func resetGlobal() {
	globalLogger = nil
	initOnce = sync.Once{}
}

func TestInit_Idempotent(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("debug", &buf)
	l1 := Global()
	Init("error", &buf) // second call should be no-op
	l2 := Global()
	if l1 != l2 {
		t.Error("Init should be idempotent")
	}
}

func TestComponent(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("debug", &buf)

	Component("workflow").Info("test message")
	if !strings.Contains(buf.String(), "workflow") {
		t.Errorf("expected component=workflow in output, got: %s", buf.String())
	}
}

func TestTask(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("debug", &buf)

	Task("engine", "task-42").Info("started")
	output := buf.String()
	if !strings.Contains(output, "engine") {
		t.Errorf("expected component=engine in output")
	}
	if !strings.Contains(output, "task-42") {
		t.Errorf("expected task_id=task-42 in output")
	}
}

func TestContext(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("debug", &buf)

	logger := Component("test")
	ctx := WithContext(context.Background(), logger)
	recovered := FromContext(ctx)
	if recovered != logger {
		t.Error("FromContext should return the logger stored by WithContext")
	}

	// FromContext with no logger should return global
	emptyCtx := context.Background()
	fallback := FromContext(emptyCtx)
	if fallback == nil {
		t.Error("FromContext should return global logger as fallback")
	}
}

func TestCostLog(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("debug", &buf)

	Cost(Global(), "task-1", 0.05, "claude-sonnet")
	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("parse log entry: %v", err)
	}
	if entry["task_id"] != "task-1" {
		t.Errorf("expected task_id=task-1, got %v", entry["task_id"])
	}
}

func TestLevelFiltering(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("error", &buf)

	Global().Info("should be filtered")
	if buf.Len() > 0 {
		t.Error("info message should be filtered at error level")
	}

	Global().Error("should appear")
	if buf.Len() == 0 {
		t.Error("error message should appear at error level")
	}
}

func TestWith(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("debug", &buf)

	logger := With("key", "val")
	logger.Info("test")
	if !strings.Contains(buf.String(), "val") {
		t.Error("expected key=val in output")
	}
}

func TestAttempt(t *testing.T) {
	resetGlobal()

	var buf bytes.Buffer
	Init("debug", &buf)

	Attempt(Global(), "task-5", 2, true, 1500)
	output := buf.String()
	if !strings.Contains(output, "task-5") {
		t.Error("expected task_id in output")
	}
	if !strings.Contains(output, "attempt") {
		t.Error("expected attempt in output")
	}
}
