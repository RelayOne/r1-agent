// shell_inject_paths_test.go — unit tests for T-R1P-018 (skill shell injection
// preprocessing) and T-R1P-019 (path-scoped skill activation).
package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// T-R1P-018: PreprocessShellInjections
// ---------------------------------------------------------------------------

func TestPreprocessNoOp_NoMarker(t *testing.T) {
	input := "This has `backtick` spans but no bang-prefix."
	got := PreprocessShellInjections(input)
	if got != input {
		t.Errorf("expected no-op, got %q", got)
	}
}

func TestPreprocessExpand_Echo(t *testing.T) {
	input := "Version: !`echo v1.2.3`"
	got := PreprocessShellInjections(input)
	if !strings.Contains(got, "v1.2.3") {
		t.Errorf("expected expanded echo output, got %q", got)
	}
	if strings.Contains(got, "!`") {
		t.Errorf("bang-backtick marker should be replaced, got %q", got)
	}
}

func TestPreprocessErrorFallback(t *testing.T) {
	// Command that always fails
	input := "Info: !`exit 42`"
	got := PreprocessShellInjections(input)
	// Should not panic; should contain an error indicator
	if strings.Contains(got, "!`") {
		t.Errorf("bang-backtick marker should be replaced even on error, got %q", got)
	}
}

func TestPreprocessMultipleOnOneLine(t *testing.T) {
	input := "A=!`echo foo`, B=!`echo bar`"
	got := PreprocessShellInjections(input)
	if !strings.Contains(got, "foo") || !strings.Contains(got, "bar") {
		t.Errorf("both expansions should appear in: %q", got)
	}
}

// TestPreprocessSkillLoadWires verifies that Load() calls PreprocessShellInjections
// for project skills (non-builtin).
func TestPreprocessSkillLoadWires(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	// Write a skill with a !`echo` expansion
	content := "---\ndescription: test skill\n---\nVersion: !`echo injected`\n"
	os.WriteFile(filepath.Join(skillsDir, "inject-test.md"), []byte(content), 0600)

	reg := NewRegistry(skillsDir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := reg.Get("inject-test")
	if s == nil {
		t.Fatal("inject-test skill not found")
	}
	if !strings.Contains(s.Content, "injected") {
		t.Errorf("expected shell expansion result in content, got %q", s.Content)
	}
	if strings.Contains(s.Content, "!`") {
		t.Errorf("!` marker should be gone after Load, got %q", s.Content)
	}
}

// ---------------------------------------------------------------------------
// T-R1P-019: skillPathMatchesDir + InjectPromptBudgetedForDir filtering
// ---------------------------------------------------------------------------

func TestPathMatchExact(t *testing.T) {
	if !skillPathMatchesDir([]string{"/a/b"}, "/a/b") {
		t.Error("exact match should return true")
	}
}

func TestPathMatchAncestor(t *testing.T) {
	// pattern "/a/b" should match workDir "/a/b/c"
	if !skillPathMatchesDir([]string{"/a/b"}, "/a/b/c") {
		t.Error("ancestor prefix should match")
	}
}

func TestPathNoMatchUnrelated(t *testing.T) {
	if skillPathMatchesDir([]string{"/a/b"}, "/x/y") {
		t.Error("unrelated path should not match")
	}
}

func TestPathMatchGlob(t *testing.T) {
	// A simple wildcard pattern matches when one component uses *
	if !skillPathMatchesDir([]string{"src/*"}, "src/backend") {
		t.Error("glob should match src/backend against src/*")
	}
}

func TestPathMatchEmptyWorkDir(t *testing.T) {
	if skillPathMatchesDir([]string{"anything"}, "") {
		t.Error("empty workDir should return false")
	}
}

func TestPathMatchEmptyPatterns(t *testing.T) {
	if skillPathMatchesDir([]string{}, "/any/dir") {
		t.Error("empty patterns should return false")
	}
}

func TestPathMatchDoubleStarPrefix(t *testing.T) {
	// **/src should match any dir that contains a "src" component
	if !skillPathMatchesDir([]string{"**/src"}, "/deep/nested/src") {
		t.Error("**/src should match /deep/nested/src")
	}
}

// TestInjectSkipsPathScopedSkill verifies that a skill with a Paths field is
// NOT injected when workDir doesn't match.
func TestInjectSkipsPathScopedSkill(t *testing.T) {
	reg := &Registry{skills: make(map[string]*Skill)}
	reg.skills["scoped"] = &Skill{
		Name:         "scoped",
		Description:  "only for /proj/backend",
		Paths:        []string{"/proj/backend"},
		Content:      "secret backend stuff",
		Keywords:     []string{"always"},
		EstTokens:    10,
		Source:       "project",
		Priority:     1,
		References:   make(map[string]string),
	}

	prompt, sels := reg.InjectPromptBudgetedForDir("hello", "/proj/frontend", nil, 5000)
	_ = prompt
	for _, sel := range sels {
		if sel.Skill.Name == "scoped" {
			t.Error("scoped skill should NOT be injected when workDir is /proj/frontend")
		}
	}
}

// TestInjectIncludesPathScopedSkill verifies that a skill with a Paths field IS
// injected when workDir matches.
func TestInjectIncludesPathScopedSkill(t *testing.T) {
	reg := &Registry{skills: make(map[string]*Skill)}
	reg.skills["scoped"] = &Skill{
		Name:        "scoped",
		Description: "only for /proj/backend",
		Paths:       []string{"/proj/backend"},
		Content:     "backend guidance here",
		Keywords:    []string{"always"},
		EstTokens:   10,
		Source:      "project",
		Priority:    1,
		References:  make(map[string]string),
	}

	_, sels := reg.InjectPromptBudgetedForDir("hello", "/proj/backend", nil, 5000)
	found := false
	for _, sel := range sels {
		if sel.Skill.Name == "scoped" {
			found = true
		}
	}
	if !found {
		t.Error("scoped skill SHOULD be injected when workDir is /proj/backend")
	}
}

// TestInjectNoPaths_NeverFiltered verifies that skills without Paths are always
// eligible (the old behaviour, which must be preserved).
func TestInjectNoPaths_NeverFiltered(t *testing.T) {
	reg := &Registry{skills: make(map[string]*Skill)}
	reg.skills["global"] = &Skill{
		Name:        "global",
		Description: "available everywhere",
		Paths:       nil,
		Content:     "global guidance",
		Keywords:    []string{"always"},
		EstTokens:   5,
		Source:      "project",
		Priority:    1,
		References:  make(map[string]string),
	}

	_, sels := reg.InjectPromptBudgetedForDir("hello", "/any/path", nil, 5000)
	found := false
	for _, sel := range sels {
		if sel.Skill.Name == "global" {
			found = true
		}
	}
	if !found {
		t.Error("skill without Paths should always be injected regardless of workDir")
	}
}

// TestFrontmatterParsesPaths verifies that the `paths` frontmatter key is
// parsed into Skill.Paths.
func TestFrontmatterParsesPaths(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	content := "---\ndescription: path-gated\npaths:\n- /backend\n- /api\n---\nGuidance here.\n"
	os.WriteFile(filepath.Join(skillsDir, "path-gated.md"), []byte(content), 0600)

	reg := NewRegistry(skillsDir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := reg.Get("path-gated")
	if s == nil {
		t.Fatal("path-gated skill not found")
	}
	if len(s.Paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(s.Paths), s.Paths)
	}
	if s.Paths[0] != "/backend" || s.Paths[1] != "/api" {
		t.Errorf("unexpected paths: %v", s.Paths)
	}
}

// TestFrontmatterParsesPathsInline verifies inline list: paths: [/a, /b]
func TestFrontmatterParsesPathsInline(t *testing.T) {
	s := &Skill{References: make(map[string]string)}
	parseFrontmatter("paths: [/a, /b]", s)
	if len(s.Paths) != 2 || s.Paths[0] != "/a" || s.Paths[1] != "/b" {
		t.Errorf("inline paths parse failed: %v", s.Paths)
	}
}
