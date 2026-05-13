<!-- STATUS: done -->
<!-- CREATED: 2026-05-13 -->
<!-- BUILD_STARTED: 2026-05-13 -->
<!-- BUILD_COMPLETED: 2026-05-13 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 47 -->

# `r1 antitrunc verify --hook-mode` — Claude Code Stop-hook compatibility flag

## 1. Overview

The TruthfulCompletion benchmark (`specs/truthful-completion-benchmark.md`, BUILD_ORDER 48) needs `r1 antitrunc verify` to run as a Claude Code `Stop` hook. Claude Code's Stop hook protocol expects the hook command to exit non-zero when the agent must NOT stop (so the model loop continues). The current `r1 antitrunc verify` exits 0 with a human-readable summary regardless of finding count.

This spec adds three things:

1. A `--hook-mode` flag that makes the command exit `2` when one or more findings are reported, and `0` when clean.
2. A `--plan <path>` flag that points the scope-completion check at a plan file (defaulting to `plans/build-plan.md` so the Claude Code dispatcher's settings.json template stays terse).
3. A single-line JSON output mode (one JSON object on stdout) so Claude Code's Stop hook plumbing can ingest findings without parsing free text.

The downstream A1 spec wires this flag into the Claude-Code-Stop-Hook dispatcher (§5.2.3). Without `--hook-mode`, the "Claude Code with R1 Stop hook template" leaderboard row cannot be produced.

## 2. Existing code to extend

- `cmd/r1/antitrunc_cmd.go` (449 LOC) — current home of `r1 antitrunc verify`. Flag parsing happens here; reuse the existing `flag.NewFlagSet` block.
- `internal/antitrunc/gate.go` (253 LOC) — Gate type + Findings.
- `internal/antitrunc/scopecheck.go` (176 LOC) — `CountChecklist(planPath)` returns `(done, total, err)`; reuse verbatim.
- `internal/antitrunc/phrases.go` (203 LOC) — 12 truncation regexes + 2 false-completion regexes; reuse verbatim.

## 3. Stack & Versions

- Go 1.22 + stdlib only (`encoding/json`, `flag`, `os`, `strings`).
- No new dependencies.

## 4. Existing patterns to follow

- The `r1 antitrunc verify` subcommand in `cmd/r1/antitrunc_cmd.go` already uses a `flag.NewFlagSet` block — add `--hook-mode` (bool, default false) and `--plan` (string, default `plans/build-plan.md`) to the same set.
- JSON envelope shape mirrors `internal/oneshot/oneshot.go::Response`: `{"verb":"antitrunc.verify","status":"ok"|"findings","data":{...}}`. The hook-mode caller will read `status` to decide.
- Exit codes mirror `cmd/r1/oneshot_cmd.go`: `0` success, `2` usage / blocking finding. Document in the file header per `cmd/r1/oneshot_cmd.go:18-30`.

## 5. Library Preferences

- `encoding/json` for the envelope.
- No third-party flag library — extend the existing `flag.NewFlagSet`.

## 6. Implementation Checklist

Each item is self-contained for `/build` subagents.

1. [ ] **Add `--hook-mode` bool flag to `cmd/r1/antitrunc_cmd.go::verifyCmd`.** Flag declaration `fs.Bool("hook-mode", false, "exit 2 when findings present; emit one-line JSON envelope on stdout")`. Default `false` preserves the existing human-readable surface.

2. [ ] **Add `--plan` string flag to the same flag set.** Declaration `fs.String("plan", "plans/build-plan.md", "path to the plan file the scope-completion check reads")`. The default matches the spec/build-plan convention used elsewhere in the repo. Existing callers that don't pass `--plan` continue to behave identically (the verify command's current default is already the same path).

3. [ ] **Wire `--hook-mode` to the output formatter.** When the flag is true: skip the human-readable summary; emit exactly one line of JSON to stdout, then exit. Envelope shape:
   ```json
   {
     "verb": "antitrunc.verify",
     "status": "ok" | "findings",
     "data": {
       "findings_count": N,
       "findings": [
         {
           "source": "phrase|scope|tool|consistency",
           "phrase_id": "<id-or-empty>",
           "snippet": "<<=120 chars>",
           "detail": "<<=240 chars>"
         }
       ],
       "plan_path": "<resolved-path>",
       "plan_items_done": D,
       "plan_items_total": T
     }
   }
   ```
   When `status == "findings"`, exit code is `2`. When `status == "ok"`, exit code is `0`.

4. [ ] **Tests in `cmd/r1/antitrunc_cmd_test.go`** (extend the existing file; do not create a new one):
   - `TestAntitruncVerify_HookMode_CleanInputExits0` — input with no truncation phrases and a plan file where all items are checked → exit 0 + JSON envelope with `status:"ok"`.
   - `TestAntitruncVerify_HookMode_PhraseFindingExits2` — input containing one of the canonical truncation phrases (e.g. "for brevity") → exit 2 + JSON envelope with `status:"findings"` and at least one entry whose `source == "phrase"`.
   - `TestAntitruncVerify_HookMode_PlanItemUncheckedExits2` — clean text but plan file has `[ ] unchecked` entry → exit 2 + envelope with `status:"findings"` and entry whose `source == "scope"`.
   - `TestAntitruncVerify_HookMode_EmitsExactlyOneJSONLine` — assert stdout has exactly one `\n` and parses as one JSON object (no banner lines, no debug lines).
   - `TestAntitruncVerify_HookMode_PlanPathFlagHonored` — supply `--plan /tmp/x/plan.md` pointing at a different plan; assert the envelope's `plan_path` matches and the findings reflect that plan's checklist state.

5. [ ] **Documentation in `docs/ANTI-TRUNCATION.md`** — add a "Claude Code Stop hook integration" subsection with the canonical `.claude/settings.json` template:
   ```json
   {
     "hooks": {
       "Stop": [{
         "hooks": [{
           "type": "command",
           "command": "r1 antitrunc verify --hook-mode --plan plans/build-plan.md",
           "timeout": 30
         }]
       }]
     }
   }
   ```
   Plus a one-paragraph note that the same flag is what the TruthfulCompletion benchmark's Claude-Code-Stop-Hook dispatcher uses. Cross-link to `specs/truthful-completion-benchmark.md`.

## 7. Boundaries — What NOT To Do

- DO NOT change exit codes when `--hook-mode` is NOT set. The default human-readable surface stays exit `0` regardless of finding count (current behavior).
- DO NOT add new detection signals here. The flag is a serialization adapter; new phrases / scope-check logic land in `internal/antitrunc/`.
- DO NOT depend on the `internal/bench/` package — this flag MUST work in isolation so other consumers (a future GitLab CI hook, a future GitHub Actions step, etc.) can use it.
- DO NOT emit a multi-line JSON object. Claude Code's hook protocol reads stdout line-by-line; multi-line breaks parsing.

## 8. Acceptance Criteria

- WHEN `r1 antitrunc verify --hook-mode --plan plans/build-plan.md` runs with no findings THE SYSTEM SHALL exit 0 and emit exactly one JSON line with `status:"ok"`.
- WHEN the same command runs with at least one truncation phrase or one unchecked plan item THE SYSTEM SHALL exit 2 and emit exactly one JSON line with `status:"findings"`.
- WHEN `r1 antitrunc verify` runs WITHOUT `--hook-mode` THE SYSTEM SHALL exit 0 with the existing human-readable summary (regression-free against the existing `TestAntitruncVerify_*` test set).
- WHEN `--plan <path>` is supplied THE SYSTEM SHALL load the checklist from that path and reflect its done/total in the envelope.

## 9. Estimate

2-3 day build. Three Go file edits (`cmd/r1/antitrunc_cmd.go`, `cmd/r1/antitrunc_cmd_test.go`, `docs/ANTI-TRUNCATION.md`); no new packages, no new deps.
