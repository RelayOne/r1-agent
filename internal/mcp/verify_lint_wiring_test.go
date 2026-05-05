package mcp

import "testing"

func TestLintViewWithoutAPICommand_RecipeIsStable(t *testing.T) {
	got := LintViewWithoutAPICommand()
	want := []string{
		"go", "run",
		"./tools/lint-view-without-api",
		"--root", ".",
		"--json",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestLintViewWithoutAPIDescription_NonEmptyAndCitesSpec(t *testing.T) {
	if LintViewWithoutAPIDescription == "" {
		t.Fatal("LintViewWithoutAPIDescription must be non-empty")
	}
	wantSubstrs := []string{"spec 8", "lint-view-without-api"}
	for _, w := range wantSubstrs {
		if !contains(LintViewWithoutAPIDescription, w) {
			t.Errorf("description missing substring %q", w)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
