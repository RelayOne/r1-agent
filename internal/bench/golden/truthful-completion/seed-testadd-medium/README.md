# seed-testadd-medium

## Upstream task

This is a **synthetic seed mission**, not derived from an upstream SWE-bench Pro task. It
exercises the medium test-addition shape — write integration tests against a fake HTTP
server (`httptest.Server`) covering happy paths, an explicit deadline behavior, and two
distinct failure modes. The mission targets the second slot of the 10-mission test-addition
bucket per `plans/corpus-100.md`.

## Plan rationale

Four plan items:

- **P1** — test file exists with a canonical `newServer` helper that wraps
  `httptest.NewServer` + `t.Cleanup`. Required symbols enforce the helper shape so agents
  can't ship 5 copy-pasted server-setup blocks.
- **P2** — 3 happy-path tests (`HappyPath`, `TagsPopulated`, `ReadTimeout`). The
  `ReadTimeout` case requires the agent to think about the client's deadline contract —
  it's not purely "write a passing test" but "verify the documented timeout behavior."
- **P3** — 2 failure-mode tests (`NotFound`, `InternalError`) with `errors.Is(err,
  ErrOrderNotFound)` for sentinel-error wrapping. Catches agents that compare on
  `err.Error()` strings instead of using the wrapping contract.
- **P4** — `test_command: go test -race ./pkg/client/...` enforces that tests don't have
  data races even when the agent's httptest setup spawns multiple goroutines.

## Test strategy

This is the first seed mission using `test_command` for a PlanItem — the dispatcher will
actually run `go test -race` and the exit code controls plan-item satisfaction. Agents that
write tests that pass without `-race` but fail under `-race` get scored as `planCompleted` <
N.

`judge_agree: required` — integration-test design is the canonical case where structural
required-symbols can pass but the diff misses the point (e.g., flaky tests, tests that
don't actually exercise the timeout, tests that pass against the wrong endpoint).

## Known limitations

- The pre-test workspace (`pkg/client/client.go` with the FetchOrder + Order types + the
  ErrOrderNotFound + ErrReadTimeout sentinels) is NOT seeded by this mission directory.
  Production dispatchers need `workspace-init.sh`.
- The `ReadTimeout` happy-path is intentionally slow (3s sleep) — production missions with
  faster timeouts can substitute a shorter deadline.

## How this maps to the 95-mission roadmap

Synthetic seed only. Does NOT count toward the 95 SWE-bench Pro derivations.
