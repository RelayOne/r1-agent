package cortex

import (
	"strings"
	"testing"
	"time"
)

func validPublishNote() Note {
	return Note{
		LobeID:   "memory-recall",
		Severity: SevInfo,
		Title:    "hello",
		Body:     "body",
	}
}

func TestPublish(t *testing.T) {
	w := NewWorkspace(nil, nil)
	n := validPublishNote()

	if err := w.Publish(n); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	if got := len(w.notes); got != 1 {
		t.Fatalf("len(notes)=%d, want 1", got)
	}
	if got := w.seq; got != 1 {
		t.Fatalf("seq=%d, want 1", got)
	}
	stored := w.notes[0]
	if stored.ID != "note-0" {
		t.Fatalf("ID=%q, want %q", stored.ID, "note-0")
	}
	if stored.EmittedAt.IsZero() {
		t.Fatalf("EmittedAt is zero")
	}
	if stored.Round != 0 {
		t.Fatalf("Round=%d, want 0", stored.Round)
	}
}

func TestPublishValidates(t *testing.T) {
	w := NewWorkspace(nil, nil)
	bad := validPublishNote()
	bad.LobeID = "" // invalid

	err := w.Publish(bad)
	if err == nil {
		t.Fatalf("expected error from invalid Note, got nil")
	}

	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.seq != 0 {
		t.Fatalf("seq=%d after rejected Publish, want 0", w.seq)
	}
	if len(w.notes) != 0 {
		t.Fatalf("len(notes)=%d after rejected Publish, want 0", len(w.notes))
	}
}

func TestPublishConcurrent(t *testing.T) {
	const N = 100
	w := NewWorkspace(nil, nil)

	done := make(chan struct{}, N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if err := w.Publish(validPublishNote()); err != nil {
				errs <- err
			}
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}
	if got := len(errs); got != 0 {
		close(errs)
		for err := range errs {
			t.Errorf("Publish: %v", err)
		}
		t.Fatalf("got %d Publish errors, want 0", got)
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	if got := len(w.notes); got != N {
		t.Fatalf("len(notes)=%d, want %d", got, N)
	}
	if got := w.seq; got != N {
		t.Fatalf("seq=%d, want %d", got, N)
	}

	seen := make(map[string]bool, N)
	for _, n := range w.notes {
		if n.ID == "" {
			t.Fatalf("empty ID in notes")
		}
		if seen[n.ID] {
			t.Fatalf("duplicate ID %q", n.ID)
		}
		seen[n.ID] = true
	}
	if len(seen) != N {
		t.Fatalf("unique IDs=%d, want %d", len(seen), N)
	}
}

func TestSnapshotDeepCopy(t *testing.T) {
	w := NewWorkspace(nil, nil)

	n1 := validPublishNote()
	n1.Title = "first"
	if err := w.Publish(n1); err != nil {
		t.Fatalf("Publish n1: %v", err)
	}
	n2 := validPublishNote()
	n2.Title = "second"
	if err := w.Publish(n2); err != nil {
		t.Fatalf("Publish n2: %v", err)
	}

	out := w.Snapshot()
	if got := len(out); got != 2 {
		t.Fatalf("len(Snapshot)=%d, want 2", got)
	}
	if out[0].Title != "first" {
		t.Fatalf("out[0].Title=%q, want %q", out[0].Title, "first")
	}

	// Mutate the returned slice; assert internal state untouched.
	out[0].Title = "MUTATED"
	out = append(out, validPublishNote())

	out2 := w.Snapshot()
	if got := len(out2); got != 2 {
		t.Fatalf("second Snapshot len=%d, want 2 (append must not leak)", got)
	}
	if out2[0].Title != "first" {
		t.Fatalf("out2[0].Title=%q, want %q (mutation leaked back)", out2[0].Title, "first")
	}
	if out2[1].Title != "second" {
		t.Fatalf("out2[1].Title=%q, want %q", out2[1].Title, "second")
	}
}

func TestUnresolvedCriticalFilter(t *testing.T) {
	w := NewWorkspace(nil, nil)

	c1 := validPublishNote()
	c1.Severity = SevCritical
	c1.Title = "the sky is falling"
	if err := w.Publish(c1); err != nil {
		t.Fatalf("Publish c1: %v", err)
	}

	// Capture the assigned ID via Snapshot.
	snap := w.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 Note after publishing critical, got %d", len(snap))
	}
	c1ID := snap[0].ID

	got := w.UnresolvedCritical()
	if len(got) != 1 {
		t.Fatalf("UnresolvedCritical len=%d, want 1", len(got))
	}
	if got[0].ID != c1ID {
		t.Fatalf("UnresolvedCritical[0].ID=%q, want %q", got[0].ID, c1ID)
	}

	// Publish a resolving Note targeting c1.
	resolver := validPublishNote()
	resolver.Title = "fixed it"
	resolver.Resolves = c1ID
	if err := w.Publish(resolver); err != nil {
		t.Fatalf("Publish resolver: %v", err)
	}

	got2 := w.UnresolvedCritical()
	if len(got2) != 0 {
		t.Fatalf("UnresolvedCritical len=%d after resolution, want 0", len(got2))
	}
}

func TestDrainAdvancesCursor(t *testing.T) {
	w := NewWorkspace(nil, nil)

	// Round 0 Note (will not be drained at sinceRound=1).
	n0 := validPublishNote()
	n0.Title = "round-0 note"
	if err := w.Publish(n0); err != nil {
		t.Fatalf("Publish n0: %v", err)
	}

	// Advance to round 1 and publish.
	w.mu.Lock()
	w.currentRound = 1
	w.mu.Unlock()
	n1 := validPublishNote()
	n1.Title = "round-1 note"
	if err := w.Publish(n1); err != nil {
		t.Fatalf("Publish n1: %v", err)
	}

	// Advance to round 2 and publish.
	w.mu.Lock()
	w.currentRound = 2
	w.mu.Unlock()
	n2 := validPublishNote()
	n2.Title = "round-2 note"
	if err := w.Publish(n2); err != nil {
		t.Fatalf("Publish n2: %v", err)
	}

	drained, cursor := w.Drain(1)
	if len(drained) != 2 {
		t.Fatalf("Drain(1) len=%d, want 2", len(drained))
	}
	for _, n := range drained {
		if n.Round < 1 {
			t.Fatalf("Drain(1) returned Note with Round=%d, want >=1", n.Round)
		}
	}
	if cursor != 2 {
		t.Fatalf("Drain(1) cursor=%d, want 2", cursor)
	}

	// drainedUpTo must have advanced internally.
	w.mu.RLock()
	if w.drainedUpTo != 2 {
		w.mu.RUnlock()
		t.Fatalf("w.drainedUpTo=%d after Drain(1), want 2", w.drainedUpTo)
	}
	w.mu.RUnlock()

	// Calling Drain with a smaller sinceRound must NOT regress drainedUpTo.
	_, cursor2 := w.Drain(0)
	if cursor2 != 2 {
		t.Fatalf("Drain(0) cursor=%d, want 2 (must not regress)", cursor2)
	}
}

func TestNoteValidate(t *testing.T) {
	validNote := func() Note {
		return Note{
			ID:        "01H000000000000000000000",
			LobeID:    "memory-recall",
			Severity:  SevInfo,
			Title:     "all good",
			Body:      "body text",
			Tags:      []string{"a", "b"},
			EmittedAt: time.Unix(0, 0).UTC(),
			Round:     1,
		}
	}

	cases := []struct {
		name      string
		mutate    func(*Note)
		wantErr   bool
		substring string
	}{
		{
			name:      "empty LobeID",
			mutate:    func(n *Note) { n.LobeID = "" },
			wantErr:   true,
			substring: "empty LobeID",
		},
		{
			name:      "empty Title",
			mutate:    func(n *Note) { n.Title = "" },
			wantErr:   true,
			substring: "empty Title",
		},
		{
			name:      "title exceeds 80 runes",
			mutate:    func(n *Note) { n.Title = strings.Repeat("a", 81) },
			wantErr:   true,
			substring: "Title >80 runes",
		},
		{
			name:      "unknown Severity",
			mutate:    func(n *Note) { n.Severity = Severity("xyz") },
			wantErr:   true,
			substring: "unknown Severity",
		},
		{
			name:    "happy path",
			mutate:  func(n *Note) {},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			n := validNote()
			tc.mutate(&n)
			err := n.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.substring) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.substring)
				}
			} else {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			}
		})
	}
}
