package antitrunc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestTruncationPhraseDetected_Evaluate_Fires(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	r := NewTruncationPhraseDetected()
	payload, _ := json.Marshal(map[string]string{"summary": "i'll stop here for now"})
	evt := bus.Event{
		Type:      bus.EvtWorkerDeclarationDone,
		Timestamp: time.Now(),
		Payload:   payload,
	}
	fired, err := r.Evaluate(context.Background(), evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Error("expected rule to fire on truncation phrase")
	}
}

func TestTruncationPhraseDetected_Evaluate_NoFire(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.New(dir)
	defer l.Close()

	r := NewTruncationPhraseDetected()
	payload, _ := json.Marshal(map[string]string{"summary": "build is green; tests pass"})
	evt := bus.Event{Type: bus.EvtWorkerDeclarationDone, Payload: payload}
	fired, err := r.Evaluate(context.Background(), evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Error("expected clean rule to NOT fire")
	}
}

func TestTruncationPhraseDetected_Evaluate_NoExtractor(t *testing.T) {
	r := &TruncationPhraseDetected{Extractor: nil}
	dir := t.TempDir()
	l, _ := ledger.New(dir)
	defer l.Close()
	fired, err := r.Evaluate(context.Background(), bus.Event{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("expected nil extractor to disable rule")
	}
}

func TestTruncationPhraseDetected_Action_Publishes(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	r := NewTruncationPhraseDetected()
	payload, _ := json.Marshal(map[string]string{"summary": "good enough to merge"})
	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtWorkerDeclarationDone,
		Timestamp: time.Now(),
		Payload:   payload,
	}

	var captured []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{TypePrefix: string(bus.EvtSupervisorRuleFired)}, func(e bus.Event) {
		mu.Lock()
		captured = append(captured, e)
		mu.Unlock()
	})

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
		t.Fatal("expected supervisor.rule.fired event")
	}
	var pl map[string]any
	if err := json.Unmarshal(captured[0].Payload, &pl); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if pl["category"] != "antitrunc" {
		t.Errorf("category = %v, want antitrunc", pl["category"])
	}
	if pl["severity"] != "critical" {
		t.Errorf("severity = %v, want critical", pl["severity"])
	}
}

func TestDefaultDeclarationTextExtractor(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"summary", map[string]any{"summary": "hello"}, "hello"},
		{"text_fallback", map[string]any{"text": "world"}, "world"},
		{"output_fallback", map[string]any{"output": "out"}, "out"},
		{"message_fallback", map[string]any{"message": "msg"}, "msg"},
		{"empty", map[string]any{}, ""},
		{"non_string", map[string]any{"summary": 42}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.in)
			evt := bus.Event{Payload: body}
			got := DefaultDeclarationTextExtractor(evt)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultDeclarationTextExtractor_BadJSON(t *testing.T) {
	evt := bus.Event{Payload: []byte("not json")}
	if got := DefaultDeclarationTextExtractor(evt); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
