package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTreeSitterExtract_TSExportedFunction(t *testing.T) {
	src := []byte(`
export function acknowledgeAlarm(id: string) { return { id }; }
export async function resolveAlarm(id: string) { return id; }
`)
	got := treeSitterExtractSymbols("x.ts", src)
	want := map[string]bool{"acknowledgeAlarm": true, "resolveAlarm": true}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("missing: %v; got: %v", want, got)
	}
}

func TestTreeSitterExtract_TSExportedClass(t *testing.T) {
	src := []byte(`
export class AlarmSchema {}
export class OfflineQueue {}
`)
	got := treeSitterExtractSymbols("x.ts", src)
	want := map[string]bool{"AlarmSchema": true, "OfflineQueue": true}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("missing classes: %v; got: %v", want, got)
	}
}

func TestTreeSitterExtract_ArrowFunctionConst(t *testing.T) {
	// The key precision win over regex: arrow-function-assigned
	// consts like `export const foo = () => ...` aren't captured by
	// the symindex regex pattern but tree-sitter sees them.
	src := []byte(`
export const getUserById = async (id: string) => { return {id}; };
export const AuthContext = () => null;
const thisIsNotAFunction = 42; // must NOT be extracted
`)
	got := treeSitterExtractSymbols("x.ts", src)
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["getUserById"] {
		t.Errorf("arrow-function const getUserById not extracted: %v", got)
	}
	if !seen["AuthContext"] {
		t.Errorf("arrow-function const AuthContext not extracted: %v", got)
	}
	if seen["thisIsNotAFunction"] {
		t.Errorf("plain const should NOT be extracted: %v", got)
	}
}

func TestTreeSitterExtract_TSInterfaceAndType(t *testing.T) {
	src := []byte(`
export interface UserSession { id: string; }
export type BuildingMetrics = { count: number };
`)
	got := treeSitterExtractSymbols("x.ts", src)
	want := map[string]bool{"UserSession": true, "BuildingMetrics": true}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("missing: %v; got: %v", want, got)
	}
}

func TestTreeSitterExtract_ClassMethods(t *testing.T) {
	src := []byte(`
export class UserService {
  findById(id: string) { return id; }
  async create(payload: unknown) { return payload; }
}
`)
	got := treeSitterExtractSymbols("x.ts", src)
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["findById"] || !seen["create"] {
		t.Errorf("class methods missing (regex in symindex under-captures these), got %v", got)
	}
}

func TestTreeSitterExtract_TSXJsxPath(t *testing.T) {
	src := []byte(`
import React from 'react';
export function AuthContext({ children }: { children: React.ReactNode }) {
  return <div>{children}</div>;
}
`)
	got := treeSitterExtractSymbols("x.tsx", src)
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["AuthContext"] {
		t.Errorf("TSX function not extracted: %v", got)
	}
}

func TestTreeSitterExtract_Python(t *testing.T) {
	src := []byte(`
def compute_checksum(data):
    return sum(data)

class OfflineQueue:
    def enqueue(self, item):
        pass
`)
	got := treeSitterExtractSymbols("x.py", src)
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["compute_checksum"] || !seen["OfflineQueue"] || !seen["enqueue"] {
		t.Errorf("Python symbols missing: %v", got)
	}
}

func TestTreeSitterEnabled_EnvVar(t *testing.T) {
	old := os.Getenv("R1_H27_TREESITTER")
	defer os.Setenv("R1_H27_TREESITTER", old)

	os.Unsetenv("R1_H27_TREESITTER")
	if treeSitterEnabled() {
		t.Error("unset env should disable tree-sitter")
	}
	os.Setenv("R1_H27_TREESITTER", "")
	if treeSitterEnabled() {
		t.Error("empty env should disable tree-sitter")
	}
	os.Setenv("R1_H27_TREESITTER", "1")
	if !treeSitterEnabled() {
		t.Error("R1_H27_TREESITTER=1 should enable")
	}
	os.Setenv("R1_H27_TREESITTER", "true")
	if !treeSitterEnabled() {
		t.Error("R1_H27_TREESITTER=true should enable")
	}
	os.Setenv("R1_H27_TREESITTER", "0")
	if treeSitterEnabled() {
		t.Error("R1_H27_TREESITTER=0 should disable")
	}
}

func TestTreeSitterHasParser(t *testing.T) {
	cases := map[string]bool{
		"x.ts":  true,
		"x.tsx": true,
		"x.js":  true,
		"x.jsx": true,
		"x.mjs": true,
		"x.cjs": true,
		"x.py":  true,
		"x.go":  false, // stays on go/parser path
		"x.rs":  false, // stays on regex
		"x.md":  false,
	}
	for f, want := range cases {
		if got := treeSitterHasParser(f); got != want {
			t.Errorf("treeSitterHasParser(%q) = %v, want %v", f, got, want)
		}
	}
}

func TestScanDeclaredSymbolsTreeSitter_FullPath(t *testing.T) {
	// End-to-end: SOW declares 3 symbols; worker creates the file
	// but only implements 2. H-28 must flag the missing one.
	root := t.TempDir()
	abs := filepath.Join(root, "src/alarm.ts")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(`
export function acknowledgeAlarm(id: string) { return id; }
export class AlarmSchema {}
// resolveAlarm stub missing — H-28 must catch this
`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Prose uses phrasing the extractor patterns handle individually
	// to avoid entangling the test in prose-extraction edge cases.
	prose := `
The acknowledgeAlarm handler must exist.
The AlarmSchema class must exist.
The resolveAlarm handler must exist.`
	got := ScanDeclaredSymbolsNotImplementedTreeSitter(root, prose, []string{"src/alarm.ts"})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding (resolveAlarm missing), got %d: %+v", len(got), got)
	}
	if got[0].Kind != "declared-symbol-not-implemented-ts" {
		t.Errorf("kind = %s, want declared-symbol-not-implemented-ts", got[0].Kind)
	}
}

func TestScanDeclaredSymbolsTreeSitter_PrecisionVsRegex(t *testing.T) {
	// The specific precision gap: symindex regex misses
	// `export const foo = () => ...` style. Tree-sitter must catch it.
	root := t.TempDir()
	abs := filepath.Join(root, "x.ts")
	if err := os.WriteFile(abs, []byte(`
export const getUserById = async (id: string) => id;
`), 0o600); err != nil {
		t.Fatal(err)
	}
	prose := "The getUserById handler must be exported."
	got := ScanDeclaredSymbolsNotImplementedTreeSitter(root, prose, []string{"x.ts"})
	if len(got) != 0 {
		t.Errorf("H-28 should find arrow-function-const getUserById (regex variant misses it), got %+v", got)
	}
}
