package manifests

import (
	"testing"
)

func TestSDMRules_Count(t *testing.T) {
	rules := SDMRules()
	got := len(rules)
	if got == 0 {
		t.Fatal("SDMRules() returned zero rules")
	}
	const want = 5
	if got != want {
		t.Errorf("SDMRules() returned %d rules, want %d", got, want)
	}
}

func TestSDMRules_UniqueNames(t *testing.T) {
	rules := SDMRules()
	seen := make(map[string]int, len(rules))
	for i, r := range rules {
		name := r.Name()
		if prev, ok := seen[name]; ok {
			t.Errorf("duplicate rule name %q at index %d and %d", name, prev, i)
		}
		seen[name] = i
	}
}

func TestSDMRules_Fields(t *testing.T) {
	for _, r := range SDMRules() {
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
