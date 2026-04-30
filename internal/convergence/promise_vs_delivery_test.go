package convergence

import (
	"strings"
	"testing"
)

func TestPromiseCheckerCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		spec          string
		diff          string
		wantPromises  int
		wantSatisfied []bool
		wantMissing   []string
	}{
		{
			name: "satisfied and unsatisfied promises are both reported",
			spec: "Build a memory drift validator for session start. Add branch checks to catch deleted branches.",
			diff: strings.Join([]string{
				"diff --git a/internal/memory/reconciler.go b/internal/memory/reconciler.go",
				"+type Reconciler struct{}",
				"+// memory drift validator runs at session start",
				"+func (r Reconciler) Reconcile() {}",
				"+// branch checks catch deleted branches",
				"",
			}, "\n"),
			wantPromises:  2,
			wantSatisfied: []bool{true, true},
		},
		{
			name: "missing evidence leaves promise unsatisfied",
			spec: "Implement a doc drift checker for canonical docs.",
			diff: strings.Join([]string{
				"diff --git a/internal/verify/pipeline.go b/internal/verify/pipeline.go",
				"+func VerificationSummary() string { return \"ok\" }",
				"",
			}, "\n"),
			wantPromises:  1,
			wantSatisfied: []bool{false},
			wantMissing:   []string{"doc", "drift"},
		},
		{
			name:         "non-promise text is ignored",
			spec:         "This section describes the rollout context only.",
			diff:         "",
			wantPromises: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			checker := PromiseChecker{Spec: tc.spec, Diff: tc.diff}
			promises, err := checker.Check()
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if len(promises) != tc.wantPromises {
				t.Fatalf("len(promises) = %d, want %d", len(promises), tc.wantPromises)
			}
			for i, wantSatisfied := range tc.wantSatisfied {
				if promises[i].Satisfied != wantSatisfied {
					t.Fatalf("promises[%d].Satisfied = %v, want %v", i, promises[i].Satisfied, wantSatisfied)
				}
			}
			for _, wantMissing := range tc.wantMissing {
				if !containsString(promises[0].Missing, wantMissing) {
					t.Fatalf("expected missing keyword %q, got %v", wantMissing, promises[0].Missing)
				}
			}
		})
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
