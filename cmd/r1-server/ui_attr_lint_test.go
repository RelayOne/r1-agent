// Package main — ui_attr_lint_test.go
//
// Locks in the data-* convention from
// specs/r1-server-ui-v2-foundation.md §5: behaviour state lives in
// `data-state="<token>"` attributes, NOT in class names. The
// rationale is single-source-of-truth — if `.collapsed` and
// `data-state="collapsed"` both exist on the same element, future
// CSS or JS will read one and ignore the other and the bug is
// invisible until QA stumbles on it.
//
// This test walks every cmd/r1-server/ui/*.html and
// cmd/r1-server/ui/partials/*.html template, slices each opening
// tag with a regex tokenizer, and fails if any element carries BOTH
// a behaviour-denylist class AND the matching `data-state` attribute.
//
// We avoid x/net/html (not vendored) and html/template's tree builder
// (chokes on `{{ block }}` markers in template source). The regex
// approach is intentionally narrow — it catches the §5 violation
// without trying to be a full HTML5 parser.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// behaviourDenylist enumerates class names that encode runtime UI
// state and therefore conflict with the §5 `data-state` convention.
var behaviourDenylist = map[string]struct{}{
	"collapsed": {},
	"active":    {},
	"expanded":  {},
	"selected":  {},
	"hidden":    {},
	"disabled":  {},
}

var (
	tagRE        = regexp.MustCompile(`<[a-zA-Z][^>]*>`)
	classAttrRE  = regexp.MustCompile(`\sclass\s*=\s*"([^"]*)"`)
	stateAttrRE  = regexp.MustCompile(`\sdata-state\s*=\s*"([^"]*)"`)
)

func classTokens(val string) []string {
	parts := strings.Fields(val)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func TestV2Templates_NoClassDataStateConflict(t *testing.T) {
	root := "ui"
	matches, err := filepath.Glob(filepath.Join(root, "*.html"))
	if err != nil {
		t.Fatalf("glob top-level templates: %v", err)
	}
	partials, err := filepath.Glob(filepath.Join(root, "partials", "*.html"))
	if err != nil {
		t.Fatalf("glob partial templates: %v", err)
	}
	matches = append(matches, partials...)

	if len(matches) == 0 {
		t.Fatalf("no v2 templates found under %s — directory missing or empty?", root)
	}

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			scanForConflicts(t, string(raw), path)
		})
	}
}

func scanForConflicts(t *testing.T, body, path string) {
	t.Helper()
	for _, tag := range tagRE.FindAllString(body, -1) {
		cm := classAttrRE.FindStringSubmatch(tag)
		sm := stateAttrRE.FindStringSubmatch(tag)
		if cm == nil || sm == nil {
			continue
		}
		state := strings.TrimSpace(sm[1])
		for _, cls := range classTokens(cm[1]) {
			if _, banned := behaviourDenylist[cls]; banned && cls == state {
				t.Errorf("%s: tag has both class=%q (banned token) and data-state=%q — pick data-state per §5\n  tag: %s",
					path, cls, state, tag)
			}
		}
	}
}
