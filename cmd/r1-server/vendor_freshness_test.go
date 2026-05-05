// Package main — vendor_freshness_test.go
//
// Spec 5 §7 + §10 T4: complements Spec 1's sri_test.go. That test
// guards the SRI table parsed live from scripts/vendor-ui.sh; this
// test guards the SAME hashes baked into vendoredSRI in
// ui_v2_flag.go. The two are belt-and-braces:
//
//   sri_test.go         — script SRI vs on-disk blob
//   vendor_freshness    — Go const SRI vs on-disk blob
//
// Drift in either direction (script-but-not-Go, or Go-but-not-blob)
// fails CI. Bumping a vendored library is the same 4-line review-
// and-commit either way.
package main

import (
	"crypto/sha512"
	"encoding/base64"
	"hash"
	"os"
	"path/filepath"
	"testing"
)

// computeOnDiskSRI re-derives the "sha384-<base64>" form of the
// file at path. Same algorithm sri_test.go uses; duplicated here
// rather than imported because the function in sri_test.go is
// test-only.
func computeOnDiskSRI(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var h hash.Hash = sha512.New384()
	if _, err := h.Write(raw); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return "sha384-" + base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// TestVendor_GoConstSRI asserts that every entry in vendoredSRI
// (declared in ui_v2_flag.go) matches the on-disk blob. Failure
// message tells the developer exactly which file is corrupt and
// which command to re-run.
func TestVendor_GoConstSRI(t *testing.T) {
	if len(vendoredSRI) == 0 {
		t.Fatal("vendoredSRI is empty — ui_v2_flag.go malformed?")
	}
	root := "ui/web/vendor"
	for rel, want := range vendoredSRI {
		path := filepath.Join(root, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("vendored file missing: %s (%v)", path, err)
			continue
		}
		got := computeOnDiskSRI(t, path)
		if got != want {
			t.Errorf("%s: SRI mismatch\n  got:  %s\n  want: %s\n  fix: re-run `bash scripts/vendor-ui.sh`, then update vendoredSRI in ui_v2_flag.go to the printed value",
				rel, got, want)
		}
	}
}

// TestVendor_GoConstMatchesScriptSRI guards against the Go-const +
// shell-script tables drifting independently. Reads the SRI table
// from scripts/vendor-ui.sh (same parser as sri_test.go) and
// asserts every shared entry is identical.
func TestVendor_GoConstMatchesScriptSRI(t *testing.T) {
	scriptTable := parseSRITable(t)
	for rel, scriptVal := range scriptTable {
		goVal, ok := vendoredSRI[rel]
		if !ok {
			// Not every script entry needs a Go const — only the
			// blobs the page templates inline as <script integrity=...>
			// (htmx + htmx-ext-sse today). Skip the rest.
			continue
		}
		if goVal != scriptVal {
			t.Errorf("%s: Go const SRI %q differs from script SRI %q — both must be updated together",
				rel, goVal, scriptVal)
		}
	}
}
