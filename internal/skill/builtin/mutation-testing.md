# mutation-testing

> Mutation testing and property-based testing to find gaps that coverage metrics miss

<!-- keywords: mutation, mutant, property-based, fuzz, fuzzing, test quality, kill rate, cargo-mutants, go-mutesting, stryker, proptest, rapid, gopter -->

## Why Mutation Testing

Code coverage lies. A test suite with 85% coverage can have only a 57% mutation kill rate -- meaning 43% of injected bugs go undetected. Mutation testing systematically replaces function bodies or operators with dummy values and checks whether tests catch the change. Surviving mutants reveal test gaps that line coverage cannot.

## Thresholds

- **70% minimum kill rate** for critical paths (authentication, payment, data validation)
- **50%** for standard business logic
- **30%** for experimental or prototype code
- Any skill-relevant code path scoring below threshold needs additional test cases

## Go Mutation Testing

**go-mutesting** (Avito fork) and **gomu** are the primary tools. Go 1.26+ is adding mutation testing directly into `go test`. Run mutation testing on PR diffs to keep CI fast:

```bash
# Run mutation testing scoped to changed files
go-mutesting --diff HEAD~1 ./pkg/auth/...
```

Mutant types to watch for: conditional boundary changes (`<` to `<=`), negated conditionals, void method call removal, return value replacement, and arithmetic operator swaps.

## Rust Mutation Testing

**cargo-mutants** replaces function bodies with dummy returns (e.g., `fn foo() -> String` becomes `String::new()`). Key flags:
- `--in-diff` for PR-scoped testing (only mutate changed code)
- `--sharding` for CI distribution across runners
- Emits GitHub Actions annotations automatically
- Nextest support for faster test execution

## JavaScript/TypeScript Mutation Testing

**Stryker Mutator** is the standard. Include `npm run mutate` in CI for critical paths. Practitioners report getting agents to iteratively improve mutation scores to 94% by feeding surviving mutants back as prompts.

## Property-Based Testing (Go)

Use `testing/quick` for simple cases or `pgregory.net/rapid` for sophisticated shrinking. Write properties that must hold for ALL inputs, not just examples:

```go
func TestRoundtrip(t *testing.T) {
    rapid.Check(t, func(t *rapid.T) {
        original := rapid.String().Draw(t, "input")
        encoded := Encode(original)
        decoded := Decode(encoded)
        if decoded != original {
            t.Fatalf("roundtrip failed: %q -> %q -> %q", original, encoded, decoded)
        }
    })
}
```

**Key properties to test:** roundtrip (encode/decode), idempotency (f(f(x)) == f(x)), commutativity, invariant preservation, and oracle comparison (compare against a known-correct but slow implementation).

## Go Native Fuzzing

`go test -fuzz` is built-in since Go 1.18. Structure fuzz targets to maximize coverage:

```go
func FuzzParse(f *testing.F) {
    f.Add([]byte(`{"key":"value"}`))  // seed corpus
    f.Fuzz(func(t *testing.T, data []byte) {
        result, err := Parse(data)
        if err != nil { return }  // invalid input is fine
        // Check invariants on valid parses
        roundtripped := result.Marshal()
        reparsed, err := Parse(roundtripped)
        if err != nil { t.Fatal("roundtrip failed") }
        if !reflect.DeepEqual(result, reparsed) { t.Fatal("not equal") }
    })
}
```

Run fuzzing in CI with time limits: `go test -fuzz=FuzzParse -fuzztime=30s`. Crashes save to `testdata/fuzz/` as regression test cases automatically.

## Rust Property-Based Testing

**proptest** generates random inputs via Strategy objects with per-value shrinking. Failed cases persist to `proptest-regressions/` for automatic regression testing:

```rust
proptest! {
    #[test]
    fn roundtrip_parse(s in "[a-z]{1,10}") {
        let parsed = parse(&s);
        prop_assert_eq!(s, render(&parsed));
    }
}
```

## Integration with AI Agents

Mutation testing is the single most important quality gate for AI-generated code because traditional coverage metrics are systematically gamed by AI test patterns. The workflow: agent generates code with tests -> run mutation testing on critical paths -> feed surviving mutants back to the agent for iterative improvement. Never let the same agent session write both implementation and tests without a mutation testing gate between them.

## Static Analysis Complement

Write custom Semgrep rules to enforce structural patterns (e.g., every HTTP handler must call `validateInput()`). Run Semgrep on every PR for fast feedback, mutation testing on critical paths before merge. CodeQL provides deeper cross-file dataflow tracking with 88% accuracy and 5% false positive rate.
