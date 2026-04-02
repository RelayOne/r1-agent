# Forge Feedback Loop: Intelligent Retry Architecture

## The Problem With Dumb Retries

Current tools (Claude Code, Codex, the enforcer) retry like this:

```
Task fails → "Try again" → Same instructions → Same failure → Give up
```

The model makes the same mistake because it has the same information. Three retries of identical instructions is not a retry strategy, it's insanity.

## What Forge Does Instead

Every failed attempt produces a structured failure analysis. The next attempt gets the original task PLUS the failure context: what went wrong, what was missing from the instructions, and specific guidance to avoid the same failure.

```
Task attempt 1 → FAIL (lint: 3 @ts-ignore added)
                    │
                    ▼
            Forge analyzes the diff:
            "Agent added @ts-ignore on lines 45, 72, 91 to bypass
             type errors in the auth middleware refactor. The actual
             type errors are: Request type missing 'user' field (L45),
             middleware return type Promise<void> vs Promise<Response> (L72),
             session type doesn't extend BaseSession (L91)."
                    │
                    ▼
Task attempt 2 → GETS ORIGINAL TASK + FAILURE BRIEF:
  "Previous attempt failed: agent used @ts-ignore to bypass type errors
   instead of fixing them. The specific type errors are:
   1. src/auth/middleware.ts:45 — Request type needs 'user: AuthUser' field.
      Fix: extend Request interface in src/types/express.d.ts
   2. src/auth/middleware.ts:72 — return type is Promise<void> but handler
      expects Promise<Response>. Fix: change return type to Promise<Response>
   3. src/auth/middleware.ts:91 — Session doesn't extend BaseSession.
      Fix: add 'extends BaseSession' to SessionData interface in src/types/session.ts
   DO NOT use @ts-ignore. Fix the actual types."
                    │
                    ▼
            SUCCESS (agent fixes the actual types)
```

## The Failure Analysis Pipeline

When a worktree fails verification, Forge doesn't just discard it. It extracts maximum information from the failure before nuking the worktree.

### Stage 1: Classify the Failure

```go
type FailureClass int
const (
    BuildFailed     FailureClass = iota  // won't compile
    TestsFailed                          // compiles but tests break
    LintFailed                           // code quality violations
    PolicyViolation                      // @ts-ignore, stubs, hardcoded secrets
    ReviewRejected                       // cross-model review found issues
    Timeout                              // ran out of turns/time
    WrongFiles                           // touched files outside task scope
    Incomplete                           // task partially done
    Regression                           // broke something that was passing
)
```

Each class has a different analysis strategy and different retry guidance.

### Stage 2: Extract Specifics From the Failed Worktree

Before discarding the worktree, Forge runs targeted analysis:

```go
type FailureAnalysis struct {
    Class       FailureClass
    Summary     string            // one-line: "3 type errors bypassed with @ts-ignore"
    RootCause   string            // what the agent did wrong
    Missing     []string          // what the original instructions didn't say
    Specifics   []FailureDetail   // file:line:exact issue:suggested fix
    DiffSummary string            // what the agent changed (compressed)
    Attempts    int               // which attempt this was
}

type FailureDetail struct {
    File        string
    Line        int
    Issue       string   // "added @ts-ignore instead of fixing type"
    Evidence    string   // the actual error message from tsc/eslint/test runner
    SuggestedFix string  // "extend Request interface with user field"
}
```

**How the specifics are extracted (by failure class):**

**BuildFailed:**
```bash
# Run the build, capture the EXACT errors
cd worktree/ && npm run build 2>&1 | head -50
# Parse: file, line, error message, error code
# Feed to Forge: "build failed with 3 errors: [exact errors]"
```

**TestsFailed:**
```bash
# Run tests, capture failures
cd worktree/ && npm test 2>&1
# Parse: which tests failed, expected vs actual, stack traces
# Diff against main: which tests were PASSING before and now FAIL?
# Feed to Forge: "2 tests regressed: [test names, expected vs actual]"
```

**LintFailed:**
```bash
# Run linter, capture violations
cd worktree/ && npm run lint 2>&1
# Parse: file, line, rule, message
# Feed to Forge: "4 lint violations: [exact violations]"
```

**PolicyViolation:**
```bash
# Diff the worktree, scan for banned patterns
git diff main...worktree-branch -- | grep -n '@ts-ignore\|as any\|eslint-disable'
# Feed to Forge: "agent added @ts-ignore on lines X, Y, Z"
# ALSO: extract the ACTUAL errors that led to the bypass
cd worktree/ && npx tsc --noEmit 2>&1 | grep -A2 'error TS'
# Feed to Forge: "the type errors the agent was trying to bypass are: [exact errors with fixes]"
```

**ReviewRejected:**
```
# Cross-model review already produced structured findings
# Feed to Forge: "reviewer found: [findings with file:line:issue]"
```

**Timeout:**
```
# Check what the agent accomplished before timing out
git diff main...worktree-branch --stat
# Feed to Forge: "agent completed changes to 3/7 files before timeout.
#   Completed: auth.ts, middleware.ts, types.ts
#   Not started: router.ts, controller.ts, tests/, docs/
#   Possible cause: agent spent 12 turns reading files it didn't need"
```

**WrongFiles:**
```
# Diff shows which files changed
git diff main...worktree-branch --name-only
# Compare against task scope (files the task should have touched)
# Feed to Forge: "task scope was src/auth/ but agent also modified
#   src/database/connection.ts and package.json"
```

### Stage 3: Generate the Retry Brief

The retry brief is structured so the next agent attempt gets EXACTLY what it needs:

```go
type RetryBrief struct {
    OriginalTask    string           // the full original task
    AttemptNumber   int              // "this is attempt 2 of 3"
    PriorFailure    FailureAnalysis  // what went wrong
    Constraints     []string         // explicit "DO NOT" list from the failure
    Hints           []string         // specific fixes extracted from error analysis
    ScopeReminder   []string         // files this task should touch (and only these)
}

func (b RetryBrief) Render() string {
    var sb strings.Builder
    sb.WriteString(b.OriginalTask)
    sb.WriteString("\n\n--- RETRY CONTEXT (attempt " + itoa(b.AttemptNumber) + ") ---\n")
    sb.WriteString("Previous attempt FAILED: " + b.PriorFailure.Summary + "\n")
    sb.WriteString("Root cause: " + b.PriorFailure.RootCause + "\n\n")

    if len(b.PriorFailure.Specifics) > 0 {
        sb.WriteString("SPECIFIC ISSUES TO ADDRESS:\n")
        for _, d := range b.PriorFailure.Specifics {
            sb.WriteString(fmt.Sprintf("  %s:%d — %s\n", d.File, d.Line, d.Issue))
            if d.Evidence != "" {
                sb.WriteString(fmt.Sprintf("    Error: %s\n", d.Evidence))
            }
            if d.SuggestedFix != "" {
                sb.WriteString(fmt.Sprintf("    Fix: %s\n", d.SuggestedFix))
            }
        }
    }

    if len(b.Constraints) > 0 {
        sb.WriteString("\nDO NOT:\n")
        for _, c := range b.Constraints {
            sb.WriteString("  - " + c + "\n")
        }
    }

    if len(b.Hints) > 0 {
        sb.WriteString("\nHINTS:\n")
        for _, h := range b.Hints {
            sb.WriteString("  - " + h + "\n")
        }
    }

    return sb.String()
}
```

### Stage 4: Decide Whether to Retry or Escalate

Not every failure deserves a retry. Some failures indicate the task itself is wrong:

```go
func (e *Engine) shouldRetry(analysis FailureAnalysis, attempt int) RetryDecision {
    // Hard limit
    if attempt >= 3 {
        return Escalate("3 attempts failed, needs human review")
    }

    switch analysis.Class {
    case BuildFailed:
        // If the SAME build errors persist across 2 attempts, the task
        // description is probably wrong (missing dependency, wrong API)
        if attempt >= 2 && analysis.sameErrorsAs(e.priorAnalysis) {
            return Escalate("same build errors after 2 attempts — task may need revision")
        }
        return Retry

    case TestsFailed:
        // If the agent broke tests it didn't write, retry with scope constraint
        if analysis.hasRegressions() {
            return RetryWithConstraint("only modify files in task scope, do not change existing tests")
        }
        return Retry

    case PolicyViolation:
        // @ts-ignore, stubs, etc — always retry with explicit constraints
        return RetryWithConstraint("do not use " + analysis.violationType())

    case ReviewRejected:
        // Security finding → retry with finding as constraint
        // Style finding → accept anyway (ROI filter should have caught this)
        if analysis.reviewSeverity() >= High {
            return Retry
        }
        return Accept  // low-severity review findings auto-accepted

    case Timeout:
        // Agent took too long — simplify the task or increase turns
        if analysis.turnsUsed() > 15 {
            return Escalate("task too complex for single agent — split it")
        }
        return RetryWithConstraint("focus only on the core change, skip exploration")

    case WrongFiles:
        // Agent modified files outside scope — retry with strict file list
        return RetryWithConstraint(fmt.Sprintf(
            "only modify these files: %s", strings.Join(analysis.allowedFiles, ", ")))

    case Regression:
        // Agent broke something that was passing — this is serious
        return RetryWithConstraint(fmt.Sprintf(
            "CRITICAL: your previous change broke: %s. Fix the task WITHOUT breaking these.",
            strings.Join(analysis.brokenTests, ", ")))
    }

    return Retry
}
```

### Stage 5: Cross-Attempt Learning (Session Memory)

Failures accumulate across a session. If task 12 fails for the same reason task 3 failed, Forge injects that pattern into ALL future task dispatches:

```go
type SessionLearning struct {
    // Patterns that failed and their fixes
    FailurePatterns []LearnedPattern

    // Things that work in this codebase
    SuccessPatterns []LearnedPattern
}

type LearnedPattern struct {
    Pattern     string  // "agent adds @ts-ignore when encountering generic type errors"
    Fix         string  // "this codebase uses declaration merging in src/types/"
    Occurrences int     // how many times this has happened
    FirstSeen   int     // task number where this first appeared
}

// Injected into EVERY task dispatch after enough data:
func (s *SessionLearning) RenderForPrompt() string {
    if len(s.FailurePatterns) == 0 {
        return ""
    }
    var sb strings.Builder
    sb.WriteString("\n--- LEARNED FROM THIS SESSION ---\n")
    for _, p := range s.FailurePatterns {
        sb.WriteString(fmt.Sprintf("KNOWN ISSUE: %s\n  SOLUTION: %s\n", p.Pattern, p.Fix))
    }
    for _, p := range s.SuccessPatterns {
        sb.WriteString(fmt.Sprintf("WORKING PATTERN: %s\n", p.Pattern))
    }
    return sb.String()
}
```

**Example of session learning in action:**

```
Task 3: "Add auth middleware"
  → Attempt 1: FAIL — added @ts-ignore for Request type
  → Analysis: "this codebase extends Request via declaration merging in src/types/express.d.ts"
  → Attempt 2: SUCCESS — extended the Request interface properly

Task 7: "Add rate limiting middleware"
  → Forge injects learned pattern: "this codebase extends Request via declaration merging"
  → Attempt 1: SUCCESS — agent adds RateLimitInfo to Request interface correctly
  (would have failed without the session learning)

Task 12: "Add logging middleware"
  → Same injection, same success
```

## The Full Retry Flow

```
┌──────────────────────────────────────────────────────────────────┐
│ FORGE DISPATCH                                                    │
│                                                                    │
│  task + session_learning + (retry_brief if attempt > 1)           │
│       │                                                            │
│       ▼                                                            │
│  ┌─────────────────────────────────────────────────────────┐      │
│  │ WORKTREE: claude -p "task + context" --worktree         │      │
│  │   Agent works... hooks fire on each tool call...         │      │
│  │   Agent finishes (or times out)                          │      │
│  └──────────────────────┬──────────────────────────────────┘      │
│                          │                                         │
│                          ▼                                         │
│  ┌──────────────────────────────────────────────────────────┐     │
│  │ VERIFY (in worktree, before discarding)                   │     │
│  │                                                            │     │
│  │  1. Build:    npm run build 2>&1 → parse errors           │     │
│  │  2. Tests:    npm test 2>&1 → parse failures + regressions│     │
│  │  3. Lint:     npm run lint 2>&1 → parse violations        │     │
│  │  4. Policy:   scan diff for @ts-ignore, stubs, secrets    │     │
│  │  5. Scope:    diff --name-only vs allowed files           │     │
│  │  6. Review:   codex exec --sandbox read-only "review diff"│     │
│  └──────────────────────┬───────────────────────────────────┘     │
│                          │                                         │
│                    ┌─────┴─────┐                                   │
│                    │           │                                    │
│                ALL PASS    ANY FAIL                                 │
│                    │           │                                    │
│                    ▼           ▼                                    │
│              ┌─────────┐ ┌──────────────────────────────────┐     │
│              │ COMMIT   │ │ ANALYZE (before discarding)      │     │
│              │ to main  │ │                                    │     │
│              │ + update │ │  Extract: class, root cause,      │     │
│              │ plan     │ │    specifics (file:line:error),   │     │
│              │ + update │ │    missing instructions, hints    │     │
│              │ session  │ │                                    │     │
│              │ learning │ │  Decide: retry? escalate? accept? │     │
│              │ (success │ │                                    │     │
│              │ pattern) │ │  If retry: build RetryBrief,      │     │
│              └─────────┘ │    discard worktree, loop back     │     │
│                           │                                    │     │
│                           │  If escalate: present to user     │     │
│                           │    with full analysis              │     │
│                           │                                    │     │
│                           │  Update session learning           │     │
│                           │    (failure pattern)               │     │
│                           └──────────────────────────────────┘     │
│                                                                    │
└──────────────────────────────────────────────────────────────────┘
```

## Escalation: When Forge Gives Up

After 3 attempts or when the same errors repeat, Forge escalates to the user with the FULL analysis:

```
Task TASK-7 "Add rate limiting middleware" FAILED after 3 attempts.

Attempt 1: Build failed — RateLimiter type not found
  → Retried with: "import RateLimiter from 'express-rate-limit'"

Attempt 2: Tests failed — rate limit test expects 429 but gets 200
  → Retried with: "rate limiter must be applied BEFORE auth middleware
     in src/app.ts middleware chain"

Attempt 3: Tests failed — SAME test still failing (429 vs 200)
  → Analysis: the test expects rate limiting on /api/health which is
     excluded from middleware in src/config/routes.ts line 12.
     The task description may need revision — /api/health is intentionally
     unprotected.

Options:
  A) Revise task: exclude health endpoint from rate limiting
  B) Revise task: include health endpoint (change routes.ts config)
  C) Skip this task (mark BLOCKED)
  D) I'll fix it manually
```

The user gets enough context to make a decision WITHOUT having to read code, run tests, or debug. Forge already did the debugging.

## What Makes This Different From "Just Retry"

| Dumb retry | Forge learning loop |
|---|---|
| Same instructions every time | Instructions augmented with specific failure context |
| No error analysis | Extracts exact errors: file, line, message, suggested fix |
| No root cause | Identifies WHY the agent failed (type bypass, scope creep, timeout) |
| No cross-task learning | Failure patterns from task 3 prevent same failure in task 12 |
| Retry until max, then fail | Escalate early when same error repeats (task is wrong, not agent) |
| User sees "failed" | User sees full analysis with options |
| 3 identical attempts | Each attempt is meaningfully different |

## The Constraint That Makes It Work

**Forge NEVER modifies the worktree.** It only analyzes. The agent starts each attempt with a CLEAN worktree (fresh copy of main). This means:

- No accumulated garbage from prior attempts
- No "fix the fix" chains that make things worse
- Each attempt is independent, with better instructions
- The diff is always clean: main → this attempt's changes

The learning is in the INSTRUCTIONS, not in the code state.
