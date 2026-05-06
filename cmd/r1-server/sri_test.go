// Package main — sri_test.go
//
// Guards integrity of the vendored frontend blobs in
// cmd/r1-server/ui/web/vendor/ against silent corruption or table drift.
// For each entry in the SRI[] map declared in scripts/vendor-ui.sh, the
// test re-derives the sha384 base64 of the on-disk blob and asserts it
// matches the recorded value. Bumping a vendored library is a four-line
// review-and-commit (URL, SRI, golden snapshot if applicable, this
// test's expected count) — the test fails closed in every other case.
//
// Spec: specs/r1-server-ui-v2-foundation.md §9 (TASK-4).

package main

import (
	"bufio"
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"hash"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// vendorRoot is resolved relative to this test file; runs from
// cmd/r1-server/ when `go test ./cmd/r1-server/...`.
const vendorRoot = "ui/web/vendor"

// vendorScript is parsed for its SRI[] map at test time so a stale
// expected-value list in this file can't quietly drift from what
// scripts/vendor-ui.sh actually checks.
const vendorScript = "../../scripts/vendor-ui.sh"

// sriEntryRE matches lines of the form
//
//	["foo/bar.js"]="sha384-AAAA..."
//
// inside the SRI[] declaration in vendor-ui.sh. We want both the
// relative path and the integrity string.
var sriEntryRE = regexp.MustCompile(`^\s*\["([^"]+)"\]="(sha384-[^"]+)"\s*$`)

// computeSRI re-derives the SRI string ("sha384-" + base64-of-binary-digest)
// for the file at path using the same pipeline the bash script uses
// (openssl dgst -sha384 -binary | openssl base64 -A). Any mismatch
// against the recorded SRI fails the test.
func computeSRI(t *testing.T, path string) string {
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

// parseSRITable extracts the SRI[] map from vendor-ui.sh. The bash form
// is intentionally simple (one entry per line, no continuations) so a
// regex parse is sufficient. Any unparseable line inside the SRI block
// fails the test — drift is louder than silently parsing some entries.
func parseSRITable(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(vendorScript)
	if err != nil {
		t.Fatalf("read %s: %v", vendorScript, err)
	}
	out := map[string]string{}
	inBlock := false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "declare -A SRI=("):
			inBlock = true
		case inBlock && strings.HasPrefix(strings.TrimSpace(line), ")"):
			return out
		case inBlock:
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			m := sriEntryRE.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("unparseable SRI entry in %s: %q", vendorScript, line)
			}
			out[m[1]] = m[2]
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", vendorScript, err)
	}
	t.Fatalf("SRI block not closed in %s", vendorScript)
	return nil
}

func TestVendorSRI(t *testing.T) {
	table := parseSRITable(t)
	if len(table) == 0 {
		t.Fatal("parsed SRI table is empty — vendor-ui.sh malformed?")
	}
	// Hard-pin the count so a future task that adds or drops a
	// vendor blob has to update this guard in the same commit.
	const wantCount = 7
	if len(table) != wantCount {
		t.Errorf("len(SRI table) = %d, want %d (update wantCount when adding/dropping a vendor blob)", len(table), wantCount)
	}

	for rel, want := range table {
		path := filepath.Join(vendorRoot, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("vendored file missing: %s (%v)", path, err)
			continue
		}
		got := computeSRI(t, path)
		if got != want {
			t.Errorf("%s\n  got:  %s\n  want: %s\n  (run `bash scripts/vendor-ui.sh` to regenerate or update SRI table)", rel, got, want)
		}
	}
}
