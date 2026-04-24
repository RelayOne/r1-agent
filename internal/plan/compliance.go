// Package plan — compliance.go
//
// Mission-end SOW compliance sweep. Closes the anti-scaffold gap
// the existing gates don't cover:
//
//   existence guard  : checks task.Files but not SOW prose
//   spec-faithfulness: same — task.Files scope only
//   stub scan        : same — scanned files are task.Files
//   AC judges        : per-session, not mission-end
//   convergence      : domain-rule check, not deliverable check
//
// What's missing: a final pass that takes EVERY deliverable named
// anywhere in the SOW prose (via ExtractDeliverables, which we
// already compute per-task) and verifies each one is present in
// the repo as a nontrivial implementation. Without this, a SOW
// that says "6 shared packages: types, api-client, design-tokens,
// ui-web, ui-mobile, i18n" can be declared done with 5 of 6 built
// — because no gate aggregates that list across sessions.
//
// This gate is deterministic — no LLM call. It runs once at
// mission-end, after all sessions complete (pass or partial),
// before the run result is finalized.

package plan

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/logging"
)

// ComplianceVerdict is the per-deliverable classification.
type ComplianceVerdict string

const (
	VerdictFoundNontrivial ComplianceVerdict = "found-nontrivial"
	VerdictFoundStub       ComplianceVerdict = "found-stub"
	VerdictMissing         ComplianceVerdict = "missing"
)

// ComplianceFinding is a single deliverable-vs-repo check result.
type ComplianceFinding struct {
	Deliverable Deliverable
	Verdict     ComplianceVerdict
	// Evidence is the path(s) found (empty if Missing) + a brief
	// note on why we classified it as stub (if applicable).
	Evidence []string
	Note     string
}

// ComplianceReport aggregates every deliverable's verdict.
type ComplianceReport struct {
	Findings      []ComplianceFinding
	OkCount       int // VerdictFoundNontrivial
	StubCount     int // VerdictFoundStub
	MissingCount  int // VerdictMissing
}

// Passed returns true when every deliverable is FoundNontrivial.
// A stub or missing deliverable fails the sweep.
func (r *ComplianceReport) Passed() bool {
	return r.StubCount == 0 && r.MissingCount == 0 && len(r.Findings) > 0
}

// RunSOWCompliance extracts every deliverable named in the SOW
// prose (all sessions' titles + task descriptions, NOT just
// per-task scoped), and cross-checks each against repoRoot.
//
// When the SOW's Prose field is populated, that's the primary
// text source (richest). Otherwise we concatenate session/task
// fields as a fallback.
func RunSOWCompliance(repoRoot string, sow *SOW) *ComplianceReport {
	if sow == nil {
		return &ComplianceReport{}
	}

	var text strings.Builder
	text.WriteString(sow.Description)
	text.WriteString("\n")
	for _, s := range sow.Sessions {
		text.WriteString(s.Title)
		text.WriteString("\n")
		text.WriteString(s.Description)
		text.WriteString("\n")
		for _, t := range s.Tasks {
			text.WriteString(t.Description)
			text.WriteString("\n")
		}
		for _, ac := range s.AcceptanceCriteria {
			text.WriteString(ac.Description)
			text.WriteString("\n")
		}
	}

	deliverables := ExtractDeliverables(text.String())
	// Deliverables already deduped + sorted by ExtractDeliverables.
	// We further filter out trivially-noisy items (single-word stop
	// words, bare prepositions) as a last line of defense.
	deliverables = filterNoiseDeliverables(deliverables)

	report := &ComplianceReport{}
	for _, d := range deliverables {
		f := classifyDeliverable(repoRoot, d)
		report.Findings = append(report.Findings, f)
		switch f.Verdict {
		case VerdictFoundNontrivial:
			report.OkCount++
		case VerdictFoundStub:
			report.StubCount++
		case VerdictMissing:
			report.MissingCount++
		}
	}
	return report
}

// classifyDeliverable finds the best evidence for one deliverable
// in repoRoot and assigns a verdict. The heuristic:
//
//  1. Build candidate filename patterns from the deliverable name
//     (kebab-case, camelCase, snake_case + extension hints from Kind).
//  2. Walk the repo (source files only) looking for ANY file whose
//     relative path matches a candidate OR whose content references
//     the name in a definition position (export const, class, func).
//  3. For each match, size + stub-pattern check. If at least one
//     match passes both, verdict = FoundNontrivial.
//  4. Match(es) exist but all are stubs: FoundStub.
//  5. Zero matches: Missing.
func classifyDeliverable(repoRoot string, d Deliverable) ComplianceFinding {
	finding := ComplianceFinding{Deliverable: d}
	candidates := candidateFilePatterns(d)
	defRegex := definitionRegex(d.Name)

	var matchedPaths []string
	stubOnly := true
	note := ""

	walkErr := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, werr error) error {
		if werr != nil {
			// Best-effort compliance scan: log and skip unreadable
			// paths but keep traversing the rest of the repo. A
			// single bad dir shouldn't flip a deliverable to Missing.
			logging.Global().Warn("plan.compliance: walk error", "path", path, "err", werr)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			// Can't relativize — log and treat like unreadable.
			logging.Global().Warn("plan.compliance: rel-path error", "path", path, "err", relErr)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if isSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSourceExt(path) {
			return nil
		}

		// Fast path — filename match.
		base := strings.ToLower(filepath.Base(path))
		for _, pat := range candidates {
			if strings.Contains(base, pat) {
				if isNontrivial(path) {
					matchedPaths = append(matchedPaths, rel)
					stubOnly = false
					return nil
				}
				matchedPaths = append(matchedPaths, rel+" (stub)")
				return nil
			}
		}

		// Slow path — content match for definition sites.
		if defRegex != nil && contentHasDefinition(path, defRegex) {
			if isNontrivial(path) {
				matchedPaths = append(matchedPaths, rel)
				stubOnly = false
				return nil
			}
			matchedPaths = append(matchedPaths, rel+" (stub)")
		}
		return nil
	})
	if walkErr != nil {
		note = fmt.Sprintf("walk error: %v", walkErr)
	}

	if len(matchedPaths) == 0 {
		finding.Verdict = VerdictMissing
		finding.Note = note
		return finding
	}
	// Limit evidence list length to keep the report readable.
	if len(matchedPaths) > 4 {
		finding.Evidence = append(matchedPaths[:4], fmt.Sprintf("… and %d more", len(matchedPaths)-4))
	} else {
		finding.Evidence = matchedPaths
	}
	if stubOnly {
		finding.Verdict = VerdictFoundStub
		finding.Note = "all matching files failed the nontrivial-content check (too small, scaffold marker, or import-only)"
		return finding
	}
	finding.Verdict = VerdictFoundNontrivial
	return finding
}

// candidateFilePatterns converts a deliverable name to probable
// filename fragments in several casing conventions. Returns
// lowercase patterns meant for substring matching against
// lowercase file basenames.
func candidateFilePatterns(d Deliverable) []string {
	words := strings.Fields(strings.ToLower(d.Name))
	if len(words) == 0 {
		return nil
	}
	kebab := strings.Join(words, "-")
	snake := strings.Join(words, "_")
	camel := words[0]
	for _, w := range words[1:] {
		if len(w) > 0 {
			camel += strings.ToUpper(string(w[0])) + w[1:]
		}
	}
	pascal := ""
	for _, w := range words {
		if len(w) > 0 {
			pascal += strings.ToUpper(string(w[0])) + w[1:]
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range []string{kebab, snake, camel, strings.ToLower(pascal)} {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// definitionRegex builds a conservative content-match regex for
// seeing the deliverable name in a typical definition context.
// Matches `export const <Name>`, `class <Name>`, `function <Name>`,
// `def <name>`, `fn <name>`, `const <Name> =`. Returns nil when
// the name is too generic to be safely content-matched.
func definitionRegex(name string) *regexp.Regexp {
	words := strings.Fields(name)
	if len(words) == 0 {
		return nil
	}
	pascal := ""
	for _, w := range words {
		if len(w) > 0 {
			pascal += strings.ToUpper(string(w[0])) + w[1:]
		}
	}
	// Too generic — skip.
	if len(pascal) < 4 {
		return nil
	}
	// Escape metacharacters in the name.
	esc := regexp.QuoteMeta(pascal)
	// Match common definition keywords immediately preceding the name.
	pat := `(?m)(?:\bexport\s+(?:const|class|function|interface|type|default)\s+|\bclass\s+|\bfunction\s+|\bdef\s+|\bfn\s+|\bconst\s+)` + esc + `\b`
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil
	}
	return re
}

// contentHasDefinition reads up to 200 KB of the file and tests
// the regex. Files larger than 200 KB are considered non-source
// for this purpose — the heuristic assumes deliverables live in
// reasonably sized source files.
func contentHasDefinition(path string, re *regexp.Regexp) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	// Quick size check.
	stat, err := f.Stat()
	if err != nil || stat.Size() > 200*1024 {
		return false
	}
	data := make([]byte, stat.Size())
	if _, err := f.Read(data); err != nil {
		return false
	}
	return re.Match(data)
}

// isNontrivial returns true when the file is bigger than 80 bytes
// AND doesn't match a common stub pattern AND contains at least
// one non-import, non-comment, non-blank line.
//
// 80 bytes because empty scaffolds are typically <50 bytes
// ("export {}\n", single-line re-export). 80 is conservative.
func isNontrivial(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() < 80 {
		return false
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	stubHits := 0
	bodyLines := 0
	lineCount := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lineCount++
		if lineCount > 5000 {
			break
		}
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		// Stub signatures (cheap subset of sow_spec_guard's set —
		// the full set lives in sow_spec_guard.go; here we care
		// about OVERALL file triviality, not per-file reporting).
		for _, pat := range scaffoldContentMarkers {
			if strings.Contains(lower, pat) {
				stubHits++
				break
			}
		}
		// Import / comment / brace-only lines don't count as body.
		if isImportOrCommentLine(line) {
			continue
		}
		if line == "{" || line == "}" || line == "};" || line == "})" {
			continue
		}
		bodyLines++
	}
	if bodyLines < 3 {
		return false
	}
	// File is mostly stubs.
	if stubHits >= bodyLines/2 && stubHits > 0 {
		return false
	}
	return true
}

// scaffoldContentMarkers is a subset of the more-thorough pattern
// list in sow_spec_guard.go. Used for overall-triviality
// classification only — single matches don't fail a file; mass
// matches do (>=50% of body lines).
var scaffoldContentMarkers = []string{
	"todo",
	"fixme",
	"not implemented",
	"not_implemented",
	"unimplemented",
	"notimplementederror",
	"return null",
	"return {}",
	"return []",
	"return undefined",
}

func isImportOrCommentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "import ") ||
		strings.HasPrefix(trimmed, "from ") ||
		strings.HasPrefix(trimmed, "use ") ||
		strings.HasPrefix(trimmed, "using ") ||
		strings.HasPrefix(trimmed, "package ") ||
		strings.HasPrefix(trimmed, "require(") ||
		strings.HasPrefix(trimmed, "include ") ||
		strings.HasPrefix(trimmed, "#include") ||
		strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "#") ||
		strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "*") ||
		strings.HasPrefix(trimmed, "export *") ||
		strings.HasPrefix(trimmed, "export { ") ||
		strings.HasPrefix(trimmed, "export {") {
		return true
	}
	return false
}

// isSkipDir returns true for directories we should never walk
// into during compliance checks — build artifacts, vcs metadata,
// dependency stores.
func isSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "build", "target",
		".stoke", ".turbo", ".next", ".cache", "__pycache__",
		"venv", ".venv", "vendor", ".idea", ".vscode":
		return true
	}
	return false
}

// isSourceExt returns true for file extensions we should scan.
func isSourceExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".go", ".rs", ".py", ".rb", ".php",
		".java", ".kt", ".kts", ".scala",
		".cs", ".fs", ".vb",
		".c", ".cc", ".cpp", ".cxx", ".h", ".hpp",
		".swift", ".m", ".mm",
		".lua", ".ex", ".exs", ".erl",
		".sh", ".bash", ".zsh",
		".json", ".yaml", ".yml", ".toml":
		return true
	}
	return false
}

// filterNoiseDeliverables drops items that look like noise from
// extraction — bare prepositions, single-word stop words, items
// shorter than 3 characters.
func filterNoiseDeliverables(ds []Deliverable) []Deliverable {
	stopWords := map[string]bool{
		"and": true, "or": true, "of": true, "in": true, "on": true,
		"for": true, "to": true, "the": true, "a": true, "an": true,
		"with": true, "from": true, "at": true, "by": true, "as": true,
		"errors": true, "logic": true, "state": true, "data": true,
		"types": true, "etc": true,
	}
	out := make([]Deliverable, 0, len(ds))
	for _, d := range ds {
		name := strings.TrimSpace(d.Name)
		if len(name) < 3 {
			continue
		}
		if stopWords[strings.ToLower(name)] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// FormatComplianceReport renders the report as a human-readable
// block ready to print or inject into a repair prompt. Missing
// items come first because they're more fundamental than stubs;
// stubs come before OK items.
func FormatComplianceReport(r *ComplianceReport) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	total := r.OkCount + r.StubCount + r.MissingCount
	fmt.Fprintf(&b, "SOW Compliance Sweep: %d/%d nontrivial, %d stub, %d missing\n",
		r.OkCount, total, r.StubCount, r.MissingCount)

	if r.MissingCount > 0 {
		fmt.Fprintln(&b, "\nMISSING (named in SOW, nothing matching on disk):")
		for _, f := range r.Findings {
			if f.Verdict == VerdictMissing {
				fmt.Fprintf(&b, "  - %s", f.Deliverable.Name)
				if f.Deliverable.Kind != "" && f.Deliverable.Kind != KindUnknown {
					fmt.Fprintf(&b, " (%s)", f.Deliverable.Kind)
				}
				fmt.Fprintln(&b, "")
				if f.Deliverable.Source != "" {
					fmt.Fprintf(&b, "      source: %s\n", truncate(f.Deliverable.Source, 140))
				}
			}
		}
	}
	if r.StubCount > 0 {
		fmt.Fprintln(&b, "\nINSUFFICIENT CONTENT (file matches found but content is trivial/scaffold):")
		for _, f := range r.Findings {
			if f.Verdict == VerdictFoundStub {
				fmt.Fprintf(&b, "  - %s", f.Deliverable.Name)
				if len(f.Evidence) > 0 {
					fmt.Fprintf(&b, " at %s", strings.Join(f.Evidence, ", "))
				}
				fmt.Fprintln(&b, "")
				if f.Note != "" {
					fmt.Fprintf(&b, "      note: %s\n", f.Note)
				}
			}
		}
	}
	return b.String()
}

// truncate helper defined in workspace_hygiene_agent.go, reused.

// DeliverableSummary returns a short one-line summary suitable
// for logging — e.g. "SOW compliance: 14/18 ok, 2 stub, 2 missing".
func (r *ComplianceReport) Summary() string {
	total := r.OkCount + r.StubCount + r.MissingCount
	return fmt.Sprintf("%d/%d ok, %d stub, %d missing", r.OkCount, total, r.StubCount, r.MissingCount)
}

// SortedFindings returns findings sorted by verdict severity
// (Missing first, then Stub, then OK) and then by name.
func (r *ComplianceReport) SortedFindings() []ComplianceFinding {
	out := append([]ComplianceFinding(nil), r.Findings...)
	sort.SliceStable(out, func(i, j int) bool {
		ri := verdictRank(out[i].Verdict)
		rj := verdictRank(out[j].Verdict)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(out[i].Deliverable.Name) < strings.ToLower(out[j].Deliverable.Name)
	})
	return out
}

func verdictRank(v ComplianceVerdict) int {
	switch v {
	case VerdictMissing:
		return 0
	case VerdictFoundStub:
		return 1
	case VerdictFoundNontrivial:
		return 2
	}
	return 3
}
