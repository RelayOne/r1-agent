package plan

import (
	"testing"
)

func TestDecomposeNumberedList(t *testing.T) {
	task := Task{
		ID: "t1",
		Description: `Implement the auth system:
1. Add JWT token generation
2. Add token validation middleware
3. Add refresh token endpoint`,
	}

	result := Decompose(task)
	if result.Strategy != "numbered" {
		t.Errorf("expected numbered, got %s", result.Strategy)
	}
	if len(result.Subtasks) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(result.Subtasks))
	}
	if result.Subtasks[0].Description != "Add JWT token generation" {
		t.Errorf("unexpected first subtask: %q", result.Subtasks[0].Description)
	}
	// Numbered lists should have dependency chain
	if len(result.Subtasks[1].Dependencies) != 1 {
		t.Error("second subtask should depend on first")
	}
}

func TestDecomposeBulletList(t *testing.T) {
	task := Task{
		ID: "t1",
		Description: `Fix the following issues:
- Fix the login timeout
- Fix the session expiry
- Fix the logout redirect`,
	}

	result := Decompose(task)
	if result.Strategy != "bullets" {
		t.Errorf("expected bullets, got %s", result.Strategy)
	}
	if len(result.Subtasks) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(result.Subtasks))
	}
}

func TestDecomposeConjunctions(t *testing.T) {
	task := Task{
		ID:          "t1",
		Description: "add input validation and add rate limiting and add error handling",
	}

	result := Decompose(task)
	if result.Strategy != "conjunction" {
		t.Errorf("expected conjunction, got %s", result.Strategy)
	}
	if len(result.Subtasks) < 3 {
		t.Errorf("expected at least 3 subtasks, got %d", len(result.Subtasks))
	}
}

func TestDecomposeSemicolons(t *testing.T) {
	task := Task{
		ID:          "t1",
		Description: "fix the login bug; add error logging; update the docs",
	}

	result := Decompose(task)
	if result.Strategy != "semicolons" {
		t.Errorf("expected semicolons, got %s", result.Strategy)
	}
	if len(result.Subtasks) != 3 {
		t.Fatalf("expected 3 subtasks, got %d", len(result.Subtasks))
	}
}

func TestDecomposeAtomic(t *testing.T) {
	task := Task{
		ID:          "t1",
		Description: "add JWT validation to the login endpoint",
	}

	result := Decompose(task)
	if result.Strategy != "none" {
		t.Errorf("expected none for atomic task, got %s", result.Strategy)
	}
	if len(result.Subtasks) != 0 {
		t.Errorf("expected 0 subtasks, got %d", len(result.Subtasks))
	}
}

func TestDecomposeWithAspectsAtomic(t *testing.T) {
	task := Task{
		ID:          "t1",
		Description: "add caching to the API",
	}

	result := DecomposeWithAspects(task, true, true)
	if result.Strategy != "aspect" {
		t.Errorf("expected aspect, got %s", result.Strategy)
	}
	if len(result.Subtasks) != 3 { // impl + test + docs
		t.Fatalf("expected 3 aspect subtasks, got %d", len(result.Subtasks))
	}
	if result.Subtasks[0].ID != "t1-impl" {
		t.Errorf("expected impl subtask, got %s", result.Subtasks[0].ID)
	}
	if result.Subtasks[1].ID != "t1-test" {
		t.Errorf("expected test subtask, got %s", result.Subtasks[1].ID)
	}
	if result.Subtasks[2].ID != "t1-docs" {
		t.Errorf("expected docs subtask, got %s", result.Subtasks[2].ID)
	}
	// Test should depend on impl
	if len(result.Subtasks[1].Dependencies) != 1 || result.Subtasks[1].Dependencies[0] != "t1-impl" {
		t.Error("test subtask should depend on impl")
	}
}

func TestDecomposeWithAspectsCompound(t *testing.T) {
	task := Task{
		ID:          "t1",
		Description: "fix login; fix logout",
	}

	result := DecomposeWithAspects(task, true, false)
	// 2 subtasks + 2 test subtasks = 4
	if len(result.Subtasks) != 4 {
		t.Fatalf("expected 4 subtasks (2 impl + 2 test), got %d", len(result.Subtasks))
	}
}

func TestIsCompound(t *testing.T) {
	tests := []struct {
		desc string
		want bool
	}{
		{"fix the bug", false},
		{"fix the bug; add logging", true},
		{"1. do A\n2. do B", true},
		{"add X and add Y and add Z", true},
	}
	for _, tc := range tests {
		got := IsCompound(tc.desc)
		if got != tc.want {
			t.Errorf("IsCompound(%q) = %v, want %v", tc.desc, got, tc.want)
		}
	}
}

func TestEstimateComplexity(t *testing.T) {
	if EstimateComplexity("simple task") != 1 {
		t.Error("expected complexity 1 for simple task")
	}
	if EstimateComplexity("do A; do B; do C") != 3 {
		t.Error("expected complexity 3 for 3-part task")
	}
}

func TestSubtaskIDs(t *testing.T) {
	task := Task{ID: "auth", Description: "fix A; fix B"}
	result := Decompose(task)
	if result.Subtasks[0].ID != "auth-1" {
		t.Errorf("expected auth-1, got %s", result.Subtasks[0].ID)
	}
	if result.Subtasks[1].ID != "auth-2" {
		t.Errorf("expected auth-2, got %s", result.Subtasks[1].ID)
	}
}

func TestSingleConjunctionNotSplit(t *testing.T) {
	// "X and Y" with only one "and" should NOT be split
	result := Decompose(Task{ID: "t1", Description: "add login and logout"})
	if result.Strategy != "none" {
		t.Errorf("single conjunction should not split, got %s", result.Strategy)
	}
}
