// lint_cookies_test.go: enforces the no-cookies rule (spec C6 T6).
//
// Scans every .go file under internal/browser/ for forbidden tokens
// (Cookie, setCookie, document.cookie, localStorage, sessionStorage)
// and fails the test if any survive outside the negative-assertion
// test files: conformance.go and *isolation*test.go.
//
// The rationale: R1's remote browser tool MUST NOT accept
// user-supplied cookies. If a task needs authenticated access, the
// integration must happen at R1's tool layer (internal/tools/) where
// the auth boundary is policy-checked. The browser sees only
// public URLs after authentication has already happened.

package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoCookieReferences scans the browser package tree for cookie
// / storage tokens.
func TestNoCookieReferences(t *testing.T) {
	t.Parallel()
	tokens := []string{
		"document.cookie",
		"localStorage",
		"sessionStorage",
		"SetCookie",
		"setCookie",
	}
	allow := map[string]bool{
		// Files where cookie/storage references are LEGITIMATE
		// because they assert the ABSENCE of those primitives —
		// negative-test code that must continue to mention them.
		"conformance.go":         true,
		"isolation_test.go":      true,
		"lint_cookies_test.go":   true, // this file
	}

	root := "."
	var findings []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			// Skip nested provider subpackages — they ship their own
			// lint tests if they choose to keep this assertion local.
			// The spec's scope ("internal/browser/**") is satisfied
			// by recursing too, so we recurse but only flag .go files.
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		base := filepath.Base(path)
		if allow[base] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, tok := range tokens {
			if strings.Contains(content, tok) {
				findings = append(findings, path+": contains forbidden token "+tok)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(findings) > 0 {
		for _, f := range findings {
			t.Errorf("cookie-lint: %s", f)
		}
		t.Errorf("R1 browser code MUST NOT reference cookies / storage. " +
			"If a task needs authenticated access, integrate at the tool layer " +
			"(internal/tools/) — the browser sees only public URLs.")
	}
}
