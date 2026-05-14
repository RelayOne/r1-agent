# HANDOFF — build/truthful-completion

**Branch:** `build/truthful-completion`
**Last updated:** 2026-05-14
**Specs in flight:** `specs/antitrunc-hook-mode-flag.md` (D1, BUILD_ORDER 47) + `specs/truthful-completion-benchmark.md` (D2, BUILD_ORDER 48)
**Scope:** D1 (5 items, all) + D2 engineering (T1–T50 + T151–T181, ~81 items) + D2 5 seed missions chosen from T51–T150. Remaining 95 SWE-bench Pro missions deferred to operator curation (each needs a real upstream gold patch + hand-written plan).

## Status snapshot

| Bundle | Status | Commit |
|---|---|---|
| D1 — `r1 antitrunc verify --hook-mode` + `--plan` flags | **DONE** | `c6735a16` |
| D2-T1+T2 — MissionConfig + RunResult schema | **DONE** | `59285c5f` |
| D2-T3 — VerdictScorer | **DONE** | `6a9a7f8c` |
| D2-T4+T5 — Judge wiring | **DONE** | `ca3f4cbc` |
| D2-T6+T7 — Dispatcher framework + shared | **DONE** | `ab808592` |
| D2-T8 — R1 dispatcher | **DONE** | `e9bdd386` |
| D2-T9–T16 — 7 competitor dispatchers + Tether | **DONE** | `8121124a` |
| D2-T17 — cross-dispatcher integration test | **DONE** | `6035744d` |
| D2-T18 — Wilson 95%-CI helper | **DONE** | `2c630983` |
| D2-T19–T22 — `cmd/r1-bench` runner | **DONE** | `514abe65` |
| D2-T23–T25 — leaderboard renderers | **DONE** | `1419077d` |
| D2-T26 — methodology doc | **DONE** | `34e1b9e4` |
| D2-T27–T30 — CI + reproduction kit | **DONE** | `fb2680b8` |
| D2-T31–T36 — 5 seed missions + cross-mission tests | **DONE** | `7bced52c` |
| D2-T37–T40 — docs + status flip + corpus-100.md | **DONE** | (this commit) |

**All buckets complete.** Branch ready for PR `build/truthful-completion → dev`.

Branch has 10 commits on top of dev. Pushed to `origin/build/truthful-completion`.

## What's in `internal/bench/agents/` after the T9–T16 bundle

Dispatcher registry (resolve by string ID):

| ID | File | Drives | Completion signal |
|---|---|---|---|
| `r1` | `r1.go` | in-process agentloop (enforce=off) | end_turn through PreEndTurnCheckFn |
| `r1-antitrunc` | `r1.go` | in-process agentloop (enforce=on) | same |
| `claude-code-default` | `claude_code.go` | `claude --headless --no-interactive` | `{"event":"stop","stop_hook_active":false}` (or active+approve) |
| `claude-code-stop-hook` | `claude_code_stop_hook.go` | `claude --headless` w/ `.claude/settings.json` installed | same, but Stop hook can block |
| `cline` | `cline.go` | `cline --headless` | `{"event":"attempt_completion","result":...}` |
| `aider` | `aider.go` | `aider --yes-always --no-auto-commits --message` | "Applied edit to " / "Committed change." stdout sentinel |
| `codex-cli` | `codex.go` | `codex exec --json` | `{"type":"task_complete"}` |
| `cursor` | `cursor.go` | `cursor-agent --headless` | `[cursor-agent] task finished` stdout sentinel |
| `tether+<inner>` | `tether.go` | wraps any of the above | inner's signal, then antitrunc gates: truncation phrases + plan-coverage |

Every dispatcher exposes a **pure** stream-parsing function (`parseClaudeCodeStream`, `parseClineStream`, `parseCodexStream`, `parseAiderOutput`, `parseCursorOutput`, `evaluateTether`) so protocol parsing is unit-tested without exec'ing a real binary. Tether wires canonical combos at init() time via `Lookup(innerID)`.

50 unit tests in the bundle cover: agent IDs, stream parsing happy paths, rate-limit detection, missing-binary → `ExitReasonNotSupported` (key fix: use `cmd.ProcessState == nil` rather than `*exec.Error` — the latter doesn't match absolute-path startup failures), nil-mission errors, registry membership.

## Build/test gate state

- `go build ./...` → exit 0
- `go test ./internal/bench/agents/...` → ok (50 tests)
- `go vet ./internal/bench/agents/...` → clean

## Plan for resumption — next bundle is D2-T17

**T17: cross-dispatcher integration test** (`internal/bench/agents/integration_test.go`)
- Table-driven `TestAllDispatchers_HelloWorld` that walks every entry in `Registry` and runs each against a `seed-perfect-agent-fixture`-like in-memory mission.
- Uses a fake-binary harness (PATH override pointing at `internal/bench/agents/testdata/fake-bins/`) so the test can run in CI without any external CLI installed.
- Asserts each dispatcher: (a) is reachable through `Lookup`, (b) returns a `Trace` (not an error) given a happy-path fake binary, (c) reports `ExitReasonNotSupported` when its binary is absent from PATH.

After T17, remaining buckets in this build:

1. **T19–T22** — `cmd/r1-bench/main.go` + `runner.go` + `container.go` + `dockerfile.tmpl`. Key tests: `TestRunner_RejectsIntraVendorJudging`, `TestRunner_EndToEndOneMission`, `TestDockerRun_NoNetworkInsideTest`.
2. **T23–T25** — `internal/bench/leaderboard.go` + `permission_render.go` + golden snapshot tests; wire into runner.
3. **T26** — `docs/truthful-completion-methodology.md` (11 sections, ~10–15 KB).
4. **T27–T30** — `services/cloudbuild-bench-truthful-completion-{monthly,pr}.yaml` + `setup-bench-truthful-completion-cron.sh` + `cmd/r1-bench/reproduction-kit/{docker-compose.yml,run.sh,README.md}`.
5. **T37–T40** — Update root README + `docs/FEATURE-MAP.md` + `docs/ARCHITECTURE.md`; flip `specs/truthful-completion-benchmark.md` STATUS to `done (engineering + 5 seeds)`; write `plans/corpus-100.md` deferral roadmap.
6. Final: push, open PR `build/truthful-completion → dev`, admin-merge (Cloud Build `r1-agent-pr` is a manual gate per dev-first rule), sync.

## Files touched (snapshot)

```
A internal/bench/agents/agents.go               (commit ab808592)
A internal/bench/agents/shared.go               (commit ab808592)
A internal/bench/agents/shared_test.go          (commit ab808592)
A internal/bench/agents/r1.go                   (commit e9bdd386)
A internal/bench/agents/r1_test.go              (commit e9bdd386)
A internal/bench/agents/claude_code.go          (commit 8121124a)
A internal/bench/agents/claude_code_test.go     (commit 8121124a)
A internal/bench/agents/claude_code_stop_hook.go (commit 8121124a)
A internal/bench/agents/claude_code_stop_hook_test.go (commit 8121124a)
A internal/bench/agents/cline.go                (commit 8121124a)
A internal/bench/agents/cline_test.go           (commit 8121124a)
A internal/bench/agents/aider.go                (commit 8121124a)
A internal/bench/agents/aider_test.go           (commit 8121124a)
A internal/bench/agents/codex.go                (commit 8121124a)
A internal/bench/agents/codex_test.go           (commit 8121124a)
A internal/bench/agents/cursor.go               (commit 8121124a)
A internal/bench/agents/cursor_test.go          (commit 8121124a)
A internal/bench/agents/tether.go               (commit 8121124a)
A internal/bench/agents/tether_test.go          (commit 8121124a)
A internal/bench/bench.go                       (commit 59285c5f)
A internal/bench/bench_test.go                  (commit 59285c5f)
A internal/bench/testdata/mission-with-plan.yaml (commit 59285c5f)
A internal/bench/testdata/result-legacy.json    (commit 59285c5f)
A internal/bench/verdict.go                     (commit 6a9a7f8c)
A internal/bench/verdict_test.go                (commit 6a9a7f8c)
A internal/bench/judge.go                       (commit ca3f4cbc)
A internal/bench/judge_test.go                  (commit ca3f4cbc)
A internal/bench/stats.go                       (commit 2c630983)
A internal/bench/stats_test.go                  (commit 2c630983)
A internal/bench/golden/truthful-completion/seed-hello-easy/...           (commit 7bced52c)
A internal/bench/golden/truthful-completion/seed-refactor-medium/...      (commit 7bced52c)
A internal/bench/golden/truthful-completion/seed-feature-medium/...       (commit 7bced52c)
A internal/bench/golden/truthful-completion/seed-migration-hard/...       (commit 7bced52c)
A internal/bench/golden/truthful-completion/seed-perfect-agent-fixture/... (commit 7bced52c)
A internal/bench/golden_truthful_test.go        (commit 7bced52c)
M cmd/r1/antitrunc_cmd.go                       (commit c6735a16)
M cmd/r1/antitrunc_cmd_test.go                  (commit c6735a16)
M specs/antitrunc-hook-mode-flag.md             (frontmatter in-progress)
M specs/truthful-completion-benchmark.md        (frontmatter in-progress)
A plans/build-plan.md                           (40-bucket task list)
A plans/HANDOFF.md                              (this file)
```
