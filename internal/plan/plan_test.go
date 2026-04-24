package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stoke-plan.json"), []byte(`{
		"id": "test", "description": "test plan",
		"tasks": [
			{"id": "T1", "description": "first", "files": ["a.go"], "dependencies": []},
			{"id": "T2", "description": "second", "dependencies": ["T1"]}
		]
	}`), 0o600)

	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "test" {
		t.Errorf("id=%q", p.ID)
	}
	if len(p.Tasks) != 2 {
		t.Fatalf("tasks=%d", len(p.Tasks))
	}
	if p.Tasks[0].ID != "T1" {
		t.Errorf("task0=%q", p.Tasks[0].ID)
	}
	if len(p.Tasks[1].Dependencies) != 1 || p.Tasks[1].Dependencies[0] != "T1" {
		t.Errorf("deps=%v", p.Tasks[1].Dependencies)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Error("expected error for missing plan")
	}
}

func TestSaveReload(t *testing.T) {
	dir := t.TempDir()
	orig := &Plan{ID: "rt", Description: "roundtrip", Tasks: []Task{{ID: "X", Description: "x"}}}
	if err := Save(dir, orig); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "rt" || len(loaded.Tasks) != 1 {
		t.Errorf("roundtrip failed: id=%q tasks=%d", loaded.ID, len(loaded.Tasks))
	}
}

func TestValidateGoodPlan(t *testing.T) {
	p := &Plan{
		ID: "good",
		Tasks: []Task{
			{ID: "A", Description: "first"},
			{ID: "B", Description: "depends on A", Dependencies: []string{"A"}},
		},
	}
	errs := p.Validate()
	if len(errs) != 0 {
		t.Errorf("valid plan should have no errors: %v", errs)
	}
}

func TestValidateDuplicateID(t *testing.T) {
	p := &Plan{
		ID:    "dup",
		Tasks: []Task{{ID: "A", Description: "first"}, {ID: "A", Description: "second"}},
	}
	errs := p.Validate()
	found := false
	for _, e := range errs {
		if contains(e, "duplicate") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected duplicate ID error: %v", errs)
	}
}

func TestValidateMissingDep(t *testing.T) {
	p := &Plan{
		ID:    "missing",
		Tasks: []Task{{ID: "A", Description: "first", Dependencies: []string{"GHOST"}}},
	}
	errs := p.Validate()
	found := false
	for _, e := range errs {
		if contains(e, "unknown task GHOST") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing dep error: %v", errs)
	}
}

func TestValidateCycle(t *testing.T) {
	p := &Plan{
		ID: "cycle",
		Tasks: []Task{
			{ID: "A", Description: "a", Dependencies: []string{"B"}},
			{ID: "B", Description: "b", Dependencies: []string{"A"}},
		},
	}
	errs := p.Validate()
	found := false
	for _, e := range errs {
		if contains(e, "cycle") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cycle error: %v", errs)
	}
}

func TestValidateEmptyPlan(t *testing.T) {
	p := &Plan{ID: "empty"}
	errs := p.Validate()
	if len(errs) == 0 {
		t.Error("empty plan should have errors")
	}
}

func TestAutoInferDependencies(t *testing.T) {
	p := &Plan{
		ID: "auto-deps",
		Tasks: []Task{
			{ID: "T1", Description: "first", Files: []string{"pkg/handler.go", "pkg/model.go"}},
			{ID: "T2", Description: "second", Files: []string{"pkg/handler.go"}},
			{ID: "T3", Description: "third", Files: []string{"other/file.go"}},
		},
	}

	added := p.AutoInferDependencies()

	// T2 shares pkg/handler.go with T1 (which comes first), so T2 should depend on T1
	if added != 1 {
		t.Errorf("AutoInferDependencies added %d deps, want 1", added)
	}

	// Check T2 now depends on T1
	foundDep := false
	for _, dep := range p.Tasks[1].Dependencies {
		if dep == "T1" {
			foundDep = true
		}
	}
	if !foundDep {
		t.Errorf("T2 should depend on T1 after auto-infer, deps: %v", p.Tasks[1].Dependencies)
	}

	// T3 has no shared files, should have no deps
	if len(p.Tasks[2].Dependencies) != 0 {
		t.Errorf("T3 should have no deps, got: %v", p.Tasks[2].Dependencies)
	}
}

func TestAutoInferDependencies_NoDoubles(t *testing.T) {
	p := &Plan{
		ID: "no-doubles",
		Tasks: []Task{
			{ID: "T1", Description: "first", Files: []string{"a.go"}},
			{ID: "T2", Description: "second", Files: []string{"a.go"}, Dependencies: []string{"T1"}},
		},
	}

	added := p.AutoInferDependencies()

	// T2 already depends on T1 explicitly, should not add a duplicate
	if added != 0 {
		t.Errorf("should not add duplicate deps, added %d", added)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
