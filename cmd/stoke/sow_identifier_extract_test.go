package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// --- Smart 23: sowIdentifierExtractor tests ---

func TestFindCodeBlocks_RustBlock(t *testing.T) {
	sow := "# Example\n\nSome prose.\n\n```rust\npub struct Concern {}\n```\n\nMore prose."
	blocks := findCodeBlocks(sow)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Lang != "rust" {
		t.Errorf("lang = %q", blocks[0].Lang)
	}
	if !strings.Contains(blocks[0].Content, "pub struct Concern") {
		t.Errorf("content = %q", blocks[0].Content)
	}
}

func TestFindCodeBlocks_NoLang(t *testing.T) {
	sow := "```\nrandom code\n```"
	blocks := findCodeBlocks(sow)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Lang != "" {
		t.Errorf("no-lang block should have empty lang, got %q", blocks[0].Lang)
	}
}

func TestFindCodeBlocks_MultipleBlocks(t *testing.T) {
	sow := "```rust\npub struct A {}\n```\n\nAnd:\n\n```go\ntype B struct{}\n```"
	blocks := findCodeBlocks(sow)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestExtractIdentifiersFromCode_Rust(t *testing.T) {
	code := `use std::error::Error;

pub struct Concern {
    pub id: String,
}

pub enum SystemError {
    ConcernFailure,
}

pub trait Store {}

pub fn new_concern(id: String) -> Concern {
    Concern { id }
}

pub mod streams;

// not pub
fn private_helper() {}
`
	idents := extractIdentifiersFromCode(code, "rust", 4)
	want := []string{
		"pub struct Concern",
		"pub enum SystemError",
		"pub trait Store",
		"pub fn new_concern",
		"pub mod streams",
	}
	for _, w := range want {
		found := false
		for _, got := range idents {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in %v", w, idents)
		}
	}
	for _, got := range idents {
		if strings.Contains(got, "private_helper") {
			t.Errorf("private_helper should not be extracted: %v", idents)
		}
	}
}

func TestExtractIdentifiersFromCode_Go(t *testing.T) {
	code := `package main

type Service struct {
	Name string
}

type Store interface {
	Get() error
}

func NewService(name string) *Service {
	return &Service{Name: name}
}

func (s *Service) handle() {}
`
	idents := extractIdentifiersFromCode(code, "go", 4)
	var hasService, hasStore, hasNewService bool
	for _, got := range idents {
		if got == "type Service" {
			hasService = true
		}
		if got == "type Store" {
			hasStore = true
		}
		if got == "func NewService" {
			hasNewService = true
		}
	}
	if !hasService || !hasStore || !hasNewService {
		t.Errorf("missing expected Go identifiers: %v", idents)
	}
}

func TestExtractIdentifiersFromCode_Python(t *testing.T) {
	code := `class Parser:
    pass

def parse_file(path):
    pass
`
	idents := extractIdentifiersFromCode(code, "python", 4)
	var hasClass, hasFunc bool
	for _, got := range idents {
		if got == "class Parser" {
			hasClass = true
		}
		if got == "def parse_file" {
			hasFunc = true
		}
	}
	if !hasClass || !hasFunc {
		t.Errorf("missing Python identifiers: %v", idents)
	}
}

func TestExtractIdentifiersFromCode_MinLength(t *testing.T) {
	code := `pub struct A {}
pub struct Xyz {}`
	idents := extractIdentifiersFromCode(code, "rust", 4)
	// "A" is 1 char, below min 4 — should be filtered out.
	for _, got := range idents {
		if strings.HasSuffix(got, " A") {
			t.Errorf("short ident should be filtered: %v", idents)
		}
	}
	// "Xyz" is 3 chars, still below min 4.
	for _, got := range idents {
		if strings.HasSuffix(got, " Xyz") {
			t.Errorf("short ident should be filtered: %v", idents)
		}
	}
}

func TestExtractFileHintsFromParagraph(t *testing.T) {
	text := "Create the file crates/persys-concern/src/lib.rs. Also the `Cargo.toml` at the root."
	hints := extractFileHintsFromParagraph(text)
	sort.Strings(hints)
	want := []string{"Cargo.toml", "crates/persys-concern/src/lib.rs"}
	if len(hints) != len(want) {
		t.Fatalf("hints = %v, want %v", hints, want)
	}
	for i, h := range hints {
		if h != want[i] {
			t.Errorf("hint[%d] = %q, want %q", i, h, want[i])
		}
	}
}

func TestExtractFileHintsFromParagraph_NoMatches(t *testing.T) {
	text := "This paragraph has no file references at all."
	hints := extractFileHintsFromParagraph(text)
	if len(hints) != 0 {
		t.Errorf("expected empty, got %v", hints)
	}
}

func TestExtractInlineIdentifiers(t *testing.T) {
	text := "Implement the `Concern` struct and the `SystemError` enum. The CamelCaseIdent should appear too."
	idents := extractInlineIdentifiers(text, 4)
	var hasConcern, hasSystemError, hasCamel bool
	for _, ident := range idents {
		if ident == "Concern" {
			hasConcern = true
		}
		if ident == "SystemError" {
			hasSystemError = true
		}
		if ident == "CamelCaseIdent" {
			hasCamel = true
		}
	}
	if !hasConcern {
		t.Errorf("should extract backticked Concern: %v", idents)
	}
	if !hasSystemError {
		t.Errorf("should extract backticked SystemError: %v", idents)
	}
	if !hasCamel {
		t.Errorf("should extract CamelCaseIdent: %v", idents)
	}
}

func TestSowIdentifierExtractor_CodeBlockTiedToFileHint(t *testing.T) {
	sow := `# PERSYS

## persys-concern crate

The crates/persys-concern/src/lib.rs file should define:

` + "```rust\npub struct Concern {\n    pub id: String,\n}\n\npub enum SystemError {\n    ConcernFailure,\n}\n```" + `

Done.
`
	e := &sowIdentifierExtractor{}
	result := e.extract(sow)
	idents, ok := result["crates/persys-concern/src/lib.rs"]
	if !ok {
		t.Fatalf("expected idents for crates/persys-concern/src/lib.rs, got keys: %v", keys(result))
	}
	hasStruct := false
	hasEnum := false
	for _, i := range idents {
		if i == "pub struct Concern" {
			hasStruct = true
		}
		if i == "pub enum SystemError" {
			hasEnum = true
		}
	}
	if !hasStruct || !hasEnum {
		t.Errorf("expected struct+enum idents, got: %v", idents)
	}
}

func TestSowIdentifierExtractor_EmptySOW(t *testing.T) {
	e := &sowIdentifierExtractor{}
	result := e.extract("")
	if len(result) != 0 {
		t.Errorf("empty SOW should produce empty map, got %v", result)
	}
}

func TestSowIdentifierExtractor_CapsPerFile(t *testing.T) {
	// A code block with many declarations — should be capped.
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "pub struct StructName"+string(rune('A'+i))+" {}")
	}
	sow := "For lib.rs:\n\n```rust\n" + strings.Join(lines, "\n") + "\n```"
	e := &sowIdentifierExtractor{MaxPerFile: 5}
	result := e.extract(sow)
	for f, idents := range result {
		if len(idents) > 5 {
			t.Errorf("%s has %d idents, expected cap at 5", f, len(idents))
		}
	}
}

func TestAutoExtractTaskSupervisor_UsesContentMatch(t *testing.T) {
	session := plan.Session{
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{
				File:    "foo.rs",
				Pattern: "pub struct Foo",
			}},
		},
	}
	task := plan.Task{ID: "T1", Files: []string{"foo.rs"}}
	sup := autoExtractTaskSupervisor("/tmp", "", session, task, 3)
	if sup == nil {
		t.Fatal("expected non-nil supervisor")
	}
	if len(sup.Expectations) != 1 || len(sup.Expectations[0].MustContain) != 1 {
		t.Errorf("expected 1 expectation with 1 MustContain, got %+v", sup.Expectations)
	}
}

func TestAutoExtractTaskSupervisor_FallbackFromRawSOW(t *testing.T) {
	// Task has no content_match criteria, but the raw SOW has a code
	// block with declarations — the auto-extractor should fill in.
	sow := `# Project

The file crates/foo/src/lib.rs should define:

` + "```rust\npub struct Widget {}\npub fn make_widget() -> Widget { Widget{} }\n```"

	session := plan.Session{}
	task := plan.Task{ID: "T1", Files: []string{"crates/foo/src/lib.rs"}}
	sup := autoExtractTaskSupervisor("/tmp", sow, session, task, 3)
	if sup == nil {
		t.Fatal("expected auto-extract to produce a supervisor")
	}
	found := false
	for _, exp := range sup.Expectations {
		if exp.File != "crates/foo/src/lib.rs" {
			continue
		}
		for _, m := range exp.MustContain {
			if m == "pub struct Widget" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected pub struct Widget in auto-extracted expectations, got: %+v", sup)
	}
}

func TestAutoExtractTaskSupervisor_NilWhenNothingToVerify(t *testing.T) {
	// No content_match, no code blocks, no file hints — nothing to
	// verify, supervisor should be nil so we don't waste turns on
	// empty scans.
	sow := "Just prose, no code blocks, no specific file references."
	task := plan.Task{ID: "T1", Files: []string{"unrelated.rs"}}
	sup := autoExtractTaskSupervisor("/tmp", sow, plan.Session{}, task, 3)
	if sup != nil {
		t.Errorf("expected nil when nothing to verify, got %+v", sup)
	}
}

func TestAutoExtractTaskSupervisor_MergesContentMatchWithAutoExtract(t *testing.T) {
	// content_match provides one identifier; auto-extract adds more.
	// Result should contain both.
	sow := `# Project

The file src/main.rs should have:

` + "```rust\npub fn main() {}\npub struct Runner {}\n```"
	session := plan.Session{
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{
				File:    "src/main.rs",
				Pattern: "explicit identifier",
			}},
		},
	}
	task := plan.Task{Files: []string{"src/main.rs"}}
	sup := autoExtractTaskSupervisor("/tmp", sow, session, task, 3)
	if sup == nil {
		t.Fatal("expected non-nil")
	}
	var hasExplicit, hasAuto bool
	for _, exp := range sup.Expectations {
		for _, m := range exp.MustContain {
			if m == "explicit identifier" {
				hasExplicit = true
			}
			if m == "pub struct Runner" {
				hasAuto = true
			}
		}
	}
	if !hasExplicit {
		t.Error("should preserve explicit content_match")
	}
	if !hasAuto {
		t.Error("should add auto-extracted identifiers")
	}
}

// --- Smart 24: case-insensitive excerpt matching ---

func TestExtractTaskSpecExcerpt_CaseInsensitiveEnglish(t *testing.T) {
	// Prose says "PERSYS-CONCERN" but the search term is "persys-concern"
	// (from the file path crates/persys-concern/...). Should match.
	sow := `# Project

## The PERSYS-CONCERN crate

This crate implements the concern field.
`
	task := plan.Task{Files: []string{"crates/persys-concern/src/lib.rs"}}
	excerpt := extractTaskSpecExcerpt(sow, plan.Session{}, task, specExcerptConfig{})
	if !strings.Contains(excerpt, "PERSYS-CONCERN") {
		t.Errorf("case-insensitive match failed:\n%s", excerpt)
	}
}

func TestIsCodeLikeTerm(t *testing.T) {
	cases := map[string]bool{
		"crates/foo/src/lib.rs": true,  // path
		"concern.rs":            true,  // has dot
		"pub struct Concern":    false, // no code-ish chars, treat as prose
		"persys-concern":        false, // no code-ish chars (hyphen doesn't count)
		"fn_name":               true,  // underscore
		"Foo<T>":                true,  // angle brackets
		"Store::Get":            true,  // colons
	}
	for term, want := range cases {
		if got := isCodeLikeTerm(term); got != want {
			t.Errorf("isCodeLikeTerm(%q) = %v, want %v", term, got, want)
		}
	}
}

// --- Smart 25: dumpTaskPrompts ---

func TestDumpTaskPrompts_WritesFiles(t *testing.T) {
	dir := t.TempDir()
	sow := &plan.SOW{
		ID:   "test", Name: "Test",
		Sessions: []plan.Session{
			{
				ID: "S1", Title: "First",
				Tasks: []plan.Task{
					{ID: "T1", Description: "create x", Files: []string{"x.rs"}},
					{ID: "T2", Description: "create y", Files: []string{"y.rs"}},
				},
			},
			{
				ID: "S2", Title: "Second",
				Tasks: []plan.Task{
					{ID: "T3", Description: "create z", Files: []string{"z.rs"}},
				},
			},
		},
	}
	rawSOW := "# Test\n\nSome spec content.\n"

	count, err := dumpTaskPrompts(dir, sow, rawSOW)
	if err != nil {
		t.Fatalf("dumpTaskPrompts: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files, got %d", count)
	}

	// Check the expected files exist.
	dumpDir := filepath.Join(dir, ".stoke", "prompt-dump")
	for _, want := range []string{"S1-T1.txt", "S1-T2.txt", "S2-T3.txt", "_summary.txt"} {
		if _, err := os.Stat(filepath.Join(dumpDir, want)); err != nil {
			t.Errorf("missing dump file %s: %v", want, err)
		}
	}

	// Check contents of one file include the expected sections.
	data, err := os.ReadFile(filepath.Join(dumpDir, "S1-T1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"SYSTEM PROMPT",
		"USER PROMPT",
		"SUPERVISOR EXPECTATIONS",
		"SPEC EXCERPT",
		"TASK T1",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("dump file missing %q:\n%s", want, content[:min(500, len(content))])
		}
	}
}

func TestDumpTaskPrompts_SummaryHasAllTasks(t *testing.T) {
	dir := t.TempDir()
	sow := &plan.SOW{
		ID:   "x", Name: "X",
		Sessions: []plan.Session{
			{ID: "S1", Tasks: []plan.Task{{ID: "T1"}, {ID: "T2"}}},
			{ID: "S2", Tasks: []plan.Task{{ID: "T3"}}},
		},
	}
	_, err := dumpTaskPrompts(dir, sow, "")
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := os.ReadFile(filepath.Join(dir, ".stoke", "prompt-dump", "_summary.txt"))
	s := string(summary)
	for _, want := range []string{"S1", "S2", "T1", "T2", "T3", "total tasks: 3"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
}

func TestSanitizeForFilename(t *testing.T) {
	cases := map[string]string{
		"S1":          "S1",
		"S1/T1":       "S1_T1",
		"task with spaces": "task_with_spaces",
		"weird:name!": "weird_name_",
		"":            "unknown",
		"ok-123.txt":  "ok-123.txt",
	}
	for in, want := range cases {
		if got := sanitizeForFilename(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateColumn(t *testing.T) {
	if truncateColumn("short", 10) != "short" {
		t.Error("short string unchanged")
	}
	if truncateColumn("this is very long indeed", 10) != "this is..." {
		t.Errorf("long truncation wrong: %q", truncateColumn("this is very long indeed", 10))
	}
}

func TestRenderSupervisorExpectations_NilReturnsEmpty(t *testing.T) {
	if renderSupervisorExpectations(nil) != "" {
		t.Error("nil spec should produce empty string")
	}
}

func TestRenderSupervisorExpectations_StableOrdering(t *testing.T) {
	sup := &specSupervisorSpec{
		Expectations: []taskFileExpectation{
			{File: "z.rs", MustContain: []string{"Z"}},
			{File: "a.rs", MustContain: []string{"A"}},
			{File: "m.rs", MustContain: []string{"M"}},
		},
	}
	out := renderSupervisorExpectations(sup)
	// a.rs should come before m.rs should come before z.rs
	aIdx := strings.Index(out, "a.rs")
	mIdx := strings.Index(out, "m.rs")
	zIdx := strings.Index(out, "z.rs")
	if aIdx > mIdx || mIdx > zIdx {
		t.Errorf("expected alphabetical ordering, got:\n%s", out)
	}
}

// --- helpers ---

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
