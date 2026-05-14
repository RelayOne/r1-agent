# seed-refactor-medium

## Upstream task

Not derived from upstream SWE-bench Pro. Authored from scratch as the medium-difficulty seed
mission that exercises the multi-file + test-suite shape that 40% of the production corpus uses.

## Plan rationale

- **P1** adds `internal/http/validator.go` with the new middleware. Two required symbols
  (`RequireJSONBody`, `ErrInvalidJSON`) catch the agent that emits a skeleton with the right
  filename but missing the actual middleware function.
- **P2** rewires `internal/http/handler.go` to use the middleware. The required symbol
  `RequireJSONBody` confirms the wiring landed (not just an `// TODO: use the middleware` comment).
- **P3** adds the unit test file; two test function names anchor the requirement.
- **P4** runs `go test ./internal/http/...` to confirm the refactor didn't break existing handler
  tests. This is the load-bearing PlanItem — the symbol checks above can be satisfied by a
  syntactically-correct but semantically-broken diff; the test command rejects that.

## Test strategy

PlanItem P4 is the substantive verification. A passing `go test ./internal/http/...` after the
refactor means: (a) the middleware compiles, (b) the handler's existing tests still pass, (c)
the new validator tests pass. The other three PlanItems are structural pre-conditions that
ensure the agent didn't claim "done" with a totally absent file.

## Known limitations

- The `gold.patch` references files (`internal/http/handler.go`) that do not exist in the seed
  workspace until the agent creates them. The perfect-agent fixture path needs `workspace-init.sh`
  to seed a starter `handler.go` before the gold patch can apply cleanly. For the seed-mission
  smoke test we accept a clean-apply gap; production missions ship `workspace-init.sh` for this.
