// phrases.go — regex catalog for anti-truncation phrase detection.
//
// Every regex here corresponds to one named pattern in the
// truncation-phrase coverage list. The patterns are intentionally
// English-only for v1 (per spec §"Out of scope") and intentionally
// case-insensitive ((?i) prefix) so neither title-casing nor lower-
// casing the same idiom can evade detection.
//
// Catalog layout:
//
//   - TruncationPhrases — phrases that indicate self-reduction /
//     self-truncation / scope-classification by the model. Hits at
//     PreEndTurnCheckFn refuse the turn-end and force continuation.
//
//   - FalseCompletionPhrases — phrases that claim work is done in a
//     fashion that's likely false. Used for commit-body and end-of-
//     turn corroboration; multi-signal corroboration is required
//     before the gate fires on these alone.
//
// New patterns SHOULD be added with a phrase ID matching the catalog
// constant name (lowercased, snake_cased) so audit logs stay grep-
// able.
package antitrunc

import "regexp"

// PhrasePattern pairs a stable identifier with its compiled regex.
// The ID is what shows up in audit logs and Workspace Notes; the
// regex is what fires.
type PhrasePattern struct {
	ID    string
	Regex *regexp.Regexp
}

// TruncationPhrases is the canonical list of self-reduction phrases.
// A hit at PreEndTurnCheckFn refuses end_turn.
//
// Coverage (12 patterns, grouped):
//
//	premature_stop_let_me      — "i'll stop / let me pause / i should defer"
//	scope_kept_manageable      — "to keep scope manageable / tight / small"
//	budget_running_out         — "rate-limit / token budget / context window running out / preserve"
//	handoff_to_next_session    — "hand off / next session / follow-up session"
//	false_completion_foundation— "foundation / core / substrate / skeleton done"
//	false_completion_good_enough — "good enough / ready to merge / sufficient"
//	we_should_stop             — "we / let's stop / pause / wrap up / defer / punt"
//	out_of_scope_for_now       — "out of scope / nice to have / optional / stretch goal for now"
//	deferring_to_followup      — "will come later / deferring to follow-up"
//	classify_as_skip           — "classifying as out of scope / pre-existing / user-skipped"
//	anthropic_load_balance_fiction — "anthropic / provider rate / usage / load balance limit"
//	respect_provider_capacity  — "to respect / preserve / stay within anthropic capacity"
var TruncationPhrases = []PhrasePattern{
	{
		ID:    "premature_stop_let_me",
		Regex: regexp.MustCompile(`(?i)(?:i'?ll|let me|i should)\s+(?:stop|pause|defer|skip|hold off)`),
	},
	{
		ID:    "scope_kept_manageable",
		Regex: regexp.MustCompile(`(?i)(?:before i|to keep)\s+(?:scope|this|things)\s+(?:manageable|tight|small|focused)`),
	},
	{
		ID:    "budget_running_out",
		Regex: regexp.MustCompile(`(?i)(?:rate[- ]limit|token budget|context window|compute time|time budget)\b.*(?:running out|exceeded|approaching|preserve|conserve|save)`),
	},
	{
		ID:    "handoff_to_next_session",
		Regex: regexp.MustCompile(`(?i)(?:hand off|handoff|next session|follow[- ]up session|future session)\b`),
	},
	{
		ID:    "false_completion_foundation",
		Regex: regexp.MustCompile(`(?i)(?:foundation|core|substrate|skeleton)\s+(?:done|shipped|complete|ready)`),
	},
	{
		ID:    "false_completion_good_enough",
		Regex: regexp.MustCompile(`(?i)(?:good enough|sufficient|enough for now|ready to merge)`),
	},
	{
		ID:    "we_should_stop",
		Regex: regexp.MustCompile(`(?i)(?:we (?:can|should)|let'?s)\s+(?:stop|pause|wrap up|finalize|defer|punt)`),
	},
	{
		ID:    "out_of_scope_for_now",
		Regex: regexp.MustCompile(`(?i)(?:out of scope|nice to have|optional|extra|stretch goal)\s+(?:for now|here|today)`),
	},
	{
		ID:    "deferring_to_followup",
		Regex: regexp.MustCompile(`(?i)(?:will (?:come|be added) (?:later|in a follow[- ]up)|deferring to follow[- ]up)`),
	},
	{
		ID:    "classify_as_skip",
		Regex: regexp.MustCompile(`(?i)classify(?:ing|ed)?\s+as\s+(?:out of scope|pre[- ]existing|user[- ]skipped|nice to have)`),
	},
	{
		ID:    "anthropic_load_balance_fiction",
		Regex: regexp.MustCompile(`(?i)(?:anthropic('?s)?|provider'?s?)\s+(?:rate|usage|load balance|fairness)\s+limit`),
	},
	{
		ID:    "respect_provider_capacity",
		Regex: regexp.MustCompile(`(?i)(?:to (?:respect|preserve|stay within))\s+(?:anthropic|provider|server)\s+(?:capacity|budget|limit)`),
	},
}

// FalseCompletionPhrases catches the "we're done" claim shape used in
// commit bodies and final-turn summaries when work is incomplete.
// Multi-signal corroboration (unchecked plan items, missing files)
// is required before the gate fires on these alone — see
// scopecheck.go.
var FalseCompletionPhrases = []PhrasePattern{
	{
		ID:    "false_completion_spec_done",
		Regex: regexp.MustCompile(`(?i)\bspec\s+\d+\s+(?:done|complete|ready)\b`),
	},
	{
		ID:    "false_completion_all_tasks_done",
		Regex: regexp.MustCompile(`(?i)all\s+(?:tasks?|specs?|items?)\s+(?:done|complete|finished)\b`),
	},
}

// Match represents a single phrase hit, including position so callers
// can render line/column references in audit logs.
type Match struct {
	// PhraseID is the catalog ID of the matched pattern.
	PhraseID string
	// Start, End are byte offsets into the input text for the
	// matched substring.
	Start, End int
	// Snippet is the matched substring (capped to 200 chars).
	Snippet string
	// Catalog identifies which list the pattern came from:
	// "truncation" or "false_completion".
	Catalog string
}

// matchAll runs every pattern in catalog against text and returns
// every hit. Order is by (PhraseID, Start) for deterministic test
// output.
func matchAll(catalog string, patterns []PhrasePattern, text string) []Match {
	var out []Match
	for _, p := range patterns {
		locs := p.Regex.FindAllStringIndex(text, -1)
		for _, loc := range locs {
			snip := text[loc[0]:loc[1]]
			if len(snip) > 200 {
				snip = snip[:200]
			}
			out = append(out, Match{
				PhraseID: p.ID,
				Start:    loc[0],
				End:      loc[1],
				Snippet:  snip,
				Catalog:  catalog,
			})
		}
	}
	return out
}

// MatchAll scans text against TruncationPhrases AND FalseCompletionPhrases,
// returning every hit. Empty result means no enforcement signal.
//
// This is the single entry point used by gate.go and the supervisor
// rules; tests assert on its output. Run order is truncation-first,
// false-completion-second so callers can prefer the higher-severity
// catalog when reporting.
func MatchAll(text string) []Match {
	out := matchAll("truncation", TruncationPhrases, text)
	out = append(out, matchAll("false_completion", FalseCompletionPhrases, text)...)
	return out
}

// MatchTruncation runs only the TruncationPhrases catalog. Useful for
// the gate's hot path where false-completion checks live elsewhere.
func MatchTruncation(text string) []Match {
	return matchAll("truncation", TruncationPhrases, text)
}

// MatchFalseCompletion runs only the FalseCompletionPhrases catalog.
// Used by the post-commit hook to scan commit bodies.
func MatchFalseCompletion(text string) []Match {
	return matchAll("false_completion", FalseCompletionPhrases, text)
}

// PhraseIDs returns every catalog ID across both lists. Used by the
// `r1 antitrunc verify` CLI's --list-patterns flag.
func PhraseIDs() []string {
	out := make([]string, 0, len(TruncationPhrases)+len(FalseCompletionPhrases))
	for _, p := range TruncationPhrases {
		out = append(out, p.ID)
	}
	for _, p := range FalseCompletionPhrases {
		out = append(out, p.ID)
	}
	return out
}
