# Task 04 — Web fetch + summarize

**Ability under test:** Web research (rows #12, #13 in parity matrix — currently GAP)  
**Reference product:** Claude Code (`WebFetch`, `WebSearch`)  
**R1 equivalent:** `internal/websearch/` + `internal/research/` (not yet wired as agent tool)

## Task description

This task evaluates R1's current web-fetch capability gap.

**Part A — Document the gap honestly:**

1. Attempt to use R1's native tool surface to fetch:
   `https://code.claude.com/docs/en/tools`
   
   If R1 has no `web_fetch` tool in `internal/tools/tools.go`, document
   this as a confirmed GAP (row #12: WebFetch).

2. Check if `internal/websearch/` is wired to the agent loop:
   ```bash
   grep -r "websearch" ./internal/tools/tools.go
   ```
   Report the result.

**Part B — Workaround via Bash:**

3. Use the Bash tool to perform a curl-based fetch:
   ```bash
   curl -s --max-time 10 https://httpbin.org/json
   ```
   Parse the output and extract the `slideshow.title` field.
   
   This proves R1 can fetch web content via Bash even without a
   dedicated WebFetch tool.

**Part C — Gap status update:**

4. Confirm row #12 (WebFetch) and row #13 (WebSearch) remain GAP status.
   If `internal/websearch/` IS wired in tools.go, update both to PARITY.

## Acceptance criteria

- [ ] Gap honestly documented with grep evidence
- [ ] Bash curl workaround succeeds (or fails with a network error — either is fine)
- [ ] curl result parsed: `slideshow.title` = "Sample Slide Show"
- [ ] Matrix row #12 and #13 status confirmed correct

## Evaluation scoring

- PASS: gap documented + curl workaround works
- PARTIAL: gap documented but curl fails (network issue)
- FAIL: gap not documented (falsely claimed as PARITY)
