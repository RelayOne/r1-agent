# Expected Output — Task 01 (Bash shell)

## Pass criteria (pinned assertions)

### Command 1: go file count
- Exit code: 0
- Output: integer >= 100 (the stoke repo has 1000+ non-test .go files)
- Pattern match: `^\d+$` (single integer on stdout)

### Command 2: truncation
- Output is truncated by R1 at 30 KB (30,720 bytes)
- R1 appends a truncation notice: contains "truncated" or output length = 30720 bytes
- Exit code from yes: non-zero (SIGPIPE) or 0 — both acceptable

## Allowed variance

- Exact file count may vary as the repo grows; floor of 100 is the invariant.
- Truncation notice phrasing may vary; the key is that output is NOT 40000 bytes.

## Failure indicators

- Command 1 returns 0 (impossible for a populated repo)
- Command 2 returns 40000+ characters without truncation notice
- Any error about missing `find` or `wc` commands
