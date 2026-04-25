# Task 10 — PDF parse + summarize (gap verification)

**Ability under test:** Document parsing / PDF (row #83 in parity matrix — GAP)  
**Reference product:** Manus (document ingestion)  
**R1 equivalent:** None — confirmed GAP; workaround via Bash

## Task description

This task verifies the PDF-parse gap and demonstrates the Bash workaround.

**Part A — Confirm the gap:**

1. Check `internal/tools/tools.go` for any document-parsing tools:
   ```bash
   grep -in "pdf\|docx\|document\|parse.*doc" \
     /home/eric/repos/stoke/internal/tools/tools.go
   ```
   Expected: zero matches — confirming GAP.

**Part B — Bash workaround:**

2. Test if `pdftotext` is available:
   ```bash
   which pdftotext 2>/dev/null && echo "AVAILABLE" || echo "NOT AVAILABLE"
   ```

3. If pdftotext is available, create a minimal test PDF using Bash and
   extract its text:
   ```bash
   # Create a tiny PostScript file and convert to PDF
   printf '%%!PS\n(Hello PDF)\n= showpage\n' > /tmp/r1-eval-test.ps 2>/dev/null \
     && ps2pdf /tmp/r1-eval-test.ps /tmp/r1-eval-test.pdf 2>/dev/null \
     && pdftotext /tmp/r1-eval-test.pdf - 2>/dev/null \
     || echo "PDF creation not available in this environment"
   ```

4. If neither tool is available, document the full dependency chain
   needed: `pdftotext (poppler-utils)` or `mutool (mupdf-tools)`.

**Part C — Gap severity:**

5. Write to `/tmp/r1-eval-task-10-gap-note.txt`:
   ```
   GAP: R1 has no native PDF-parse tool.
   Workaround: Bash + pdftotext or mutool (if installed).
   Severity: LOW — workaround covers 90% of use cases.
   Manus comparison: Manus integrates with Google Drive + has document connectors.
   Gap task: R1P-023 — add pdf_read tool wrapping pdftotext/mutool.
   ```

## Acceptance criteria

- [ ] Gap confirmed via grep (zero pdf-tool entries in tools.go)
- [ ] pdftotext availability checked and documented
- [ ] Bash workaround attempted (or documented as unavailable)
- [ ] Gap severity note written

## Evaluation scoring

- PASS: all 4 ACs met
- PARTIAL: gap confirmed but workaround not attempted
- FAIL: gap falsely marked as PARITY
