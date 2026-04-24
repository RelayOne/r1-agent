package plan

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeclaredSymbolsGate_HarnessPath exercises the sow-harness call
// site: RunQualitySweepForSOW with a scoped SOW (Sessions + Tasks +
// AcceptanceCriteria) to verify H-27 extracts from Session/Task/AC
// text and flags missing deliverables. Mirrors the exact shape
// sow_native.go builds at runtime (scopedSOW := &plan.SOW{Sessions:
// []plan.Session{session}}).
func TestDeclaredSymbolsGate_HarnessPath_H27(t *testing.T) {
	root := t.TempDir()
	file := "src/alarm.ts"
	abs := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	// Worker created the file but only implemented acknowledgeAlarm;
	// resolveAlarm is missing.
	if err := os.WriteFile(abs, []byte(`
export function acknowledgeAlarm(id: string) { return id; }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Scoped SOW as built by sow_native.go runSessionNative path.
	sow := &SOW{
		Sessions: []Session{{
			ID:          "S1",
			Title:       "Alarm handlers",
			Description: "Implement the acknowledgeAlarm handler and the resolveAlarm handler.",
			Tasks: []Task{{
				ID:          "T1",
				Description: "Add resolveAlarm to src/alarm.ts",
				Files:       []string{file},
			}},
			AcceptanceCriteria: []AcceptanceCriterion{{
				ID:          "AC1",
				Description: "Both acknowledgeAlarm and resolveAlarm must be exported.",
			}},
		}},
	}
	cfg := DefaultQualityConfig()
	report := RunQualitySweepWithConfig(root, []string{file}, sow, cfg)

	// H-27 must flag resolveAlarm as missing.
	found := 0
	for _, f := range report.Findings {
		if f.Kind == "declared-symbol-not-implemented" {
			found++
		}
	}
	if found == 0 {
		t.Fatalf("H-27 did not fire on sow-harness path; findings: %+v", report.Findings)
	}
	// Reverse check: acknowledgeAlarm was defined → must NOT be in
	// any finding detail.
	for _, f := range report.Findings {
		if f.Kind == "declared-symbol-not-implemented" &&
			containsSubstring(f.Detail, "acknowledgeAlarm") {
			t.Errorf("false positive: acknowledgeAlarm is defined, should not be flagged: %+v", f)
		}
	}
}

// TestDeclaredSymbolsGate_HarnessPath_H28 exercises the same path
// with STOKE_H27_TREESITTER=1 set so the tree-sitter variant fires.
func TestDeclaredSymbolsGate_HarnessPath_H28(t *testing.T) {
	os.Setenv("R1_H27_TREESITTER", "1")
	defer os.Unsetenv("R1_H27_TREESITTER")

	root := t.TempDir()
	file := "src/alarm.ts"
	abs := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(`
export function acknowledgeAlarm(id: string) { return id; }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	sow := &SOW{
		Sessions: []Session{{
			ID:          "S1",
			Title:       "Alarm handlers",
			Description: "Implement the acknowledgeAlarm handler and the resolveAlarm handler.",
			Tasks: []Task{{
				ID:          "T1",
				Description: "Add resolveAlarm to src/alarm.ts",
				Files:       []string{file},
			}},
		}},
	}
	cfg := DefaultQualityConfig()
	report := RunQualitySweepWithConfig(root, []string{file}, sow, cfg)

	// H-28's kind is declared-symbol-not-implemented-ts.
	foundH28 := 0
	for _, f := range report.Findings {
		if f.Kind == "declared-symbol-not-implemented-ts" {
			foundH28++
		}
	}
	if foundH28 == 0 {
		t.Fatalf("H-28 did not fire on sow-harness path; findings: %+v", report.Findings)
	}
	// Under STOKE_H27_TREESITTER=1 the regex variant must NOT fire
	// (they'd double-count). The call site dispatches one-or-other.
	for _, f := range report.Findings {
		if f.Kind == "declared-symbol-not-implemented" {
			t.Errorf("H-27 should be silent when STOKE_H27_TREESITTER=1: %+v", f)
		}
	}
}

// TestDeclaredSymbolsGate_HarnessPath_MultiLanguageSOW confirms H-27
// fires across mixed-language worker output in a sow run — the sow
// harness hits Go backends + TS frontends in the same cohort, so the
// gate has to work on both files declared in the same session.
func TestDeclaredSymbolsGate_HarnessPath_MultiLanguageSOW(t *testing.T) {
	root := t.TempDir()
	// Go handler: implements Process
	goFile := "cmd/worker/main.go"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(goFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, goFile), []byte(`package main
func Process(id string) string { return id }`), 0o600); err != nil {
		t.Fatal(err)
	}
	// TS handler: stub, no implementation
	tsFile := "apps/web/src/api/alerts.ts"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(tsFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, tsFile), []byte(`
// stub file — processAlert missing
export const VERSION = "0.0.1";
`), 0o600); err != nil {
		t.Fatal(err)
	}
	sow := &SOW{
		Sessions: []Session{{
			ID:          "S1",
			Title:       "Alert pipeline",
			Description: "Backend: a Process function in Go worker. Frontend: the processAlert handler in apps/web/src/api/alerts.ts.",
			Tasks: []Task{
				{ID: "T1", Description: "Go backend worker", Files: []string{goFile}},
				{ID: "T2", Description: "TS frontend handler", Files: []string{tsFile}},
			},
		}},
	}
	cfg := DefaultQualityConfig()
	report := RunQualitySweepWithConfig(root, []string{goFile, tsFile}, sow, cfg)

	// processAlert must be flagged; Process must NOT be.
	hasProcessAlert := false
	hasProcessFalsePositive := false
	for _, f := range report.Findings {
		if f.Kind == "declared-symbol-not-implemented" {
			if containsSubstring(f.Detail, "processAlert") {
				hasProcessAlert = true
			}
			if containsSubstring(f.Detail, "`Process`") && !containsSubstring(f.Detail, "processAlert") {
				hasProcessFalsePositive = true
			}
		}
	}
	if !hasProcessAlert {
		t.Errorf("expected processAlert to be flagged (TS stub); findings: %+v", report.Findings)
	}
	if hasProcessFalsePositive {
		t.Error("Process is defined in Go; must not be flagged as missing")
	}
}

// containsSubstring is a tiny helper to avoid pulling strings into
// the test file header (already imported via declared_symbols_test
// but kept local-ish for clarity).
func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && findIndex(s, sub) >= 0
}

func findIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
