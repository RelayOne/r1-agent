package manifests

import (
	"testing"
)

func TestMissionRules_Count(t *testing.T) {
	rules := MissionRules()
	got := len(rules)
	if got == 0 {
		t.Fatal("MissionRules() returned zero rules")
	}
	// Snapshot the count so regressions are caught.
	const want = 24
	if got != want {
		t.Errorf("MissionRules() returned %d rules, want %d", got, want)
	}
}

func TestMissionRules_UniqueNames(t *testing.T) {
	rules := MissionRules()
	seen := make(map[string]int, len(rules))
	for i, r := range rules {
		name := r.Name()
		if prev, ok := seen[name]; ok {
			t.Errorf("duplicate rule name %q at index %d and %d", name, prev, i)
		}
		seen[name] = i
	}
}

func TestMissionRules_Fields(t *testing.T) {
	for _, r := range MissionRules() {
		name := r.Name()
		t.Run(name, func(t *testing.T) {
			if name == "" {
				t.Error("Name() is empty")
			}
			// Pattern() may legitimately be zero-value for timer/poll rules.
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
