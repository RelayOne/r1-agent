package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
