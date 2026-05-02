package cortex

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
)

// TestWriteNoteDurable exercises the happy path: a real WAL-backed bus
// receives the Note via writeNote, and Replay must surface a single
// "cortex.note.published" event whose Payload round-trips through
// json.Marshal/Unmarshal back to the original Note value.
//
// This guards two regressions:
//
//  1. The event Type must be the canonical "cortex.note.published"
//     string -- subscribers and crash-replay readers rely on it.
//  2. The Payload must be the marshalled Note, not e.g. a wrapped
//     envelope or a separate field, so downstream consumers can simply
//     json.Unmarshal(evt.Payload, &Note{}).
func TestWriteNoteDurable(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "wal")
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	want := Note{
		ID:        "note-0",
		LobeID:    "memory-recall",
		Severity:  SevCritical,
		Title:     "secret-shape on input",
		Body:      "matched [A-Z0-9]{32} pattern",
		Tags:      []string{"plan-divergence", "secret-shape"},
		Resolves:  "",
		EmittedAt: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		Round:     7,
		Meta:      map[string]any{"score": float64(0.91)},
	}

	if err := writeNote(b, want); err != nil {
		t.Fatalf("writeNote: %v", err)
	}

	var got []bus.Event
	if err := b.Replay(bus.Pattern{TypePrefix: "cortex."}, 1, func(e bus.Event) {
		got = append(got, e)
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("Replay returned %d events, want 1", len(got))
	}
	if got[0].Type != EventTypeCortexNotePublished {
		t.Fatalf("Type=%q, want %q", got[0].Type, EventTypeCortexNotePublished)
	}
	if len(got[0].Payload) == 0 {
		t.Fatalf("Payload is empty")
	}

	// Round-trip the payload back into a Note and assert structural equality.
	// Note carries a map[string]any field, so reflect.DeepEqual is the
	// correct comparison rather than ==.
	var roundtrip Note
	if err := json.Unmarshal(got[0].Payload, &roundtrip); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	// time.Time round-trips through JSON as RFC3339Nano; normalise the
	// expected value to UTC to match what json.Unmarshal produces.
	want.EmittedAt = want.EmittedAt.UTC()
	roundtrip.EmittedAt = roundtrip.EmittedAt.UTC()

	if !reflect.DeepEqual(roundtrip, want) {
		t.Fatalf("Note mismatch after round-trip:\n got=%+v\nwant=%+v", roundtrip, want)
	}
}

// TestWriteNoteNilBus locks in the spec-item-22 contract that a nil
// durable bus is the in-memory mode signal: writeNote must succeed and
// must not panic. The Workspace constructor advertises this behaviour
// to callers (see NewWorkspace doc comment), so a regression here would
// silently break every test that passes a nil bus.
func TestWriteNoteNilBus(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("writeNote(nil, ...) panicked: %v", r)
		}
	}()

	n := Note{
		LobeID:   "memory-recall",
		Severity: SevInfo,
		Title:    "ok",
	}
	if err := writeNote(nil, n); err != nil {
		t.Fatalf("writeNote(nil, ...): err=%v, want nil", err)
	}
}

// TestWriteNoteDurablePublishesViaWorkspace covers the integration with
// Workspace.Publish: when a Workspace is constructed with a real durable
// bus, every successful Publish must produce exactly one
// "cortex.note.published" event whose payload decodes to the same Note
// the Workspace stored. This guards against a future refactor that
// re-routes persistence around writeNote.
func TestWriteNoteDurablePublishesViaWorkspace(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "wal")
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	w := NewWorkspace(nil, b)
	in := Note{
		LobeID:   "scope-guard",
		Severity: SevWarning,
		Title:    "out-of-scope edit",
		Body:     "modifying internal/cortex while plan said internal/bus",
		Tags:     []string{"scope"},
	}
	if err := w.Publish(in); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var got []bus.Event
	if err := b.Replay(bus.Pattern{TypePrefix: "cortex."}, 1, func(e bus.Event) {
		got = append(got, e)
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Replay returned %d events, want 1", len(got))
	}

	var decoded Note
	if err := json.Unmarshal(got[0].Payload, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.ID != "note-0" {
		t.Fatalf("ID=%q, want note-0", decoded.ID)
	}
	if decoded.LobeID != in.LobeID || decoded.Title != in.Title {
		t.Fatalf("decoded mismatch: got=%+v", decoded)
	}
}
