package manifests

import (
	"testing"
)

func TestBranchRules_Count(t *testing.T) {
	rules := BranchRules()
	got := len(rules)
	if got == 0 {
		t.Fatal("BranchRules() returned zero rules")
	}
	// 20 baseline + 3 antitrunc rules = 23
	const want = 23
	if got != want {
		t.Errorf("BranchRules() returned %d rules, want %d", got, want)
	}
}

func TestBranchRules_UniqueNames(t *testing.T) {
	rules := BranchRules()
	seen := make(map[string]int, len(rules))
	for i, r := range rules {
		name := r.Name()
		if prev, ok := seen[name]; ok {
			t.Errorf("duplicate rule name %q at index %d and %d", name, prev, i)
		}
		seen[name] = i
	}
}

func TestBranchRules_Fields(t *testing.T) {
	for _, r := range BranchRules() {
		name := r.Name()
		t.Run(name, func(t *testing.T) {
			if name == "" {
				t.Error("Name() is empty")
			}
			_ = r.Pattern()
			if r.Priority() <= 0 {
				t.Errorf("Priority() = %d, want > 0", r.Priority())
			}
			if r.Rationale() == "" {
				t.Error("Rationale() is empty")
			}
		})
	}
}
