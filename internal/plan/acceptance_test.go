package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAcceptanceCriteriaVerifyFunc(t *testing.T) {
	ctx := context.Background()

	// Non-code executors populate VerifyFunc programmatically. The
	// descent engine treats the returned (passed, output) identically
	// to a bash command's exit code — same tier ladder, same
	// multi-analyst reasoning on failure.
	criteria := []AcceptanceCriterion{
		{
			ID:          "CLAIM-1",
			Description: "research claim supported by source",
			VerifyFunc: func(ctx context.Context) (bool, string) {
				return true, "fetched https://example.com — claim verified"
			},
		},
		{
			ID:          "CLAIM-2",
			Description: "research claim contradicted by source",
			VerifyFunc: func(ctx context.Context) (bool, string) {
				return false, "source says X; report says Y"
			},
		},
	}
	results, allPassed := CheckAcceptanceCriteria(ctx, t.TempDir(), criteria)
	if allPassed {
		t.Error("mixed pass/fail should not all pass")
	}
	if !results[0].Passed {
		t.Errorf("CLAIM-1 expected pass, got output=%q", results[0].Output)
	}
	if results[1].Passed {
		t.Error("CLAIM-2 expected fail")
	}
	if !strings.Contains(results[0].Output, "verified") {
		t.Errorf("CLAIM-1 output lost: %q", results[0].Output)
	}
}

func TestCheckAcceptanceCriteriaVerifyFuncBeatsCommand(t *testing.T) {
	// Backward-compat guard: when both VerifyFunc AND Command are
	// set, VerifyFunc wins. This ensures executor code that builds
	// its own AC can't be accidentally overridden by a SOW
	// Command field set by upstream parsing.
	ctx := context.Background()
	ac := AcceptanceCriterion{
		ID:      "PRIORITY",
		Command: "false", // would fail if evaluated
		VerifyFunc: func(ctx context.Context) (bool, string) {
			return true, "verify-func path taken"
		},
	}
	res, _ := CheckAcceptanceCriteria(ctx, t.TempDir(), []AcceptanceCriterion{ac})
	if !res[0].Passed {
		t.Errorf("VerifyFunc should override Command; got passed=false output=%q", res[0].Output)
	}
	if !strings.Contains(res[0].Output, "verify-func path") {
		t.Errorf("wrong path taken: %q", res[0].Output)
	}
}

func TestCheckAcceptanceCriteriaCommand(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	criteria := []AcceptanceCriterion{
		{ID: "AC1", Description: "echo succeeds", Command: "echo ok"},
		{ID: "AC2", Description: "false fails", Command: "false"},
	}

	results, allPassed := CheckAcceptanceCriteria(ctx, dir, criteria)
	if allPassed {
		t.Error("should not all pass (AC2 uses false)")
	}
	if len(results) != 2 {
		t.Fatalf("results=%d", len(results))
	}
	if !results[0].Passed {
		t.Errorf("AC1 should pass: %s", results[0].Output)
	}
	if results[1].Passed {
		t.Error("AC2 should fail")
	}
}

func TestCheckAcceptanceCriteriaFileExists(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "schema.sql"), []byte("CREATE TABLE;"), 0644)

	criteria := []AcceptanceCriterion{
		{ID: "AC1", Description: "schema exists", FileExists: "schema.sql"},
		{ID: "AC2", Description: "missing file", FileExists: "nonexistent.txt"},
	}

	results, allPassed := CheckAcceptanceCriteria(ctx, dir, criteria)
	if allPassed {
		t.Error("should not all pass")
	}
	if !results[0].Passed {
		t.Errorf("AC1 should pass: %s", results[0].Output)
	}
	if results[1].Passed {
		t.Error("AC2 should fail")
	}
}

func TestCheckAcceptanceCriteriaContentMatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.rs"), []byte("pub struct AppConfig {\n    pub db_url: String,\n}"), 0644)

	criteria := []AcceptanceCriterion{
		{
			ID: "AC1", Description: "config has db_url",
			ContentMatch: &ContentMatchCriterion{File: "config.rs", Pattern: "db_url"},
		},
		{
			ID: "AC2", Description: "config has redis_url",
			ContentMatch: &ContentMatchCriterion{File: "config.rs", Pattern: "redis_url"},
		},
	}

	results, allPassed := CheckAcceptanceCriteria(ctx, dir, criteria)
	if allPassed {
		t.Error("should not all pass")
	}
	if !results[0].Passed {
		t.Errorf("AC1 should pass: %s", results[0].Output)
	}
	if results[1].Passed {
		t.Error("AC2 should fail")
	}
}

func TestCheckAcceptanceCriteriaNoCheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Description-only criterion with no automated check
	criteria := []AcceptanceCriterion{
		{ID: "AC1", Description: "code review complete"},
	}

	results, allPassed := CheckAcceptanceCriteria(ctx, dir, criteria)
	if !allPassed {
		t.Error("description-only should pass by default")
	}
	if !results[0].Passed {
		t.Error("AC1 should pass")
	}
	if !strings.Contains(results[0].Output, "manual") {
		t.Errorf("should mention manual verification: %s", results[0].Output)
	}
}

func TestCheckAcceptanceCriteriaEmpty(t *testing.T) {
	ctx := context.Background()
	results, allPassed := CheckAcceptanceCriteria(ctx, t.TempDir(), nil)
	if !allPassed {
		t.Error("empty criteria should pass")
	}
	if len(results) != 0 {
		t.Errorf("results=%d", len(results))
	}
}

func TestFormatAcceptanceResults(t *testing.T) {
	results := []AcceptanceResult{
		{CriterionID: "AC1", Description: "build passes", Passed: true},
		{CriterionID: "AC2", Description: "tests pass", Passed: false, Output: "3 tests failed"},
	}
	formatted := FormatAcceptanceResults(results)
	if !strings.Contains(formatted, "[PASS]") {
		t.Error("should contain PASS")
	}
	if !strings.Contains(formatted, "[FAIL]") {
		t.Error("should contain FAIL")
	}
	if !strings.Contains(formatted, "1/2 criteria passed") {
		t.Errorf("should show 1/2: %s", formatted)
	}
}

func TestCheckAcceptanceCriteriaContentMatchMissingFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	criteria := []AcceptanceCriterion{
		{
			ID: "AC1", Description: "missing file",
			ContentMatch: &ContentMatchCriterion{File: "nonexistent.rs", Pattern: "anything"},
		},
	}

	results, allPassed := CheckAcceptanceCriteria(ctx, dir, criteria)
	if allPassed {
		t.Error("should fail for missing file")
	}
	if results[0].Passed {
		t.Error("should fail")
	}
	if !strings.Contains(results[0].Output, "cannot read") {
		t.Errorf("should mention cannot read: %s", results[0].Output)
	}
}
