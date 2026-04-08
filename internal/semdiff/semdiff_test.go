package semdiff

import (
	"testing"
)

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
