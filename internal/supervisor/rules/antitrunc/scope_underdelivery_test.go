package antitrunc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func writePlan(t *testing.T, dir string, body string) string {
	t.Helper()
	p := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScopeUnderdelivery_Evaluate_Fires(t *testing.T) {
	dir := t.TempDir()
	plan := writePlan(t, dir, "- [x] one\n- [ ] two\n")
	l, _ := ledger.New(t.TempDir())
	defer l.Close()

	r := NewScopeUnderdelivery(plan)
	fired, err := r.Evaluate(context.Background(), bus.Event{}, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Error("expected fire on partial plan")
	}
}

func TestScopeUnderdelivery_Evaluate_AllChecked_NoFire(t *testing.T) {
	dir := t.TempDir()
	plan := writePlan(t, dir, "- [x] one\n- [x] two\n")
	l, _ := ledger.New(t.TempDir())
	defer l.Close()

	r := NewScopeUnderdelivery(plan)
	fired, err := r.Evaluate(context.Background(), bus.Event{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("expected NO fire on fully checked plan")
	}
}

func TestScopeUnderdelivery_Evaluate_NoPaths_NoFire(t *testing.T) {
	r := NewScopeUnderdelivery() // no paths
	l, _ := ledger.New(t.TempDir())
	defer l.Close()
	fired, err := r.Evaluate(context.Background(), bus.Event{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("expected NO fire when no paths configured")
	}
}

func TestScopeUnderdelivery_Evaluate_UnreadableSkipped(t *testing.T) {
	r := NewScopeUnderdelivery("/no/such/path.md")
	l, _ := ledger.New(t.TempDir())
	defer l.Close()
	fired, err := r.Evaluate(context.Background(), bus.Event{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("unreadable file should not fire")
	}
}

func TestScopeUnderdelivery_Evaluate_AnyPathFires(t *testing.T) {
	// First plan complete, second incomplete; rule must fire because
	// of the second.
	dirA, dirB := t.TempDir(), t.TempDir()
	planA := writePlan(t, dirA, "- [x] only\n")
	planB := writePlan(t, dirB, "- [ ] open\n")
	l, _ := ledger.New(t.TempDir())
	defer l.Close()
	r := NewScopeUnderdelivery(planA, planB)
	fired, err := r.Evaluate(context.Background(), bus.Event{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Error("expected fire when ANY path has unchecked items")
	}
}

func TestScopeUnderdelivery_Action_Publishes(t *testing.T) {
	dir := t.TempDir()
	plan := writePlan(t, dir, "- [ ] open\n- [ ] also open\n")
	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	r := NewScopeUnderdelivery(plan)

	var captured []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{TypePrefix: string(bus.EvtSupervisorRuleFired)}, func(e bus.Event) {
		mu.Lock()
		captured = append(captured, e)
		mu.Unlock()
	})

	evt := bus.Event{
		ID:   "evt-2",
		Type: bus.EvtWorkerDeclarationDone,
	}
	if err := r.Action(context.Background(), evt, b); err != nil {
		t.Fatalf("Action: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(captured)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	if len(captured) == 0 {
		t.Fatal("expected published event")
	}
	var pl map[string]any
	if err := json.Unmarshal(captured[0].Payload, &pl); err != nil {
		t.Fatalf("payload: %v", err)
	}
	reps, ok := pl["reports"].([]any)
	if !ok || len(reps) == 0 {
		t.Errorf("expected reports array; got %v", pl["reports"])
	}
}
