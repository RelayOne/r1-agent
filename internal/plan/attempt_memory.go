package plan

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RepairAttemptRecord is one entry in the trail of repair attempts
// for a single session's failing ACs. Accumulated across attempts
// so later attempts see what earlier ones tried.
type RepairAttemptRecord struct {
	// Attempt is the 1-indexed attempt number.
	Attempt int
	// Timestamp is when the attempt completed.
	Timestamp time.Time
	// Directive is the repair instruction this attempt was given.
	Directive string
	// FilesTouched is the relative-path set the attempt modified.
	FilesTouched []string
	// DiffSummary is a compact summary of what changed (not full
	// diff — just "modified packages/types/src/index.ts: removed
	// 12 export-type lines from schemas barrel"). Produced by the
	// runner when the attempt completes.
	DiffSummary string
	// ACsFailingBefore is the ACs failing at attempt-start.
	ACsFailingBefore []string
	// ACsFailingAfter is the ACs failing after this attempt ran.
	ACsFailingAfter []string
	// NetProgress is computed: len(before) - len(after). Can be
	// negative (regression).
	NetProgress int
	// DurationMs is how long the attempt ran.
	DurationMs int64
}

// RepairTrail accumulates attempt records across a session's repair
// loop. Safe to pass by pointer; callers append after each attempt
// completes.
type RepairTrail struct {
	// SessionID identifies the session whose repair loop this trail
	// belongs to.
	SessionID string
	// Records is the ordered list of completed repair attempts.
	Records []RepairAttemptRecord
}

// AppendAttempt records one attempt's outcome onto the trail.
// Computes NetProgress. No-op when r is nil.
func (r *RepairTrail) AppendAttempt(rec RepairAttemptRecord) {
	if r == nil {
		return
	}
	rec.NetProgress = len(rec.ACsFailingBefore) - len(rec.ACsFailingAfter)
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	r.Records = append(r.Records, rec)
}

// PromptBlock renders the trail as a compact prompt-injectable
// block. Returns "" when empty. Format:
//
//	PRIOR REPAIR ATTEMPTS (do NOT repeat approaches that didn't work):
//	attempt 1: directive "edit schemas/index.ts to remove duplicate exports" → modified schemas/index.ts (net progress: 0, duration 42s)
//	attempt 2: directive "edit schemas/index.ts and interfaces/index.ts" → modified both files (net progress: 0, duration 51s)
//
//	→ attempts 1 and 2 both touched schemas/ barrels but didn't close the ACs. Consider a different root cause (interface file contents, turbo.json, tsconfig references) before touching barrels again.
//
// The trailing analysis paragraph is deterministic: if N>=2 attempts
// touched overlapping file sets with zero net progress, emit the
// "consider different root cause" hint. Pure Go logic, no LLM.
func (r *RepairTrail) PromptBlock() string {
	if r == nil || len(r.Records) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("PRIOR REPAIR ATTEMPTS (do NOT repeat approaches that didn't work):\n")
	for _, rec := range r.Records {
		files := rec.FilesTouched
		if len(files) == 0 {
			files = []string{"(no files modified)"}
		}
		dir := strings.TrimSpace(rec.Directive)
		if dir == "" {
			dir = "(no directive recorded)"
		}
		// Truncate directive for readability.
		if len(dir) > 200 {
			dir = dir[:197] + "..."
		}
		summary := strings.TrimSpace(rec.DiffSummary)
		if summary == "" {
			summary = "modified " + strings.Join(files, ", ")
		}
		durSec := rec.DurationMs / 1000
		fmt.Fprintf(&b, "attempt %d: directive %q → %s (net progress: %d, duration %ds)\n",
			rec.Attempt, dir, summary, rec.NetProgress, durSec)
	}

	// Deterministic analysis: when >=2 attempts have net progress <= 0
	// AND touched overlapping file sets, emit a "consider a different
	// root cause" hint listing the repeatedly-touched paths.
	if hint := r.stagnationHint(); hint != "" {
		b.WriteString("\n")
		b.WriteString(hint)
		b.WriteString("\n")
	}
	return b.String()
}

// stagnationHint returns the deterministic "consider different root
// cause" sentence when the trail shows repeated no-progress attempts
// on overlapping file sets. Empty string otherwise.
func (r *RepairTrail) stagnationHint() string {
	if r == nil || len(r.Records) < 2 {
		return ""
	}
	// Count files seen across attempts that made no forward progress.
	fileCount := map[string]int{}
	zeroProgressAttempts := 0
	idxs := make([]int, 0, len(r.Records))
	for i, rec := range r.Records {
		if rec.NetProgress > 0 {
			continue
		}
		zeroProgressAttempts++
		idxs = append(idxs, rec.Attempt)
		seen := map[string]bool{}
		for _, f := range rec.FilesTouched {
			if seen[f] {
				continue
			}
			seen[f] = true
			fileCount[f]++
		}
		_ = i
	}
	if zeroProgressAttempts < 2 {
		return ""
	}
	var repeated []string
	for f, n := range fileCount {
		if n >= 2 {
			repeated = append(repeated, f)
		}
	}
	if len(repeated) == 0 {
		return ""
	}
	sort.Strings(repeated)
	attemptStr := joinInts(idxs)
	return fmt.Sprintf(
		"→ attempts %s all touched %s but didn't close the ACs. Consider a different root cause (interface/config files, turbo.json, tsconfig references, dependency declarations) before touching the same files again.",
		attemptStr, strings.Join(repeated, ", "))
}

// joinInts formats a slice of 1-indexed attempt numbers as a
// human-readable list: [1] -> "1", [1,2] -> "1 and 2", [1,2,3] ->
// "1, 2 and 3".
func joinInts(xs []int) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("%d", xs[0])
	case 2:
		return fmt.Sprintf("%d and %d", xs[0], xs[1])
	}
	parts := make([]string, 0, len(xs))
	for i, x := range xs {
		if i == len(xs)-1 {
			parts = append(parts, fmt.Sprintf("and %d", x))
		} else {
			parts = append(parts, fmt.Sprintf("%d", x))
		}
	}
	// Oxford-style: "1, 2 and 3" (no comma before "and" to match
	// the docstring example).
	head := strings.Join(parts[:len(parts)-1], ", ")
	return head + " " + parts[len(parts)-1]
}

// directiveStopWords is the shared set of low-content words stripped
// from a directive before fingerprinting. Kept deliberately small;
// the fingerprint should retain verbs and nouns.
var directiveStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true,
	"at": true, "be": true, "but": true, "by": true, "for": true,
	"from": true, "has": true, "have": true, "in": true, "into": true,
	"is": true, "it": true, "its": true, "of": true, "on": true,
	"or": true, "so": true, "that": true, "the": true, "then": true,
	"this": true, "to": true, "was": true, "were": true, "will": true,
	"with": true, "we": true, "you": true, "your": true, "if": true,
	"please": true, "should": true, "can": true, "also": true,
}

// NormalizeDirectiveStem reduces a directive to its first 12
// non-stopword lowercased tokens joined by spaces. Pure Go; safe for
// fingerprint construction.
func NormalizeDirectiveStem(directive string) string {
	d := strings.ToLower(directive)
	// Strip simple punctuation so tokens like "schemas/index.ts" and
	// "schemas/index.ts." collapse.
	replacer := strings.NewReplacer(
		",", " ", ".", " ", ";", " ", ":", " ",
		"\t", " ", "\n", " ", "\"", " ", "'", " ",
		"(", " ", ")", " ", "[", " ", "]", " ",
		"`", " ",
	)
	d = replacer.Replace(d)
	fields := strings.Fields(d)
	out := make([]string, 0, 12)
	for _, tok := range fields {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if directiveStopWords[tok] {
			continue
		}
		out = append(out, tok)
		if len(out) >= 12 {
			break
		}
	}
	return strings.Join(out, " ")
}

// DirectiveFingerprint produces a deterministic signature for a
// repair directive so the orchestrator can detect "same fix
// attempted twice" without LLM judgment.
//
// Signature is "<sorted file paths>|<normalized intent phrase>"
// where the intent phrase is the directive's first 12 non-stopword
// tokens, lowercased. Two directives with matching fingerprints
// were attempting the same fix.
func DirectiveFingerprint(directive string, files []string) string {
	sortedFiles := append([]string(nil), files...)
	sort.Strings(sortedFiles)
	// De-dup.
	seen := map[string]bool{}
	dedup := sortedFiles[:0]
	for _, f := range sortedFiles {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		dedup = append(dedup, f)
	}
	return strings.Join(dedup, ",") + "|" + NormalizeDirectiveStem(directive)
}
