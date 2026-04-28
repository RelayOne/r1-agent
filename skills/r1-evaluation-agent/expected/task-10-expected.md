# Expected Output — Task 10 (PDF parse — gap verification)

## Pass criteria (pinned assertions)

### Part A — gap confirmation
- grep of tools.go for pdf/docx keywords returns 0 matches
- Gap (row #83) remains as GAP in the matrix

### Part B — pdftotext availability
- `which pdftotext` result documented: "AVAILABLE" or "NOT AVAILABLE"

### Part C — workaround attempt
- If pdftotext available: PDF created and text extracted (or attempted)
- If not available: documented as "PDF creation not available in this environment"

### Gap note file
- `/tmp/r1-eval-task-10-gap-note.txt` exists
- Contains: GAP, Workaround, Severity: LOW, Manus comparison, Gap task R1P-023

## Allowed variance

- pdftotext / ps2pdf may not be installed in the eval environment — that's fine.
- Gap note content may rephrase the template text as long as the semantics match.

## Failure indicators

- Claiming PDF parsing works in R1 without code evidence
- Missing the gap note file
- Severity labeled as HIGH (it's LOW — workaround exists)
