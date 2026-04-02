package model

import "testing"

func TestInferTaskType(t *testing.T) {
	tests := map[string]TaskType{
		"design the service architecture": TaskTypeArchitecture,
		"fix docker deployment":           TaskTypeDevOps,
		"debug a race condition":          TaskTypeConcurrency,
		"update the README":               TaskTypeDocs,
		"tighten auth checks":             TaskTypeSecurity,
		"remove type errors":              TaskTypeTypeSafety,
		"review this patch":               TaskTypeReview,
		"refactor the handler":            TaskTypeRefactor,
	}
	for in, want := range tests {
		if got := InferTaskType(in); got != want {
			t.Fatalf("InferTaskType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePrimaryAvailable(t *testing.T) {
	p := Resolve(TaskTypeRefactor, func(p Provider) bool { return true })
	if p != ProviderClaude {
		t.Errorf("refactor primary should be claude, got %s", p)
	}
}

func TestResolveFallbackWhenPrimaryUnavailable(t *testing.T) {
	p := Resolve(TaskTypeRefactor, func(p Provider) bool { return p != ProviderClaude })
	if p != ProviderCodex {
		t.Errorf("should fall back to codex, got %s", p)
	}
}

func TestResolveAllUnavailableFallsToLintOnly(t *testing.T) {
	p := Resolve(TaskTypeRefactor, func(p Provider) bool { return false })
	if p != ProviderLintOnly {
		t.Errorf("should fall back to lint-only, got %s", p)
	}
}

func TestResolveLintOnlyInChain(t *testing.T) {
	// Docs and Review have lint-only in their fallback chain
	p := Resolve(TaskTypeDocs, func(p Provider) bool { return false })
	if p != ProviderLintOnly {
		t.Errorf("docs should have lint-only fallback, got %s", p)
	}
}

func TestCrossModelReviewer(t *testing.T) {
	if CrossModelReviewer(ProviderClaude) != ProviderCodex {
		t.Error("claude execute -> codex review")
	}
	if CrossModelReviewer(ProviderCodex) != ProviderClaude {
		t.Error("codex execute -> claude review")
	}
}

func TestAllRoutesHaveFallbackChain(t *testing.T) {
	for tt, route := range Routes {
		if len(route.FallbackChain) == 0 {
			t.Errorf("route %s has no fallback chain", tt)
		}
		if route.Primary == "" {
			t.Errorf("route %s has no primary", tt)
		}
	}
}
