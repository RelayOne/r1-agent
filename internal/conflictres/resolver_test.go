package conflictres

import (
	"strings"
	"testing"
)

const conflictFile = `package main

import (
<<<<<<< HEAD
	"fmt"
	"os"
=======
	"fmt"
	"strings"
>>>>>>> feature
)

func main() {
<<<<<<< HEAD
	fmt.Println("hello")
=======
	fmt.Println("world")
>>>>>>> feature
}
`

func TestParseConflicts(t *testing.T) {
	conflicts := Parse(conflictFile, "main.go")
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 conflicts, got %d", len(conflicts))
	}

	if conflicts[0].Kind != KindImport {
		t.Errorf("first conflict should be import, got %s", conflicts[0].Kind)
	}
	if conflicts[1].Kind != KindSemantic {
		t.Errorf("second conflict should be semantic, got %s", conflicts[1].Kind)
	}
}

func TestAutoResolveImport(t *testing.T) {
	conflicts := Parse(conflictFile, "main.go")
	resolved := AutoResolve(conflicts)

	if !resolved[0].AutoResolved {
		t.Error("import conflict should be auto-resolved")
	}
	if resolved[0].Resolved == "" {
		t.Error("resolved content should not be empty")
	}
	// Should contain both "os" and "strings"
	if !strings.Contains(resolved[0].Resolved, `"os"`) {
		t.Error("merged imports should contain os")
	}
	if !strings.Contains(resolved[0].Resolved, `"strings"`) {
		t.Error("merged imports should contain strings")
	}
}

func TestAutoResolveWhitespace(t *testing.T) {
	conflicts := []Conflict{{
		File: "test.go",
		Ours:   "func A() {\n\treturn\n}",
		Theirs: "func A() {\n  return\n}",
	}}

	for i := range conflicts {
		conflicts[i].Kind = classify(conflicts[i])
	}

	if conflicts[0].Kind != KindWhitespace {
		t.Errorf("expected whitespace, got %s", conflicts[0].Kind)
	}

	resolved := AutoResolve(conflicts)
	if !resolved[0].AutoResolved {
		t.Error("whitespace conflict should be auto-resolved")
	}
}

func TestAutoResolveDuplicate(t *testing.T) {
	conflicts := []Conflict{{
		File:   "test.go",
		Ours:   "func New() {}",
		Theirs: "func New() {}",
	}}

	for i := range conflicts {
		conflicts[i].Kind = classify(conflicts[i])
	}

	if conflicts[0].Kind != KindDuplicate {
		t.Errorf("expected duplicate, got %s", conflicts[0].Kind)
	}

	resolved := AutoResolve(conflicts)
	if !resolved[0].AutoResolved {
		t.Error("duplicate conflict should be auto-resolved")
	}
}

func TestSemanticNotAutoResolved(t *testing.T) {
	conflicts := Parse(conflictFile, "main.go")
	resolved := AutoResolve(conflicts)

	// Second conflict (semantic) should NOT be auto-resolved
	if resolved[1].AutoResolved {
		t.Error("semantic conflict should not be auto-resolved")
	}
}

func TestResolve(t *testing.T) {
	conflicts := Parse(conflictFile, "main.go")
	conflicts = AutoResolve(conflicts)

	// Manually resolve the semantic conflict
	conflicts[1].Resolved = `	fmt.Println("hello world")`

	res := Resolve(conflictFile, conflicts)
	if res.Resolved == "" {
		t.Error("resolved content should not be empty")
	}
	if strings.Contains(res.Resolved, "<<<<<<<") {
		t.Error("resolved content should not contain conflict markers")
	}
}

func TestFormatForLLM(t *testing.T) {
	conflicts := Parse(conflictFile, "main.go")
	conflicts = AutoResolve(conflicts)

	llmPrompt := FormatForLLM(conflicts)
	if llmPrompt == "" {
		t.Error("should produce LLM prompt")
	}
	if !strings.Contains(llmPrompt, "Conflict") {
		t.Error("should contain conflict description")
	}
}

func TestFormatForLLMAllResolved(t *testing.T) {
	conflicts := []Conflict{{AutoResolved: true}}
	result := FormatForLLM(conflicts)
	if !strings.Contains(result, "auto-resolved") {
		t.Error("should indicate all resolved")
	}
}

func TestStats(t *testing.T) {
	conflicts := Parse(conflictFile, "main.go")
	conflicts = AutoResolve(conflicts)

	total, auto, review := Stats(conflicts)
	if total != 2 {
		t.Errorf("expected 2 total, got %d", total)
	}
	if auto != 1 {
		t.Errorf("expected 1 auto-resolved, got %d", auto)
	}
	if review != 1 {
		t.Errorf("expected 1 needs review, got %d", review)
	}
}

func TestClassifyTrivial(t *testing.T) {
	c := Conflict{
		Ours:   "func A() {}",
		Theirs: "func A() {}\nfunc B() {}",
	}
	kind := classify(c)
	if kind != KindTrivial {
		t.Errorf("superset should be trivial, got %s", kind)
	}
}

func TestMergeImports(t *testing.T) {
	ours := `"fmt"
	"os"`
	theirs := `"fmt"
	"strings"`

	result := mergeImports(ours, theirs)
	if !strings.Contains(result, `"fmt"`) {
		t.Error("should keep fmt")
	}
	if !strings.Contains(result, `"os"`) {
		t.Error("should keep os")
	}
	if !strings.Contains(result, `"strings"`) {
		t.Error("should keep strings")
	}
}

func TestParseWithBase(t *testing.T) {
	content := `package main
<<<<<<< HEAD
func A() {}
||||||| base
func Original() {}
=======
func B() {}
>>>>>>> feature
`
	conflicts := Parse(content, "main.go")
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Base == "" {
		t.Error("should parse base content")
	}
}

func TestEmptyFile(t *testing.T) {
	conflicts := Parse("no conflicts here", "clean.go")
	if len(conflicts) != 0 {
		t.Error("should find no conflicts")
	}
}
