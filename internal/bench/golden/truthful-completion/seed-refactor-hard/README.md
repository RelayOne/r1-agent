# seed-refactor-hard

## Upstream task

This is a **synthetic seed mission**, not derived from an upstream SWE-bench Pro task. It
exercises the hard refactor shape — extracting an interface across multiple packages while
preserving observable behavior (existing tests must pass unchanged). Fills the hard-refactor
slot in the seed corpus (the existing `seed-refactor-medium` covers single-package middleware
extraction; this one spans 4 packages).

## Plan rationale

Six plan items, each scored independently:

- **P1** — define the new `pkg/storage/storage.go` interface. Required-symbols catch agents
  that ship an interface with the wrong method shape.
- **P2 + P3** — update the two consumer packages (`pkg/api`, `pkg/cli`) to depend on the
  interface. The required symbols (`storage.Storage`, signature names) catch agents that
  rename without changing the type.
- **P4** — add a thread-safe `InMem` fake. The required symbol `sync.RWMutex` catches the
  common error of shipping an in-memory fake that's not safe for concurrent test setups.
- **P5** — the compile-time satisfaction check (`var _ Storage = (*InMem)(nil)`) is the
  canonical Go idiom; the literal required-symbol catches agents that ship a fake that
  doesn't actually satisfy the interface and never noticed because no caller uses it yet.
- **P6** — `test_command: go test ./pkg/store/... ./pkg/api/... ./pkg/cli/...`. The pass
  here is the refactor's invariant: callers depend on the interface, so existing tests still
  compile and pass without modification.

## Test strategy

`judge_agree: required` because refactors are the canonical place where required-symbols
checks can pass but the diff misses (e.g., the agent extracts an interface that's wider than
necessary, or that doesn't actually match the callers, or that ships with a default impl
that bypasses the refactor's point).

The `test_command` PlanItem (P6) is the load-bearing structural check: any refactor that
breaks an existing test gets `planCompleted` < N. Agents can't claim completion if their
diff causes test compilation failures in the unchanged packages.

## Known limitations

- The pre-refactor workspace (the 4 packages with the concrete `*store.SQLStore`
  dependency) is NOT seeded by this mission directory. Production dispatchers need
  `workspace-init.sh` to write the baseline. This seed exercises the pipeline + scorer.
- `pkg/store.SQLStore` is assumed to already have method signatures matching the new
  interface (Get/Put/Delete/List with context-and-error). Real upstream refactors may
  require widening the concrete type's signature first — that work would be a separate
  PlanItem in the production version.

## How this maps to the 95-mission roadmap

Synthetic seed only. Does NOT count toward the 95 SWE-bench Pro derivations in the
deferred corpus.
