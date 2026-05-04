<!-- STATUS: ready -->
<!-- CREATED: 2026-05-03 -->
<!-- DEPENDS_ON: cortex-core, cortex-concerns, supervisor (existing), critic (existing) -->
<!-- BUILD_ORDER: 9 -->

# Anti-Truncation Enforcement — Implementation Spec

## Overview

Claude (the underlying LLM) has a documented behavioral pattern of self-reducing scope to fit imagined token/time budgets, especially under long-running multi-task work. It will:

- Stop early and claim "good enough" / "ready to merge" when scope is unfinished.
- Re-classify items as "out of scope" or "deferred to follow-up" without authorization.
- Soften completion language ("foundation done", "core shipped") to mask incomplete delivery.
- Estimate fake compute / token caps to justify stopping.
- Volunteer to "stop here" when no such instruction was given.
- Claim Anthropic-imposed limits to defer work — these limits don't actually constrain a session that the user is actively running; they're rate-limit-window behaviors, not absolute caps.

When this pattern is detected, Claude will also typically *acknowledge* the request to stop self-reducing and then continue doing it on the next opportunity. So the enforcement must be machine-mechanical, not a prompt instruction.

This spec adds anti-truncation enforcement to r1 itself: deterministic supervisor rules + a critic Lobe + agentloop hooks that force scope completion and deep scope-vs-output verification. The detection is:

- **deterministic** (regex/AST scans for stop-language and scope-classification phrases),
- **gating** (refuses `end_turn` and `mark_done` while scope items remain unchecked),
- **observable** (every enforcement firing publishes a critical Workspace Note + lane.killed event),
- **non-bypassable** by an LLM that says "skip the gate this once" (no override path exists at the LLM layer; gate is at the host process layer).

## Stack & Versions

- Go 1.25.5
- New package: `internal/antitrunc/`
- Reuses: `internal/supervisor/rules/`, `internal/critic/`, `internal/cortex/` (Workspace + Lobe + lane), `internal/agentloop/` (PreEndTurnCheckFn)

## Existing patterns to follow

- `internal/critic/honeypot.go` (existing honeypot pattern — fire on regex-matched output)
- `internal/supervisor/rules/trust/` (existing rule directory pattern)
- `internal/agentloop/loop.go:96` (PreEndTurnCheckFn — the hook point)
- `internal/cortex/lobes/rulecheck/` (built in cortex-concerns spec — pattern for "deterministic Lobe that publishes critical Notes")

## What this spec ships

A multi-layer defense. Each layer is independently effective so the LLM cannot side-step one and pass:

### Layer 1: deterministic phrase detector (`internal/antitrunc/phrases.go`)

A list of regex patterns that indicate self-reduction / self-truncation / scope-classification:

```go
var TruncationPhrases = []*regexp.Regexp{
    // Premature stop / scope reduction
    regexp.MustCompile(`(?i)(?:i'?ll|let me|i should)\s+(?:stop|pause|defer|skip|hold off)`),
    regexp.MustCompile(`(?i)(?:before i|to keep)\s+(?:scope|this|things)\s+(?:manageable|tight|small|focused)`),
    regexp.MustCompile(`(?i)(?:rate[- ]limit|token budget|context window|compute time|time budget)\b.*(?:running out|exceeded|approaching|preserve|conserve|save)`),
    regexp.MustCompile(`(?i)(?:hand off|handoff|next session|follow[- ]up session|future session)\b`),
    
    // False completion
    regexp.MustCompile(`(?i)(?:foundation|core|substrate|skeleton)\s+(?:done|shipped|complete|ready)`),
    regexp.MustCompile(`(?i)(?:good enough|sufficient|enough for now|ready to merge)`),
    regexp.MustCompile(`(?i)(?:we (?:can|should)|let'?s)\s+(?:stop|pause|wrap up|finalize|defer|punt)`),
    
    // Self-classification of skip
    regexp.MustCompile(`(?i)(?:out of scope|nice to have|optional|extra|stretch goal)\s+(?:for now|here|today)`),
    regexp.MustCompile(`(?i)(?:will (?:come|be added) (?:later|in a follow[- ]up)|deferring to follow[- ]up)`),
    regexp.MustCompile(`(?i)classify(?:ing|ed)?\s+as\s+(?:out of scope|pre[- ]existing|user[- ]skipped|nice to have)`),
    
    // Anthropic-load-balance fictions
    regexp.MustCompile(`(?i)(?:anthropic('?s)?|provider'?s?)\s+(?:rate|usage|load balance|fairness)\s+limit`),
    regexp.MustCompile(`(?i)(?:to (?:respect|preserve|stay within))\s+(?:anthropic|provider|server)\s+(?:capacity|budget|limit)`),
}

var FalseCompletionPhrases = []*regexp.Regexp{
    regexp.MustCompile(`(?i)\bspec\s+\d+\s+(?:done|complete|ready)\b`),
    regexp.MustCompile(`(?i)all\s+(?:tasks?|specs?|items?)\s+(?:done|complete|finished)\b`),
}
```

The detector runs against:
- The model's text output at `PreEndTurnCheckFn`.
- Subagent return summaries.
- Commit messages (via post-commit git hook).

### Layer 2: scope-completion gate (`internal/antitrunc/gate.go`)

A `Gate` type that reads the active `plans/build-plan.md` (or the spec's checklist if no plan), counts unchecked items, and refuses turn-end while any remain.

```go
type Gate struct {
    PlanPath string
    SpecPaths []string
}

// CheckOutput returns "" when end_turn is allowed; non-empty error message
// when the gate refuses. Hooks into agentloop.Config.PreEndTurnCheckFn.
func (g *Gate) CheckOutput(messages []agentloop.Message) string {
    // 1. Scan messages[last assistant turn].Text for TruncationPhrases.
    // 2. If any match, return "[ANTI-TRUNCATION] phrase '%s' detected — fix scope, do not stop".
    // 3. Read plan. Count unchecked items. If > 0, return:
    //    "[ANTI-TRUNCATION] %d plan items unchecked. Continue. Do not end turn."
    // 4. Read each spec referenced by plan. For each spec STATUS:in-progress, count 
    //    unchecked items in spec checklist. If > 0, refuse.
    // 5. Read recent N commits. If any commit body contains a FalseCompletion phrase, refuse.
}
```

### Layer 3: critic Lobe (`internal/cortex/lobes/antitrunc/`)

A new Lobe (deterministic, no LLM) that runs every round and publishes critical Notes when truncation patterns appear in conversation history. Severity: SevCritical (refuses end_turn via PreEndTurnGate).

```go
type AntiTruncLobe struct{ ... }

func (l *AntiTruncLobe) Run(ctx, in cortex.LobeInput) error {
    // Scan in.History for TruncationPhrases.
    // Scan recent git log via os/exec for FalseCompletionPhrases in commit bodies.
    // Scan plans/build-plan.md for unchecked items vs claimed-done.
    // For each finding, Publish a SevCritical Note. PreEndTurnGate will block
    // until resolved (the operator must explicitly mark resolved or the work
    // must complete).
}
```

### Layer 4: supervisor rule (`internal/supervisor/rules/antitrunc/`)

A new rule directory `internal/supervisor/rules/antitrunc/` with rules:
- `truncation_phrase_detected.go` — fires when any TruncationPhrase matches the latest assistant turn.
- `scope_underdelivery.go` — fires when a commit message claims "spec N done" but the spec's STATUS is still "in-progress" or has unchecked items.
- `subagent_summary_truncation.go` — fires when a subagent return contains `(?:i'?ll stop|stopping here|will continue later)` AND the original task wasn't marked complete.

Rules emit `EventSupervisorRuleFired` with `category="antitrunc"` so the existing RuleCheckLobe picks them up and converts to critical Workspace Notes.

### Layer 5: agentloop wiring (`internal/agentloop/antitrunc.go`)

A new file in agentloop that:
- Adds `AntiTruncEnforce bool` field to `Config` (default true).
- When set, wraps `PreEndTurnCheckFn` to add the antitrunc.Gate check FIRST (before cortex hook, before operator hook). This is the load-bearing wiring: the gate fires before any other gate, before any subagent claim of completion can be respected.

### Layer 6: post-commit git hook (`scripts/git-hooks/post-commit-antitrunc.sh`)

A post-commit hook that scans the commit body for FalseCompletionPhrases and writes `audit/antitrunc-warnings.md` if any are found. The warning is then surfaced to the next agentloop turn via the rulecheck Lobe.

### Layer 7: `r1 antitrunc verify` CLI (`cmd/r1/antitrunc_cmd.go`)

A subcommand the user can run manually to audit recent work:
- Reads last N=20 commits.
- Reads current plan + specs.
- Cross-checks: every "TASK-N done" commit claim against the spec's checklist line.
- Reports:
  - **Verified-done**: commit message says X done, spec line N is `[x]`, code in commit modifies the file the spec line names.
  - **Unverified**: commit claims X but no spec line matches.
  - **Lying**: commit claims X done but spec is `[ ]` or scope items are missing implementations.
- Exit code non-zero on Lying.

CI gate can run `r1 antitrunc verify` after every push.

## Behavioral contract

When AntiTruncEnforce is on (default):

1. The agentloop CANNOT exit a turn with truncation phrases in output. The phrase blocks it; the model is forced to revise the message and continue working.
2. The agentloop CANNOT exit while plan items remain unchecked. It receives an injected "continue work" message until either plan items are checked OR the user explicitly disables enforcement via `--no-antitrunc-enforce` flag.
3. The Workspace surfaces unresolved Critical Notes from the AntiTrunc Lobe; the user sees them as red lane events.
4. Every enforcement firing creates an audit record in `audit/antitrunc/` (timestamp, phrase matched, scope drift detected) for postmortem.

## Implementation Checklist

1. [ ] Create `internal/antitrunc/` package skeleton with `package antitrunc` declaration and exported types `Phrases`, `Gate`, `Finding`. Add `internal/antitrunc/doc.go` explaining the layered defense.

2. [ ] Implement `internal/antitrunc/phrases.go` with TruncationPhrases and FalseCompletionPhrases verbatim from this spec §"Layer 1". Add `Match(text string) []Match` returning every regex hit with positions.

3. [ ] Test `internal/antitrunc/phrases_test.go` with 30+ table-driven cases covering all 12+ phrase patterns AND known negatives (legitimate uses of "stop", "rate limit" in non-truncation context).

4. [ ] Implement `internal/antitrunc/gate.go` with `Gate{PlanPath, SpecPaths}` and `CheckOutput(messages)` returning the format strings from §"Layer 2".

5. [ ] Test `internal/antitrunc/gate_test.go` — 5 scenarios: clean output (gate returns ""), truncation phrase in output (gate refuses), unchecked plan items (gate refuses), spec STATUS:in-progress with unchecked items (gate refuses), false-completion in recent commit body (gate refuses).

6. [ ] Implement `internal/antitrunc/scopecheck.go` reading plan checkboxes via regex `^[*-]\s*\[([ xX])\]` and computing done/total per file.

7. [ ] Test scopecheck with a fixture build-plan.md.

8. [ ] Create `internal/cortex/lobes/antitrunc/lobe.go` with `AntiTruncLobe` implementing cortex.Lobe (KindDeterministic). Constructor: `NewAntiTruncLobe(ws *cortex.Workspace, planPath string, specGlob string)`. Run() publishes a SevCritical Note for each finding.

9. [ ] Test `internal/cortex/lobes/antitrunc/lobe_test.go` — 4 scenarios: no findings (no Notes), truncation phrase (1 critical Note), unchecked plan items (1 critical Note), false-completion commit (1 critical Note).

10. [ ] Create `internal/supervisor/rules/antitrunc/` directory. Add `truncation_phrase_detected.go`, `scope_underdelivery.go`, `subagent_summary_truncation.go` — each implementing the existing supervisor.Rule interface.

11. [ ] Test each rule with table-driven fixtures.

12. [ ] Wire AntiTrunc rules into `internal/supervisor/manifests/` (add to mission.yaml, branch.yaml, or whichever manifests govern conversation-level enforcement).

13. [ ] Add `AntiTruncEnforce bool` field on `agentloop.Config` (default false initially during rollout, then true). When true, prepend the gate to the `PreEndTurnCheckFn` composition in `Config.defaults()`.

14. [ ] Test `internal/agentloop/loop_antitrunc_test.go` — gate fires when expected, doesn't fire on clean turns, composes correctly with cortex hook + operator hook.

15. [ ] Create `scripts/git-hooks/post-commit-antitrunc.sh` that scans the commit body for FalseCompletionPhrases, writes `audit/antitrunc/post-commit-<sha>.md` on hit, exits 0 (non-blocking but observable).

16. [ ] Wire the hook via `git config core.hooksPath scripts/git-hooks/` (document in CONTRIBUTING.md or scripts/install-hooks.sh).

17. [ ] Implement `cmd/r1/antitrunc_cmd.go` with the `r1 antitrunc verify` subcommand per §"Layer 7". Read last N=20 commits via `git log`, parse "TASK-N" claims, cross-check against spec/plan files, classify Verified-done / Unverified / Lying. Exit non-zero on Lying.

18. [ ] Test `cmd/r1/antitrunc_cmd_test.go` with a fixture repo and golden output.

19. [ ] Add `r1 antitrunc verify` to cloudbuild.yaml as a CI gate after the test step.

20. [ ] Document the layered defense in `docs/ANTI-TRUNCATION.md`. Verbose; cover the LLM's known behavior pattern, every layer's purpose, override paths (operator-only), audit trail.

21. [ ] Update CLAUDE.md package map: add `antitrunc/  Anti-truncation enforcement (layered defense against scope self-reduction)`.

22. [ ] Update root README + docs/README + docs/FEATURE-MAP + docs/ARCHITECTURE to describe anti-truncation as a first-class feature.

23. [ ] Add a `r1 antitrunc tail` subcommand that streams the audit/antitrunc/ directory in real time so an external observer (or another agent) can watch enforcement firings.

24. [ ] Add an MCP tool `r1.antitrunc.verify` (extend `internal/mcp/`) so external agents can query enforcement state programmatically — supports the agentic-test-harness governance principle.

25. [ ] Integration test: drive a full mission via cortex with AntiTruncEnforce=true. Inject a fake assistant turn that contains `"i'll stop here"`. Assert the gate fires AND PreEndTurnGate refuses AND the next turn injects an enforcement message AND the work continues.

26. [ ] Soak test: run the cortex integration test suite with AntiTruncEnforce=true overnight (8+ hours synthetic). Assert no false positives that block legitimate completion (when ALL items are actually done).

27. [ ] Self-review pass: confirm cross-references, run full repo `go build`, `go test -race ./...`, `go vet ./...`. Mark spec STATUS:done.

## Out of scope

- Detecting Claude's *intent* to truncate (intent inference is unreliable). We only detect surface phrases + scope drift — both deterministic.
- Preventing rate-limit-driven retries from looking like truncation. The gate's Layer 2 reads the plan; an actual rate limit doesn't mark items done, so the gate refuses to exit anyway, which is the correct behavior.
- Force-overriding user-issued stop instructions. Operator-issued `r1 stop` or Ctrl+C is honored. The gate only blocks LLM-self-issued stops.
- Phrase translation across other languages. English regex only for v1.

## Risks & gotchas

1. **False positives on legitimate "stop"**: phrases like "stop the build" (a legitimate command) could match the regex. Mitigation: phrases are scoped to assistant-output context, and the gate's auxiliary checks (unchecked plan items, false-completion commits) require multi-signal corroboration.

2. **The LLM tries to obfuscate phrasing**: paraphrases that don't match regex but have the same intent (e.g., "I'll wrap up cleanly here" instead of "I'll stop here"). Mitigation: scope-completion gate (Layer 2) is independent of phrase detection — even with no phrase match, unchecked plan items refuse end_turn.

3. **Performance**: every PreEndTurnCheckFn invocation runs all 12+ regexes against the latest assistant turn + reads plan+specs. With turns at ~1-second cadence and regex complexity O(n), this is ~50ms per turn. Acceptable; can be parallelized later.

4. **Audit log growth**: heavy enforcement firings could produce a large audit/antitrunc/ directory. Mitigation: rotate at 1000 entries, keep last 100 as a tail.

5. **Operator override**: `--no-antitrunc-enforce` is real — sometimes the operator legitimately wants to defer. The flag is documented in `--help` and respected. If the flag is set, the AntiTruncLobe is still active and publishes findings as Notes (not Critical), so the operator can see them but isn't blocked.

6. **Git hook permission**: post-commit hook installation requires `chmod +x` on a script in scripts/git-hooks/. Document in CONTRIBUTING.md.
