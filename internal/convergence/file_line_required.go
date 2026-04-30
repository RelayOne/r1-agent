// file_line_required.go — TIER 1-A: refuse to mark a mission complete when
// its final report contains "shipped/done/implemented/wired" claims that
// lack at least one verifiable evidence cite (file:line / commit / PR /
// gh URL / curl probe / Cloud Run rev).
//
// Maps directly to the W29 failure mode where a sub-agent claimed "16
// handlers shipped" with no proof; the supervisor relayed that to the
// user; later code-grep showed only enum entries existed.
package convergence

import (
	"regexp"
	"strings"
)

// EvidenceKind classifies a single evidence citation.
type EvidenceKind string

const (
	EvidenceFileLine    EvidenceKind = "file_line"
	EvidenceCommit      EvidenceKind = "commit"
	EvidencePR          EvidenceKind = "pr"
	EvidenceGHURL       EvidenceKind = "gh_url"
	EvidenceCurlProbe   EvidenceKind = "curl_probe"
	EvidenceCloudRunRev EvidenceKind = "cloud_run_rev"
)

// Citation is one piece of evidence found in a report.
type Citation struct {
	Kind  EvidenceKind
	Value string
}

// FileLineRequiredFinding is a sentence in the report that asserts
// completion but has no evidence to back it.
type FileLineRequiredFinding struct {
	Sentence string
	Reason   string
}

var (
	completionVerb = regexp.MustCompile(`(?i)\b(shipped|merged|done|implemented|completed|wired|landed|deployed|live|production-ready|ready-to-ship)\b`)
	blockedNote    = regexp.MustCompile(`(?i)\bblocked-?[a-z]`)
	fileLineRE     = regexp.MustCompile(`[A-Za-z0-9_./-]+\.(?:ts|tsx|js|jsx|go|py|sh|sql|prisma|yaml|yml|json|md|rs|java|kt|swift|rb|php|cs|cpp|h|hpp):[0-9]+`)
	commitRE       = regexp.MustCompile(`(?i)(?:commit|sha|hash):?\s*([0-9a-f]{7,40})|` + "`" + `([0-9a-f]{7,40})` + "`")
	prRE           = regexp.MustCompile(`PR #[0-9]+`)
	ghURLRE        = regexp.MustCompile(`https://github\.com/[^ )]+/(?:pull|commit)/[a-z0-9]+`)
	curlProbeRE    = regexp.MustCompile(`(?i)curl[^\n]*(?:HTTP/[12]|HTTP=|→|->)\s*[12345][0-9]{2}`)
	cloudRunRevRE  = regexp.MustCompile(`\b(actium|cloudswarm|wellytic|deeptap|coderadar|parentproof|relayone|heroa|coder1|attestik|veritize|truecom|relaygate|framebright)-[a-z0-9-]+-[0-9]{5}-[a-z]{3}\b`)
)

// ExtractCitations finds every evidence citation in a report.
func ExtractCitations(report string) []Citation {
	var out []Citation
	for _, m := range fileLineRE.FindAllString(report, -1) {
		out = append(out, Citation{Kind: EvidenceFileLine, Value: m})
	}
	for _, m := range commitRE.FindAllStringSubmatch(report, -1) {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		if v != "" {
			out = append(out, Citation{Kind: EvidenceCommit, Value: v})
		}
	}
	for _, m := range prRE.FindAllString(report, -1) {
		out = append(out, Citation{Kind: EvidencePR, Value: m})
	}
	for _, m := range ghURLRE.FindAllString(report, -1) {
		out = append(out, Citation{Kind: EvidenceGHURL, Value: m})
	}
	for _, m := range curlProbeRE.FindAllString(report, -1) {
		out = append(out, Citation{Kind: EvidenceCurlProbe, Value: strings.TrimSpace(m)})
	}
	for _, m := range cloudRunRevRE.FindAllString(report, -1) {
		out = append(out, Citation{Kind: EvidenceCloudRunRev, Value: m})
	}
	return out
}

// CheckFileLineRequired walks every sentence (split on .!?) in the report,
// flags the ones that contain a completion verb but have NO evidence
// citation in the same OR neighboring sentence, and returns a list of
// findings the supervisor should act on.
//
// "Neighboring sentence" means: the prior sentence and the next sentence.
// This handles common writing patterns like "Shipped X. See file.ts:42."
func CheckFileLineRequired(report string) []FileLineRequiredFinding {
	if report == "" {
		return nil
	}
	sentences := splitSentences(report)
	var findings []FileLineRequiredFinding
	for i, s := range sentences {
		if !completionVerb.MatchString(s) {
			continue
		}
		window := s
		if i > 0 {
			window = sentences[i-1] + " " + window
		}
		if i+1 < len(sentences) {
			window = window + " " + sentences[i+1]
		}
		// If any sentence in the window declares BLOCKED-<reason>, this
		// is not a completion claim — skip.
		if blockedNote.MatchString(window) {
			continue
		}
		if hasAnyEvidence(window) {
			continue
		}
		findings = append(findings, FileLineRequiredFinding{
			Sentence: strings.TrimSpace(s),
			Reason:   "completion verb without nearby file:line / commit / PR / URL / curl probe / Cloud Run rev citation",
		})
	}
	return findings
}

func hasAnyEvidence(s string) bool {
	return fileLineRE.MatchString(s) ||
		commitRE.MatchString(s) ||
		prRE.MatchString(s) ||
		ghURLRE.MatchString(s) ||
		curlProbeRE.MatchString(s) ||
		cloudRunRevRE.MatchString(s)
}

// splitSentences is a deliberately simple sentence splitter; it does NOT
// try to be NLP-perfect. It splits on `.`, `!`, `?`, or newline, but ONLY
// when followed by whitespace, end-of-string, or a closing quote/paren.
// This avoids splitting `apps/web/src/signup.tsx:18` mid-path.
func splitSentences(s string) []string {
	var out []string
	cur := strings.Builder{}
	rs := []rune(s)
	for i, r := range rs {
		cur.WriteRune(r)
		if r != '.' && r != '!' && r != '?' && r != '\n' {
			continue
		}
		// Look at what follows.
		next := rune(0)
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		// Boundary if EOF, whitespace, or closing punctuation.
		isBoundary := next == 0 ||
			next == ' ' || next == '\t' || next == '\n' ||
			next == ')' || next == ']' || next == '"' || next == '\''
		if !isBoundary {
			continue
		}
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}
