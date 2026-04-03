package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryLoadAndGet(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillDir, 0755)

	// Create a skill file
	content := `# build-fix

> Automatically fix build errors

<!-- keywords: build, compile, error -->

When the build fails:
1. Read the error message carefully
2. Identify the root cause
3. Apply the minimal fix
4. Re-run the build
`
	os.WriteFile(filepath.Join(skillDir, "build-fix.md"), []byte(content), 0644)

	reg := NewRegistry(skillDir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	s := reg.Get("build-fix")
	if s == nil {
		t.Fatal("expected to find build-fix skill")
	}
	if s.Description != "Automatically fix build errors" {
		t.Errorf("unexpected description: %q", s.Description)
	}
	if len(s.Keywords) != 3 {
		t.Errorf("expected 3 keywords, got %d: %v", len(s.Keywords), s.Keywords)
	}
}

func TestRegistryLoadDirectory(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "tdd")
	os.MkdirAll(skillDir, 0755)

	content := `# tdd

> Test-driven development workflow

<!-- keywords: test, tdd -->

Write the test first, then make it pass.
`
	os.WriteFile(filepath.Join(skillDir, "index.md"), []byte(content), 0644)

	reg := NewRegistry(filepath.Join(dir, "skills"))
	reg.Load()

	s := reg.Get("tdd")
	if s == nil {
		t.Fatal("expected to find tdd skill")
	}
}

func TestRegistryMatch(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillDir, 0755)

	os.WriteFile(filepath.Join(skillDir, "build-fix.md"),
		[]byte("# build-fix\n<!-- keywords: build, compile -->\nFix builds."), 0644)
	os.WriteFile(filepath.Join(skillDir, "security.md"),
		[]byte("# security\n<!-- keywords: security, auth, xss -->\nSecurity review."), 0644)

	reg := NewRegistry(skillDir)
	reg.Load()

	matches := reg.Match("the build is failing with a compile error")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Name != "build-fix" {
		t.Errorf("expected build-fix, got %s", matches[0].Name)
	}

	matches = reg.Match("check for xss vulnerabilities")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Name != "security" {
		t.Errorf("expected security, got %s", matches[0].Name)
	}

	matches = reg.Match("unrelated task")
	if len(matches) != 0 {
		t.Errorf("expected no matches, got %d", len(matches))
	}
}

func TestRegistryMatchOne(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "test.md"),
		[]byte("# test\n<!-- keywords: test -->\nRun tests."), 0644)

	reg := NewRegistry(skillDir)
	reg.Load()

	s := reg.MatchOne("run the test suite")
	if s == nil || s.Name != "test" {
		t.Error("expected test skill match")
	}
	if reg.MatchOne("deploy to prod") != nil {
		t.Error("expected no match")
	}
}

func TestRegistryInjectPrompt(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "build-fix.md"),
		[]byte("# build-fix\n<!-- keywords: build -->\nFix the build."), 0644)

	reg := NewRegistry(skillDir)
	reg.Load()

	original := "Fix the build error in main.go"
	injected := reg.InjectPrompt(original)

	if injected == original {
		t.Error("expected prompt to be augmented")
	}
	if !containsStr(injected, "Skill: build-fix") {
		t.Error("expected skill header in injected prompt")
	}
	if !containsStr(injected, original) {
		t.Error("expected original prompt to be preserved")
	}
}

func TestRegistryAddAndRemove(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")

	reg := NewRegistry(skillDir)

	err := reg.Add("my-skill", "A custom skill", "Do the thing.", []string{"custom", "thing"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	s := reg.Get("my-skill")
	if s == nil {
		t.Fatal("expected skill after add")
	}

	// File should exist
	if _, err := os.Stat(filepath.Join(skillDir, "my-skill.md")); err != nil {
		t.Error("expected skill file to exist")
	}

	// Remove
	if err := reg.Remove("my-skill"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if reg.Get("my-skill") != nil {
		t.Error("expected skill to be removed")
	}
}

func TestRegistryPriority(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "project", "skills")
	userDir := filepath.Join(dir, "user", "skills")
	os.MkdirAll(projDir, 0755)
	os.MkdirAll(userDir, 0755)

	// Same skill in both dirs
	os.WriteFile(filepath.Join(projDir, "build.md"),
		[]byte("# build\n<!-- keywords: build -->\nProject version."), 0644)
	os.WriteFile(filepath.Join(userDir, "build.md"),
		[]byte("# build\n<!-- keywords: build -->\nUser version."), 0644)

	reg := NewRegistry(projDir, userDir)
	reg.Load()

	s := reg.Get("build")
	if s == nil {
		t.Fatal("expected skill")
	}
	if s.Source != "project" {
		t.Errorf("expected project source (priority), got %s", s.Source)
	}
}

func TestSuggestSimilar(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "build-fix.md"),
		[]byte("# build-fix\n<!-- keywords: build -->\nFix."), 0644)
	os.WriteFile(filepath.Join(skillDir, "build-run.md"),
		[]byte("# build-run\n<!-- keywords: build -->\nRun."), 0644)

	reg := NewRegistry(skillDir)
	reg.Load()

	suggestions := reg.SuggestSimilar("build-fxi")
	found := false
	for _, s := range suggestions {
		if s == "build-fix" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'build-fix' in suggestions for 'build-fxi', got %v", suggestions)
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"abc", "abcd", 1},
		{"kitten", "sitting", 3},
	}
	for _, tc := range tests {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRegistryList(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "beta.md"),
		[]byte("# beta\n<!-- keywords: beta -->\nBeta."), 0644)
	os.WriteFile(filepath.Join(skillDir, "alpha.md"),
		[]byte("# alpha\n<!-- keywords: alpha -->\nAlpha."), 0644)

	reg := NewRegistry(skillDir)
	reg.Load()

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(list))
	}
	if list[0].Name != "alpha" {
		t.Errorf("expected alpha first, got %s", list[0].Name)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
