# Failure Analysis & Recovery

Package: `internal/failure/`

## 10 Failure Classes

| Class | Description | Default Action |
|-------|-------------|---------------|
| `BuildFailed` | Compilation error | Retry with build error context |
| `TestsFailed` | Test failure | Retry with test output |
| `LintFailed` | Linter violation | Retry with lint findings |
| `PolicyViolation` | Code policy breach | Retry with constraint |
| `ReviewRejected` | Human review rejection | Retry with review feedback |
| `Timeout` | Execution time exceeded | Retry 2x, then escalate |
| `WrongFiles` | Modified files outside scope | Retry with scope constraint |
| `Incomplete` | Partial implementation | Retry with completion prompt |
| `Regression` | Broke existing functionality | Retry with regression details |
| `RateLimited` | Provider rate limit hit | Retry (pool manager rotates) |

## Analysis Pipeline

```go
analysis := failure.Analyze(buildOutput, testOutput, lintOutput, diff)
// Returns: Class, Summary, RootCause, Missing[], Specifics[], DiffSummary
```

### Language-Specific Parsers

**Build errors:** TypeScript, Go, Rust, Python
**Test errors:** Jest/Vitest, Go test, Pytest, Rust test
**Lint errors:** ESLint, Golint, Ruff, Clippy

### Policy Violation Detection

9 patterns scanned:
- `@ts-ignore`, `as any` (TypeScript type bypasses)
- `eslint-disable` (ESLint suppression)
- `# type: ignore`, `# noqa` (Python suppression)
- `#[allow(clippy::)]` (Rust suppression)
- `.only()` (test focusing)
- `console.log` (debug artifacts)
- `fmt.Print` (Go debug artifacts)

## Recovery Decisions

```go
decision := failure.ShouldRetry(analysis, attemptNumber, priorFailure)
// Returns: Action (Retry|Escalate), Reason, Constraint
```

### Retry Strategy

| Condition | Action |
|-----------|--------|
| PolicyViolation | Retry with "do not use bypasses" constraint |
| Timeout, attempt < 3 | Retry with extended timeout |
| Timeout, attempt >= 3 | Escalate |
| WrongFiles | Retry with explicit scope constraint |
| RateLimited | Retry (pool rotates automatically) |
| Same error twice | Escalate (fingerprint dedup) |

## Fingerprint Dedup

`failure.Compute()` generates a fingerprint from the failure class, root cause,
and specific error details. `MatchHistory()` checks if the same fingerprint has
appeared in prior attempts for this task. Two consecutive identical fingerprints
trigger escalation rather than another retry.

## Integration

- **Workflow** (`internal/workflow/`): Calls `Analyze()` after each phase failure
- **Retry**: Clean worktree per retry; learning is injected via instructions, not code state
- **DiffSummary**: Previous attempt's diff is injected into the retry prompt
- **Bridge**: Failure events published to v2 bus for supervisor visibility
