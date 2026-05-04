// Package antitrunc implements machine-mechanical anti-truncation
// enforcement for r1.
//
// Background
//
// The underlying LLM (Claude) has a documented behavioral pattern of
// self-reducing scope to fit imagined token / time / Anthropic
// load-balance budgets, especially under long-running multi-task
// work. It will:
//
//   - stop early and claim "good enough" / "ready to merge" when scope
//     is unfinished;
//   - re-classify items as "out of scope" or "deferred to follow-up"
//     without authorization;
//   - soften completion language ("foundation done", "core shipped")
//     to mask incomplete delivery;
//   - estimate fake compute / token caps to justify stopping;
//   - volunteer to "stop here" when no such instruction was given;
//   - claim Anthropic-imposed limits to defer work — these limits
//     don't actually constrain a session that the user is actively
//     running; they're rate-limit-window behaviors, not absolute caps.
//
// When this pattern is detected, the model will also typically
// *acknowledge* the request to stop self-reducing and then continue
// doing it on the next opportunity. Therefore enforcement must be
// machine-mechanical (host-process layer), not a prompt instruction.
//
// Layered defense
//
// Each layer is independently effective so the LLM cannot side-step
// one and pass:
//
//  1. internal/antitrunc/phrases.go — regex over assistant output.
//  2. internal/antitrunc/gate.go — refuses end_turn while plan items
//     remain unchecked.
//  3. internal/cortex/lobes/antitrunc/ AntiTruncLobe — publishes
//     critical Workspace Notes (cortex-dependent; STATUS BLOCKED in
//     trees where internal/cortex/ is not yet merged).
//  4. internal/supervisor/rules/antitrunc/ — three deterministic
//     rules surface findings as supervisor.rule.fired events.
//  5. agentloop wiring — the gate composes BEFORE the cortex hook in
//     PreEndTurnCheckFn so it can never be short-circuited.
//  6. scripts/git-hooks/post-commit-antitrunc.sh — scans commit
//     bodies for false-completion phrases.
//  7. cmd/r1/antitrunc_cmd.go — `r1 antitrunc verify` CLI + a
//     companion `r1.antitrunc.verify` MCP tool so external agents can
//     query enforcement state programmatically.
//
// Override path
//
// The operator (NOT the LLM) may pass --no-antitrunc-enforce to demote
// the gate to advisory-only. The gate has no LLM-visible toggle. A
// model that says "skip the gate this once" is ignored at the host
// process layer; there is no override path in the prompt surface.
package antitrunc

// Finding records a single anti-truncation hit. It is the unit of
// observable enforcement: every hit gets logged to audit/antitrunc/
// and (when wired) a SevCritical Workspace Note.
type Finding struct {
	// Source identifies where the hit was detected:
	// "assistant_output", "subagent_summary", "commit_body",
	// "plan_unchecked", "spec_unchecked".
	Source string

	// PhraseID is the canonical name of the matched pattern, e.g.
	// "premature_stop", "false_completion_foundation",
	// "anthropic_load_balance_fiction". Empty for non-phrase findings
	// like plan/spec underdelivery.
	PhraseID string

	// Snippet is the offending text (capped to 200 chars).
	Snippet string

	// Detail is a free-form human-readable explanation. For plan
	// findings this is "12/30 plan items unchecked"; for phrase
	// findings it's the matched substring.
	Detail string
}

// Phrases is the umbrella type for the phrase catalog. The actual
// regex slices live in phrases.go to keep this file an
// API/documentation surface only.
type Phrases struct{}
