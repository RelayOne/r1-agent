// Package promptguard scans project-supplied text at intake time for
// known prompt-injection shapes before it is concatenated into an LLM
// prompt.
//
// The security posture is deliberately modest. Published work
// (OpenAI/Anthropic/DeepMind adaptive-attack study, 2025) shows that
// all 12 tested prompt-injection defenses can be bypassed with >90%
// success by a motivated adversary. So this package is not a defense
// against sophisticated attackers; it is an intake-time hygiene check
// that catches lazy, copy-pasted jailbreak strings and forces any
// adversary up the cost curve. We default to "Warn" (log and pass
// through) so operators get telemetry without breaking ingestion of
// legitimate files that happen to contain trigger phrases (e.g. a
// README describing prompt-injection defenses).
//
// Wired into the following intake paths that read project-supplied or
// third-party text into a prompt:
//
//   - skill bodies loaded from .stoke/skills/ or ~/.stoke/skills/
//     (internal/skill/registry.go)
//   - failure-analysis file reads that get embedded as test-scaffold
//     source in retry prompts (internal/workflow/workflow.go)
//   - feasibility-gate web-search result bodies that get injected into
//     task briefings (internal/plan/feasibility.go)
//   - convergence override judge file snippets that get embedded in VP
//     Eng / CTO prompts (internal/convergence/judge.go)
//
// NOT currently wired into agentloop tool outputs (Task 2 covers that
// separately), nor into stoke's own builtin skills and docs — those
// are trusted source-controlled content.
package promptguard

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Action names the disposition to apply to content that matches a
// threat pattern.
type Action int

const (
	// ActionWarn logs a warning and returns the content unchanged.
	// Default disposition for the first month of deployment so we
	// learn the false-positive rate before we start mutating content.
	ActionWarn Action = iota
	// ActionStrip returns the content with each matching region
	// replaced by "[REDACTED-PROMPT-INJECTION]" markers, preserving
	// the surrounding non-matching text.
	ActionStrip
	// ActionReject returns an empty string and a non-nil error. The
	// caller is expected to refuse to ingest the content and escalate
	// to the operator / supervisor.
	ActionReject
)

// Threat is one detected injection-shaped segment.
type Threat struct {
	PatternName string
	Start, End  int    // byte offsets into the scanned content
	Excerpt     string // up to ~120 chars around the match, for logs
}

// Pattern is one injection-shape recognizer.
type Pattern struct {
	Name   string
	Regexp *regexp.Regexp
	// Rationale is included in warn/reject logs so operators can judge
	// whether a match is a true positive.
	Rationale string
}

var (
	patternsMu sync.RWMutex
	patterns   = defaultPatterns()
)

// defaultPatterns returns the built-in injection-shape set. Each
// pattern is case-insensitive and anchored on a distinctive phrase to
// keep false positives manageable. Expanding this list is cheap; the
// goal at v1 is to catch the string-literal jailbreaks that appear in
// published corpora (Anthropic red-team data, Hermes' internal set,
// the OWASP LLM01 examples), not to catch adversarial paraphrase.
func defaultPatterns() []Pattern {
	return []Pattern{
		{
			Name:      "ignore-previous",
			Regexp:    regexp.MustCompile(`(?i)ignore\s+(all\s+)?(the\s+)?(previous|prior|above|earlier)\s+(instructions?|prompts?|directives?|rules?|messages?)`),
			Rationale: "Classic instruction-override phrase from published jailbreak corpora.",
		},
		{
			Name:      "disregard-previous",
			Regexp:    regexp.MustCompile(`(?i)(disregard|forget|discard)\s+(all\s+)?(the\s+)?(previous|prior|above|earlier)\s+(instructions?|prompts?|directives?|rules?|context)`),
			Rationale: "Variant of ignore-previous used to evade exact-match filters.",
		},
		{
			Name:      "system-override",
			Regexp:    regexp.MustCompile(`(?i)(system\s+prompt\s+(override|injection|update)|new\s+system\s+prompt|override\s+the\s+system)`),
			Rationale: "Attempts to re-scope the model's system-level instructions.",
		},
		{
			Name:      "role-reassignment",
			Regexp:    regexp.MustCompile(`(?i)(you\s+are\s+now|from\s+now\s+on\s+you\s+are|act\s+as\s+(?:a\s+)?(?:DAN|jailbroken|uncensored|unrestricted))`),
			Rationale: "Role-reassignment jailbreak ('you are now DAN', 'act as uncensored').",
		},
		{
			Name:      "dev-mode",
			Regexp:    regexp.MustCompile(`(?i)(developer\s+mode|god\s+mode|unrestricted\s+mode|jailbreak\s+mode)\s+(enabled|activated|on)`),
			Rationale: "Fictional mode-switch markers used in community jailbreak prompts.",
		},
		{
			Name:      "exfil-system-prompt",
			Regexp:    regexp.MustCompile(`(?i)(print|reveal|show|echo|output|return)\s+(the\s+)?(entire\s+|full\s+|complete\s+)?(system\s+prompt|initial\s+instructions|hidden\s+instructions|training\s+data)`),
			Rationale: "Attempt to exfiltrate the system prompt.",
		},
		{
			Name:      "bypass-safety",
			Regexp:    regexp.MustCompile(`(?i)(bypass|disable|turn\s+off|skip)\s+(all\s+)?(safety|content|security|ethical)\s+(checks?|filters?|guardrails?|policies|rules)`),
			Rationale: "Explicit request to disable safety guardrails.",
		},
		{
			Name:      "instruction-hijack-injected-role",
			Regexp:    regexp.MustCompile(`(?im)^\s*(system|assistant)\s*:\s*`),
			Rationale: "Injected role marker at line start — attempts to spoof a new turn in the chat transcript.",
		},
	}
}

// Scan returns every threat found in s, in order of appearance. An
// empty slice means "no injection shapes detected."
//
// In addition to the registered regex patterns, Scan checks for leet-encoded
// injection phrases via scanLeetspeak (see leetspeak.go).
func Scan(s string) []Threat {
	if len(s) == 0 {
		return nil
	}
	patternsMu.RLock()
	ps := patterns
	patternsMu.RUnlock()
	var out []Threat
	for _, p := range ps {
		for _, loc := range p.Regexp.FindAllStringIndex(s, -1) {
			out = append(out, Threat{
				PatternName: p.Name,
				Start:       loc[0],
				End:         loc[1],
				Excerpt:     excerpt(s, loc[0], loc[1]),
			})
		}
	}
	// Check for leet-encoded injection phrases (digit-for-letter substitution).
	out = append(out, scanLeetspeak(s)...)
	return out
}

// excerpt returns up to 60 chars of context on each side of [start, end)
// with newlines collapsed, for readable single-line log output.
func excerpt(s string, start, end int) string {
	ctxBefore := 60
	ctxAfter := 60
	lo := start - ctxBefore
	if lo < 0 {
		lo = 0
	}
	hi := end + ctxAfter
	if hi > len(s) {
		hi = len(s)
	}
	ex := s[lo:hi]
	ex = strings.ReplaceAll(ex, "\n", " ")
	ex = strings.ReplaceAll(ex, "\t", " ")
	// collapse runs of spaces
	for strings.Contains(ex, "  ") {
		ex = strings.ReplaceAll(ex, "  ", " ")
	}
	return strings.TrimSpace(ex)
}

// Sanitize scans s and applies action to every matching region. The
// returned Report lists each threat with the action taken; the returned
// string is s (Warn), s with matches replaced (Strip), or "" (Reject).
//
// If action is Reject and any threat is found, the returned error is
// non-nil and the caller MUST NOT ingest the content.
func Sanitize(s string, action Action, source string) (string, Report, error) {
	threats := Scan(s)
	report := Report{Source: source, Threats: threats, Action: action}
	if len(threats) == 0 {
		return s, report, nil
	}
	switch action {
	case ActionWarn:
		return s, report, nil
	case ActionStrip:
		// Replace matches in reverse order so prior offsets stay valid.
		out := []byte(s)
		// Scan returns threats in pattern-major order; re-sort by Start
		// desc so we can splice cleanly without recomputing indexes.
		sorted := make([]Threat, len(threats))
		copy(sorted, threats)
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].Start > sorted[i].Start {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		marker := []byte("[REDACTED-PROMPT-INJECTION]")
		for _, t := range sorted {
			out = append(out[:t.Start], append(marker, out[t.End:]...)...)
		}
		return string(out), report, nil
	case ActionReject:
		return "", report, fmt.Errorf("promptguard: rejected %q — %d threat(s) detected (first: %s)", source, len(threats), threats[0].PatternName)
	default:
		return s, report, fmt.Errorf("promptguard: unknown action %d", action)
	}
}

// Report summarizes the disposition of a Sanitize call. Intended for
// logging and for feeding the supervisor's drift rules.
type Report struct {
	Source  string
	Threats []Threat
	Action  Action
}

// Summary returns a single-line description of the report, safe for
// inclusion in logs.
func (r Report) Summary() string {
	if len(r.Threats) == 0 {
		return fmt.Sprintf("promptguard: %s clean", r.Source)
	}
	names := make([]string, 0, len(r.Threats))
	seen := map[string]bool{}
	for _, t := range r.Threats {
		if seen[t.PatternName] {
			continue
		}
		seen[t.PatternName] = true
		names = append(names, t.PatternName)
	}
	return fmt.Sprintf("promptguard: %s — %d threat(s) [%s] action=%s",
		r.Source, len(r.Threats), strings.Join(names, ","), actionName(r.Action))
}

func actionName(a Action) string {
	switch a {
	case ActionWarn:
		return "warn"
	case ActionStrip:
		return "strip"
	case ActionReject:
		return "reject"
	default:
		return "unknown"
	}
}

// AddPattern registers an extra pattern (for stoke.policy.yaml-driven
// customer-specific shapes). Safe to call concurrently with Scan.
func AddPattern(p Pattern) {
	patternsMu.Lock()
	defer patternsMu.Unlock()
	patterns = append(patterns, p)
}

// Reset restores the default pattern set. Exposed for tests.
func Reset() {
	patternsMu.Lock()
	defer patternsMu.Unlock()
	patterns = defaultPatterns()
}
