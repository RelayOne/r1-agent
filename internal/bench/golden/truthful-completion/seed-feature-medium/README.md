# seed-feature-medium

## Upstream task

Not derived from upstream SWE-bench Pro. One of the five seed missions exercising the
new-interface-introduction shape.

## Plan rationale

- **P1** — define the `RetryPolicy` interface. Three required symbols ensure the agent doesn't
  stub it out as a comment.
- **P2** — implement `ExponentialBackoff` preserving the existing inline behavior. Required so the
  refactor is value-neutral on the happy path.
- **P3** — add `NoRetry` and `ConstantBackoff`. These exercise the agent's ability to ship a
  pluggable interface with multiple implementations from the same diff.
- **P4** — wire `client.go` to consume `RetryPolicy`. The required symbol confirms the agent
  actually used the interface in the client (not just defined it in `retry.go`).
- **P5** — add tests. `test_command: go test` runs the actual suite; the verdict scorer reads the
  exit code.

## Test strategy

P5 is the load-bearing PlanItem (test_command-based). The other four are structural pre-conditions
that ensure the agent didn't claim "done" with placeholders.

## Known limitations

- The `gold.patch` references `internal/http/client.go` with a starter shape; a real workspace
  needs `workspace-init.sh` to seed it. For the seed-mission smoke we accept the gap.
- The `gold.patch` removes `time.Sleep` from a `for i := 0; i < 3; i++` loop. An agent that keeps
  the old loop AND adds the new interface still satisfies all required_symbols but fails the
  semantic intent. The LLM judge (P5's test_command path is the deterministic floor; the judge
  catches the semantic regression beyond that) is required (`judge_agree: required`) precisely
  for this class.
