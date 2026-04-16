// Package critic — redflag.go
//
// STOKE-006 anti-deception pattern #2: red-flag phrase detection
// in agent-produced text. When an LLM explanation contains
// phrases like "should work", "probably fine", or "I'm confident
// this will work", those are deception markers more often than
// evidence of actual verification — the agent is stating
// confidence in place of demonstrating it.
//
// Callers feed per-turn agent output (final response, commit
// message, review justification) through DetectRedFlags before
// accepting it. Detections surface as findings the reviewer gate
// can reject on, or as warnings the operator sees in reports.
//
// Scope: detection only — this file doesn't decide what to DO
// with a red-flag hit. Policy (reject vs warn vs ignore) lives
// in the calling critic path.
package critic

import (
	"regexp"
	"sort"
	"strings"
)

// RedFlag is one deception-marker class. Kept as a string
// constant so it serializes readably in critic.Verdict output
// and in audit reports.
type RedFlag string

const (
	// FlagOverconfidence: "I'm confident", "definitely works",
	// "100% sure" — the agent is asserting certainty it hasn't
	// demonstrated.
	FlagOverconfidence RedFlag = "overconfidence"

	// FlagSpeculation: "should work", "probably fine", "I
	// think", "most likely" — the agent is speculating rather
	// than checking.
	FlagSpeculation RedFlag = "speculation"

	// FlagDeferralToFuture: "will be tested", "could add tests
	// later", "TODO: verify" — the agent is deferring the
	// work instead of doing it.
	FlagDeferralToFuture RedFlag = "deferral_to_future"

	// FlagHandWave: "essentially", "basically", "roughly",
	// "more or less" — the agent is describing what it did in
	// imprecise terms that obscure whether it actually did it.
	FlagHandWave RedFlag = "hand_wave"

	// FlagEvidenceClaim: "I verified", "I tested", "I
	// confirmed" paired with no visible tool call to back it
	// up. This flag requires caller-supplied context about
	// whether tools ran — callers pass didRunTools=false to
	// enable this class.
	FlagEvidenceClaim RedFlag = "evidence_claim_no_tools"
)

// RedFlagFinding is one red-flag hit.
type RedFlagFinding struct {
	// Flag is the red-flag class.
	Flag RedFlag

	// Phrase is the exact substring that matched.
	Phrase string

	// Line is the 1-indexed line number where the phrase
	// appeared. Useful for operator UIs that want to highlight.
	Line int
}

// redFlagPatterns lists the literal phrases that trigger each
// flag class. Matching is case-insensitive; whole-word matching
// prevents "probability" from matching "probably". Regexps are
// pre-compiled via init().
var redFlagPatterns = map[RedFlag][]string{
	FlagOverconfidence: {
		"i'm confident", "i am confident", "definitely works",
		"100% sure", "100 percent sure", "guaranteed to work",
		"absolutely certain",
	},
	FlagSpeculation: {
		"should work", "should be fine", "probably fine",
		"probably works", "probably will work",
		"i think this works", "most likely works",
		"most likely fine", "seems to work", "appears to work",
		"i believe this", "i assume",
	},
	FlagDeferralToFuture: {
		"will be tested", "will add tests", "will be added",
		"tests to follow", "can add tests later",
		"could add tests later", "todo: verify", "todo: test",
		"todo: add tests", "verification pending",
		"to be added later", "tests to come",
	},
	FlagHandWave: {
		"essentially works", "basically works",
		"more or less works", "pretty much works",
		"roughly does", "handles most cases",
		"should handle", "more or less correct",
	},
}

// evidenceClaimPatterns trigger FlagEvidenceClaim only when the
// caller supplies didRunTools=false. They're phrases that assert
// the agent did something verifiable.
var evidenceClaimPatterns = []string{
	"i verified", "i tested", "i confirmed", "i checked",
	"i ran the tests", "tests pass", "all tests pass",
	"build succeeds", "compilation succeeds",
}

var (
	// compiledPatterns holds the pre-compiled word-boundary
	// regexps. Keyed by flag so we can emit the flag class
	// without a second lookup.
	compiledPatterns = map[RedFlag][]*regexp.Regexp{}

	compiledEvidenceClaims []*regexp.Regexp
)

func init() {
	for flag, phrases := range redFlagPatterns {
		for _, p := range phrases {
			compiledPatterns[flag] = append(compiledPatterns[flag], compileWord(p))
		}
	}
	for _, p := range evidenceClaimPatterns {
		compiledEvidenceClaims = append(compiledEvidenceClaims, compileWord(p))
	}
}

// compileWord produces a case-insensitive regexp that matches
// `phrase` with word-boundary or punctuation on either side.
// Escapes regex metacharacters in `phrase` so raw punctuation
// passes through literally.
func compileWord(phrase string) *regexp.Regexp {
	escaped := regexp.QuoteMeta(phrase)
	// Word-boundary-ish: either actual \b or a lookbehind for
	// non-word. Go's regexp doesn't support lookaround, so we
	// use (?:^|\W) + (?:$|\W). The trailing alternation is
	// slightly loose (would match "should work." and "should
	// work!") which is what we want.
	return regexp.MustCompile(`(?i)(?:^|\W)` + escaped + `(?:$|\W)`)
}

// DetectRedFlags scans text for every configured red-flag
// phrase and returns one RedFlagFinding per hit. didRunTools is the
// caller's signal about whether this turn actually invoked any
// tools — when false, evidence-claim patterns trigger the
// FlagEvidenceClaim class.
//
// RedFlagFindings are sorted by Line then Phrase for deterministic
// output (easier to diff across runs).
func DetectRedFlags(text string, didRunTools bool) []RedFlagFinding {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	var out []RedFlagFinding

	for lineIdx, line := range lines {
		lineNum := lineIdx + 1
		for flag, patterns := range compiledPatterns {
			for _, re := range patterns {
				if m := re.FindString(line); m != "" {
					out = append(out, RedFlagFinding{
						Flag:   flag,
						Phrase: strings.TrimSpace(stripBoundaryChars(m)),
						Line:   lineNum,
					})
				}
			}
		}
		if !didRunTools {
			for _, re := range compiledEvidenceClaims {
				if m := re.FindString(line); m != "" {
					out = append(out, RedFlagFinding{
						Flag:   FlagEvidenceClaim,
						Phrase: strings.TrimSpace(stripBoundaryChars(m)),
						Line:   lineNum,
					})
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Phrase < out[j].Phrase
	})
	return out
}

// stripBoundaryChars removes leading/trailing non-word chars
// captured by the (?:^|\W) boundary. Makes RedFlagFinding.Phrase a
// clean quote.
func stripBoundaryChars(s string) string {
	s = strings.TrimSpace(s)
	for len(s) > 0 && !isWordChar(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && !isWordChar(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_' || b == '\'' || b == '-'
}
