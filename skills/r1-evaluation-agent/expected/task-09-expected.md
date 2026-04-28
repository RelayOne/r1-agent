# Expected Output — Task 09 (Image understanding — gap verification)

## Pass criteria (pinned assertions)

### Part A — gap confirmation
- grep of tools.go for image/vision/screenshot keywords returns 0 matches
- Gap (row #82) remains as GAP in the matrix

### Part B — provider check
- Either: "SEVERITY: MEDIUM — API supports images, agent tool not wired"
  (if provider has vision support)
- Or: "SEVERITY: HIGH — full stack missing"
  (if provider has no vision API calls)
- Both are valid PASS outcomes; the assessment must match the grep evidence

### Part C — Bash workaround
- `which tesseract` or `which identify` result documented
- Either "available" or "not available" — both are PASS

### Severity note file
- `/tmp/r1-eval-task-09-gap-note.txt` exists
- Contains SEVERITY: MEDIUM or HIGH with justification

## Failure indicators

- Claiming image input works in R1 without code evidence
- Missing the severity note file
- Severity mismatch with grep evidence
