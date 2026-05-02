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
