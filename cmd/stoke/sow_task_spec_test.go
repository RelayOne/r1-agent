package main

import (
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/plan"
)

// --- Smart 19: extractTaskSpecExcerpt tests ---

func TestExtractTaskSpecExcerpt_FindsByTaskFile(t *testing.T) {
	sow := `# PERSYS SOW

## Overview

Build a concern-memory-wander architecture.

## persys-concern crate

The persys-concern crate implements the 11-stream concern field.
The Concern struct has these streams:
- stream_ego
- stream_theory
- stream_chosen

Files: crates/persys-concern/src/lib.rs, crates/persys-concern/src/concern.rs

## persys-memory crate

The persys-memory crate is unrelated to this task.
`
	session := plan.Session{ID: "S1"}
	task := plan.Task{
		ID:          "T1",
		Description: "Create concern.rs and lib.rs",
		Files:       []string{"crates/persys-concern/src/concern.rs"},
	}
	excerpt := extractTaskSpecExcerpt(sow, session, task, specExcerptConfig{})
	if excerpt == "" {
		t.Fatal("expected non-empty excerpt")
	}
	if !strings.Contains(excerpt, "persys-concern") {
		t.Errorf("excerpt should contain persys-concern:\n%s", excerpt)
	}
	if !strings.Contains(excerpt, "11-stream concern field") {
		t.Errorf("excerpt should contain the 11-stream mention:\n%s", excerpt)
	}
	if strings.Contains(excerpt, "persys-memory crate is unrelated") {
		t.Errorf("excerpt should NOT include the unrelated crate section:\n%s", excerpt)
	}
}

func TestExtractTaskSpecExcerpt_FindsByContentMatchPattern(t *testing.T) {
	sow := `# Project

## Error types

The SystemError enum has these variants:
ConcernFailure, MemoryOverflow, WanderStalled

## Unrelated

This section should not appear in any task excerpt.
`
	session := plan.Session{
		ID: "S1",
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{
				File:    "crates/persys-concern/src/error.rs",
				Pattern: "ConcernFailure",
			}},
		},
	}
	task := plan.Task{
		ID:    "T1",
		Files: []string{"crates/persys-concern/src/error.rs"},
	}
	excerpt := extractTaskSpecExcerpt(sow, session, task, specExcerptConfig{})
	if !strings.Contains(excerpt, "ConcernFailure") {
		t.Errorf("excerpt should contain ConcernFailure from content_match:\n%s", excerpt)
	}
}

func TestExtractTaskSpecExcerpt_NoMatchesReturnsEmpty(t *testing.T) {
	sow := "# Project\n\nSomething completely unrelated.\n"
	task := plan.Task{ID: "T1", Files: []string{"random/path.rs"}}
	excerpt := extractTaskSpecExcerpt(sow, plan.Session{}, task, specExcerptConfig{})
	if excerpt != "" {
		t.Errorf("expected empty excerpt, got:\n%s", excerpt)
	}
}

func TestExtractTaskSpecExcerpt_RespectsMaxChars(t *testing.T) {
	var sowBuilder strings.Builder
	sowBuilder.WriteString("# Project\n\n")
	for i := 0; i < 50; i++ {
		sowBuilder.WriteString("persys-concern is important. ")
		sowBuilder.WriteString("This paragraph has the keyword persys-concern.\n\n")
	}
	task := plan.Task{ID: "T1", Files: []string{"crates/persys-concern/src/lib.rs"}}
	excerpt := extractTaskSpecExcerpt(sowBuilder.String(), plan.Session{}, task, specExcerptConfig{MaxChars: 500})
	if len(excerpt) > 600 {
		t.Errorf("excerpt exceeds MaxChars cap: %d chars", len(excerpt))
	}
}

func TestExtractTaskSpecExcerpt_EmptySOW(t *testing.T) {
	task := plan.Task{ID: "T1", Files: []string{"foo.rs"}}
	if extractTaskSpecExcerpt("", plan.Session{}, task, specExcerptConfig{}) != "" {
		t.Error("empty SOW should produce empty excerpt")
	}
	if extractTaskSpecExcerpt("   \n\n", plan.Session{}, task, specExcerptConfig{}) != "" {
		t.Error("whitespace-only SOW should produce empty excerpt")
	}
}

// --- Smart 16 refresh: collectTaskSearchTerms ---

func TestCollectTaskSearchTerms_FromFiles(t *testing.T) {
	task := plan.Task{
		ID:    "T1",
		Files: []string{"crates/persys-concern/src/concern.rs"},
	}
	terms := collectTaskSearchTerms(plan.Session{}, task)
	want := []string{
		"crates/persys-concern/src/concern.rs",
		"concern.rs",
		"persys-concern",
	}
	for _, w := range want {
		found := false
		for _, t := range terms {
			if t == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected term %q, got %v", w, terms)
		}
	}
}

func TestCollectTaskSearchTerms_FromContentMatch(t *testing.T) {
	session := plan.Session{
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{
				File:    "foo.rs",
				Pattern: "pub struct Concern",
			}},
		},
	}
	task := plan.Task{Files: []string{"foo.rs"}}
	terms := collectTaskSearchTerms(session, task)
	found := false
	for _, t := range terms {
		if t == "pub struct Concern" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected content_match pattern in terms: %v", terms)
	}
}

func TestCollectTaskSearchTerms_ShortTermsDropped(t *testing.T) {
	// Terms shorter than 3 chars should not make it into the list.
	task := plan.Task{Files: []string{"a/b.rs"}}
	terms := collectTaskSearchTerms(plan.Session{}, task)
	for _, term := range terms {
		if len(term) < 3 {
			t.Errorf("short term leaked through: %q", term)
		}
	}
}

// --- Smart 20: buildTaskIdentifierChecklist ---

func TestBuildTaskIdentifierChecklist_HasEntries(t *testing.T) {
	session := plan.Session{
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{
				File:    "crates/foo/src/lib.rs",
				Pattern: "pub struct Widget",
			}},
		},
	}
	task := plan.Task{
		ID:    "T1",
		Files: []string{"crates/foo/src/lib.rs"},
	}
	out := buildTaskIdentifierChecklist(session, task)
	if out == "" {
		t.Fatal("expected non-empty checklist")
	}
	if !strings.Contains(out, "BEFORE YOU WRITE ANY CODE") {
		t.Error("checklist should start with BEFORE YOU WRITE")
	}
	if !strings.Contains(out, "crates/foo/src/lib.rs") {
		t.Error("checklist should include task files")
	}
	if !strings.Contains(out, "pub struct Widget") {
		t.Error("checklist should include content_match pattern")
	}
}

func TestBuildTaskIdentifierChecklist_EmptyTaskReturnsEmpty(t *testing.T) {
	out := buildTaskIdentifierChecklist(plan.Session{}, plan.Task{})
	if out != "" {
		t.Errorf("empty task should produce empty checklist:\n%s", out)
	}
}

// --- Smart 22: buildTaskSupervisor ---

func TestBuildTaskSupervisor_HappyPath(t *testing.T) {
	session := plan.Session{
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{
				File:    "crates/persys-concern/Cargo.toml",
				Pattern: `name = "persys-concern"`,
			}},
			{ID: "AC2", ContentMatch: &plan.ContentMatchCriterion{
				File:    "crates/persys-concern/src/lib.rs",
				Pattern: "pub struct Concern",
			}},
		},
	}
	task := plan.Task{
		ID: "T1",
		Files: []string{
			"crates/persys-concern/Cargo.toml",
			"crates/persys-concern/src/lib.rs",
		},
	}
	sup := buildTaskSupervisor("/tmp/project", session, task, 3)
	if sup == nil {
		t.Fatal("expected non-nil supervisor")
	}
	if sup.WorkDir != "/tmp/project" {
		t.Errorf("workdir = %q", sup.WorkDir)
	}
	if sup.WritesPerCheck != 3 {
		t.Errorf("WritesPerCheck = %d", sup.WritesPerCheck)
	}
	if len(sup.Expectations) != 2 {
		t.Errorf("expected 2 expectations, got %d", len(sup.Expectations))
	}
	// Each expectation should have its content_match pattern as a MustContain
	var cargo, lib *taskFileExpectation
	for i := range sup.Expectations {
		if sup.Expectations[i].File == "crates/persys-concern/Cargo.toml" {
			cargo = &sup.Expectations[i]
		}
		if sup.Expectations[i].File == "crates/persys-concern/src/lib.rs" {
			lib = &sup.Expectations[i]
		}
	}
	if cargo == nil || len(cargo.MustContain) != 1 || cargo.MustContain[0] != `name = "persys-concern"` {
		t.Errorf("cargo expectations wrong: %+v", cargo)
	}
	if lib == nil || len(lib.MustContain) != 1 || lib.MustContain[0] != "pub struct Concern" {
		t.Errorf("lib expectations wrong: %+v", lib)
	}
}

func TestBuildTaskSupervisor_NoFilesReturnsNil(t *testing.T) {
	sup := buildTaskSupervisor("/tmp", plan.Session{}, plan.Task{ID: "T1"}, 3)
	if sup != nil {
		t.Errorf("expected nil when task has no Files, got %+v", sup)
	}
}

func TestBuildTaskSupervisor_NoContentMatchReturnsNil(t *testing.T) {
	// Task has files but no content_match criteria — nothing to check,
	// so supervisor should be skipped.
	task := plan.Task{ID: "T1", Files: []string{"foo.rs"}}
	sup := buildTaskSupervisor("/tmp", plan.Session{}, task, 3)
	if sup != nil {
		t.Errorf("expected nil when no content_match criteria, got %+v", sup)
	}
}

func TestBuildTaskSupervisor_DefaultsWritesPerCheck(t *testing.T) {
	session := plan.Session{
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{File: "a.rs", Pattern: "X"}},
		},
	}
	task := plan.Task{Files: []string{"a.rs"}}
	sup := buildTaskSupervisor("/tmp", session, task, 0)
	if sup == nil || sup.WritesPerCheck != 3 {
		t.Errorf("WritesPerCheck should default to 3, got %+v", sup)
	}
}

func TestToEngineSupervisor_NilPassthrough(t *testing.T) {
	if toEngineSupervisor(nil) != nil {
		t.Error("nil spec should produce nil engine supervisor")
	}
}

func TestToEngineSupervisor_Translates(t *testing.T) {
	in := &specSupervisorSpec{
		WorkDir:        "/x",
		WritesPerCheck: 5,
		Expectations: []taskFileExpectation{
			{File: "a.rs", MustContain: []string{"foo", "bar"}},
			{File: "b.rs", MustNotContain: []string{"baz"}},
		},
	}
	out := toEngineSupervisor(in)
	if out == nil {
		t.Fatal("nil out")
	}
	if out.WorkDir != "/x" || out.WritesPerCheck != 5 || len(out.Expectations) != 2 {
		t.Errorf("translation lost fields: %+v", out)
	}
	if out.Expectations[0].File != "a.rs" || len(out.Expectations[0].MustContain) != 2 {
		t.Errorf("expectation[0] wrong: %+v", out.Expectations[0])
	}
	if out.Expectations[1].File != "b.rs" || len(out.Expectations[1].MustNotContain) != 1 {
		t.Errorf("expectation[1] wrong: %+v", out.Expectations[1])
	}
}

// --- splitIntoParagraphs ---

func TestSplitIntoParagraphs_ByBlankLine(t *testing.T) {
	s := "first para\nline two of first\n\nsecond para\n\nthird"
	paras := splitIntoParagraphs(s)
	if len(paras) != 3 {
		t.Errorf("expected 3 paragraphs, got %d: %v", len(paras), paras)
	}
}

func TestSplitIntoParagraphs_ByHeading(t *testing.T) {
	s := "# Section 1\n\ncontent of section 1\n\n## Section 2\ncontent of section 2"
	paras := splitIntoParagraphs(s)
	if len(paras) < 3 {
		t.Errorf("headings should break paragraphs, got %d: %v", len(paras), paras)
	}
	foundHeader := false
	for _, p := range paras {
		if strings.HasPrefix(p, "# Section 1") {
			foundHeader = true
		}
	}
	if !foundHeader {
		t.Error("heading paragraph should appear")
	}
}

// --- WorkDir + RawSOW + SpecExcerpt in the actual user prompt ---

func TestBuildSOWNativePromptsWithOpts_InjectsWorkDir(t *testing.T) {
	sys, _ := buildSOWNativePromptsWithOpts(&plan.SOW{Name: "X"}, plan.Session{ID: "S1"}, plan.Task{ID: "T1"}, promptOpts{
		RepoRoot: "/abs/project/root",
	})
	if !strings.Contains(sys, "WORKING DIRECTORY (absolute): /abs/project/root") {
		t.Errorf("system prompt should include WORKING DIRECTORY anchor:\n%s", sys)
	}
	if !strings.Contains(sys, "Cargo.toml") {
		// The anchor explanation includes a Cargo.toml example
		t.Errorf("system prompt should explain relative-path semantics")
	}
}

func TestBuildSOWNativePromptsWithOpts_InjectsSpecExcerptIntoUser(t *testing.T) {
	sow := &plan.SOW{ID: "x", Name: "X"}
	session := plan.Session{
		ID: "S1",
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", ContentMatch: &plan.ContentMatchCriterion{
				File:    "crates/persys-concern/src/lib.rs",
				Pattern: "pub struct Concern",
			}},
		},
	}
	task := plan.Task{
		ID:          "T5",
		Description: "Create error.rs and concern.rs",
		Files:       []string{"crates/persys-concern/src/lib.rs"},
	}
	rawSOW := `# PERSYS

## persys-concern crate

The Concern struct has 11 streams. This is the authoritative spec.
`
	_, user := buildSOWNativePromptsWithOpts(sow, session, task, promptOpts{
		RawSOW:  rawSOW,
		RepoRoot: "/tmp",
	})
	if !strings.Contains(user, "SPEC EXCERPT") {
		t.Errorf("user prompt should include SPEC EXCERPT header:\n%s", user)
	}
	if !strings.Contains(user, "11 streams") {
		t.Errorf("user prompt should include the authoritative spec content:\n%s", user)
	}
	if !strings.Contains(user, "BEFORE YOU WRITE ANY CODE") {
		t.Errorf("user prompt should include the identifier checklist:\n%s", user)
	}
	if !strings.Contains(user, "pub struct Concern") {
		t.Errorf("user prompt should include the canonical identifier:\n%s", user)
	}
}

func TestBuildSOWNativePromptsWithOpts_NoRawSOWNoExcerpt(t *testing.T) {
	_, user := buildSOWNativePromptsWithOpts(&plan.SOW{Name: "X"}, plan.Session{}, plan.Task{ID: "T1", Description: "do x"}, promptOpts{})
	if strings.Contains(user, "SPEC EXCERPT") {
		t.Error("no raw SOW should produce no SPEC EXCERPT section")
	}
}
