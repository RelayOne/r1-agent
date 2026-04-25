package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1-agent/internal/plan"
)

// --- Smart 15: RawSOW injection tests ---

func TestBuildSOWNativePrompts_InjectsRawSOW(t *testing.T) {
	sow := &plan.SOW{ID: "test", Name: "Test"}
	session := plan.Session{ID: "S1", Title: "First"}
	task := plan.Task{ID: "T1", Description: "do a thing"}

	rawSpec := "# PERSYS SPEC\n\nCrate names: persys-concern, persys-memory, persys-wander.\n"
	sys, _ := buildSOWNativePromptsWithOpts(sow, session, task, promptOpts{RawSOW: rawSpec})

	if !strings.Contains(sys, "SPEC (verbatim from the SOW") {
		t.Error("system prompt should include SPEC header when RawSOW is set")
	}
	if !strings.Contains(sys, "persys-concern") {
		t.Error("raw SOW text should appear verbatim in system prompt")
	}
	if !strings.Contains(sys, "----- BEGIN SOW -----") || !strings.Contains(sys, "----- END SOW -----") {
		t.Error("SOW should be framed with BEGIN/END markers")
	}
}

func TestBuildSOWNativePrompts_RawSOWTruncatedAtCap(t *testing.T) {
	// Build a 50k-char raw SOW; it should be truncated to the soft cap.
	rawSpec := strings.Repeat("x", 50000)
	sow := &plan.SOW{ID: "big", Name: "Big"}
	sys, _ := buildSOWNativePromptsWithOpts(sow, plan.Session{ID: "S1"}, plan.Task{ID: "T1"}, promptOpts{RawSOW: rawSpec})

	if !strings.Contains(sys, "SOW truncated") {
		t.Error("oversized SOW should produce a truncation notice")
	}
	// System prompt shouldn't contain all 50k chars
	if strings.Count(sys, "x") >= 50000 {
		t.Errorf("SOW should have been truncated, got %d x's", strings.Count(sys, "x"))
	}
}

func TestBuildSOWNativePrompts_NoRawSOW_NoSection(t *testing.T) {
	sow := &plan.SOW{ID: "test", Name: "Test"}
	sys, _ := buildSOWNativePromptsWithOpts(sow, plan.Session{ID: "S1"}, plan.Task{ID: "T1"}, promptOpts{})

	if strings.Contains(sys, "SPEC (verbatim") {
		t.Error("SPEC header should NOT appear when RawSOW is empty")
	}
}

// --- Smart 16: canonical names tests ---

func TestBuildCanonicalNamesBlock_PullsFromAllSources(t *testing.T) {
	sow := &plan.SOW{
		ID:   "x",
		Name: "X",
		Stack: plan.StackSpec{
			Infra: []plan.InfraRequirement{{Name: "postgres"}},
		},
	}
	session := plan.Session{
		ID:      "S1",
		Outputs: []string{"crates/persys-concern/"},
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", FileExists: "crates/persys-concern/Cargo.toml"},
			{ID: "AC2", ContentMatch: &plan.ContentMatchCriterion{
				File: "crates/persys-concern/Cargo.toml",
				Pattern: "name = \"persys-concern\"",
			}},
		},
	}
	task := plan.Task{
		ID:    "T1",
		Files: []string{"crates/persys-concern/src/lib.rs"},
	}

	block := buildCanonicalNamesBlock(sow, session, task)
	for _, want := range []string{
		"CANONICAL IDENTIFIERS",
		"USE THESE EXACTLY AS WRITTEN",
		"crates/persys-concern/",
		"crates/persys-concern/Cargo.toml",
		"crates/persys-concern/src/lib.rs",
		`name = \"persys-concern\"`,
		"postgres",
		"task.files",
		"session.outputs",
		"content_match.pattern",
		"stack.infra",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("canonical block missing %q:\n%s", want, block)
		}
	}
}

func TestBuildCanonicalNamesBlock_DedupesAcrossSources(t *testing.T) {
	// Same file appearing in task.Files and session.Outputs should
	// only show up once.
	session := plan.Session{
		ID:      "S1",
		Outputs: []string{"Cargo.toml"},
	}
	task := plan.Task{
		ID:    "T1",
		Files: []string{"Cargo.toml"},
	}
	block := buildCanonicalNamesBlock(&plan.SOW{Name: "X"}, session, task)
	count := strings.Count(block, `"Cargo.toml"`)
	if count != 1 {
		t.Errorf("Cargo.toml should appear once, got %d", count)
	}
}

func TestBuildCanonicalNamesBlock_EmptyReturnsEmpty(t *testing.T) {
	block := buildCanonicalNamesBlock(&plan.SOW{}, plan.Session{}, plan.Task{})
	if block != "" {
		t.Errorf("no identifiers should produce empty string, got:\n%s", block)
	}
}

func TestBuildCanonicalNamesBlock_NilSOW(t *testing.T) {
	block := buildCanonicalNamesBlock(nil, plan.Session{ID: "S1"}, plan.Task{ID: "T1"})
	if block != "" {
		t.Error("nil SOW should produce empty block")
	}
}

func TestBuildSOWNativePrompts_InjectsCanonicalNames(t *testing.T) {
	sow := &plan.SOW{ID: "x", Name: "X"}
	session := plan.Session{
		ID:      "S1",
		Outputs: []string{"crates/persys-concern/"},
	}
	task := plan.Task{ID: "T1", Files: []string{"crates/persys-concern/src/lib.rs"}}

	sys, _ := buildSOWNativePromptsWithOpts(sow, session, task, promptOpts{})
	if !strings.Contains(sys, "CANONICAL IDENTIFIERS") {
		t.Error("system prompt should include canonical names block")
	}
	if !strings.Contains(sys, "crates/persys-concern") {
		t.Error("canonical names should include the declared paths")
	}
}

// --- Smart 17: placeholder/stub scan tests ---

func TestScanPlaceholderStubs_RustUnimplemented(t *testing.T) {
	dir := t.TempDir()
	src := `pub fn do_work() {
    unimplemented!()
}`
	os.WriteFile(filepath.Join(dir, "lib.rs"), []byte(src), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"lib.rs"})
	if len(findings) == 0 {
		t.Fatal("should detect unimplemented!()")
	}
	if !strings.Contains(findings[0].Pattern, "unimplemented") {
		t.Errorf("pattern name = %q, want unimplemented", findings[0].Pattern)
	}
}

func TestScanPlaceholderStubs_RustPubFnPlaceholder(t *testing.T) {
	dir := t.TempDir()
	src := `// lib.rs

pub fn placeholder() -> i32 {
    42
}
`
	os.WriteFile(filepath.Join(dir, "lib.rs"), []byte(src), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"lib.rs"})
	if len(findings) == 0 {
		t.Fatal("should detect pub fn placeholder()")
	}
	if findings[0].Line != 3 {
		t.Errorf("expected line 3, got %d", findings[0].Line)
	}
}

func TestScanPlaceholderStubs_RustTodoBang(t *testing.T) {
	dir := t.TempDir()
	src := `pub fn f() { todo!() }`
	os.WriteFile(filepath.Join(dir, "a.rs"), []byte(src), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"a.rs"})
	if len(findings) == 0 {
		t.Error("should detect todo!()")
	}
}

func TestScanPlaceholderStubs_GoPanicTODO(t *testing.T) {
	dir := t.TempDir()
	src := `package main

func foo() {
	panic("TODO: implement foo")
}
`
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"main.go"})
	if len(findings) == 0 {
		t.Error("should detect panic(\"TODO\")")
	}
}

func TestScanPlaceholderStubs_PythonNotImplementedError(t *testing.T) {
	dir := t.TempDir()
	src := `def process(data):
    raise NotImplementedError("coming soon")
`
	os.WriteFile(filepath.Join(dir, "mod.py"), []byte(src), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"mod.py"})
	if len(findings) == 0 {
		t.Error("should detect raise NotImplementedError")
	}
}

func TestScanPlaceholderStubs_TypeScriptNotImplemented(t *testing.T) {
	dir := t.TempDir()
	src := `export function f() {
  throw new Error("Not implemented");
}
`
	os.WriteFile(filepath.Join(dir, "mod.ts"), []byte(src), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"mod.ts"})
	if len(findings) == 0 {
		t.Error("should detect throw new Error('Not implemented')")
	}
}

func TestScanPlaceholderStubs_CleanFileNoFindings(t *testing.T) {
	dir := t.TempDir()
	src := `pub fn add(a: i32, b: i32) -> i32 {
    a + b
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn it_adds() {
        assert_eq!(add(1, 2), 3);
    }
}
`
	os.WriteFile(filepath.Join(dir, "lib.rs"), []byte(src), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"lib.rs"})
	if len(findings) != 0 {
		t.Errorf("clean file should have no findings, got %+v", findings)
	}
}

func TestScanPlaceholderStubs_NonexistentFileSkipped(t *testing.T) {
	dir := t.TempDir()
	findings := scanPlaceholderStubs(dir, []string{"does-not-exist.rs"})
	if len(findings) != 0 {
		t.Error("missing file should be silently skipped, not produce findings")
	}
}

func TestScanPlaceholderStubs_BinaryExtensionSkipped(t *testing.T) {
	dir := t.TempDir()
	// Even a literal "unimplemented!()" in a binary file should be skipped.
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("pub fn placeholder() { unimplemented!() }"), 0o600)

	findings := scanPlaceholderStubs(dir, []string{"blob.bin"})
	if len(findings) != 0 {
		t.Errorf("binary files should be skipped, got %+v", findings)
	}
}

func TestIsSourceFile(t *testing.T) {
	cases := map[string]bool{
		"lib.rs":       true,
		"main.go":      true,
		"app.py":       true,
		"mod.ts":       true,
		"Component.tsx": true,
		"Cargo.toml":   true,
		"package.json": true,
		"config.yaml":  true,
		"README.md":    false,
		"image.png":    false,
		"blob.bin":     false,
		"data.db":      false,
	}
	for path, want := range cases {
		if got := isSourceFile(path); got != want {
			t.Errorf("isSourceFile(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestFormatPlaceholderFindings(t *testing.T) {
	findings := []PlaceholderFinding{
		{File: "lib.rs", Line: 10, Pattern: "unimplemented!()", Snippet: "    unimplemented!()"},
		{File: "main.rs", Line: 3, Pattern: "pub fn placeholder", Snippet: "pub fn placeholder() {}"},
	}
	out := formatPlaceholderFindings(findings)
	for _, want := range []string{"STUB/PLACEHOLDER", "lib.rs:10", "main.rs:3", "unimplemented", "pub fn placeholder"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// --- Smart 17: checkSpecFaithfulness tests ---

func TestCheckSpecFaithfulness_MissingFile(t *testing.T) {
	dir := t.TempDir()
	session := plan.Session{
		ID: "S1",
		Tasks: []plan.Task{
			{ID: "T1", Files: []string{"crates/persys-concern/Cargo.toml"}},
		},
	}
	missing, _ := checkSpecFaithfulness(dir, session)
	if len(missing) != 1 {
		t.Errorf("expected 1 missing file, got %v", missing)
	}
}

func TestCheckSpecFaithfulness_EmptyFileFlagged(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "crates/foo"), 0o755)
	// Create a 0-byte file
	os.WriteFile(filepath.Join(dir, "crates/foo/Cargo.toml"), nil, 0o600)

	session := plan.Session{
		ID: "S1",
		Tasks: []plan.Task{
			{ID: "T1", Files: []string{"crates/foo/Cargo.toml"}},
		},
	}
	missing, _ := checkSpecFaithfulness(dir, session)
	if len(missing) != 1 || !strings.Contains(missing[0], "0 bytes") {
		t.Errorf("0-byte file should be flagged, got %v", missing)
	}
}

func TestCheckSpecFaithfulness_PlaceholderFileFlagged(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "crates/foo/src"), 0o755)
	// Create a valid Cargo.toml but a stub lib.rs
	os.WriteFile(filepath.Join(dir, "crates/foo/Cargo.toml"), []byte("[package]\nname = \"foo\"\nversion = \"0.1.0\"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "crates/foo/src/lib.rs"), []byte("pub fn placeholder() {}\n"), 0o600)

	session := plan.Session{
		ID: "S1",
		Tasks: []plan.Task{
			{ID: "T1", Files: []string{"crates/foo/Cargo.toml", "crates/foo/src/lib.rs"}},
		},
	}
	missing, suspicious := checkSpecFaithfulness(dir, session)
	if len(missing) != 0 {
		t.Errorf("no files should be missing: %v", missing)
	}
	if len(suspicious) == 0 {
		t.Error("lib.rs with pub fn placeholder should be flagged as suspicious")
	}
}

func TestFormatSpecFaithfulnessBlob(t *testing.T) {
	missing := []string{"crates/foo/Cargo.toml", "crates/bar/src/lib.rs (0 bytes)"}
	suspicious := []PlaceholderFinding{
		{File: "crates/baz/src/lib.rs", Line: 5, Pattern: "unimplemented!()", Snippet: "unimplemented!()"},
	}
	blob := formatSpecFaithfulnessBlob(missing, suspicious)
	for _, want := range []string{"MISSING OR EMPTY", "crates/foo/Cargo.toml", "0 bytes", "STUB/PLACEHOLDER", "crates/baz/src/lib.rs:5"} {
		if !strings.Contains(blob, want) {
			t.Errorf("blob missing %q:\n%s", want, blob)
		}
	}
}

// --- Smart 18: workspace-root baseline criteria tests ---

func TestInferBaselineCriteria_RustHasCargoRootCheck(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{Language: "rust"})
	var hasRoot, hasWorkspaceCheck bool
	for _, c := range crit {
		if c.ID == "inferred-cargo-root" && c.FileExists == "Cargo.toml" {
			hasRoot = true
		}
		if c.ID == "inferred-cargo-workspace-consistent" {
			hasWorkspaceCheck = true
		}
	}
	if !hasRoot {
		t.Error("Rust baseline should check for root Cargo.toml")
	}
	if !hasWorkspaceCheck {
		t.Error("Rust baseline should check workspace/member consistency")
	}
}

func TestInferBaselineCriteria_GoHasGoModRootCheck(t *testing.T) {
	crit := inferBaselineCriteria(plan.StackSpec{Language: "go"})
	var hasRoot bool
	for _, c := range crit {
		if c.ID == "inferred-gomod-root" && c.FileExists == "go.mod" {
			hasRoot = true
		}
	}
	if !hasRoot {
		t.Error("Go baseline should check for root go.mod")
	}
}

// --- Smart 15: loadRawSOWText tests ---

func TestLoadRawSOWText_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.md")
	content := "# My project\n\nStuff."
	os.WriteFile(path, []byte(content), 0o600)

	got := loadRawSOWText(path, nil)
	if got != content {
		t.Errorf("expected file content, got %q", got)
	}
}

func TestLoadRawSOWText_FallsBackToMarshal(t *testing.T) {
	// No file path, but a parsed SOW → should return marshaled JSON
	sow := &plan.SOW{ID: "marshaled", Name: "Marshal Test"}
	got := loadRawSOWText("", sow)
	if !strings.Contains(got, "marshaled") {
		t.Errorf("expected marshaled SOW, got %q", got)
	}
}

func TestLoadRawSOWText_NilSOWEmptyPath(t *testing.T) {
	got := loadRawSOWText("", nil)
	if got != "" {
		t.Errorf("no source should yield empty string, got %q", got)
	}
}

func TestLoadRawSOWText_NonexistentFileFallsBack(t *testing.T) {
	sow := &plan.SOW{ID: "x", Name: "X"}
	got := loadRawSOWText("/definitely/not/a/real/path.md", sow)
	// Should fall back to marshaling the SOW
	if !strings.Contains(got, "\"id\"") {
		t.Errorf("expected fallback to marshaled SOW, got %q", got)
	}
}

