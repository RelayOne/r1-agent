package plan

import (
	"strings"
	"testing"
)

func TestExtractDeliverables_ComponentList(t *testing.T) {
	// Exact shape from run 40's T16 failure.
	text := "Scaffold packages/ui-web with shadcn/ui components (data table, date picker, multi-select, modal) copied into this repo."
	got := ExtractDeliverables(text)
	want := []string{"data table", "date picker", "modal", "multi-select"}
	if len(got) != len(want) {
		t.Fatalf("got %d deliverables (%+v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, d := range got {
		if d.Name != want[i] {
			t.Errorf("[%d] name=%q want %q", i, d.Name, want[i])
		}
		if d.Kind != KindComponent {
			t.Errorf("[%d] kind=%q want component", i, d.Kind)
		}
	}
}

func TestExtractDeliverables_IncludingList(t *testing.T) {
	text := "The API must support standard HTTP methods including get, post, put, and delete endpoints."
	got := ExtractDeliverables(text)
	if len(got) < 3 {
		t.Fatalf("expected at least 3 items from 'including' pattern; got %v", got)
	}
}

func TestExtractDeliverables_ImplementVerb(t *testing.T) {
	text := "Implement login, logout, and refresh handlers for session management."
	got := ExtractDeliverables(text)
	if len(got) != 3 {
		t.Errorf("expected 3 items from 'implement X, Y, and Z'; got %d (%+v)", len(got), got)
	}
}

func TestExtractDeliverables_SemicolonList(t *testing.T) {
	// Semicolons not handled — doc the behavior.
	text := "Ship A; B; C"
	got := ExtractDeliverables(text)
	// Not expected to catch this shape — conservative by design.
	_ = got
}

func TestExtractDeliverables_EmptyInput(t *testing.T) {
	if got := ExtractDeliverables(""); len(got) != 0 {
		t.Errorf("empty input should produce nothing, got %v", got)
	}
	if got := ExtractDeliverables("   \n   "); len(got) != 0 {
		t.Errorf("whitespace input should produce nothing, got %v", got)
	}
}

func TestExtractDeliverables_NoisyRejected(t *testing.T) {
	// Generic nouns shouldn't become deliverables.
	text := "implement errors, logic, and state management"
	got := ExtractDeliverables(text)
	for _, d := range got {
		low := strings.ToLower(d.Name)
		if low == "errors" || low == "logic" || low == "state management" {
			t.Errorf("generic noun %q leaked through", d.Name)
		}
	}
}

func TestExtractDeliverables_Deduplicates(t *testing.T) {
	// Same item mentioned in two patterns → one deliverable.
	text := "Components (data table, date picker). Also implement data table, multi-select"
	got := ExtractDeliverables(text)
	seen := map[string]bool{}
	for _, d := range got {
		low := strings.ToLower(d.Name)
		if seen[low] {
			t.Errorf("duplicate: %q", d.Name)
		}
		seen[low] = true
	}
}

func TestExtractDeliverables_DeterministicOrder(t *testing.T) {
	text := "Components (data table, date picker, multi-select, modal)"
	a := ExtractDeliverables(text)
	b := ExtractDeliverables(text)
	if len(a) != len(b) {
		t.Fatal("non-deterministic count")
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			t.Errorf("[%d] order differs: %q vs %q", i, a[i].Name, b[i].Name)
		}
	}
}

func TestRenderChecklist_Empty(t *testing.T) {
	if s := RenderChecklist(nil); s != "" {
		t.Errorf("empty deliverables should render empty, got %q", s)
	}
}

func TestRenderChecklist_HasAntiBarrelGuidance(t *testing.T) {
	ds := []Deliverable{
		{Name: "data table", Kind: KindComponent},
		{Name: "date picker", Kind: KindComponent},
	}
	out := RenderChecklist(ds)
	for _, want := range []string{
		"MANDATORY DELIVERABLES",
		"data table",
		"date picker",
		"[component]",
		"NOT a single barrel file",
		"does NOT satisfy",
		"does not satisfy", // normalized to catch variants
	} {
		// Case-insensitive substring check so trivial
		// wording tweaks don't break this.
		if !strings.Contains(strings.ToUpper(out), strings.ToUpper(want)) {
			t.Errorf("checklist missing %q\n---\n%s", want, out)
		}
	}
}

func TestIsValidDeliverable(t *testing.T) {
	valid := []string{"data table", "auth handler", "date picker", "modal"}
	invalid := []string{"", "x", "etc", "more", "various", strings.Repeat("a", 100)}
	for _, s := range valid {
		if !isValidDeliverable(s) {
			t.Errorf("valid rejected: %q", s)
		}
	}
	for _, s := range invalid {
		if isValidDeliverable(s) {
			t.Errorf("invalid accepted: %q", s)
		}
	}
}

func TestSplitList_HandlesAndConjunction(t *testing.T) {
	got := splitList("a, b, c, and d")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("[%d] %q want %q", i, g, want[i])
		}
	}
}

func TestFileMinBytes_Reasonable(t *testing.T) {
	if FileMinBytes < 100 || FileMinBytes > 1024 {
		t.Errorf("FileMinBytes=%d seems off; expected somewhere between 100 and 1024", FileMinBytes)
	}
}
