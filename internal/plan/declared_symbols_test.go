package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestExtractDeclaredSymbols_CommonPatterns(t *testing.T) {
	prose := `
The system must include the acknowledgeAlarm handler that processes
incoming alarm events. It depends on the AlarmSchema class defined
in packages/types. The frontend exposes an AuthContext provider.

We also need a BuildingMetrics type and a UserSession interface.
Export function setupRouter from apps/api/app/router.ts. The form
uses the validateInput function to check submitted data.

Finally: a helper named ` + "`computeChecksum`" + ` and a class
` + "`OfflineQueue`" + `.`
	got := ExtractDeclaredSymbols(prose)
	want := map[string]bool{
		"acknowledgeAlarm": true,
		"AlarmSchema":      true,
		"AuthContext":      true,
		"BuildingMetrics":  true,
		"UserSession":      true,
		"setupRouter":      true,
		"validateInput":    true,
		"computeChecksum":  true,
		"OfflineQueue":     true,
	}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("missing: %v; got: %v", want, got)
	}
}

func TestExtractDeclaredSymbols_RejectsEnglishNoise(t *testing.T) {
	// Pure-lowercase English words should not be treated as identifiers
	// just because they appear in "the X handler" constructions.
	prose := `The system sends the request through the main controller.
The user logs in via the login form. The file handler writes to disk.`
	got := ExtractDeclaredSymbols(prose)
	if len(got) != 0 {
		t.Errorf("expected no symbols extracted from English prose, got %v", got)
	}
}

func TestExtractDeclaredSymbols_RejectsAllUppercaseAcronyms(t *testing.T) {
	prose := "The HTTP handler and the SQL processor connect via API."
	got := ExtractDeclaredSymbols(prose)
	if len(got) != 0 {
		t.Errorf("all-uppercase acronyms should not be treated as identifiers, got %v", got)
	}
}

func TestExtractDeclaredSymbols_RejectsTsconfigCompilerOptions(t *testing.T) {
	// Real false positives caught on E5 at 12:36: SOW prose mentions
	// tsconfig compiler options (`strictNullChecks`, `noImplicitAny`,
	// `exactOptionalPropertyTypes`) which look like camelCase code
	// identifiers but are configuration keys, not deliverables.
	prose := `
Enable TypeScript strict mode by setting strictNullChecks to true.
Also configure noImplicitAny, noImplicitReturns, and
exactOptionalPropertyTypes in tsconfig.json. Don't forget about
esModuleInterop for CommonJS compatibility.`
	got := ExtractDeclaredSymbols(prose)
	for _, sym := range got {
		switch strings.ToLower(sym) {
		case "strictnullchecks", "noimplicitany", "noimplicitreturns",
			"exactoptionalpropertytypes", "esmoduleinterop":
			t.Errorf("tsconfig compiler option %q should be blocklisted, got in: %v", sym, got)
		}
	}
}

func TestExtractDeclaredSymbols_RejectsConfigFileKeys(t *testing.T) {
	prose := `
The package.json has scripts, dependencies, devDependencies blocks.
The .eslintrc configures parserOptions and ignorePatterns for the
monorepo.`
	got := ExtractDeclaredSymbols(prose)
	for _, sym := range got {
		switch strings.ToLower(sym) {
		case "scripts", "dependencies", "devdependencies",
			"parseroptions", "ignorepatterns":
			t.Errorf("config file key %q should be blocklisted, got in: %v", sym, got)
		}
	}
}

func TestExtractDeclaredSymbols_SnakeCaseAccepted(t *testing.T) {
	prose := "We need a helper function called process_event and a class named User_Profile."
	got := ExtractDeclaredSymbols(prose)
	// process_event has an underscore; User_Profile has underscore + mixed case
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["process_event"] && !seen["User_Profile"] {
		t.Errorf("snake_case identifiers should extract, got %v", got)
	}
}

func TestExtractDeclaredSymbols_EmptyInputs(t *testing.T) {
	if got := ExtractDeclaredSymbols(""); got != nil {
		t.Errorf("empty prose should yield nil, got %v", got)
	}
	if got := ExtractDeclaredSymbols("   \n\t  "); got != nil {
		t.Errorf("whitespace-only prose should yield nil, got %v", got)
	}
}

func TestExtractDeclaredSymbols_BackfillsBackticks(t *testing.T) {
	prose := "Use `fooBar` to transform `MyType` at runtime."
	got := ExtractDeclaredSymbols(prose)
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["fooBar"] || !seen["MyType"] {
		t.Errorf("backtick-quoted identifiers should extract, got %v", got)
	}
}

func TestScanDeclaredSymbolsNotImplemented_AllPresent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/alarm.ts", `
export function acknowledgeAlarm(id: string) { return { id }; }
export class AlarmSchema {}
`)
	prose := "The acknowledgeAlarm handler and the AlarmSchema class must exist."
	got := ScanDeclaredSymbolsNotImplemented(root, prose, []string{"src/alarm.ts"})
	if len(got) != 0 {
		t.Errorf("expected 0 findings (all symbols present), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredSymbolsNotImplemented_MissingSymbol(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/alarm.ts", `
// Stub file — the worker created it but didn't implement the handler.
export const placeholder = "TODO";
`)
	prose := "The acknowledgeAlarm handler must be implemented."
	got := ScanDeclaredSymbolsNotImplemented(root, prose, []string{"src/alarm.ts"})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "declared-symbol-not-implemented" {
		t.Errorf("kind = %s", got[0].Kind)
	}
	// H-83: prose-extracted declared-symbol findings default to
	// advisory so false positives on natural-language prose don't
	// block real completion. Set STOKE_DECLARED_SYMBOL_BLOCKING=1
	// to restore strict behavior.
	if got[0].Severity != SevAdvisory {
		t.Errorf("severity = %v, want advisory (H-83)", got[0].Severity)
	}
}

func TestScanDeclaredSymbolsNotImplemented_MultiLanguage(t *testing.T) {
	// One symbol per language; verify the gate works across the full
	// extractor registry. Each file defines exactly the symbol the
	// SOW names, so expected findings = 0.
	root := t.TempDir()
	writeFile(t, root, "go/main.go", `package main
func AcknowledgeAlarm() {}`)
	writeFile(t, root, "ts/alarm.ts", `export function processEvent() {}`)
	writeFile(t, root, "py/foo.py", `def compute_checksum():
    pass`)
	writeFile(t, root, "rs/lib.rs", `pub fn handle_request() {}`)
	writeFile(t, root, "kt/App.kt", `fun setupRouter() {}`)
	writeFile(t, root, "swift/Lib.swift", `public func buildMetrics() {}`)
	writeFile(t, root, "rb/svc.rb", `def process_payment
end`)
	writeFile(t, root, "php/svc.php", `<?php function resolveUser() {}`)
	writeFile(t, root, "cs/Lib.cs", `public class OfflineQueue {}`)
	writeFile(t, root, "ex/app.ex", `defmodule MyApp do
  def run_worker do
    :ok
  end
end`)
	prose := `
We need AcknowledgeAlarm in Go, processEvent in TS, compute_checksum in Python,
handle_request in Rust, setupRouter in Kotlin, buildMetrics in Swift,
process_payment in Ruby, resolveUser in PHP, OfflineQueue in C#, and run_worker
in Elixir.`
	files := []string{
		"go/main.go", "ts/alarm.ts", "py/foo.py", "rs/lib.rs",
		"kt/App.kt", "swift/Lib.swift", "rb/svc.rb", "php/svc.php",
		"cs/Lib.cs", "ex/app.ex",
	}
	got := ScanDeclaredSymbolsNotImplemented(root, prose, files)
	if len(got) != 0 {
		t.Errorf("expected 0 findings (all symbols present across 10 langs), got %d:\n", len(got))
		for _, f := range got {
			t.Errorf("  - %s", f.Detail)
		}
	}
}

func TestScanDeclaredSymbolsNotImplemented_StubFileFlagsMissing(t *testing.T) {
	// The H1-v2 failure pattern: worker creates the declared FILE as
	// a stub, reviewer rubber-stamps. H-27 must catch this where
	// declared-file-not-created could not.
	root := t.TempDir()
	writeFile(t, root, "apps/api/handlers.ts", `
// Worker created this file but the content is unrelated boilerplate
// that doesn't implement the declared handlers.
import { unused } from "other";
export const VERSION = "0.0.1";
`)
	prose := `
The handlers module must export acknowledgeAlarm, resolveAlarm, and
previewAlertRule as handler functions.`
	got := ScanDeclaredSymbolsNotImplemented(root, prose, []string{"apps/api/handlers.ts"})
	if len(got) != 3 {
		t.Fatalf("expected 3 findings (3 stub-masked handlers), got %d: %+v", len(got), got)
	}
}

func TestScanDeclaredSymbolsNotImplemented_NoChangedFilesNoOp(t *testing.T) {
	// Docs-only commit: gate silently exits without scanning.
	root := t.TempDir()
	got := ScanDeclaredSymbolsNotImplemented(root, "The foo handler", []string{"README.md"})
	if got != nil {
		t.Errorf("docs-only commit should yield nil, got %+v", got)
	}
}

func TestScanDeclaredSymbolsNotImplemented_CaseInsensitiveMatch(t *testing.T) {
	// SOW prose sometimes sentence-cases an identifier at the start of
	// a sentence. The gate should accept either case.
	root := t.TempDir()
	writeFile(t, root, "x.ts", "export function acknowledgeAlarm() {}")
	prose := "Acknowledgealarm handler is the entrypoint."
	got := ScanDeclaredSymbolsNotImplemented(root, prose, []string{"x.ts"})
	if len(got) != 0 {
		t.Errorf("case-insensitive match should find acknowledgeAlarm, got %+v", got)
	}
}

func TestLooksLikeSource(t *testing.T) {
	cases := map[string]bool{
		"foo.go":      true,
		"x.ts":        true,
		"y.tsx":       true,
		"z.py":        true,
		"bar.rs":      true,
		"X.java":      true,
		"A.kt":        true,
		"L.swift":     true,
		"m.rb":        true,
		"m.php":       true,
		"Y.cs":        true,
		"z.ex":        true,
		"foo.c":       true,
		"foo.cpp":     true,
		"S.scala":     true,
		"README.md":   false,
		"config.yaml": false,
		"x.json":      false,
		"noext":       false,
	}
	for f, want := range cases {
		if got := looksLikeSource(f); got != want {
			t.Errorf("looksLikeSource(%q) = %v, want %v", f, got, want)
		}
	}
}

func TestValidDeclaredSymbol(t *testing.T) {
	cases := map[string]bool{
		"":                 false,
		"x":                false, // too short
		"xy":               false, // too short
		"HTTP":             false, // all caps
		"the":              false, // blocklisted English
		"request":          false, // blocklisted English
		"handler":          false, // pure lowercase single word — no internal signal (would need camelCase)
		"foo":              false, // pure lowercase, no internal signal
		"fooBar":           true,  // camelCase
		"FooBar":           true,  // PascalCase
		"foo_bar":          true,  // snake_case
		"acknowledgeAlarm": true,  // real-world identifier
		"AlarmSchema":      true,
		"API_KEY":          false, // all caps + underscore
	}
	for s, want := range cases {
		if got := validDeclaredSymbol(s); got != want {
			t.Errorf("validDeclaredSymbol(%q) = %v, want %v", s, got, want)
		}
	}
}
