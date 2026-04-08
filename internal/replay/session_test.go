package replay

import (
	"path/filepath"
	"testing"
)

func TestRecordAndFinish(t *testing.T) {
	r := NewRecorder("s1", "t1")

	r.RecordMessage("user", "Fix the bug")
	r.RecordToolCall("grep", map[string]any{"pattern": "bug"})
	r.RecordMessage("assistant", "Found the issue")
	r.RecordError("compilation failed", map[string]any{"file": "main.go"})

	rec := r.Finish("success", "go", "bugfix")

	if rec.ID != "s1" || rec.TaskID != "t1" {
		t.Error("ID/TaskID mismatch")
	}
	if len(rec.Events) != 4 {
		t.Errorf("expected 4 events, got %d", len(rec.Events))
	}
	if rec.Outcome != "success" {
		t.Error("outcome should be success")
	}
	if len(rec.Tags) != 2 {
		t.Error("should have 2 tags")
	}
}

func TestEventSequence(t *testing.T) {
	r := NewRecorder("s1", "")
	r.RecordMessage("user", "hello")
	r.RecordMessage("assistant", "hi")

	rec := r.Finish("success")
	if rec.Events[0].Seq != 1 || rec.Events[1].Seq != 2 {
		t.Error("events should have sequential numbers")
	}
	if rec.Events[1].Elapsed <= 0 {
		t.Error("elapsed should be positive for second event")
	}
}

func TestPlayerNext(t *testing.T) {
	rec := &Recording{
		Events: []Event{
			{Seq: 1, Type: EventMessage},
			{Seq: 2, Type: EventToolCall},
			{Seq: 3, Type: EventMessage},
		},
	}

	p := NewPlayer(rec)
	e := p.Next()
	if e.Seq != 1 {
		t.Errorf("expected seq 1, got %d", e.Seq)
	}
	e = p.Next()
	if e.Seq != 2 {
		t.Errorf("expected seq 2, got %d", e.Seq)
	}
	if p.Remaining() != 1 {
		t.Errorf("expected 1 remaining, got %d", p.Remaining())
	}
}

func TestPlayerSeek(t *testing.T) {
	rec := &Recording{
		Events: []Event{
			{Seq: 1, Type: EventMessage},
			{Seq: 2, Type: EventToolCall},
			{Seq: 3, Type: EventError},
		},
	}

	p := NewPlayer(rec)
	if !p.Seek(3) {
		t.Error("should find seq 3")
	}
	e := p.Next()
	if e.Seq != 3 {
		t.Errorf("expected seq 3, got %d", e.Seq)
	}
}

func TestPlayerSeekToType(t *testing.T) {
	rec := &Recording{
		Events: []Event{
			{Seq: 1, Type: EventMessage},
			{Seq: 2, Type: EventToolCall},
			{Seq: 3, Type: EventError},
		},
	}

	p := NewPlayer(rec)
	e := p.SeekToType(EventError)
	if e == nil || e.Seq != 3 {
		t.Error("should find error event")
	}
}

func TestPlayerReset(t *testing.T) {
	rec := &Recording{Events: []Event{{Seq: 1}, {Seq: 2}}}
	p := NewPlayer(rec)
	p.Next()
	p.Reset()
	if p.Remaining() != 2 {
		t.Error("reset should go back to start")
	}
}

func TestPlayerPeek(t *testing.T) {
	rec := &Recording{Events: []Event{{Seq: 1}}}
	p := NewPlayer(rec)
	e := p.Peek()
	if e.Seq != 1 {
		t.Error("peek should return next event")
	}
	if p.Remaining() != 1 {
		t.Error("peek should not advance")
	}
}

func TestSaveAndLoad(t *testing.T) {
	rec := &Recording{
		ID:      "test",
		Outcome: "success",
		Events:  []Event{{Seq: 1, Type: EventMessage, Data: map[string]any{"text": "hi"}}},
	}

	path := filepath.Join(t.TempDir(), "replay.json")
	if err := Save(rec, path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "test" || loaded.Outcome != "success" {
		t.Error("loaded data mismatch")
	}
	if len(loaded.Events) != 1 {
		t.Error("should have 1 event")
	}
}

func TestEventCounts(t *testing.T) {
	rec := &Recording{
		Events: []Event{
			{Type: EventMessage},
			{Type: EventMessage},
			{Type: EventToolCall},
			{Type: EventError},
		},
	}

	counts := rec.EventCounts()
	if counts[EventMessage] != 2 {
		t.Error("expected 2 messages")
	}
	if counts[EventToolCall] != 1 {
		t.Error("expected 1 tool call")
	}
}

func TestErrors(t *testing.T) {
	rec := &Recording{
		Events: []Event{
			{Type: EventMessage},
			{Type: EventError, Data: map[string]any{"error": "fail"}},
			{Type: EventMessage},
		},
	}

	errors := rec.Errors()
	if len(errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(errors))
	}
}

func TestSummary(t *testing.T) {
	r := NewRecorder("s1", "t1")
	r.RecordMessage("user", "test")
	rec := r.Finish("success")

	s := rec.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
}

func TestPlayerEndOfStream(t *testing.T) {
	rec := &Recording{Events: []Event{{Seq: 1}}}
	p := NewPlayer(rec)
	p.Next()
	if p.Next() != nil {
		t.Error("should return nil at end")
	}
	if p.Peek() != nil {
		t.Error("peek should return nil at end")
	}
}
