package semdiff

import (
	"testing"
)

// TestAnalyzeSameNameDifferentReceivers proves that a change to one of two
// same-named methods on different receiver types is NOT silently dropped.
// Before the qualified-identity fix, both methods keyed to the bare name
// "Do", so one clobbered the other in the symbol map and its change was lost.
func TestAnalyzeSameNameDifferentReceivers(t *testing.T) {
	// A.Do is declared FIRST, so under bare-name keying B.Do (declared last)
	// wins the "Do" map slot and A.Do's change is silently dropped. We change
	// A.Do (the non-survivor) precisely to expose that masking.
	old := "package main\n\n" +
		"type A struct{}\n" +
		"type B struct{}\n" +
		"func (a *A) Do() int { return 1 }\n" +
		"func (b *B) Do() int { return 2 }\n"
	new := "package main\n\n" +
		"type A struct{}\n" +
		"type B struct{}\n" +
		"func (a *A) Do() string { return \"one\" }\n" +
		"func (b *B) Do() int { return 2 }\n"

	a := Analyze(old, new, "main.go")

	var sigChanges []SymbolChange
	for _, c := range a.Changes {
		if c.Name == "Do" && (c.Kind == KindSignature || c.Kind == KindModified) {
			sigChanges = append(sigChanges, c)
		}
	}
	if len(sigChanges) != 1 {
		t.Fatalf("expected exactly 1 change to a Do method, got %d: %+v", len(sigChanges), a.Changes)
	}
	if sigChanges[0].Kind != KindSignature {
		t.Errorf("expected signature change on A.Do, got %s", sigChanges[0].Kind)
	}
	// The unchanged A.Do must not appear as removed/added.
	for _, c := range a.Changes {
		if c.Name == "Do" && (c.Kind == KindRemoved || c.Kind == KindAdded) {
			t.Errorf("unexpected %s change for a Do method: %+v", c.Kind, c)
		}
	}
}

// TestAnalyzeFuncAndMethodSameName proves a package func and a method sharing
// a name are tracked independently (removing the func must not be masked by
// the method still existing).
func TestAnalyzeFuncAndMethodSameName(t *testing.T) {
	old := "package main\n\n" +
		"type A struct{}\n" +
		"func Run() {}\n" +
		"func (a *A) Run() {}\n"
	// Remove the package-level func Run; keep the method A.Run.
	new := "package main\n\n" +
		"type A struct{}\n" +
		"func (a *A) Run() {}\n"

	a := Analyze(old, new, "main.go")

	removedFunc := false
	for _, c := range a.Changes {
		if c.Kind == KindRemoved && c.Name == "Run" && c.SymbolType == "func" {
			removedFunc = true
		}
		if c.Name == "Run" && c.SymbolType == "method" && c.Kind != KindModified {
			t.Errorf("method A.Run should be unchanged, got %s", c.Kind)
		}
	}
	if !removedFunc {
		t.Errorf("expected package func Run to be detected as removed, changes: %+v", a.Changes)
	}
}

func TestAnalyzeAdded(t *testing.T) {
	old := "package main\n\nfunc Existing() {}\n"
	new := "package main\n\nfunc Existing() {}\n\nfunc NewFunc() {}\n"

	a := Analyze(old, new, "main.go")
	if len(a.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(a.Changes))
	}
	if a.Changes[0].Kind != KindAdded {
		t.Errorf("expected added, got %s", a.Changes[0].Kind)
	}
	if a.Changes[0].Name != "NewFunc" {
		t.Errorf("expected NewFunc, got %s", a.Changes[0].Name)
	}
}

func TestAnalyzeRemoved(t *testing.T) {
	old := "package main\n\nfunc A() {}\n\nfunc B() {}\n"
	new := "package main\n\nfunc A() {}\n"

	a := Analyze(old, new, "main.go")
	found := false
	for _, c := range a.Changes {
		if c.Kind == KindRemoved && c.Name == "B" {
			found = true
		}
	}
	if !found {
		t.Error("should detect removed function B")
	}
}

func TestAnalyzeModified(t *testing.T) {
	old := "package main\n\nfunc Do() {\n\treturn 1\n}\n"
	new := "package main\n\nfunc Do() {\n\treturn 2\n}\n"

	a := Analyze(old, new, "main.go")
	found := false
	for _, c := range a.Changes {
		if c.Kind == KindModified && c.Name == "Do" {
			found = true
		}
	}
	if !found {
		t.Error("should detect modified function Do")
	}
}

func TestAnalyzeRenamed(t *testing.T) {
	old := "package main\n\nfunc OldName() {\n\tx := 1\n\ty := 2\n\treturn x + y\n}\n"
	new := "package main\n\nfunc NewName() {\n\tx := 1\n\ty := 2\n\treturn x + y\n}\n"

	a := Analyze(old, new, "main.go")
	found := false
	for _, c := range a.Changes {
		if c.Kind == KindRenamed && c.OldName == "OldName" && c.Name == "NewName" {
			found = true
		}
	}
	if !found {
		t.Error("should detect rename OldName -> NewName")
	}
}

func TestBreakingChange(t *testing.T) {
	old := "package main\n\nfunc PublicFunc() {}\n"
	new := "package main\n"

	a := Analyze(old, new, "main.go")
	if !a.HasBreaking() {
		t.Error("removing exported func should be breaking")
	}
}

func TestInternalChange(t *testing.T) {
	old := "package main\n\nfunc helper() {}\n"
	new := "package main\n\nfunc helper() {\n\t// changed\n}\n"

	a := Analyze(old, new, "main.go")
	if a.HasBreaking() {
		t.Error("modifying private func should not be breaking")
	}
}

func TestSignatureChange(t *testing.T) {
	old := "package main\n\nfunc Do(x int) {}\n"
	new := "package main\n\nfunc Do(x int, y string) {}\n"

	a := Analyze(old, new, "main.go")
	found := false
	for _, c := range a.Changes {
		if c.Kind == KindSignature && c.Name == "Do" {
			found = true
			if c.Impact != ImpactBreaking {
				t.Error("signature change on exported func should be breaking")
			}
		}
	}
	if !found {
		t.Error("should detect signature change")
	}
}

func TestByImpact(t *testing.T) {
	old := "package main\n\nfunc A() {}\nfunc b() {}\n"
	new := "package main\n"

	a := Analyze(old, new, "main.go")
	breaking := a.ByImpact(ImpactBreaking)
	internal := a.ByImpact(ImpactInternal)

	if len(breaking) == 0 {
		t.Error("should have breaking changes (removed A)")
	}
	if len(internal) == 0 {
		t.Error("should have internal changes (removed b)")
	}
}

func TestAnalyzeMultiFile(t *testing.T) {
	files := map[string][2]string{
		"a.go": {"package a\n\nfunc A() {}\n", "package a\n\nfunc A() {}\n\nfunc B() {}\n"},
		"b.go": {"package b\n\nfunc X() {}\n", ""},
	}

	a := AnalyzeMultiFile(files)
	if len(a.Changes) < 2 {
		t.Errorf("expected at least 2 changes, got %d", len(a.Changes))
	}
	if len(a.FileChanges) != 2 {
		t.Errorf("expected 2 file changes, got %d", len(a.FileChanges))
	}
}

func TestNewFile(t *testing.T) {
	files := map[string][2]string{
		"new.go": {"", "package new\n\nfunc Hello() {}\n"},
	}

	a := AnalyzeMultiFile(files)
	if len(a.FileChanges) != 1 || !a.FileChanges[0].IsNew {
		t.Error("should detect new file")
	}
}

func TestSummary(t *testing.T) {
	old := "package main\n\nfunc A() {}\n"
	new := "package main\n"

	a := Analyze(old, new, "main.go")
	if a.Summary == "" {
		t.Error("summary should not be empty")
	}
}

func TestSimilarity(t *testing.T) {
	if similarity("a b c", "a b c") != 1.0 {
		t.Error("identical should be 1.0")
	}
	if similarity("a b c", "x y z") != 0 {
		t.Error("disjoint should be 0")
	}
	s := similarity("a b c d", "a b c e")
	if s < 0.5 || s > 0.9 {
		t.Errorf("partial overlap should be moderate, got %f", s)
	}
}

func TestEmptyInput(t *testing.T) {
	a := Analyze("", "", "empty.go")
	if len(a.Changes) != 0 {
		t.Error("empty to empty should have no changes")
	}
}
