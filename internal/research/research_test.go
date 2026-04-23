package research

import (
	"reflect"
	"strings"
	"testing"
)

func TestHeuristicDecomposer_Single(t *testing.T) {
	d := HeuristicDecomposer{}
	got := d.Decompose("How does tree-sitter work?")
	if len(got) != 1 {
		t.Fatalf("want 1 sub-question, got %d: %#v", len(got), got)
	}
	if got[0].ID != "SQ-1" {
		t.Errorf("want ID SQ-1, got %q", got[0].ID)
	}
	if !strings.Contains(got[0].Text, "tree-sitter") {
		t.Errorf("want sub-question to reference tree-sitter, got %q", got[0].Text)
	}
}

func TestHeuristicDecomposer_Versus(t *testing.T) {
	d := HeuristicDecomposer{}
	got := d.Decompose("Postgres vs MySQL")
	if len(got) != 2 {
		t.Fatalf("want 2 sub-questions, got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "Postgres") {
		t.Errorf("first sub-question should mention Postgres, got %q", got[0].Text)
	}
	if !strings.Contains(got[1].Text, "MySQL") {
		t.Errorf("second sub-question should mention MySQL, got %q", got[1].Text)
	}
	// IDs must be stable and ordered
	if got[0].ID != "SQ-1" || got[1].ID != "SQ-2" {
		t.Errorf("want SQ-1/SQ-2 IDs, got %q/%q", got[0].ID, got[1].ID)
	}
}

func TestHeuristicDecomposer_VersusAbbrev(t *testing.T) {
	d := HeuristicDecomposer{}
	got := d.Decompose("Go vs. Rust")
	if len(got) != 2 {
		t.Fatalf("want 2 sub-questions, got %d", len(got))
	}
}

func TestHeuristicDecomposer_CommaList(t *testing.T) {
	d := HeuristicDecomposer{}
	got := d.Decompose("Compare Redis, Memcached, and Valkey")
	if len(got) < 2 {
		t.Fatalf("want at least 2 sub-questions from comma list, got %d: %#v", len(got), got)
	}
	// The "and" before the last item should not leave "and" in the text.
	for _, sq := range got {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(sq.Text, "What is ")), "and ") {
			t.Errorf("sub-question %q still has a leading 'and'", sq.Text)
		}
	}
}

func TestHeuristicDecomposer_AndConjunction(t *testing.T) {
	d := HeuristicDecomposer{}
	got := d.Decompose("GraphQL and gRPC")
	if len(got) != 2 {
		t.Fatalf("want 2 sub-questions for A-and-B, got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "GraphQL") || !strings.Contains(got[1].Text, "gRPC") {
		t.Errorf("sub-questions should reference each operand, got %q / %q", got[0].Text, got[1].Text)
	}
}

func TestHeuristicDecomposer_Empty(t *testing.T) {
	d := HeuristicDecomposer{}
	if got := d.Decompose(""); got != nil {
		t.Errorf("empty input should return nil, got %#v", got)
	}
}

func TestPlanner_FallbackToSingle(t *testing.T) {
	// Decomposer returns nothing → Plan must fall back to a single
	// sub-question equal to the input.
	p := &Planner{Decomposer: zeroDecomposer{}}
	got := p.Plan("something")
	if !reflect.DeepEqual(got, []SubQuestion{{ID: "SQ-1", Text: "something"}}) {
		t.Errorf("fallback mismatch: %#v", got)
	}
}

func TestPlanner_DefaultDecomposerWhenNil(t *testing.T) {
	p := &Planner{Decomposer: nil}
	got := p.Plan("Docker vs Podman")
	if len(got) != 2 {
		t.Errorf("want 2 sub-questions via default decomposer, got %d", len(got))
	}
}

type zeroDecomposer struct{}

func (zeroDecomposer) Decompose(string) []SubQuestion { return nil }
