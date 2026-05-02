package cortex

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
)

// makeReplayNote constructs a Note suitable for round-tripping through
// the durable WAL and back into a Workspace via Replay. Test helpers
// that exercise replay rebuild semantics need three things:
//
//  1. A non-zero EmittedAt in UTC so reflect.DeepEqual after JSON
//     round-trip matches the value the test wrote.
//  2. A unique LobeID/Title pair per call so ordering assertions can
//     distinguish the events.
//  3. A populated ID and Round, since Replay must restore both verbatim
//     -- it does not re-derive them from w.seq the way Publish does.
func makeReplayNote(idx int) Note {
	return Note{
		ID:        fmt.Sprintf("note-%d", idx),
		LobeID:    fmt.Sprintf("test-lobe-%d", idx),
		Severity:  SevInfo,
		Title:     fmt.Sprintf("replay-note-%d", idx),
		Body:      fmt.Sprintf("body-%d", idx),
		Tags:      []string{"replay"},
		EmittedAt: time.Date(2026, 5, 2, 12, 0, idx, 0, time.UTC),
		Round:     uint64(idx),
	}
}

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

// TestReplayRebuilds is the happy-path round-trip: write three
// cortex.note.published events directly to a durable bus via writeNote,
// then call Workspace.Replay on a fresh Workspace pointing at the same
// bus. Snapshot must surface all three Notes in append order, the
// drainedUpTo cursor must equal the count of replayed Notes, and w.seq
// must be advanced so a subsequent Publish would not reuse an ID.
func TestReplayRebuilds(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "wal")
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	want := []Note{
		makeReplayNote(0),
		makeReplayNote(1),
		makeReplayNote(2),
	}
	for _, n := range want {
		if err := writeNote(b, n); err != nil {
			t.Fatalf("writeNote: %v", err)
		}
	}

	w := NewWorkspace(nil, b)
	if err := w.Replay(); err != nil {
		t.Fatalf("Replay: %v", err)
	}

	got := w.Snapshot()
	if len(got) != len(want) {
		t.Fatalf("Snapshot len=%d, want %d", len(got), len(want))
	}
	for i := range want {
		// JSON round-trip normalises EmittedAt; force UTC on both sides
		// so reflect.DeepEqual compares wall-clock equivalents.
		want[i].EmittedAt = want[i].EmittedAt.UTC()
		got[i].EmittedAt = got[i].EmittedAt.UTC()
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("Snapshot[%d] mismatch:\n got=%+v\nwant=%+v", i, got[i], want[i])
		}
	}

	// drainedUpTo must equal len(notes) so the next Drain caller sees a
	// self-consistent cursor immediately after recovery.
	if _, cursor := w.Drain(^uint64(0)); cursor < uint64(len(want)) {
		t.Fatalf("drainedUpTo cursor=%d, want >=%d", cursor, len(want))
	}

	// w.seq must be advanced so a subsequent Publish does not collide
	// with a replayed Note. A fresh Publish should land at "note-N"
	// where N == len(replayed notes).
	pub := Note{LobeID: "post-replay", Severity: SevInfo, Title: "after"}
	if err := w.Publish(pub); err != nil {
		t.Fatalf("Publish post-replay: %v", err)
	}
	all := w.Snapshot()
	if got := all[len(all)-1].ID; got != fmt.Sprintf("note-%d", len(want)) {
		t.Fatalf("post-replay Publish ID=%q, want note-%d", got, len(want))
	}
}

// TestReplayIdempotent guards the spec-mandated no-op behaviour: if
// Replay has already populated the workspace, a second call must not
// re-append the same Notes. Boot code may call Replay unconditionally,
// so a regression here would silently double the workspace on every
// restart that touched Replay twice.
func TestReplayIdempotent(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "wal")
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	for i := 0; i < 3; i++ {
		if err := writeNote(b, makeReplayNote(i)); err != nil {
			t.Fatalf("writeNote: %v", err)
		}
	}

	w := NewWorkspace(nil, b)
	if err := w.Replay(); err != nil {
		t.Fatalf("Replay #1: %v", err)
	}
	first := len(w.Snapshot())

	if err := w.Replay(); err != nil {
		t.Fatalf("Replay #2: %v", err)
	}
	second := len(w.Snapshot())

	if first != second {
		t.Fatalf("second Replay duplicated notes: first=%d, second=%d", first, second)
	}
	if first != 3 {
		t.Fatalf("first Replay produced %d notes, want 3", first)
	}
}

// TestReplayNilDurable locks in the in-memory mode contract: a
// Workspace constructed without a durable bus must accept Replay as a
// silent no-op. Boot code that calls Replay unconditionally relies on
// this so the same call site works for both modes.
func TestReplayNilDurable(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Replay() panicked with nil durable: %v", r)
		}
	}()

	w := NewWorkspace(nil, nil)
	if err := w.Replay(); err != nil {
		t.Fatalf("Replay(nil durable): err=%v, want nil", err)
	}
	if got := len(w.Snapshot()); got != 0 {
		t.Fatalf("Snapshot after nil-durable Replay len=%d, want 0", got)
	}
}

// TestReplayCorruptPayloadSkipped exercises the per-event resilience
// contract: if a single payload fails to JSON-decode into a Note,
// Replay must log a warning and continue with the rest of the WAL.
// We simulate corruption by publishing an event whose Payload is a
// JSON string ("oops") rather than a JSON object -- valid JSON at the
// envelope level (so the WAL accepts it) but invalid as a Note.
func TestReplayCorruptPayloadSkipped(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "wal")
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	first := makeReplayNote(0)
	last := makeReplayNote(2)
	if err := writeNote(b, first); err != nil {
		t.Fatalf("writeNote first: %v", err)
	}
	// Corrupt middle event: Payload is a JSON string, not an object,
	// so json.Unmarshal into &Note{} fails. The bus envelope itself
	// remains valid NDJSON.
	if err := b.Publish(bus.Event{
		Type:    EventTypeCortexNotePublished,
		Payload: json.RawMessage(`"oops-not-a-note"`),
	}); err != nil {
		t.Fatalf("Publish corrupt: %v", err)
	}
	if err := writeNote(b, last); err != nil {
		t.Fatalf("writeNote last: %v", err)
	}

	w := NewWorkspace(nil, b)
	if err := w.Replay(); err != nil {
		t.Fatalf("Replay: %v", err)
	}

	got := w.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot len=%d, want 2 (corrupt event must be skipped)", len(got))
	}
	if got[0].ID != first.ID || got[1].ID != last.ID {
		t.Fatalf("Snapshot order wrong: got IDs=[%q,%q], want [%q,%q]",
			got[0].ID, got[1].ID, first.ID, last.ID)
	}
}
