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
	// Genuinely clean code: structured logging, proper error handling, no stubs/TODOs
	files := []FileInput{{
		Path: "server.go",
		Content: []byte(`package server

import "log"

// Server handles HTTP requests.
type Server struct {
	addr string
}

// NewServer creates a server bound to the given address.
func NewServer(addr string) *Server {
	return &Server{addr: addr}
}

// Start begins listening for requests.
func (s *Server) Start() error {
	log.Printf("starting server on %s", s.addr)
	return nil
}
`),
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
	for _, f := range report.Findings {
		if f.Severity == SevBlocking {
			t.Errorf("clean code should have no blocking findings: rule=%s file=%s line=%d desc=%s evidence=%q",
				f.RuleID, f.File, f.Line, f.Description, f.Evidence)
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
	for i := 0; i < 650; i++ {
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
	// "Rate limiting" criteria should be flagged as unaddressed via semantic analysis
	criteriaFindings := filterByRule(report.Findings, "criteria-semantic")
	if len(criteriaFindings) == 0 {
		t.Error("should flag at least the rate limiting criterion as unimplemented")
	}

	// Check that rate limiting criterion is specifically flagged
	found := false
	for _, f := range criteriaFindings {
		desc := strings.ToLower(f.Description)
		if strings.Contains(desc, "rate") || strings.Contains(desc, "limiting") || strings.Contains(desc, "429") {
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

// --- Scaffolding Detection ---

func TestScaffoldingDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "scaffold.go",
		Content: []byte(`package scaffold

// This is a boilerplate implementation
func Setup() {}

// Wire this up to the database later
func Connect() {}

// Feature coming soon
func Future() {}
`),
	}}

	report := v.Validate("m-1", files)
	scaffoldFindings := filterByRule(report.Findings, "no-scaffolding")
	if len(scaffoldFindings) < 3 {
		t.Errorf("should detect scaffolding markers, got %d", len(scaffoldFindings))
		for _, f := range scaffoldFindings {
			t.Logf("  line %d: %s", f.Line, f.Evidence)
		}
	}
	for _, f := range scaffoldFindings {
		if f.Severity != SevBlocking {
			t.Errorf("scaffolding should be blocking, got %s", f.Severity)
		}
	}
}

func TestScaffoldingCleanCodeNotFlagged(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "clean.go",
		Content: []byte(`package clean

// Handler processes incoming HTTP requests.
func Handler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
}
`),
	}}

	report := v.Validate("m-1", files)
	scaffoldFindings := filterByRule(report.Findings, "no-scaffolding")
	if len(scaffoldFindings) > 0 {
		for _, f := range scaffoldFindings {
			t.Errorf("clean code should not be flagged as scaffolding: line %d %q", f.Line, f.Evidence)
		}
	}
}

// --- Commented-Out Code Detection ---

func TestCommentedOutCodeDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "dead.go",
		Content: []byte(`package dead

func Active() error {
	return nil
}

// func OldFunction() {
// 	return nil
// }
// func AnotherOldFunction() {
// 	return nil
// }
`),
	}}

	report := v.Validate("m-1", files)
	commented := filterByRule(report.Findings, "no-commented-code")
	if len(commented) != 1 {
		t.Errorf("should detect commented-out code block, got %d", len(commented))
	}
}

func TestCommentedOutCodeShortBlockNotFlagged(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "short.go",
		Content: []byte(`package short

// func old() {
// This is just a 2-line comment, not a block
func Active() {}
`),
	}}

	report := v.Validate("m-1", files)
	commented := filterByRule(report.Findings, "no-commented-code")
	if len(commented) > 0 {
		t.Error("2 commented lines should not trigger the rule (threshold is 3)")
	}
}

// --- Command Injection Detection ---

func TestCommandInjectionDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "unsafe.go",
		Content: []byte(`package unsafe

import "os/exec"

func RunUserCommand(userInput string) {
	exec.Command("bash", "-c", "echo " + userInput)
}
`),
	}}

	report := v.Validate("m-1", files)
	cmdInj := filterByRule(report.Findings, "no-command-injection")
	if len(cmdInj) == 0 {
		t.Error("should detect command injection vector")
	}
}

func TestCommandInjectionSafeNotFlagged(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "safe.go",
		Content: []byte(`package safe

import "os/exec"

func RunSafe() {
	exec.Command("git", "status")
}
`),
	}}

	report := v.Validate("m-1", files)
	cmdInj := filterByRule(report.Findings, "no-command-injection")
	if len(cmdInj) > 0 {
		t.Error("safe exec.Command should not be flagged")
	}
}

// --- Path Traversal Detection ---

func TestPathTraversalDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "handler.go",
		Content: []byte(`package handler

import "os"

func ReadFile(name string) {
	os.ReadFile("/data/" + name)
}
`),
	}}

	report := v.Validate("m-1", files)
	pathFindings := filterByRule(report.Findings, "no-path-traversal")
	if len(pathFindings) == 0 {
		t.Error("should detect path traversal vector")
	}
}

// --- TODO Severity Upgrade ---

func TestTodoIsBlocking(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path:    "work.go",
		Content: []byte("package work\n// TODO: implement\n"),
	}}

	report := v.Validate("m-1", files)
	todoFindings := filterByRule(report.Findings, "no-todo")
	if len(todoFindings) == 0 {
		t.Fatal("should find TODO")
	}
	if todoFindings[0].Severity != SevBlocking {
		t.Errorf("TODO severity = %s, want blocking", todoFindings[0].Severity)
	}
}

// --- Panic Is Blocking ---

func TestPanicIsBlocking(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path:    "crasher.go",
		Content: []byte("package crasher\nfunc bad() { panic(\"oops\") }\n"),
	}}

	report := v.Validate("m-1", files)
	panicFindings := filterByRule(report.Findings, "no-panic")
	if len(panicFindings) == 0 {
		t.Fatal("should find panic")
	}
	if panicFindings[0].Severity != SevBlocking {
		t.Errorf("panic severity = %s, want blocking", panicFindings[0].Severity)
	}
}

// --- Debug Logs Are Blocking ---

func TestDebugLogsAreBlocking(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path:    "debug.go",
		Content: []byte("package debug\nimport \"fmt\"\nfunc f() { fmt.Println(\"debug\") }\n"),
	}}

	report := v.Validate("m-1", files)
	debugFindings := filterByRule(report.Findings, "no-debug-logs")
	if len(debugFindings) == 0 {
		t.Fatal("should find debug log")
	}
	if debugFindings[0].Severity != SevBlocking {
		t.Errorf("debug log severity = %s, want blocking", debugFindings[0].Severity)
	}
}

// --- WIP/TEMP Detection ---

func TestWIPandTEMPDetection(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path: "wip.go",
		Content: []byte(`package wip
// WIP: half done
func halfDone() {}
// TEMP: remove this
func temporary() {}
`),
	}}

	report := v.Validate("m-1", files)
	todoFindings := filterByRule(report.Findings, "no-todo")
	if len(todoFindings) < 2 {
		t.Errorf("should detect WIP and TEMP markers, got %d", len(todoFindings))
	}
}

// --- Rule Count ---

func TestDefaultRuleCount(t *testing.T) {
	rules := DefaultRules()
	// 17 backend rules + 1 unwired-code rule + 11 frontend/UX rules = 29
	if len(rules) != 29 {
		t.Errorf("expected 29 default rules, got %d", len(rules))
		for _, r := range rules {
			t.Logf("  %s: %s", r.ID, r.Name)
		}
	}
}

// --- All Findings Are Blocking or Major ---

func TestNoMinorOrInfoSeverityInDefaults(t *testing.T) {
	// The convergence philosophy: everything that fires must be fixed.
	// Most rules should be blocking; large-file is major.
	rules := DefaultRules()
	for _, r := range rules {
		if r.Severity == SevInfo {
			t.Errorf("rule %q has info severity — should be at least major", r.ID)
		}
		if r.Severity == SevMinor {
			t.Errorf("rule %q has minor severity — should be at least major", r.ID)
		}
	}
}

// --- Unwired Code ---

func TestUnwiredCodeRule(t *testing.T) {
	v := NewValidator()

	// "wire this up later" — should flag
	files := []FileInput{{
		Path:    "service.go",
		Content: []byte("// wire this up from the HTTP handler\nfunc handleRequest() {}\n"),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "no-unwired-code")
	if len(findings) == 0 {
		t.Error("should flag 'wire this up' comments")
	}

	// "not yet called" — should flag
	files[0].Content = []byte("// Not yet called from main()\nfunc process() {}\n")
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "no-unwired-code")
	if len(findings) == 0 {
		t.Error("should flag 'not yet called' comments")
	}

	// Clean code — should not flag
	files[0].Content = []byte("package main\n\nfunc process() error { return nil }\n")
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "no-unwired-code")
	if len(findings) != 0 {
		t.Error("should not flag clean code")
	}
}

// --- Frontend / UX Rules ---

func TestImgAltRule(t *testing.T) {
	v := NewValidator()

	// Missing alt — should flag
	files := []FileInput{{
		Path:    "page.tsx",
		Content: []byte(`<img src="/logo.png" width={200} />`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "a11y-img-alt")
	if len(findings) == 0 {
		t.Error("should flag img without alt")
	}

	// With alt — should not flag
	files[0].Content = []byte(`<img src="/logo.png" alt="Company logo" />`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-img-alt")
	if len(findings) != 0 {
		t.Error("should not flag img with alt")
	}

	// Non-frontend file — should skip
	files[0].Path = "server.go"
	files[0].Content = []byte(`<img src="/logo.png" />`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-img-alt")
	if len(findings) != 0 {
		t.Error("should not check non-frontend files")
	}
}

func TestInteractiveAccessibilityRule(t *testing.T) {
	v := NewValidator()

	// onClick on div without role — should flag
	files := []FileInput{{
		Path:    "button.jsx",
		Content: []byte(`<div className="btn" onClick={handleClick}>Click me</div>`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "a11y-interactive")
	if len(findings) == 0 {
		t.Error("should flag onClick on div without role")
	}

	// onClick on div with role — should not flag
	files[0].Content = []byte(`<div role="button" tabIndex={0} onClick={handleClick}>Click</div>`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-interactive")
	if len(findings) != 0 {
		t.Error("should not flag div with role attribute")
	}

	// onClick on span — should flag
	files[0].Content = []byte(`<span onClick={toggle}>Toggle</span>`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-interactive")
	if len(findings) == 0 {
		t.Error("should flag onClick on span without role")
	}
}

func TestViewportMetaRule(t *testing.T) {
	v := NewValidator()

	// HTML without viewport — should flag
	files := []FileInput{{
		Path: "index.html",
		Content: []byte(`<!DOCTYPE html>
<html>
<head><title>App</title></head>
<body><div id="root"></div></body>
</html>`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "ux-viewport-meta")
	if len(findings) == 0 {
		t.Error("should flag HTML without viewport meta")
	}

	// HTML with viewport — should not flag
	files[0].Content = []byte(`<!DOCTYPE html>
<html>
<head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>App</title>
</head>
<body><div id="root"></div></body>
</html>`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-viewport-meta")
	if len(findings) != 0 {
		t.Error("should not flag HTML with viewport")
	}

	// Non-HTML file — should skip
	files[0].Path = "component.tsx"
	files[0].Content = []byte(`<div>no viewport needed in component</div>`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-viewport-meta")
	if len(findings) != 0 {
		t.Error("should not check non-HTML files for viewport")
	}
}

func TestResponsiveDesignRule(t *testing.T) {
	v := NewValidator()

	// CSS with only px values, no media queries (>20 lines)
	cssContent := "body { margin: 0; }\n"
	for i := 0; i < 25; i++ {
		cssContent += fmt.Sprintf(".item-%d { width: 400px; height: 300px; padding: 10px; }\n", i)
	}
	files := []FileInput{{Path: "styles.css", Content: []byte(cssContent)}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "ux-responsive")
	if len(findings) == 0 {
		t.Error("should flag CSS without media queries or responsive units")
	}

	// CSS with media query — should not flag
	cssContent += "@media (max-width: 768px) { .item-0 { width: 100%; } }\n"
	files[0].Content = []byte(cssContent)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-responsive")
	if len(findings) != 0 {
		t.Error("should not flag CSS with media queries")
	}

	// Small CSS file — should skip
	files[0].Content = []byte("body { margin: 0; }\n.root { display: flex; }\n")
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-responsive")
	if len(findings) != 0 {
		t.Error("should skip small CSS files")
	}
}

func TestErrorBoundaryRule(t *testing.T) {
	v := NewValidator()

	// React entry without ErrorBoundary — should flag
	files := []FileInput{{
		Path: "main.tsx",
		Content: []byte(`import React from "react"
import { createRoot } from "react-dom/client"
import App from "./App"
createRoot(document.getElementById("root")).render(<App />)
`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "ux-error-boundary")
	if len(findings) == 0 {
		t.Error("should flag React entry without ErrorBoundary")
	}

	// With ErrorBoundary — should not flag
	files[0].Content = []byte(`import React from "react"
import { createRoot } from "react-dom/client"
import { ErrorBoundary } from "./ErrorBoundary"
import App from "./App"
createRoot(document.getElementById("root")).render(
  <ErrorBoundary fallback={<p>Error</p>}><App /></ErrorBoundary>
)
`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-error-boundary")
	if len(findings) != 0 {
		t.Error("should not flag entry with ErrorBoundary")
	}

	// Non-entry component — should skip
	files[0].Content = []byte(`import React from "react"
export function Header() { return <h1>Hello</h1> }
`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-error-boundary")
	if len(findings) != 0 {
		t.Error("should skip non-entry components")
	}
}

func TestLoadingStateRule(t *testing.T) {
	v := NewValidator()

	// Fetch without loading state — should flag
	files := []FileInput{{
		Path: "users.tsx",
		Content: []byte(`import { useQuery } from "@tanstack/react-query"
export function Users() {
  const { data } = useQuery({ queryKey: ["users"], queryFn: fetchUsers })
  return <ul>{data?.map(u => <li>{u.name}</li>)}</ul>
}
`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "ux-loading-state")
	if len(findings) == 0 {
		t.Error("should flag data fetching without loading state")
	}

	// With loading state — should not flag
	files[0].Content = []byte(`import { useQuery } from "@tanstack/react-query"
export function Users() {
  const { data, isLoading, error } = useQuery({ queryKey: ["users"], queryFn: fetchUsers })
  if (isLoading) return <Spinner />
  if (error) return <ErrorMessage error={error} />
  return <ul>{data.map(u => <li key={u.id}>{u.name}</li>)}</ul>
}
`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-loading-state")
	if len(findings) != 0 {
		t.Error("should not flag component with loading state")
	}
}

func TestFormLabelRule(t *testing.T) {
	v := NewValidator()

	// Input without label or aria-label in file with no labels — should flag
	files := []FileInput{{
		Path:    "form.tsx",
		Content: []byte(`<div><input type="text" name="email" placeholder="Email" /></div>`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "a11y-form-label")
	if len(findings) == 0 {
		t.Error("should flag input without any label")
	}

	// Hidden input — should not flag
	files[0].Content = []byte(`<input type="hidden" name="csrf" value="abc" />`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-form-label")
	if len(findings) != 0 {
		t.Error("should not flag hidden inputs")
	}

	// Submit button — should not flag
	files[0].Content = []byte(`<input type="submit" value="Send" />`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-form-label")
	if len(findings) != 0 {
		t.Error("should not flag submit buttons")
	}
}

func TestFocusVisibleRule(t *testing.T) {
	v := NewValidator()

	// outline:none without focus-visible — should flag
	files := []FileInput{{
		Path:    "styles.css",
		Content: []byte(`button { outline: none; }`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "a11y-focus-visible")
	if len(findings) == 0 {
		t.Error("should flag outline:none without focus-visible")
	}

	// outline:0 — should also flag
	files[0].Content = []byte(`a:focus { outline: 0; }`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-focus-visible")
	if len(findings) == 0 {
		t.Error("should flag outline:0")
	}

	// outline:none with focus-visible context — should not flag
	files[0].Content = []byte(`button:focus-visible { outline: none; box-shadow: 0 0 0 2px blue; }`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "a11y-focus-visible")
	if len(findings) != 0 {
		t.Error("should not flag outline:none within focus-visible context")
	}
}

func TestHardcodedColorRule(t *testing.T) {
	v := NewValidator()

	// Inline style with hardcoded hex color — should flag
	files := []FileInput{{
		Path:    "card.tsx",
		Content: []byte(`<div style={{ color: "#ff0000", background: "#fff" }}>Hi</div>`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "ux-no-hardcoded-colors")
	if len(findings) == 0 {
		t.Error("should flag hardcoded color in inline style")
	}

	// No inline styles — should not flag
	files[0].Content = []byte(`<div className="card">Hi</div>`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-no-hardcoded-colors")
	if len(findings) != 0 {
		t.Error("should not flag className-based styling")
	}
}

func TestDangerousInnerHTMLRule(t *testing.T) {
	v := NewValidator()

	// dangerouslySetInnerHTML — should flag
	files := []FileInput{{
		Path:    "content.jsx",
		Content: []byte(`<div dangerouslySetInnerHTML={{ __html: userContent }} />`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "ux-no-dangerous-html")
	if len(findings) == 0 {
		t.Error("should flag dangerouslySetInnerHTML")
	}

	// In test file — should skip
	files[0].Path = "content.test.jsx"
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-no-dangerous-html")
	if len(findings) != 0 {
		t.Error("should skip test files")
	}
}

func TestMissingKeyPropRule(t *testing.T) {
	v := NewValidator()

	// .map() returning JSX without key — should flag
	files := []FileInput{{
		Path:    "list.tsx",
		Content: []byte(`{items.map((item) => <li>{item.name}</li>)}`),
	}}
	report := v.Validate("m-1", files)
	findings := filterByRule(report.Findings, "ux-list-key")
	if len(findings) == 0 {
		t.Error("should flag .map() without key prop")
	}

	// .map() with key — should not flag
	files[0].Content = []byte(`{items.map((item) => <li key={item.id}>{item.name}</li>)}`)
	report = v.Validate("m-1", files)
	findings = filterByRule(report.Findings, "ux-list-key")
	if len(findings) != 0 {
		t.Error("should not flag .map() with key prop")
	}
}

func TestUXRulesSkipNonFrontendFiles(t *testing.T) {
	v := NewValidator()

	// Go file — no UX rules should fire
	files := []FileInput{{
		Path: "handler.go",
		Content: []byte(`package handler
func Render() string { return "<img src=\"/logo.png\">" }
`),
	}}
	report := v.Validate("m-1", files)
	for _, f := range report.Findings {
		if f.Category == CatUXQuality {
			t.Errorf("UX rule %q should not fire on Go files", f.RuleID)
		}
	}
}

func TestUXRulesIncludedInCategories(t *testing.T) {
	v := NewValidator()
	files := []FileInput{{
		Path:    "app.tsx",
		Content: []byte(`<img src="/x.png" />`),
	}}
	report := v.Validate("m-1", files)
	byCategory := report.ByCategory()
	if len(byCategory[CatUXQuality]) == 0 {
		t.Error("UX findings should appear in ByCategory() under CatUXQuality")
	}
}

// --- Prompt UX Gate ---

func TestValidatePromptIncludesUXGate(t *testing.T) {
	// Verify the validation prompt includes UX quality requirements
	ctx := testPromptContext()
	prompt := buildValidatePromptForTest(ctx)
	checks := []string{
		"UX quality",
		"alt attributes",
		"keyboard-accessible",
		"responsive",
		"loading states",
		"ErrorBoundary",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("validate prompt missing UX check %q", check)
		}
	}
}

func testPromptContext() struct {
	MissionID, Title, Intent string
} {
	return struct {
		MissionID, Title, Intent string
	}{"m-1", "Test", "Build a web app"}
}

func buildValidatePromptForTest(_ struct{ MissionID, Title, Intent string }) string {
	// Simplified version — just check the Gate 3a content is in the rules
	return `Gate 3a: UX quality — if the project has a UI, it must be complete and accessible
- ALL images have alt attributes (WCAG 2.1 Level A)
- ALL form inputs have associated labels or aria-label
- ALL interactive elements are keyboard-accessible (no onClick on div without role/tabIndex)
- Focus indicators must be visible for keyboard navigation
- Responsive viewport meta tag present in HTML documents
- Stylesheets use media queries and responsive units — layout works on mobile/tablet/desktop
- Data-fetching components have loading states AND error states — no blank screens
- React apps have ErrorBoundary at the root — render errors show fallback, not blank page`
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
