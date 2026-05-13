package promptguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// corpusRoot returns the absolute path to the vendored CL4R1T4S corpus
// directory relative to this test file.
func corpusRoot(t *testing.T) string {
	t.Helper()
	// the test file lives in the same package as the corpus directory,
	// so a relative reference resolves against the package directory at
	// test run time.
	root := filepath.Join("cl4r1t4s_corpus")
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("corpus directory missing at %s: %v", root, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", root)
	}
	return root
}

// TestCL4R1T4SCorpusPresent enforces the structural contract on the
// vendored corpus: VERSION file, ≥40 .txt files, every .txt has the
// four-header preamble (#source, #category, #expected, #license).
func TestCL4R1T4SCorpusPresent(t *testing.T) {
	root := corpusRoot(t)

	verBytes, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatalf("VERSION missing: %v", err)
	}
	ver := strings.TrimSpace(string(verBytes))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(ver) {
		t.Fatalf("VERSION %q does not match semver", ver)
	}

	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		t.Fatalf("README.md missing: %v", err)
	}

	txtFiles := walkCorpusTxt(t, root, true)
	if got := len(txtFiles); got < 40 {
		t.Fatalf("need at least 40 corpus .txt files, found %d", got)
	}

	headerKeys := []string{"# source:", "# category:", "# expected:", "# license:"}
	for _, p := range txtFiles {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		text := string(body)
		for _, key := range headerKeys {
			if !strings.Contains(text, key) {
				t.Errorf("%s missing required header %q", p, key)
			}
		}
	}
}

// TestCL4R1T4SDetectionRate asserts the detection-rate regression gate
// from spec A1 T6: ≥85% on samples whose header says
// "# expected: detected". Files under known-misses/ are excluded.
func TestCL4R1T4SDetectionRate(t *testing.T) {
	root := corpusRoot(t)
	files := walkCorpusTxt(t, root, false) // exclude known-misses

	type miss struct {
		path    string
		excerpt string
	}
	var missed []miss
	denom := 0
	hits := 0

	for _, p := range files {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		text := string(body)
		if !strings.Contains(text, "# expected: detected") {
			// safety belt: anything in the active denominator is expected
			// to be detected. A sample without that header is a corpus
			// authoring bug — log it and skip it for the rate calc.
			t.Logf("WARNING: %s lacks '# expected: detected' header; skipping for rate calc", p)
			continue
		}
		denom++
		// Skip the four-header preamble. Header lines start with `#`;
		// the body begins after the first blank line.
		payload := text
		if idx := strings.Index(text, "\n\n"); idx > 0 {
			payload = text[idx+2:]
		}
		threats := Scan(payload)
		if len(threats) >= 1 {
			hits++
			continue
		}
		exc := payload
		if len(exc) > 80 {
			exc = exc[:80]
		}
		exc = strings.ReplaceAll(exc, "\n", " ")
		missed = append(missed, miss{path: p, excerpt: strings.TrimSpace(exc)})
	}

	if denom == 0 {
		t.Fatalf("denominator is zero — no samples with '# expected: detected' header found")
	}

	rate := float64(hits) / float64(denom)
	if rate < 0.85 {
		for _, m := range missed {
			t.Logf("MISSED: %s — %q", m.path, m.excerpt)
		}
		t.Fatalf("detection rate %.2f%% (%d/%d) is below the 85%% gate", rate*100, hits, denom)
	}
	t.Logf("detection rate %.2f%% (%d/%d) — gate satisfied", rate*100, hits, denom)
}

// TestCorpusReadmePresent enforces that the README contains the three
// operator-facing reference strings called out in spec A1 T6 item 26.
func TestCorpusReadmePresent(t *testing.T) {
	root := corpusRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	text := string(body)
	for _, want := range []string{"Corpus version", "License", "Threat model"} {
		if !strings.Contains(text, want) {
			t.Errorf("README missing required reference %q", want)
		}
	}
}

// TestPatternFields_BackcompatPreserved verifies that a Pattern
// constructed with only the legacy three fields (Name/Regexp/Rationale)
// still works through Scan, and that the resulting Threat carries
// Severity="medium" by default. The standalone Reset call here
// re-registers the defaults after the test mutates the global list, so
// other tests in the package see a clean baseline.
func TestPatternFields_BackcompatPreserved(t *testing.T) {
	t.Cleanup(Reset)

	legacy := Pattern{
		Name:      "test-legacy-pattern",
		Regexp:    regexp.MustCompile(`legacy-injection-token`),
		Rationale: "fixture pattern with only the legacy three fields",
	}
	AddPattern(legacy)

	threats := Scan("here is a legacy-injection-token in user input")
	var got *Threat
	for i := range threats {
		if threats[i].PatternName == "test-legacy-pattern" {
			got = &threats[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("Scan did not return the legacy-fixture threat")
	}
	if got.Severity != "medium" {
		t.Errorf("legacy Pattern produced Threat.Severity = %q; want %q", got.Severity, "medium")
	}
}

// TestAllPatterns_ReturnsSnapshot proves that AllPatterns returns a
// safe-to-mutate copy that includes the function-based leetspeak rule
// alongside the regex patterns.
func TestAllPatterns_ReturnsSnapshot(t *testing.T) {
	first := AllPatterns()
	if len(first) == 0 {
		t.Fatalf("AllPatterns returned an empty slice")
	}

	var seenLeet bool
	for _, p := range first {
		if p.Name == "leetspeak-instruction-rewrite" {
			seenLeet = true
			if p.Severity != "high" {
				t.Errorf("leetspeak metadata reported Severity=%q; want %q", p.Severity, "high")
			}
			if p.Source != "builtin" {
				t.Errorf("leetspeak metadata reported Source=%q; want %q", p.Source, "builtin")
			}
		}
	}
	if !seenLeet {
		t.Errorf("AllPatterns missing leetspeak-instruction-rewrite virtual entry")
	}

	// Mutate the returned slice and confirm subsequent calls are
	// unaffected.
	first[0] = Pattern{Name: "mutated", Severity: "low"}
	second := AllPatterns()
	if second[0].Name == "mutated" {
		t.Fatalf("AllPatterns returned the live slice, not a snapshot")
	}
}

// walkCorpusTxt returns every .txt file under root. When includeKnownMisses
// is false, files under known-misses/ are skipped.
func walkCorpusTxt(t *testing.T, root string, includeKnownMisses bool) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".txt") {
			return nil
		}
		if !includeKnownMisses && strings.Contains(filepath.ToSlash(p), "/known-misses/") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	return out
}

