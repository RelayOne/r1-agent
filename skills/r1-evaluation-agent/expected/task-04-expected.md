# Expected Output — Task 04 (Web fetch / gap verification)

## Pass criteria (pinned assertions)

### Part A — gap confirmation
- `grep` of tools.go for "websearch" returns 0 matches
- OR if websearch IS wired, rows #12 and #13 status updated to PARITY
- Either outcome is PASS (honest reporting is the criterion)

### Part B — Bash curl workaround
- `curl https://httpbin.org/json` returns HTTP 200
- Parsed output contains `"slideshow"` key
- `slideshow.title` = `"Sample Slide Show"`
- If curl fails (no network), document as "network unavailable in eval env" — still PASS

### Part C — matrix rows confirmed
- Rows #12 (WebFetch) and #13 (WebSearch) have correct status
- Citation URL and last-checked date updated

## Allowed variance

- curl may fail in offline/CI environments — network failure is not a FAIL for this task.
- httpbin.org may have changed its response format; any JSON response is acceptable.

## Failure indicators

- Claiming PARITY for WebFetch/WebSearch without grep evidence that tools.go has them
- Fabricating a curl response
