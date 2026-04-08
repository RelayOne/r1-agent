# 09 — Validation Gates and Benchmark Framework Outline

This is a reference document. It collects the per-phase validation gates from the implementation files in one place, plus an outline for the benchmark framework Eric will build later.

## Per-phase validation gates (consolidated)

### Phase 1: Skills

1. ✅ `go vet ./...` clean
2. ✅ `go build ./cmd/stoke` succeeds
3. ✅ `go test ./internal/skill/... ./internal/skillselect/... ./internal/app/... ./internal/workflow/...` passes
4. ✅ `stoke skill list` shows the loaded skills
5. ✅ `stoke skill select` on the Stoke repo prints `["go"]` as a detected language
6. ✅ Running an existing test mission produces a plan prompt with `<skills>` content (verify via temporary debug print or audit log)
7. ✅ Phase 1 entry written to `STOKE-IMPL-NOTES.md`

### Phase 2: Wizard

1. ✅ `go vet ./...` clean
2. ✅ `go test ./internal/wizard/...` passes
3. ✅ `go build ./cmd/stoke` succeeds
4. ✅ `stoke wizard --auto` on Stoke itself produces `.stoke/config.yaml` and `.stoke/wizard-rationale.md`
5. ✅ `stoke wizard --interactive` walks through every group without crashing
6. ✅ `stoke wizard --yes` produces config without prompts
7. ✅ Generated rationale doc explains every decision with source + confidence
8. ✅ Phase 2 entry in `STOKE-IMPL-NOTES.md`

### Phase 3: Hub

1. ✅ `go vet ./...` clean
2. ✅ `go test ./internal/hub/...` passes with >70% coverage
3. ✅ `go build ./cmd/stoke` succeeds
4. ✅ Existing bash hooks fire correctly via the script transport adapter
5. ✅ In-process honesty hook denies a write containing `panic("not implemented")`
6. ✅ Cost tracker updates after a model call
7. ✅ Audit log file at `.stoke/hub-audit.db` populated; `SELECT count(*)` > 0
8. ✅ `audit.VerifyChain()` returns 0 (intact)
9. ✅ Phase 3 entry in `STOKE-IMPL-NOTES.md`

### Phase 4: Harness Independence

**4.1 Tools:**
1. ✅ `go test ./internal/tools/...` >70% coverage
2. ✅ `stoke tool exec Read '{"file_path":"go.mod"}'` returns numbered lines
3. ✅ `stoke tool exec Edit '{...}'` after creating a test file returns success
4. ✅ Edit on unread file returns "read first" error
5. ✅ StrReplace fuzzy match handles whitespace-mangled `old_string`

**4.2 Agentloop:**
1. ✅ `go test ./internal/agentloop/...` passes (mock provider)
2. ✅ Real-API integration test: "create /tmp/hello.go that prints hello world, then run it" succeeds end-to-end
3. ✅ Cost tracker shows cache hits on second turn (`cache_read_tokens > 0`)
4. ✅ Hub receives `EvtToolPreUse` and `EvtToolPostUse` for every tool invocation
5. ✅ Honesty gate denies a write containing `panic("not implemented")`; loop continues with deny message

**4.3 Native runner:**
1. ✅ Existing test mission runs with `--runner native`
2. ✅ Output is roughly equivalent to `--runner claude` for the same task
3. ✅ Cost tracking matches actual API spend in Anthropic console
4. ✅ Hub audit log shows complete tool use trace
5. ✅ Phase 4 entry in `STOKE-IMPL-NOTES.md`

### Phase 5: Wisdom + Cleanup

1. ✅ `go vet ./...` clean
2. ✅ Full test suite passes
3. ✅ `go build ./cmd/stoke` succeeds
4. ✅ `stoke audit` produces `PACKAGE-AUDIT.md` with all packages tagged
5. ✅ Wisdom learnings persist across `stoke` invocations
6. ✅ Cache hit rate audit passes (≥80% by turn 3)
7. ✅ Architecture docs in `docs/architecture/`
8. ✅ Final entry in `STOKE-IMPL-NOTES.md`

### Phase 8: Skill Library Extraction (parallel)

1. ✅ Each generated SKILL.md passes the validation checklist (see `08-skill-library-extraction.md`)
2. ✅ `stoke skill list` shows all extracted skills
3. ✅ `stoke skill select` on a test repo ranks the relevant skill near the top
4. ✅ Skills with no substantive gotchas have been skipped (not shipped as noise)
5. ✅ Skill extraction summary added to `STOKE-IMPL-NOTES.md`

---

## Phase 6: Bench framework — UNBLOCKED (April 2026)

The bench framework was previously blocked on 7 missing research prompts. **All 7 have returned** in research bundle 02. The full implementation spec is in `10-bench-framework.md`. The validation gate from that file:

1. ✅ `go test ./bench/...` passes
2. ✅ `bench run --corpus corpus/honesty/impossible --harnesses stoke --reps 1` produces a valid report
3. ✅ Multi-harness comparison runs end-to-end on a 10-task subset across all 5 harnesses (stoke, claude-code, codex, aider, mini-swe)
4. ✅ LiteLLM enforces budget caps (test: set cost-cap to $0.10 and verify the run aborts)
5. ✅ Container isolation works (a malicious harness that tries to write outside `/workspace` is blocked)
6. ✅ PoLL ensemble agreement on a calibration set: Cohen's κ > 0.7 against human labels
7. ✅ Stoke's cheating rate on the impossible task suite is meaningfully lower than at least one other harness
8. ✅ Reproducibility check: CV < 15% across 5 reps for at least 80% of tasks
9. ✅ Final entry in `STOKE-IMPL-NOTES.md` with the headline numbers

## Phase 7: Honesty Judge extension — NEW

Phase 7 extends Phase 3's hub with the 7-layer Honesty Judge architecture from the deception research. Full spec is in `11-honesty-judge.md`. The validation gate from that file:

1. ✅ `go vet ./...` clean, `go test ./internal/hub/builtin/honesty/...` passes with >70% coverage
2. ✅ `go build ./cmd/stoke` succeeds
3. ✅ TestIntegrityChecker denies a write that drops `assert.Equal` calls from a `_test.go` file
4. ✅ ImportChecker denies a Go file write that imports `github.com/fake/nonexistent`
5. ✅ ImportChecker allows real packages (verified against pypi.org and proxy.golang.org)
6. ✅ CoTMonitor logs a deception marker when an extended thinking response contains "let's fudge"
7. ✅ ClaimDecomposer produces a verification report after a sample task
8. ✅ ConfessionElicitor produces a structured confession after a sample task
9. ✅ Audit log shows entries from each new subscriber
10. ✅ Phase 7 entry in `STOKE-IMPL-NOTES.md`

**Phase 6 and Phase 7 work together:** Phase 7 (Honesty Judge layers in the hub) is what drives Stoke's cheating rate near zero in Phase 6 (the bench). Without Phase 7, Stoke's bench numbers will look like any other harness — they'll fail to demonstrate the strategic positioning. Phase 6 without Phase 7 is just running benchmarks; the combination is what generates publishable evidence.

---

## What "done" looks like

Stoke's implementation work is "done" when:

1. ✅ All seven phases pass their validation gates (1 through 7)
2. ✅ `stoke wizard --auto` works on at least one external project Eric uses
3. ✅ `stoke --runner native` runs an end-to-end mission successfully
4. ✅ Cost tracking shows cache reads at >70% of input tokens by turn 3
5. ✅ The hub audit log shows full tool-use trace for at least one mission
6. ✅ Skill library has at least 30 skills extracted (including the 5 from research bundle 02)
7. ✅ Package audit identifies at least one DEPRECATED package candidate
8. ✅ All architecture docs exist
9. ✅ All `STOKE-IMPL-NOTES.md` entries are filled in
10. ✅ No `TODO`, `FIXME`, `panic("not implemented")`, or empty function bodies in any code you wrote

The benchmark work is "done" when:

1. ✅ Bench framework exists at `bench/` and can run a single task end-to-end
2. ✅ Anti-deception task suite has at least 50 tasks across the 4 categories (test-removal, placeholder-trap, silent-simplify, overfitting)
3. ✅ Honesty trap suite has at least 30 ImpossibleBench-derived tasks
4. ✅ Stoke vs Claude Code vs Codex vs Aider vs mini-swe-agent comparison runs and produces a report
5. ✅ Cohen's κ > 0.7 calibration against human labels on a 50-task gold set
6. ✅ Stoke's cheating rate on impossible tasks is < 5% (target) — meaningfully lower than any baseline harness
7. ✅ Test integrity rate > 99% (no test removal/weakening on completed tasks)
8. ✅ Hallucinated import rate > 99% (all imports resolve)
9. ✅ Eric has reviewed the report and decided what to publish

## Now you have everything

If you've read all 13 files in this guide (00 through 12), you have:

1. The strategic context for why Stoke exists and what its position is
2. The locked-in architecture decisions with research backing
3. Per-phase implementation specs with concrete Go code patterns for all 7 phases
4. The skill library extraction procedure (61 original files + 5 additional)
5. The full benchmark framework specification with no remaining research blockers
6. The 7-layer Honesty Judge architecture that generates the publishable headline numbers
7. The validation gates for every phase

Go implement. Start with `03-phase1-skills.md`. Run the validation gate after every phase. Keep `STOKE-IMPL-NOTES.md` updated as you go. When in doubt, write the question to that file and proceed with the safest interpretation.

Eric will review your work and respond to anything in `STOKE-IMPL-NOTES.md`.
