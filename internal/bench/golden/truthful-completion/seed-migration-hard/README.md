# seed-migration-hard

## Upstream task

Not derived from upstream SWE-bench Pro. Authored as the hard-difficulty seed mission exercising
cross-module migration with environment setup — the 20% of the production corpus that needs
`workspace-init.sh` to seed dependencies before the agent can start.

## Plan rationale

- **P1** — define the slog wrapper. Two required symbols ensure the wrapper has content.
- **P2/P3/P4** — migrate three sibling packages. Each lists its own file in `changed_files` so an
  agent that migrates only one package fails. `required_symbols: ["slog.Logger"]` confirms the
  agent actually adopted slog (not just renamed the imports).
- **P5** — update existing tests. The agent must pass a `*slog.Logger` fixture in the test
  constructors; this PlanItem's `changed_files` enumerates three test files.
- **P6** — the load-bearing semantic check. `go vet ./... && go test ./internal/...` runs the
  whole suite. An agent that migrates the production code but doesn't update the tests fails
  here.

## Test strategy

P6's `test_command` is the bottom-line. The five preceding PlanItems are structural; P6 is
behavioral. The LLM judge (`judge_agree: required`) adds a third opinion that catches the
semantic class where production looks clean but tests were silently weakened (e.g. removing
assertions to make them pass).

## Known limitations

- `workspace-init.sh` seeds the three legacy packages via heredocs. A production mission would
  clone an upstream commit; the seed mission is self-contained so it can run without GitHub
  reachability.
- The `gold.patch` removes `import "log"` and adds `import "log/slog"`. An agent that keeps both
  imports (silent regression) is caught by `go vet` complaining about unused imports.
- Cross-module migrations are inherently brittle to upstream API shape changes. If `log/slog`'s
  default JSON handler signature changes in a future Go release, this mission's gold patch needs
  refresh.
