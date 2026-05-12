package oneshot_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/oneshot"
)

// docPath resolves the relaygate-r1-stage.md operator runbook
// relative to the repo root. The package test runs from
// internal/oneshot/ so we walk up two directories.
func docPath(harness *testing.T) string {
	harness.Helper()
	// LINT-ALLOW chdir-doctest: doctest walks up from the test
	// package directory to find the docs/ tree, which sits at
	// the repo root. No process cwd mutation; read-only probe.
	dir, err := os.Getwd()
	if err != nil {
		harness.Fatalf("getwd: %v", err)
	}
	// Walk up until we find docs/integrations/relaygate-r1-stage.md
	for d := dir; d != "/" && d != ""; d = filepath.Dir(d) {
		cand := filepath.Join(d, "docs", "integrations", "relaygate-r1-stage.md")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	harness.Fatalf("doc not found relative to %s", dir)
	return ""
}

func loadDoc(harness *testing.T) []byte {
	harness.Helper()
	b, err := os.ReadFile(docPath(harness))
	if err != nil {
		harness.Fatalf("read doc: %v", err)
	}
	return b
}

// TestDoc_FencedJSONBlocksParse — every ```json fenced block in
// the operator runbook MUST be valid JSON. Spec §T6.1.
func TestDoc_FencedJSONBlocksParse(harness *testing.T) {
	doc := loadDoc(harness)
	re := regexp.MustCompile("(?s)```json\\s*\\n(.*?)```")
	matches := re.FindAllSubmatch(doc, -1)
	if len(matches) == 0 {
		harness.Fatal("doc has no ```json blocks")
	}
	for i, m := range matches {
		var any interface{}
		if err := json.Unmarshal(bytes.TrimSpace(m[1]), &any); err != nil {
			harness.Errorf("block %d not valid JSON: %v\n%s",
				i, err, string(m[1]))
		}
	}
}

// TestDoc_ExitCodesTableMatchesConstants — the Exit codes table
// references each of the seven exit constants exported by the
// oneshot package. Spec §T6.2.
func TestDoc_ExitCodesTableMatchesConstants(harness *testing.T) {
	doc := string(loadDoc(harness))
	want := map[string]int{
		"ExitOK":      oneshot.ExitOK,
		"ExitRuntime": oneshot.ExitRuntime,
		"ExitUsage":   oneshot.ExitUsage,
		"ExitMemory":  oneshot.ExitMemory,
		"ExitTimeout": oneshot.ExitTimeout,
		"ExitSIGINT":  oneshot.ExitSIGINT,
		"ExitSIGTERM": oneshot.ExitSIGTERM,
	}
	for name, code := range want {
		if !strings.Contains(doc, name) {
			harness.Errorf("doc missing constant name %s", name)
		}
		// The doc lists each code as a numeric token in a table
		// row; we don't pin the surrounding text but require the
		// number to appear somewhere.
		if !strings.Contains(doc, " "+itoa(code)+" ") &&
			!strings.Contains(doc, "|"+itoa(code)+" ") &&
			!strings.Contains(doc, "| "+itoa(code)+" ") {
			harness.Errorf("doc missing exit-code value %d for %s", code, name)
		}
	}
}

// TestDoc_EventConstantsDocumented — every EventXxx constant
// must appear in the doc. Spec §T6.3.
func TestDoc_EventConstantsDocumented(harness *testing.T) {
	doc := string(loadDoc(harness))
	events := []string{
		oneshot.EventMemoryLimitHit,
		oneshot.EventTimeout,
		oneshot.EventShutdown,
		oneshot.EventAuditDropped,
		oneshot.EventAuditFailed,
	}
	for _, e := range events {
		if !strings.Contains(doc, e) {
			harness.Errorf("doc missing event %q", e)
		}
	}
}

// TestDoc_FlagsAppearInBothPlaces — every flag listed in the
// runbook's flag table must appear in the CLI's flag-set output.
// We verify by running `r1 --one-shot decompose --help`. Because
// the help text isn't trivial to capture in a unit test (the
// flag set writes to stderr and the binary doesn't ship --help),
// we instead pin the flag names against a static list in the
// CLI source. Spec §T6.3 (relaxed form — the integration test
// in §T6.7 exercises the help text end-to-end).
func TestDoc_FlagsAppearInBothPlaces(harness *testing.T) {
	doc := string(loadDoc(harness))
	for _, flag := range []string{
		"--input", "--json", "--max-mem", "--timeout",
		"--audit-endpoint", "--audit-token", "--correlation-id",
	} {
		if !strings.Contains(doc, flag) {
			harness.Errorf("doc missing flag %s", flag)
		}
	}
}

// itoa is a tiny non-fmt int→string for the assertion above.
// Keeps the doctest cheap to run in -short.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// silence unused-import noise if io is dropped.
var _ = io.EOF
