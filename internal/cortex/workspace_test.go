package cortex

import (
	"strings"
	"testing"
	"time"
)

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
