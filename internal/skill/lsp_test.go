// lsp_test.go — tests for the LSP language registry (T-R1P-020).

package skill

import (
	"sort"
	"strings"
	"testing"
)

func TestLSPLanguagesNonEmpty(t *testing.T) {
	langs := LSPLanguages()
	if len(langs) < 4 {
		t.Fatalf("expected at least 4 LSP languages, got %d", len(langs))
	}
	wantIDs := map[string]bool{"go": false, "python": false, "typescript": false, "rust": false}
	for _, l := range langs {
		if _, ok := wantIDs[l.ID]; ok {
			wantIDs[l.ID] = true
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("missing required LSP language: %s", id)
		}
	}
}

func TestLSPLanguageIDsSorted(t *testing.T) {
	ids := LSPLanguageIDs()
	if !sort.StringsAreSorted(ids) {
		t.Errorf("LSPLanguageIDs() not sorted: %v", ids)
	}
}

func TestLSPLanguageForByAlias(t *testing.T) {
	cases := []struct {
		query string
		wantID string
	}{
		{"go", "go"},
		{"GO", "go"},
		{"py", "python"},
		{"python", "python"},
		{"ts", "typescript"},
		{"javascript", "typescript"},
		{"jsx", "typescript"},
		{"rs", "rust"},
		{"rust", "rust"},
	}
	for _, tc := range cases {
		l, ok := LSPLanguageFor(tc.query)
		if !ok {
			t.Errorf("LSPLanguageFor(%q) not found", tc.query)
			continue
		}
		if l.ID != tc.wantID {
			t.Errorf("LSPLanguageFor(%q).ID = %q, want %q", tc.query, l.ID, tc.wantID)
		}
	}
}

func TestLSPLanguageForUnknown(t *testing.T) {
	if _, ok := LSPLanguageFor("cobol"); ok {
		t.Error("expected no match for cobol")
	}
	if _, ok := LSPLanguageFor(""); ok {
		t.Error("expected no match for empty id")
	}
}

func TestLSPLanguageForFile(t *testing.T) {
	cases := []struct {
		path   string
		wantID string
		ok     bool
	}{
		{"main.go", "go", true},
		{"app/server.go", "go", true},
		{"util.py", "python", true},
		{"types.pyi", "python", true},
		{"index.ts", "typescript", true},
		{"index.tsx", "typescript", true},
		{"app.js", "typescript", true},
		{"crate/src/lib.rs", "rust", true},
		{"README.md", "", false},
		{"noext", "", false},
	}
	for _, tc := range cases {
		l, ok := LSPLanguageForFile(tc.path)
		if ok != tc.ok {
			t.Errorf("LSPLanguageForFile(%q) ok = %v, want %v", tc.path, ok, tc.ok)
			continue
		}
		if ok && l.ID != tc.wantID {
			t.Errorf("LSPLanguageForFile(%q).ID = %q, want %q", tc.path, l.ID, tc.wantID)
		}
	}
}

func TestLSPLanguagesHaveBinaries(t *testing.T) {
	for _, l := range LSPLanguages() {
		if len(l.Binaries) == 0 {
			t.Errorf("language %q has no Binaries", l.ID)
		}
		for _, bin := range l.Binaries {
			if strings.TrimSpace(bin) == "" {
				t.Errorf("language %q has empty binary entry", l.ID)
			}
		}
	}
}

func TestLSPLanguagesIsCopy(t *testing.T) {
	a := LSPLanguages()
	b := LSPLanguages()
	if len(a) > 0 {
		a[0].ID = "MUTATED"
	}
	if b[0].ID == "MUTATED" {
		t.Error("LSPLanguages() returned shared backing array; expected a copy")
	}
}
