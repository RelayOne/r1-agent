# HANDOFF — completion SOW closed

**Last updated:** 2026-05-14
**Branch state:** all completion-SOW specs merged to `dev`. No open `ready` or `draft` specs remain.

## Final spec status

| Status | Count | Notes |
|---|---|---|
| `done` | 60+ | All completion-SOW Tier A/B/C/D specs + earlier work |
| `superseded` | 1 | `p0-hardening-s0-foundation.md` → see `encryption-at-rest.md` + `retention-policies.md` |
| no STATUS | 5 | Templates / historical reports (TEMPLATE.md, QUICKSTART.md, reconciliation-report.md, r1-rename-s{1,2}-*.md) |

Verification: `grep -lE "^<!-- STATUS: (ready\|draft\|in-progress) -->" specs/*.md` returns empty.

## PRs landed this session

| PR | Commit | Title |
|---|---|---|
| #301 | `0fe7e63d` | TruthfulCompletion benchmark (engineering + 5 seeds) |
| #302 | `41a071c1` | docs+test: close A5 admin-panel + B2 customerio-lifecycle; supersede A2 |
| (this branch) | — | docs: collapse duplicate spec entries in root README |

## Known findings (not blockers)

- **`internal/tui` flake.** Showed up once in a full `go test ./...` run; passes deterministically on isolated runs and under `-race` and `-count=3`. Doesn't touch any of the recently-merged code paths. Investigation tabled — non-reproducible regression fishing violates the "don't add code without evidence" principle.

## Operator-only items remaining (NOT subagent work)

- 95-mission SWE-bench Pro corpus per `plans/corpus-100.md` (10 missions/week → 10 weeks of curator effort).
- DNS records for `admin.r1.run` + the 9-service domain mappings (Cloudflare CNAMEs, proxy OFF).
- Secret values for the 6 secret-manager placeholders (CUSTOMERIO_*, POSTHOG_*, etc.).
- Cloud Build trigger creation for the monthly + PR TruthfulCompletion runs.
- dev → staging → main promotion (sync merges, not feature PRs).

## What's left in this directory

| File | Status |
|---|---|
| `HANDOFF.md` (this file) | current snapshot |
| `corpus-100.md` | deferred roadmap (operator-curated) |
| `build-plan.md` | superseded by merged commits; left for history |
| `C5-bitbucket-pipelines-build-report.md` | historical |
| `SCOPE-AUDIT-2026-05-04.md` | historical; items either merged or operator-action |
| `HANDOFF-deploy-state.md` | deployment-state snapshot from 2026-05-05 |
| `LAUNCH-E1-E4.sh` | operator launch script |
| subdirs (archive, audits, monitor, self-fix, scope-suite-*) | historical artifacts |
