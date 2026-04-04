package convergence

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// --- Rule Engine ---

func TestDefaultRulesAllEnabled(t *testing.T) {
	rules := DefaultRules()
	if len(rules) == 0 {
		t.Fatal("DefaultRules should return rules")
	}
	for _, r := range rules {
		if !r.Enabled {
			t.Errorf("rule %q should be enabled by default", r.ID)
		}
		if r.ID == "" || r.Name == "" || r.Category == "" || r.Severity == "" {
			t.Errorf("rule %q has empty required fields", r.ID)
		}
	}
}

func TestValidateCleanCode(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path:    "main.go",
		Content: []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"),
	}}

	report := v.Validate("test-mission", files)
	if report.MissionID != "test-mission" {
		t.Errorf("MissionID = %q", report.MissionID)
	}
	if report.RulesApplied == 0 {
		t.Error("should apply rules")
	}
	if report.Duration == 0 {
		t.Error("should record duration")
	}
	// fmt.Println is flagged by debug-log rule, but only as minor
	for _, f := range report.Findings {
		if f.Severity == SevBlocking {
			t.Errorf("clean code should have no blocking findings: %s", f.Description)
		}
	}
}

// --- TODO/FIXME Detection ---

func TestTodoDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "handler.go",
		Content: []byte(`package handler

func Handle() error {
	// TODO: implement real logic
	return nil
}

// FIXME: this is broken
func Broken() {}

// HACK: workaround for now
func Workaround() {}
`),
	}}

	report := v.Validate("m-1", files)
	todoFindings := filterByRule(report.Findings, "no-todo")
	if len(todoFindings) != 3 {
		t.Errorf("should find 3 TODO/FIXME/HACK markers, got %d", len(todoFindings))
		for _, f := range todoFindings {
			t.Logf("  %s:%d %s", f.File, f.Line, f.Evidence)
		}
	}
	for _, f := range todoFindings {
		if f.File != "handler.go" {
			t.Errorf("file = %q, want handler.go", f.File)
		}
		if f.Line == 0 {
			t.Error("line number should be set")
		}
	}
}

// --- Stub Detection ---

func TestStubDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "service.go",
		Content: []byte(`package service

func Process() error {
	// not implemented
	return nil
}

func Other() string {
	return "placeholder"
}
`),
	}}

	report := v.Validate("m-1", files)
	stubs := filterByRule(report.Findings, "no-stubs")
	if len(stubs) < 2 {
		t.Errorf("should detect stub markers, got %d", len(stubs))
	}
	// Stubs are blocking
	for _, f := range stubs {
		if f.Severity != SevBlocking {
			t.Errorf("stubs should be blocking, got %s", f.Severity)
		}
	}
}

// --- Secret Detection ---

func TestSecretDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "config.go",
		Content: []byte(`package config

const apiKey = "sk-1234567890abcdefghijklmnop"
const awsKey = "AKIAIOSFODNN7EXAMPLE1"
`),
	}}

	report := v.Validate("m-1", files)
	secrets := filterByRule(report.Findings, "no-secrets")
	if len(secrets) < 2 {
		t.Errorf("should detect hardcoded secrets, got %d", len(secrets))
	}
	for _, f := range secrets {
		if f.Severity != SevBlocking {
			t.Errorf("secrets should be blocking")
		}
		if f.Category != CatSecurity {
			t.Errorf("secrets should be security category")
		}
	}
}

func TestSecretDetectionSkipsTestFiles(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path:    "auth_test.go",
		Content: []byte(`package auth\n\nconst testKey = "sk-1234567890abcdefghijklmnop"\n`),
	}}

	report := v.Validate("m-1", files)
	secrets := filterByRule(report.Findings, "no-secrets")
	if len(secrets) > 0 {
		t.Error("should not flag secrets in test files")
	}
}

// --- Empty Test Detection ---

func TestEmptyTestDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "service_test.go",
		Content: []byte(`package service

import "testing"

func TestSomething(t *testing.T) {
}

func TestReal(t *testing.T) {
	if 1+1 != 2 {
		t.Error("math is broken")
	}
}
`),
	}}

	report := v.Validate("m-1", files)
	empties := filterByRule(report.Findings, "no-empty-tests")
	if len(empties) != 1 {
		t.Errorf("should detect 1 empty test, got %d", len(empties))
	}
}

// --- Tautological Test Detection ---

func TestTautologicalTestDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "useless_test.go",
		Content: []byte(`package useless

import "testing"

func TestAlwaysTrue(t *testing.T) {
	if true {
		// this always runs
	}
}
`),
	}}

	report := v.Validate("m-1", files)
	tautological := filterByRule(report.Findings, "no-tautological-tests")
	if len(tautological) == 0 {
		t.Error("should detect tautological test pattern")
	}
}

// --- Type Bypass Detection ---

func TestTypeBypassDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{
		{Path: "handler.ts", Content: []byte(`const x = foo as any;\n// @ts-ignore\nbar();\n`)},
		{Path: "lint.py", Content: []byte(`x = 1  # type: ignore\n`)},
	}

	report := v.Validate("m-1", files)
	bypasses := filterByRule(report.Findings, "no-type-bypass")
	if len(bypasses) < 2 {
		t.Errorf("should detect type bypasses, got %d", len(bypasses))
	}
}

// --- Large File Detection ---

func TestLargeFileDetection(t *testing.T) {
	v := NewValidator()
	var large strings.Builder
	for i := 0; i < 600; i++ {
		large.WriteString(fmt.Sprintf("line %d\n", i))
	}
	files := []FileInput{{Path: "huge.go", Content: []byte(large.String())}}

	report := v.Validate("m-1", files)
	largeFindings := filterByRule(report.Findings, "no-large-files")
	if len(largeFindings) != 1 {
		t.Errorf("should detect large file, got %d", len(largeFindings))
	}
}

func TestSmallFileNotFlagged(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{Path: "small.go", Content: []byte("package small\n\nfunc Hello() {}\n")}}

	report := v.Validate("m-1", files)
	largeFindings := filterByRule(report.Findings, "no-large-files")
	if len(largeFindings) != 0 {
		t.Error("small file should not be flagged")
	}
}

// --- SQL Injection Detection ---

func TestSQLInjectionDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "db.go",
		Content: []byte(`package db

import "database/sql"

func GetUser(db *sql.DB, name string) {
	db.Query("SELECT * FROM users WHERE name='" + name + "'")
}
`),
	}}

	report := v.Validate("m-1", files)
	sqli := filterByRule(report.Findings, "no-sql-injection")
	if len(sqli) == 0 {
		t.Error("should detect SQL injection vector")
	}
}

// --- Scoring ---

func TestScoring(t *testing.T) {
	v := NewValidator()

	// Clean file should score near 1.0
	clean := v.Validate("m-1", []FileInput{{Path: "clean.go", Content: []byte("package clean\n")}})
	if clean.Score < 0.9 {
		t.Errorf("clean file score = %f, want >= 0.9", clean.Score)
	}

	// File with blocking issues should score low
	dirty := v.Validate("m-1", []FileInput{{
		Path: "dirty.go",
		Content: []byte(`package dirty
const key = "AKIAIOSFODNN7EXAMPLE1"
func broken() { /* not implemented */ }
`),
	}})
	if dirty.Score > 0.8 {
		t.Errorf("dirty file score = %f, want < 0.8", dirty.Score)
	}
}

func TestScoreFloorAtZero(t *testing.T) {
	v := NewValidator()
	// Generate many blocking findings
	var content strings.Builder
	content.WriteString("package bad\n")
	for i := 0; i < 20; i++ {
		content.WriteString(fmt.Sprintf("const key%d = \"AKIAIOSFODNN7EXAMPLE%d\"\n", i, i))
	}
	report := v.Validate("m-1", []FileInput{{Path: "bad.go", Content: []byte(content.String())}})
	if report.Score < 0 {
		t.Error("score should not go below 0")
	}
}

// --- Convergence ---

func TestIsConverged(t *testing.T) {
	v := NewValidator()

	// No blocking findings = converged
	clean := v.Validate("m-1", []FileInput{{Path: "ok.go", Content: []byte("package ok\n")}})
	if !clean.IsConverged {
		t.Error("clean code should be converged")
	}

	// Blocking findings = not converged
	dirty := v.Validate("m-1", []FileInput{{
		Path:    "stub.go",
		Content: []byte("package stub\n// not implemented\n"),
	}})
	if dirty.IsConverged {
		t.Error("stub code should not be converged")
	}
}

// --- Report Grouping ---

func TestReportByCategory(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "mixed.go",
		Content: []byte(`package mixed
// TODO: fix this
const secret = "AKIAIOSFODNN7EXAMPLE1"
`),
	}}

	report := v.Validate("m-1", files)
	byCat := report.ByCategory()
	if len(byCat) == 0 {
		t.Error("should have findings grouped by category")
	}
}

func TestReportBySeverity(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "mixed.go",
		Content: []byte(`package mixed
// TODO: fix this
const secret = "AKIAIOSFODNN7EXAMPLE1"
`),
	}}

	report := v.Validate("m-1", files)
	bySev := report.BySeverity()
	if len(bySev) == 0 {
		t.Error("should have findings grouped by severity")
	}
}

// --- Custom Rules ---

func TestCustomRule(t *testing.T) {
	v := NewValidatorWithRules([]Rule{{
		ID: "custom-check", Name: "Custom", Category: CatCodeQuality,
		Severity: SevBlocking, Enabled: true,
		Check: func(file string, content []byte) []Finding {
			if strings.Contains(string(content), "FORBIDDEN") {
				return []Finding{{
					RuleID: "custom-check", Category: CatCodeQuality,
					Severity: SevBlocking, File: file,
					Description: "Found FORBIDDEN keyword",
				}}
			}
			return nil
		},
	}})

	report := v.Validate("m-1", []FileInput{
		{Path: "good.go", Content: []byte("package good\n")},
		{Path: "bad.go", Content: []byte("package bad\n// FORBIDDEN\n")},
	})

	if report.BlockingCount() != 1 {
		t.Errorf("blocking count = %d, want 1", report.BlockingCount())
	}
	if report.Findings[0].File != "bad.go" {
		t.Errorf("finding file = %q", report.Findings[0].File)
	}
}

func TestAddRule(t *testing.T) {
	v := NewValidator()
	initialCount := len(DefaultRules())

	v.AddRule(Rule{
		ID: "extra", Name: "Extra", Category: CatCodeQuality,
		Severity: SevInfo, Enabled: true,
		Check: func(file string, content []byte) []Finding { return nil },
	})

	report := v.Validate("m-1", []FileInput{{Path: "x.go", Content: []byte("package x\n")}})
	if report.RulesApplied != initialCount+1 {
		t.Errorf("rules applied = %d, want %d", report.RulesApplied, initialCount+1)
	}
}

func TestEnableDisableRule(t *testing.T) {
	v := NewValidator()
	v.EnableRule("no-todo", false)

	report := v.Validate("m-1", []FileInput{{
		Path:    "todo.go",
		Content: []byte("package todo\n// TODO: fix\n"),
	}})

	todoFindings := filterByRule(report.Findings, "no-todo")
	if len(todoFindings) > 0 {
		t.Error("disabled rule should not produce findings")
	}
}

// --- Criteria Validation ---

func TestValidateWithCriteria(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "auth.go",
		Content: []byte(`package auth

func Login(username, password string) (string, error) {
	token := generateJWT(username)
	return token, nil
}

func ValidateToken(token string) error {
	return nil
}
`),
	}}

	criteria := []string{
		"JWT tokens are issued on login",
		"Rate limiting returns 429 after threshold",
	}

	report := v.ValidateWithCriteria("m-1", files, criteria)
	// "JWT tokens" criteria should be partially matched (jwt, token, login all appear)
	// "Rate limiting" criteria should be flagged as unaddressed
	criteriaFindings := filterByRule(report.Findings, "criterion-check")
	if len(criteriaFindings) == 0 {
		t.Error("should flag at least the rate limiting criterion as unimplemented")
	}

	// Check that rate limiting criterion is specifically flagged
	found := false
	for _, f := range criteriaFindings {
		if strings.Contains(f.Description, "Rate limiting") {
			found = true
		}
	}
	if !found {
		t.Error("should specifically flag the rate limiting criterion")
	}
}

// --- Concurrency ---

func TestConcurrentValidation(t *testing.T) {
	v := NewValidator()
	var wg sync.WaitGroup
	reports := make(chan *Report, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			files := []FileInput{{
				Path:    fmt.Sprintf("file_%d.go", n),
				Content: []byte(fmt.Sprintf("package p%d\n// TODO: fix\n", n)),
			}}
			reports <- v.Validate(fmt.Sprintf("m-%d", n), files)
		}(i)
	}
	wg.Wait()
	close(reports)

	count := 0
	for r := range reports {
		count++
		if r.RulesApplied == 0 {
			t.Error("concurrent validation should apply rules")
		}
	}
	if count != 20 {
		t.Errorf("got %d reports, want 20", count)
	}
}

// --- Empty Input ---

func TestValidateNoFiles(t *testing.T) {
	v := NewValidator()
	report := v.Validate("empty", nil)
	if len(report.Findings) != 0 {
		t.Error("no files should produce no findings")
	}
	if !report.IsConverged {
		t.Error("no files should be converged (vacuously true)")
	}
}

func TestValidateEmptyFile(t *testing.T) {
	v := NewValidator()
	report := v.Validate("m-1", []FileInput{{Path: "empty.go", Content: []byte{}}})
	for _, f := range report.Findings {
		if f.Severity == SevBlocking {
			t.Errorf("empty file should not have blocking findings: %s", f.Description)
		}
	}
}

// --- Summary ---

func TestSummaryFormat(t *testing.T) {
	v := NewValidator()
	report := v.Validate("m-1", []FileInput{{
		Path:    "messy.go",
		Content: []byte("package messy\n// TODO: cleanup\nconst key = \"AKIAIOSFODNN7EXAMPLE1\"\n"),
	}})
	if report.Summary == "" {
		t.Error("summary should not be empty when findings exist")
	}
	if !strings.Contains(report.Summary, "findings") {
		t.Errorf("summary should mention findings: %q", report.Summary)
	}
}

func TestCleanSummary(t *testing.T) {
	v := NewValidator()
	report := v.Validate("m-1", []FileInput{{
		Path:    "clean.go",
		Content: []byte("package clean\n"),
	}})
	if !strings.Contains(report.Summary, "converged") {
		t.Errorf("clean summary should say converged: %q", report.Summary)
	}
}

// --- Helpers ---

func filterByRule(findings []Finding, ruleID string) []Finding {
	var filtered []Finding
	for _, f := range findings {
		if f.RuleID == ruleID {
			filtered = append(filtered, f)
		}
	}
	return filtered
}
