package bridge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/audit"
	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/wisdom"
)

func setup(t *testing.T) (*bus.Bus, *ledger.Ledger) {
	t.Helper()
	dir := t.TempDir()
	b, err := bus.New(filepath.Join(dir, "wal"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { b.Close() })

	l, err := ledger.New(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	return b, l
}

// collectEvents subscribes to a pattern and collects events until the
// returned cancel function is called.
func collectEvents(t *testing.T, b *bus.Bus, prefix string) (events func() []bus.Event, cancel func()) {
	t.Helper()
	var mu sync.Mutex
	var collected []bus.Event
	sub := b.Subscribe(bus.Pattern{TypePrefix: prefix}, func(evt bus.Event) {
		mu.Lock()
		collected = append(collected, evt)
		mu.Unlock()
	})
	return func() []bus.Event {
			mu.Lock()
			defer mu.Unlock()
			out := make([]bus.Event, len(collected))
			copy(out, collected)
			return out
		}, func() {
			sub.Cancel()
		}
}

func TestCostBridgeRecord(t *testing.T) {
	b, l := setup(t)
	events, cancel := collectEvents(t, b, "cost.")
	defer cancel()

	cb := NewCostBridge(b, l, 100.0)
	cost := cb.Record("claude-sonnet-4", "task-1", 1000, 500, 0, 0)

	if cost <= 0 {
		t.Fatalf("expected positive cost, got %f", cost)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(events()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	evts := events()
	if len(evts) == 0 {
		t.Fatal("expected at least one cost event")
	}
	if evts[0].Type != EvtCostRecorded {
		t.Fatalf("expected event type %s, got %s", EvtCostRecorded, evts[0].Type)
	}

	// Verify payload contains usage data.
	var usage costtrack.Usage
	if err := json.Unmarshal(evts[0].Payload, &usage); err != nil {
		t.Fatalf("unmarshal usage payload: %v", err)
	}
	if usage.Model != "claude-sonnet-4" {
		t.Fatalf("expected model claude-sonnet-4, got %s", usage.Model)
	}

	// Verify ledger node was written.
	nodes, err := l.Query(context.Background(), ledger.QueryFilter{Type: "cost_record"})
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 ledger node, got %d", len(nodes))
	}

	// Verify pass-throughs.
	if cb.Total() != cost {
		t.Fatalf("Total() = %f, want %f", cb.Total(), cost)
	}
	byModel := cb.ByModel()
	if byModel["claude-sonnet-4"] != cost {
		t.Fatalf("ByModel mismatch")
	}
	byTask := cb.ByTask()
	if byTask["task-1"] != cost {
		t.Fatalf("ByTask mismatch")
	}
	if cb.OverBudget() {
		t.Fatal("should not be over budget")
	}
}

func TestCostBridgeBudgetAlert(t *testing.T) {
	b, l := setup(t)
	events, cancel := collectEvents(t, b, "cost.")
	defer cancel()

	// Very small budget so that recording triggers alerts.
	cb := NewCostBridge(b, l, 0.0000001)
	cb.Record("claude-sonnet-4", "task-1", 10000, 5000, 0, 0)

	// Allow time for async delivery.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(events()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	evts := events()
	var alertCount int
	for _, e := range evts {
		if e.Type == EvtBudgetAlert {
			alertCount++
		}
	}
	if alertCount == 0 {
		t.Fatal("expected at least one budget alert event")
	}
}

func TestVerifyBridgeRun(t *testing.T) {
	b, l := setup(t)
	events, cancel := collectEvents(t, b, "verify.")
	defer cancel()

	// Use "true" as a command that always succeeds.
	vb := NewVerifyBridge(b, l, "true", "", "")
	outcomes, err := vb.Run(context.Background(), t.TempDir(), "task-v1", "mission-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes, got %d", len(outcomes))
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(events()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	evts := events()
	if len(evts) < 2 {
		t.Fatalf("expected at least 2 verify events, got %d", len(evts))
	}
	if evts[0].Type != EvtVerifyStarted {
		t.Fatalf("first event should be %s, got %s", EvtVerifyStarted, evts[0].Type)
	}
	if evts[1].Type != EvtVerifyCompleted {
		t.Fatalf("second event should be %s, got %s", EvtVerifyCompleted, evts[1].Type)
	}

	// Verify ledger node.
	nodes, err := l.Query(context.Background(), ledger.QueryFilter{Type: "verification"})
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 verification node, got %d", len(nodes))
	}
}

func TestWisdomBridgeRecord(t *testing.T) {
	b, l := setup(t)
	events, cancel := collectEvents(t, b, "wisdom.")
	defer cancel()

	wb := NewWisdomBridge(b, l)
	wb.Record("task-w1", wisdom.Learning{
		Category:    wisdom.Gotcha,
		Description: "nil pointer on empty slice",
		File:        "pkg/foo.go",
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(events()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	evts := events()
	if len(evts) != 1 {
		t.Fatalf("expected 1 wisdom event, got %d", len(evts))
	}
	if evts[0].Type != EvtLearningRecorded {
		t.Fatalf("expected %s, got %s", EvtLearningRecorded, evts[0].Type)
	}

	// Verify ledger node.
	nodes, err := l.Query(context.Background(), ledger.QueryFilter{Type: "wisdom_learning"})
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 wisdom_learning node, got %d", len(nodes))
	}

	// Verify pass-throughs.
	if wb.ForPrompt() == "" {
		t.Fatal("ForPrompt should return non-empty after recording")
	}
	found := wb.FindByPattern("nonexistent")
	if found != nil {
		t.Fatal("FindByPattern should return nil for unknown hash")
	}
}

func TestAuditBridgeRecordReport(t *testing.T) {
	b, l := setup(t)
	events, cancel := collectEvents(t, b, "audit.")
	defer cancel()

	ab := NewAuditBridge(b, l)
	report := audit.AuditReport{
		Findings: []audit.ReviewFinding{
			{
				PersonaID: "security",
				Severity:  "high",
				Issue:     "hardcoded secret in config",
			},
		},
	}

	err := ab.RecordReport(context.Background(), "task-a1", "mission-1", report)
	if err != nil {
		t.Fatalf("RecordReport: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(events()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	evts := events()
	if len(evts) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(evts))
	}
	if evts[0].Type != EvtAuditCompleted {
		t.Fatalf("expected %s, got %s", EvtAuditCompleted, evts[0].Type)
	}

	// Verify ledger node.
	nodes, err := l.Query(context.Background(), ledger.QueryFilter{Type: "audit_report"})
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 audit_report node, got %d", len(nodes))
	}
}
