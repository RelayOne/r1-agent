package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileProtectionGate(t *testing.T) {
	b := New()
	b.Register(FileProtectionGate([]string{"go.mod", "go.sum"}))

	// Should block protected file
	resp := b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "go.mod", Operation: "write"},
	})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny for protected file, got %s", resp.Decision)
	}

	// Should allow non-protected file
	resp = b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "main.go", Operation: "write"},
	})
	if resp.Decision == Deny {
		t.Fatal("should allow non-protected file")
	}
}

func TestScopeEnforcementGate(t *testing.T) {
	b := New()
	b.Register(ScopeEnforcementGate([]string{"src/main.go", "src/util.go"}))

	// Should allow in-scope file
	resp := b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "src/main.go", Operation: "write"},
	})
	if resp.Decision == Deny {
		t.Fatal("should allow in-scope file")
	}

	// Should block out-of-scope file
	resp = b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "config/db.go", Operation: "write"},
	})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny for out-of-scope file, got %s", resp.Decision)
	}
}

func TestScopeEnforcementGateEmptyAllowAll(t *testing.T) {
	b := New()
	b.Register(ScopeEnforcementGate(nil))

	resp := b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "anything.go", Operation: "write"},
	})
	if resp.Decision == Deny {
		t.Fatal("empty scope should allow all files")
	}
}

func TestWisdomSubscriber(t *testing.T) {
	var recorded atomic.Int32
	b := New()
	b.Register(WisdomSubscriber(func(task string, success bool, attempt int) {
		recorded.Add(1)
	}))

	b.Emit(context.Background(), &Event{
		Type:   EventTaskCompleted,
		TaskID: "task-1",
		Lifecycle: &LifecycleEvent{Entity: "task", State: "completed", Attempt: 1},
	})

	time.Sleep(50 * time.Millisecond)
	if recorded.Load() != 1 {
		t.Fatalf("expected 1 wisdom record, got %d", recorded.Load())
	}
}

func TestCostAlertSubscriber(t *testing.T) {
	var alerts atomic.Int32
	b := New()
	b.Register(CostAlertSubscriber(func(threshold string, spent, budget float64) {
		alerts.Add(1)
	}))

	b.Emit(context.Background(), &Event{
		Type: EventCostBudget80,
		Cost: &CostEvent{Threshold: "80%", TotalSpent: 8.0, BudgetLimit: 10.0},
	})

	time.Sleep(50 * time.Millisecond)
	if alerts.Load() != 1 {
		t.Fatalf("expected 1 alert, got %d", alerts.Load())
	}
}

func TestPromptInjectionTransformer(t *testing.T) {
	b := New()
	b.Register(PromptInjectionTransformer("test-wisdom", func() string {
		return "Remember: always check nil"
	}))

	injections := b.Transform(context.Background(), &Event{Type: EventPromptBuilding})
	if len(injections) != 1 {
		t.Fatalf("expected 1 injection, got %d", len(injections))
	}
	if injections[0].Content != "Remember: always check nil" {
		t.Fatalf("unexpected content: %s", injections[0].Content)
	}
}

func TestPromptInjectionTransformerEmpty(t *testing.T) {
	b := New()
	b.Register(PromptInjectionTransformer("empty", func() string { return "" }))

	injections := b.Transform(context.Background(), &Event{Type: EventPromptBuilding})
	if len(injections) != 0 {
		t.Fatalf("expected 0 injections for empty content, got %d", len(injections))
	}
}

func TestLoadConfigMissing(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("expected nil error for missing config, got %v", err)
	}
	if len(cfg.Scripts) != 0 || len(cfg.Webhooks) != 0 {
		t.Fatal("expected empty config")
	}
}

func TestLoadConfigValid(t *testing.T) {
	dir := t.TempDir()
	stokeDir := filepath.Join(dir, ".stoke")
	os.MkdirAll(stokeDir, 0o755)

	cfg := HookConfig{
		Scripts: []ScriptHookConfig{
			{ID: "lint-check", Events: []string{"tool.file_write"}, Mode: "gate", Priority: 10, Command: "golangci-lint run", Timeout: "10s"},
		},
		Webhooks: []WebhookHookConfig{
			{ID: "notify", Events: []string{"task.completed"}, Mode: "observe", URL: "http://localhost:8080/hook"},
		},
	}
	data, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(stokeDir, "hooks.json"), data, 0o644)

	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(loaded.Scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(loaded.Scripts))
	}
	if loaded.Scripts[0].ID != "lint-check" {
		t.Fatalf("expected lint-check, got %s", loaded.Scripts[0].ID)
	}
	if len(loaded.Webhooks) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(loaded.Webhooks))
	}
}

func TestApplyConfig(t *testing.T) {
	b := New()
	cfg := HookConfig{
		Scripts: []ScriptHookConfig{
			{ID: "s1", Events: []string{"task.completed"}, Mode: "observe", Command: "echo done"},
		},
	}
	b.ApplyConfig(cfg)
	if b.SubscriberCount() != 1 {
		t.Fatalf("expected 1 subscriber after ApplyConfig, got %d", b.SubscriberCount())
	}
}

func TestSecurityScanObserver(t *testing.T) {
	var logged atomic.Int32
	b := New()
	b.Register(SecurityScanObserver(func(category, severity, details string) {
		logged.Add(1)
	}))

	b.Emit(context.Background(), &Event{
		Type:     EventSecuritySecretDetected,
		Security: &SecurityEvent{Category: "secret", Severity: "critical", Details: "API key in source"},
	})

	time.Sleep(50 * time.Millisecond)
	if logged.Load() != 1 {
		t.Fatalf("expected 1 log entry, got %d", logged.Load())
	}
}
