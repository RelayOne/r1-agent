package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1-agent/internal/bus"
)

// tempLog creates a Log in a per-test temp dir and schedules its cleanup.
func tempLog(t *testing.T) (*Log, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "events.db")
	l, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, dbPath
}

func newEvent(typeName, mission, task, loop string, payload map[string]any) bus.Event {
	raw, _ := json.Marshal(payload)
	return bus.Event{
		Type:      bus.EventType(typeName),
		EmitterID: "test",
		Scope: bus.Scope{
			MissionID: mission,
			TaskID:    task,
			LoopID:    loop,
		},
		Payload: raw,
	}
}

func TestAppend_AssignsIDAndSequence(t *testing.T) {
	l, _ := tempLog(t)
	for i := 1; i <= 3; i++ {
		ev := newEvent("task.dispatch", "M1", "T1", "L1", map[string]any{"i": i})
		if err := l.Append(&ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if ev.ID == "" {
			t.Fatalf("Append %d: ID not assigned", i)
		}
		if got, want := ev.Sequence, uint64(i); got != want {
			t.Fatalf("Append %d: Sequence=%d, want %d", i, got, want)
		}
		if ev.Timestamp.IsZero() {
			t.Fatalf("Append %d: Timestamp not set", i)
		}
	}
}

func TestReadFrom_InOrder(t *testing.T) {
	l, _ := tempLog(t)
	for i := 0; i < 10; i++ {
		ev := newEvent("task.progress", "M1", "T1", "L1", map[string]any{"i": i})
		if err := l.Append(&ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	var seen []uint64
	for ev, err := range l.ReadFrom(context.Background(), 0) {
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		seen = append(seen, ev.Sequence)
	}
	if len(seen) != 10 {
		t.Fatalf("got %d events, want 10", len(seen))
	}
	for i, s := range seen {
		if s != uint64(i+1) {
			t.Fatalf("seen[%d]=%d, want %d", i, s, i+1)
		}
	}
}

func TestReadFrom_FromSequence(t *testing.T) {
	l, _ := tempLog(t)
	for i := 0; i < 10; i++ {
		ev := newEvent("x", "", "", "", map[string]any{"i": i})
		if err := l.Append(&ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	var seqs []uint64
	for ev, err := range l.ReadFrom(context.Background(), 5) {
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		seqs = append(seqs, ev.Sequence)
	}
	want := []uint64{5, 6, 7, 8, 9, 10}
	if len(seqs) != len(want) {
		t.Fatalf("got %v, want %v", seqs, want)
	}
	for i, s := range seqs {
		if s != want[i] {
			t.Fatalf("seqs[%d]=%d, want %d", i, s, want[i])
		}
	}
}

func TestHashChain_Valid(t *testing.T) {
	l, _ := tempLog(t)
	for i := 0; i < 100; i++ {
		ev := newEvent("x", "M", "T", "L", map[string]any{"i": i, "k": "value"})
		if err := l.Append(&ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := l.Verify(context.Background()); err != nil {
		t.Fatalf("Verify clean log: %v", err)
	}
}

func TestVerify_TamperedPayload(t *testing.T) {
	l, dbPath := tempLog(t)
	for i := 0; i < 20; i++ {
		ev := newEvent("x", "M", "T", "L", map[string]any{"i": i})
		if err := l.Append(&ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Close, tamper, reopen.
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(`UPDATE events SET payload = X'00' WHERE sequence = 10`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	l2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer l2.Close()

	err = l2.Verify(context.Background())
	if err == nil {
		t.Fatalf("Verify returned nil on tampered log")
	}
	var cb *ChainBrokenError
	if !errors.As(err, &cb) {
		t.Fatalf("Verify error type = %T (%v), want *ChainBrokenError", err, err)
	}
	// Tampering the payload at sequence=10 makes the NEXT row's
	// expected parent_hash disagree with its stored parent_hash; the
	// walk detects the break at sequence=11. Either detection point is
	// acceptable — we just require that Verify pinpoints near the
	// tamper.
	if cb.Sequence != 11 && cb.Sequence != 10 {
		t.Fatalf("ChainBrokenError.Sequence=%d, want 10 or 11", cb.Sequence)
	}
}

func TestReplaySession_MatchesOnAnyScope(t *testing.T) {
	l, _ := tempLog(t)
	// Event A: MissionID=X
	a := newEvent("a", "X", "", "", map[string]any{"p": 1})
	// Event B: TaskID=Y
	b := newEvent("b", "", "Y", "", map[string]any{"p": 2})
	// Event C: LoopID=Z
	c := newEvent("c", "", "", "Z", map[string]any{"p": 3})
	// Event D: unrelated
	d := newEvent("d", "W", "", "", map[string]any{"p": 4})
	for _, ev := range []*bus.Event{&a, &b, &c, &d} {
		if err := l.Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	collect := func(sessionID string) []string {
		var types []string
		for ev, err := range l.ReplaySession(context.Background(), sessionID) {
			if err != nil {
				t.Fatalf("ReplaySession %q: %v", sessionID, err)
			}
			types = append(types, string(ev.Type))
		}
		return types
	}

	if got := collect("X"); !equal(got, []string{"a"}) {
		t.Fatalf("ReplaySession(X) got %v, want [a]", got)
	}
	if got := collect("Y"); !equal(got, []string{"b"}) {
		t.Fatalf("ReplaySession(Y) got %v, want [b]", got)
	}
	if got := collect("Z"); !equal(got, []string{"c"}) {
		t.Fatalf("ReplaySession(Z) got %v, want [c]", got)
	}
	if got := collect("nonexistent"); len(got) != 0 {
		t.Fatalf("ReplaySession(nonexistent) got %v, want []", got)
	}
}

func TestAppend_ConcurrentWriters(t *testing.T) {
	l, _ := tempLog(t)

	const workers = 4
	const perWorker = 50
	done := make(chan struct{}, workers)
	var errCount atomic.Int32
	for w := 0; w < workers; w++ {
		go func(wid int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perWorker; i++ {
				ev := newEvent(
					"parallel",
					fmt.Sprintf("M-%d", wid),
					"",
					fmt.Sprintf("L-%d", wid),
					map[string]any{"w": wid, "i": i},
				)
				if err := l.Append(&ev); err != nil {
					errCount.Add(1)
					t.Errorf("worker %d: Append %d: %v", wid, i, err)
					return
				}
			}
		}(w)
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	if got := errCount.Load(); got != 0 {
		t.Fatalf("concurrent writers produced %d errors, want 0", got)
	}

	// Walk the log. Sequences should be 1..workers*perWorker, each unique.
	seen := make(map[uint64]bool)
	var maxSeq uint64
	for ev, err := range l.ReadFrom(context.Background(), 0) {
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		if seen[ev.Sequence] {
			t.Fatalf("duplicate sequence %d", ev.Sequence)
		}
		seen[ev.Sequence] = true
		if ev.Sequence > maxSeq {
			maxSeq = ev.Sequence
		}
	}
	if want := uint64(workers * perWorker); maxSeq != want {
		t.Fatalf("maxSeq=%d, want %d", maxSeq, want)
	}
	if uint64(len(seen)) != maxSeq {
		t.Fatalf("seen %d unique sequences, want %d", len(seen), maxSeq)
	}
	// Chain must still verify.
	if err := l.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after concurrent appends: %v", err)
	}
}

// fakeBus records publish calls so we can assert ordering relative to the
// SQLite append. We can't use the real *bus.Bus here because EmitBus's
// signature is *bus.Bus; for ordering assertion we split the test into
// verifying that Append runs *before* Publish by inspecting the log state
// at publish time via a bus.Subscribe callback.
func TestEmitBus_AppendBeforePublish(t *testing.T) {
	l, _ := tempLog(t)

	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	defer b.Close()

	// Subscribe: when the event arrives, the durable row should already
	// exist. We capture a flag to assert ordering.
	rowExistsAtPublish := make(chan bool, 1)
	done := make(chan struct{})
	sub := b.Subscribe(bus.Pattern{TypePrefix: "emitbus.test"}, func(ev bus.Event) {
		// ReadFrom(0) will return at least one row iff the Append already
		// committed.
		count := 0
		for _, err := range l.ReadFrom(context.Background(), 0) {
			if err != nil {
				rowExistsAtPublish <- false
				close(done)
				return
			}
			count++
			if count > 0 {
				break
			}
		}
		rowExistsAtPublish <- count > 0
		close(done)
	})
	defer sub.Cancel()

	ev := bus.Event{
		Type:    "emitbus.test",
		Payload: json.RawMessage(`{"k":"v"}`),
	}
	if err := EmitBus(b, l, ev); err != nil {
		t.Fatalf("EmitBus: %v", err)
	}
	<-done
	if ok := <-rowExistsAtPublish; !ok {
		t.Fatalf("Publish fired before Append committed the row")
	}
}

// equal compares two string slices element-wise.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
