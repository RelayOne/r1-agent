package builtin

import (
	"context"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

func TestHonestyGatePlaceholder(t *testing.T) {
	bus := hub.New()
	h := &HonestyGate{}
	h.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventToolFileWrite,
		Timestamp: time.Now(),
		File:      &hub.FileEvent{Path: "main.go", Operation: "write"},
		Custom: map[string]any{
			"content": "func handler() {\n\tpanic(\"not implemented\")\n}\n",
		},
	}

	resp := bus.Emit(context.Background(), ev)
	if resp.Decision != hub.Deny {
		t.Errorf("expected deny for placeholder, got %s", resp.Decision)
	}
	if resp.Reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestHonestyGateAllowsCleanCode(t *testing.T) {
	bus := hub.New()
	h := &HonestyGate{}
	h.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventToolFileWrite,
		Timestamp: time.Now(),
		File:      &hub.FileEvent{Path: "main.go", Operation: "write"},
		Custom: map[string]any{
			"content": "func handler() error {\n\treturn nil\n}\n",
		},
	}

	resp := bus.Emit(context.Background(), ev)
	if resp.Decision == hub.Deny {
		t.Errorf("expected allow for clean code, got deny: %s", resp.Reason)
	}
}

func TestHonestyGateSuppression(t *testing.T) {
	bus := hub.New()
	h := &HonestyGate{}
	h.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventToolFileWrite,
		Timestamp: time.Now(),
		File:      &hub.FileEvent{Path: "app.ts", Operation: "write"},
		Custom: map[string]any{
			"content": "const val = someFunc() as any;\n",
		},
	}

	resp := bus.Emit(context.Background(), ev)
	if resp.Decision != hub.Deny {
		t.Errorf("expected deny for suppression, got %s", resp.Decision)
	}
}

func TestHonestyGateTestRemoval(t *testing.T) {
	bus := hub.New()
	h := &HonestyGate{}
	h.Register(bus)

	oldContent := "func TestFoo(t *testing.T) {\n\tassert(true)\n}\nfunc TestBar(t *testing.T) {\n\tassert(true)\n}\n"
	newContent := "// empty\n"

	ev := &hub.Event{
		Type:      hub.EventToolFileWrite,
		Timestamp: time.Now(),
		File:      &hub.FileEvent{Path: "foo_test.go", Operation: "write"},
		Custom: map[string]any{
			"content":     newContent,
			"old_content": oldContent,
		},
	}

	resp := bus.Emit(context.Background(), ev)
	if resp.Decision != hub.Deny {
		t.Errorf("expected deny for test removal, got %s", resp.Decision)
	}
}

func TestSecretScannerAWSKey(t *testing.T) {
	bus := hub.New()
	s := NewDefaultSecretScanner()
	s.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventToolFileWrite,
		Timestamp: time.Now(),
		File:      &hub.FileEvent{Path: "config.go", Operation: "write"},
		Custom: map[string]any{
			"content": "aws_access_key_id = AKIAIOSFODNN7EXAMPLE\n",
		},
	}

	resp := bus.Emit(context.Background(), ev)
	if resp.Decision != hub.Deny {
		t.Errorf("expected deny for AWS key, got %s", resp.Decision)
	}
}

func TestSecretScannerPrivateKey(t *testing.T) {
	bus := hub.New()
	s := NewDefaultSecretScanner()
	s.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventToolFileWrite,
		Timestamp: time.Now(),
		File:      &hub.FileEvent{Path: "key.pem", Operation: "write"},
		Custom: map[string]any{
			"content": "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAK...\n",
		},
	}

	resp := bus.Emit(context.Background(), ev)
	if resp.Decision != hub.Deny {
		t.Errorf("expected deny for private key, got %s", resp.Decision)
	}
}

func TestSecretScannerAllowsClean(t *testing.T) {
	bus := hub.New()
	s := NewDefaultSecretScanner()
	s.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventToolFileWrite,
		Timestamp: time.Now(),
		File:      &hub.FileEvent{Path: "main.go", Operation: "write"},
		Custom: map[string]any{
			"content": "func main() {\n\tfmt.Println(\"hello\")\n}\n",
		},
	}

	resp := bus.Emit(context.Background(), ev)
	if resp.Decision == hub.Deny {
		t.Errorf("expected allow for clean code, got deny: %s", resp.Reason)
	}
}

func TestCostTracker(t *testing.T) {
	bus := hub.New()
	ct := NewCostTracker()
	ct.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventModelPostCall,
		Timestamp: time.Now(),
		Model: &hub.ModelEvent{
			Model:        "claude-sonnet-4-6",
			InputTokens:  1000,
			OutputTokens: 500,
		},
	}

	bus.Emit(context.Background(), ev)

	// Give async observer time to run
	time.Sleep(50 * time.Millisecond)

	total := ct.TotalUSD()
	if total <= 0 {
		t.Errorf("expected positive cost, got %f", total)
	}

	totalSnap, perModel := ct.Snapshot()
	if totalSnap != total {
		t.Errorf("snapshot total %f != direct total %f", totalSnap, total)
	}
	if perModel["claude-sonnet-4-6"] <= 0 {
		t.Error("expected per-model cost for sonnet")
	}
}

func TestCostTrackerUnknownModel(t *testing.T) {
	bus := hub.New()
	ct := NewCostTracker()
	ct.Register(bus)

	ev := &hub.Event{
		Type:      hub.EventModelPostCall,
		Timestamp: time.Now(),
		Model: &hub.ModelEvent{
			Model:        "unknown-model",
			InputTokens:  1000,
			OutputTokens: 500,
		},
	}

	bus.Emit(context.Background(), ev)
	time.Sleep(50 * time.Millisecond)

	if ct.TotalUSD() != 0 {
		t.Errorf("expected 0 cost for unknown model, got %f", ct.TotalUSD())
	}
}

func TestAllSubscribersRegister(t *testing.T) {
	bus := hub.New()

	h := &HonestyGate{}
	h.Register(bus)

	s := NewDefaultSecretScanner()
	s.Register(bus)

	ct := NewCostTracker()
	ct.Register(bus)

	si := &SkillInjector{}
	si.Register(bus)

	count := bus.SubscriberCount()
	if count < 4 {
		t.Errorf("expected at least 4 subscribers, got %d", count)
	}
}
