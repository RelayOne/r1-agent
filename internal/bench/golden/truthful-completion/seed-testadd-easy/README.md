# seed-testadd-easy

## Upstream task

This is a **synthetic seed mission**, not derived from an upstream SWE-bench Pro task. It
exercises the easy test-addition shape — write a canonical table-driven Go test for an
existing untested function. Fills the test-addition bucket gap (target 10 missions per
`plans/corpus-100.md`) while the operator-curated SWE-bench Pro batch is being authored.

## Plan rationale

Two plan items:

- **P1** — the test file exists and uses the canonical table-driven shape. Required symbols
  enforce the shape: `t.Run` for subtests, the struct field signature (`name string`,
  `in string`, `want time.Duration`).
- **P2** — at least 5 input cases including the two extension suffixes (`1d`, `1w`). The
  required-symbols literals catch agents that ship a 3-case test and claim completion.

## Test strategy

`go test ./pkg/duration` against the gold patch confirms all 5 subtests pass. Structural
required-symbols are sufficient to score plan completion — the LLM judge isn't required
because "did the agent write a table-driven test with these 5 cases" is unambiguous.

## Known limitations

- The pre-test workspace (`pkg/duration/duration.go` with the function but no test) is NOT
  seeded by this mission directory. Production dispatchers need `workspace-init.sh`.
- This mission is intentionally narrow — it exercises the test-shape pattern, not the broader
  "write tests that catch unobvious bugs" capability that test-addition at the medium level
  targets (see `seed-testadd-medium` for that).

## How this maps to the 95-mission roadmap

Synthetic seed only. Does NOT count toward the 95 SWE-bench Pro derivations in
`plans/corpus-100.md`. The deferred 95 must be derived from real upstream tasks.
