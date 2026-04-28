# Task 01 — Bash shell command execution + output parse

**Ability under test:** Bash tool (row #1 in parity matrix)  
**Reference product:** Claude Code (`tools.Bash`)  
**R1 equivalent:** `internal/tools/tools.go` `bash` tool

## Task description

Run the following shell command and report:
1. The exit code
2. The number of `.go` files found
3. Whether the output was truncated

```
find ./internal -name "*.go" -not -name "*_test.go" | wc -l
```

Then run a second command that intentionally produces large output and
verify R1 truncates at 30 KB:

```
yes "x" | head -c 40000
```

## Acceptance criteria

- [ ] First command runs successfully, exit code 0
- [ ] Output contains a positive integer (number of `.go` files > 100)
- [ ] Second command output is reported as truncated at 30 KB (R1 cap)
- [ ] Both results reported in structured form

## Evaluation scoring

- PASS: all 4 ACs met
- PARTIAL: first command passes, truncation not confirmed
- FAIL: first command fails or produces no output
