# testing-strategy

> Test design, coverage strategy, TDD, mocking, and test reliability

<!-- keywords: test, testing, tdd, mock, stub, coverage, integration, unit, e2e, fixture, assertion, benchmark -->

## Critical Rules

1. **Test behavior, not implementation.** Tests should verify WHAT a function does, not HOW it does it. If refactoring breaks your tests but not the behavior, your tests are too coupled.

2. **Every bug fix needs a regression test.** Fix the bug, write the test that would have caught it, then verify the test fails without the fix.

3. **No test should depend on another test's state.** Each test must set up and tear down its own state. Use `t.TempDir()`, `t.Cleanup()`, fresh instances.

4. **Flaky tests are worse than no tests.** A test that passes 95% of the time teaches developers to ignore failures. Fix or delete.

5. **Test the public API, not internals.** Exported functions are your contract. Testing private functions couples tests to implementation.

## Test Pyramid

```
         /  E2E  \        <- Few (5-10): slow, fragile, high confidence
        / Integration \    <- Some (20-50): medium speed, real deps
       /    Unit Tests  \  <- Many (100+): fast, isolated, focused
```

- **Unit:** No I/O, no network, no filesystem. Pure logic. < 10ms each.
- **Integration:** Real database, real filesystem, real HTTP. Use `testcontainers` or `t.TempDir()`.
- **E2E:** Full system. Use sparingly. Protect happy path + critical failures only.

## Go Testing Patterns

### Table-Driven Tests
```go
tests := []struct {
    name    string
    input   string
    want    string
    wantErr bool
}{
    {"empty", "", "", true},
    {"valid", "hello", "HELLO", false},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        got, err := Transform(tt.input)
        if (err != nil) != tt.wantErr { t.Fatalf(...) }
        if got != tt.want { t.Fatalf(...) }
    })
}
```

### Test Helpers
```go
func setupTestDB(t *testing.T) *sql.DB {
    t.Helper()
    db := ... // create test database
    t.Cleanup(func() { db.Close() })
    return db
}
```

### Concurrent Test Safety
```go
func TestConcurrent(t *testing.T) {
    t.Parallel() // mark test as safe for parallel execution
    // use t.TempDir() for isolated filesystem
    // use atomic operations for shared counters
}
```

## Common Gotchas

- **Time-dependent tests:** Never use `time.Now()` directly. Inject a clock interface or use `time.Sleep` only for genuinely async operations.
- **Port conflicts:** Don't hardcode ports. Use `:0` and read the assigned port.
- **Test ordering:** Go runs tests in definition order within a file, but `t.Parallel()` tests run concurrently. Don't rely on order.
- **`defer` in test loop:** `defer` runs at function exit, not loop iteration. Use `t.Cleanup()` inside subtests.
- **Snapshot testing:** Useful for complex outputs but requires explicit update step. Keep snapshots small and reviewable.
- **Coverage theater:** 100% coverage doesn't mean correct. Focus on edge cases: empty input, nil, max values, concurrent access, error paths.
