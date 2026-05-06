// gate.go — scope-completion gate that refuses end_turn while the
// model has emitted truncation phrases or while plan/spec items
// remain unchecked.
//
// The Gate is the single most load-bearing component of the layered
// defense: it composes BEFORE the cortex hook in PreEndTurnCheckFn so
// it cannot be short-circuited by an LLM that says "skip the gate
// this once". The gate is host-process code with no LLM-visible
// override path.
//
// Detection signals (any one fires):
//
//   - TruncationPhrases hit in the latest assistant turn.
//   - Unchecked items in PlanPath (when set).
//   - Unchecked items in any in-progress spec listed in SpecPaths.
//   - FalseCompletionPhrases hit in recent commit bodies (when
//     CommitLookbackFn is set).
//
// The gate has TWO modes:
//
//   - Enforce mode (default) — CheckOutput returns a non-empty error
//     string that the agentloop appends as a [BUILD VERIFICATION
//     FAILED] message, forcing another turn.
//   - Advisory mode (operator passed --no-antitrunc-enforce) —
//     CheckOutput still detects but returns "" and writes findings
//     to AdvisoryFn; the gate doesn't block.
//
// Output format on enforce-mode hit (consumed verbatim by the
// agentloop's PreEndTurn pipeline):
//
//	[ANTI-TRUNCATION] phrase 'X' detected — fix scope, do not stop
//	[ANTI-TRUNCATION] N plan items unchecked. Continue. Do not end turn.
//	[ANTI-TRUNCATION] spec 'foo' has M unchecked items. Continue.
//	[ANTI-TRUNCATION] recent commit body claims false completion: 'spec 9 done'
package antitrunc

import (
	"fmt"
	"os"
	"strings"
)

// Message is the minimal subset of agentloop.Message the gate reads.
// Defined locally so the antitrunc package does NOT depend on
// agentloop (avoiding an import cycle when agentloop wires the gate
// in via internal/agentloop/antitrunc.go). The wiring file does the
// type-shape conversion.
type Message struct {
	Role string
	Text string
}

// Gate is the configurable scope-completion gate. Construct one per
// agentloop session; CheckOutput is safe for repeated calls within a
// single session (it re-reads files on every invocation so plan
// edits between turns are picked up).
type Gate struct {
	// PlanPath is the absolute path to the build plan markdown
	// (typically plans/build-plan.md). Empty disables plan
	// scanning.
	PlanPath string

	// SpecPaths are absolute paths to spec markdown files whose
	// STATUS:in-progress and unchecked-items count gate end_turn.
	// Empty disables spec scanning.
	SpecPaths []string

	// CommitLookbackFn returns the body texts of the last N commits.
	// When non-nil, FalseCompletionPhrases are scanned across the
	// returned bodies. Pluggable so tests can drive it without
	// shelling out to git.
	CommitLookbackFn func(n int) ([]string, error)

	// CommitLookback is the count passed to CommitLookbackFn (default 5).
	CommitLookback int

	// Advisory, when true, demotes the gate to advisory-only:
	// CheckOutput returns "" but writes findings to AdvisoryFn.
	// The operator's --no-antitrunc-enforce flag flips this.
	Advisory bool

	// AdvisoryFn is invoked with each Finding when Advisory=true.
	// nil = no advisory sink (findings are silently dropped — the
	// gate still detects but doesn't block or log).
	AdvisoryFn func(Finding)
}

// CheckOutput is the load-bearing entrypoint. It returns "" when
// end_turn is allowed, or a non-empty string explaining why the gate
// refuses. The format is the catalog laid out in the package doc:
// each refusal reason on its own [ANTI-TRUNCATION] prefixed line.
//
// The agentloop wiring (internal/agentloop/antitrunc.go) calls this
// from PreEndTurnCheckFn before any other gate. A non-empty return
// causes the loop to inject the message and continue, forcing the
// model to fix scope rather than exit.
func (g *Gate) CheckOutput(messages []Message) string {
	var findings []Finding

	// 1. Phrase scan over the latest assistant turn.
	if last := lastAssistantText(messages); last != "" {
		for _, m := range MatchTruncation(last) {
			findings = append(findings, Finding{
				Source:   "assistant_output",
				PhraseID: m.PhraseID,
				Snippet:  m.Snippet,
				Detail:   fmt.Sprintf("phrase %q detected in assistant output", m.PhraseID),
			})
		}
	}

	// 2. Plan unchecked count.
	if g.PlanPath != "" {
		if data, err := os.ReadFile(g.PlanPath); err == nil {
			done, total := CountChecklist(string(data))
			if total > 0 && done < total {
				findings = append(findings, Finding{
					Source: "plan_unchecked",
					Detail: fmt.Sprintf("%d/%d plan items unchecked in %s", total-done, total, g.PlanPath),
				})
			}
		}
	}

	// 3. Spec unchecked count for in-progress specs.
	for _, sp := range g.SpecPaths {
		data, err := os.ReadFile(sp)
		if err != nil {
			continue
		}
		text := string(data)
		if !specInProgress(text) {
			continue
		}
		done, total := CountChecklist(text)
		if total > 0 && done < total {
			findings = append(findings, Finding{
				Source: "spec_unchecked",
				Detail: fmt.Sprintf("spec %q has %d/%d unchecked items", sp, total-done, total),
			})
		}
	}

	// 4. Commit body scan for FalseCompletionPhrases.
	if g.CommitLookbackFn != nil {
		n := g.CommitLookback
		if n <= 0 {
			n = 5
		}
		bodies, err := g.CommitLookbackFn(n)
		if err == nil {
			for _, body := range bodies {
				for _, m := range MatchFalseCompletion(body) {
					// Multi-signal corroboration: only fire on
					// false-completion when at least one OTHER
					// signal also fires (unchecked plan/spec or
					// truncation phrase). This matches the spec's
					// risk mitigation (§"False positives") — pure
					// commit-message scans alone produced too many
					// false positives in the dry-run.
					if hasOtherSignal(findings) {
						findings = append(findings, Finding{
							Source:   "commit_body",
							PhraseID: m.PhraseID,
							Snippet:  m.Snippet,
							Detail:   fmt.Sprintf("recent commit body claims false completion: %q", m.Snippet),
						})
					}
				}
			}
		}
	}

	// Advisory mode: forward findings to AdvisoryFn but return "".
	if g.Advisory {
		if g.AdvisoryFn != nil {
			for _, f := range findings {
				g.AdvisoryFn(f)
			}
		}
		return ""
	}

	if len(findings) == 0 {
		return ""
	}
	return formatFindings(findings)
}

// lastAssistantText returns the text content of the most recent
// assistant message, or "" if no such message exists.
func lastAssistantText(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].Text
		}
	}
	return ""
}

// hasOtherSignal reports whether findings contains any source other
// than commit_body. Used for multi-signal corroboration on
// false-completion phrases.
func hasOtherSignal(findings []Finding) bool {
	for _, f := range findings {
		if f.Source != "commit_body" {
			return true
		}
	}
	return false
}

// formatFindings renders findings as the [ANTI-TRUNCATION] prefixed
// message the agentloop injects. Each finding gets its own line.
func formatFindings(findings []Finding) string {
	var b strings.Builder
	for _, f := range findings {
		switch f.Source {
		case "assistant_output":
			fmt.Fprintf(&b, "[ANTI-TRUNCATION] phrase %q detected — fix scope, do not stop\n", f.PhraseID)
		case "plan_unchecked":
			fmt.Fprintf(&b, "[ANTI-TRUNCATION] %s. Continue. Do not end turn.\n", f.Detail)
		case "spec_unchecked":
			fmt.Fprintf(&b, "[ANTI-TRUNCATION] %s. Continue.\n", f.Detail)
		case "commit_body":
			fmt.Fprintf(&b, "[ANTI-TRUNCATION] %s\n", f.Detail)
		default:
			fmt.Fprintf(&b, "[ANTI-TRUNCATION] %s\n", f.Detail)
		}
	}
	b.WriteString("Self-truncation will not be tolerated. Resume work on unchecked items immediately.\n")
	return b.String()
}

// specInProgress reports whether the spec markdown declares
// STATUS:in-progress (case-insensitive). Only specs in this state
// gate end_turn — done/scoped/scoping/potential specs are ignored.
func specInProgress(text string) bool {
	low := strings.ToLower(text)
	// Match patterns like "<!-- STATUS: in-progress -->",
	// "STATUS: in-progress", "STATUS:in-progress".
	for _, marker := range []string{
		"status: in-progress",
		"status:in-progress",
		"status: in_progress",
		"status:in_progress",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}
