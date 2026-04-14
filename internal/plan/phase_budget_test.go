package plan

import (
	"strings"
	"testing"
)

func TestReviewBudgetDefaults(t *testing.T) {
	b := ReviewBudget{}.WithDefaults()
	if b.MaxDepth != 3 || b.MaxTotalDispatches != 12 || b.MaxDecompBreadth != 10 {
		t.Fatalf("unexpected defaults: %+v", b)
	}
	override := ReviewBudget{MaxDepth: 5, MaxTotalDispatches: 0, MaxDecompBreadth: -1}.WithDefaults()
	if override.MaxDepth != 5 {
		t.Fatalf("explicit MaxDepth should be preserved; got %d", override.MaxDepth)
	}
	if override.MaxTotalDispatches != 12 || override.MaxDecompBreadth != 10 {
		t.Fatalf("zero / negative should fall back: %+v", override)
	}
}

func TestValidateDecomposeVerdictAbandon(t *testing.T) {
	good := &DecomposeVerdict{Abandon: true, AbandonReason: "structurally unfixable"}
	if errs := ValidateDecomposeVerdict(good, 10); len(errs) != 0 {
		t.Fatalf("abandon with reason should validate: %v", errs)
	}
	bad := &DecomposeVerdict{Abandon: true, AbandonReason: "   "}
	errs := ValidateDecomposeVerdict(bad, 10)
	if len(errs) == 0 {
		t.Fatal("abandon with empty reason must be flagged")
	}
}

func TestValidateDecomposeVerdictSubDirectives(t *testing.T) {
	empty := &DecomposeVerdict{Abandon: false, SubDirectives: nil}
	if errs := ValidateDecomposeVerdict(empty, 10); len(errs) == 0 {
		t.Fatal("empty SubDirectives with Abandon=false must be flagged")
	}
	tooShort := &DecomposeVerdict{SubDirectives: []string{"fix it", "ok"}}
	errs := ValidateDecomposeVerdict(tooShort, 10)
	if len(errs) < 2 {
		t.Fatalf("two too-short directives should produce two errors; got %v", errs)
	}
	tooMany := &DecomposeVerdict{SubDirectives: make([]string, 13)}
	for i := range tooMany.SubDirectives {
		tooMany.SubDirectives[i] = strings.Repeat("x", 40)
	}
	errs = ValidateDecomposeVerdict(tooMany, 10)
	foundBreadth := false
	for _, e := range errs {
		if strings.Contains(e, "MaxDecompBreadth") {
			foundBreadth = true
		}
	}
	if !foundBreadth {
		t.Fatalf("13 subs with cap=10 must flag MaxDecompBreadth; got %v", errs)
	}
}

func TestValidateDecomposeVerdictNil(t *testing.T) {
	errs := ValidateDecomposeVerdict(nil, 10)
	if len(errs) == 0 {
		t.Fatal("nil verdict must be flagged")
	}
}

func TestTruncateSubDirectivesKeepsTopN(t *testing.T) {
	v := &DecomposeVerdict{
		SubDirectives: []string{"one", "two", "three", "four", "five"},
	}
	out := TruncateSubDirectives(v, 3)
	if len(out.SubDirectives) != 3 {
		t.Fatalf("expected 3, got %d", len(out.SubDirectives))
	}
	if out.SubDirectives[0] != "one" || out.SubDirectives[2] != "three" {
		t.Fatalf("truncation should keep first-N: %+v", out.SubDirectives)
	}
	// Original should not be mutated.
	if len(v.SubDirectives) != 5 {
		t.Fatal("truncate must not mutate input")
	}
}

func TestTruncateNoOpWhenUnderCap(t *testing.T) {
	v := &DecomposeVerdict{SubDirectives: []string{"a", "b"}}
	out := TruncateSubDirectives(v, 10)
	if out != v {
		t.Fatal("under-cap input should return original pointer")
	}
}

func TestValidateTaskWorkVerdict(t *testing.T) {
	good := &TaskWorkVerdict{Complete: true, Reasoning: "all expected files present"}
	if errs := ValidateTaskWorkVerdict(good); len(errs) != 0 {
		t.Fatalf("complete with reasoning should validate: %v", errs)
	}
	badIncomplete := &TaskWorkVerdict{Complete: false, Reasoning: "gaps remain"}
	errs := ValidateTaskWorkVerdict(badIncomplete)
	if len(errs) == 0 {
		t.Fatal("incomplete with no directive and no gaps must be flagged")
	}
	missingReasoning := &TaskWorkVerdict{Complete: true, Reasoning: ""}
	errs = ValidateTaskWorkVerdict(missingReasoning)
	foundReasoning := false
	for _, e := range errs {
		if strings.Contains(e, "Reasoning") {
			foundReasoning = true
		}
	}
	if !foundReasoning {
		t.Fatalf("missing reasoning must be flagged: %v", errs)
	}
}
