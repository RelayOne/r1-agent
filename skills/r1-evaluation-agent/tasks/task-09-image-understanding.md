# Task 09 — Image understanding (gap verification)

**Ability under test:** Image / vision input (row #82 in parity matrix — GAP)  
**Reference product:** Claude Code (image attachment support)  
**R1 equivalent:** None — confirmed GAP

## Task description

This task verifies that R1's image-input gap is correctly documented and
assesses the severity.

**Part A — Confirm the gap:**

1. Check `internal/tools/tools.go` for any image-related tools:
   ```bash
   grep -in "image\|vision\|screenshot\|png\|jpg\|jpeg" \
     ./internal/tools/tools.go
   ```
   Expected: zero matches — confirming GAP.

2. Check the `internal/provider/` package for vision/image API support:
   ```bash
   grep -rn "vision\|image_url\|base64" \
     ./internal/provider/ --include="*.go" | head -20
   ```
   Report findings. If vision API calls exist in the provider layer,
   the gap is "not wired to agent tools" rather than "no capability at all".

**Part B — Severity assessment:**

3. Write a severity note to `/tmp/r1-eval-task-09-gap-note.txt`:
   - If provider has vision support: "SEVERITY: MEDIUM — API supports images, agent tool not wired (gap task R1P-004 = 1-2 day fix)"
   - If provider has NO vision support: "SEVERITY: HIGH — full stack missing (gap task R1P-004 = requires provider + tool work)"

**Part C — Workaround via Bash:**

4. Demonstrate a partial workaround: extract text from a PNG using
   ImageMagick or tesseract if available:
   ```bash
   which tesseract 2>/dev/null || which identify 2>/dev/null || echo "no image tools available"
   ```
   Document the result (available or not).

## Acceptance criteria

- [ ] Gap confirmed via grep (zero image-tool entries in tools.go)
- [ ] Provider vision support checked and documented
- [ ] Severity note written with correct severity level
- [ ] Bash workaround availability documented

## Evaluation scoring

- PASS: all 4 ACs met
- PARTIAL: gap confirmed but severity not assessed
- FAIL: gap falsely marked as PARITY
