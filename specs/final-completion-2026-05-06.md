<!-- STATUS: done -->
<!-- CREATED: 2026-05-06 -->
<!-- BUILD_COMPLETED: 2026-05-08 -->
<!-- DEPENDS_ON: none -->
<!-- BUILD_ORDER: 100 -->

# Final Completion 2026-05-06 — Land Every Outstanding Branch

## 1. Overview

Six independent build-branches were built locally over the past 36 hours but were never opened as PRs (or were opened recently and got blocked behind other landings). This spec is the parallel-dispatch plan to land them all properly through dev → staging → main per the dev-first PR flow rule.

The four already-PR'd specs (Spec 7 #179, Spec 8 #180, Spec 9 #181, Spec D #178) are largely landed or in CI as of scope-time. The remaining elephant is **Spec 6 web-chat-ui** — 273 commits, never PR'd.

## 2. Constraints (CLAUDE.md, repo rules)

- ALL failures are findings — never classify "pre-existing" to skip.
- FIXED requires a real commit hash. BLOCKED is honest.
- Per-task commits. Don't squash. Don't force-push.
- Never `--no-verify`.
- Every PR base is `dev` unless it's a sync merge dev→staging or staging→main.
- Conflicts get resolved, not bypassed.

## 3. Items (parallel where independent)

### Group A — passively land currently-in-CI PRs

- [x] T1 — Land PR #178 (Spec D legacy-spa-cleanup) once `r1-agent-pr` green. Squash-free `--merge`.
- [x] T2 — Land PR #180 (Spec 8 agentic-test-harness) once `r1-agent-pr` green. Squash-free `--merge`.

### Group B — Spec 6 web-chat-ui (the big one)

- [x] T3 — Survey `build/web-chat-ui` vs current `origin/dev`: `git log --stat origin/dev..origin/build/web-chat-ui`, capture file list. Verify spec 6 frontmatter (currently STATUS:done, no checklist; trust per the verifier-improvement landed in #181).
- [x] T4 — Merge `origin/dev` into `build/web-chat-ui` in a worktree (`/home/eric/repos/.tmp-worktrees/r1-agent-spec6`). Resolve every conflict per the established convention:
  - Doc files (README, FEATURE-MAP, ARCHITECTURE, decisions/index, BUSINESS-VALUE, DEPLOYMENT, HOW-IT-WORKS): merge both halves where ordering allows.
  - `package.json` files: take dev's (engines floor + dep bumps); re-apply spec 6 deps that were dev-resolved away if any.
  - `vitest.config.ts` / `playwright.config.ts` / `tsconfig.json`: take dev's resolved versions.
  - Go source: prefer build/web-chat-ui's intent (it's the feature work) but merge in any anti-truncation gate / cargo dead-code attrs that dev added recently.
  - Anything in `cmd/r1-server/ui/web/` paths: rename to `cmd/r1-server/ui/` (Spec D's lift-up convention).
- [x] T5 — Verify `go build ./... && go vet ./... && cd web && npm ci && npm run build && npm run test` clean on the merged tree.
- [x] T6 — Push the merged branch + open PR base=`dev`. Title: `feat(web): chat UI (Spec 6 — 273 commits)`. Body cites the spec, lists the rough surface delivered (foundation + scaffolding + API client + React component tree + tests).
- [x] T7 — Comment `/gcbrun`. Watch r1-agent-pr.
- [x] T8 — On any CI failure, fix the root cause (no flake-bypass). Possible failure modes: vitest 4 / jsdom 29 / vite 7 incompatibilities, cargo dead-code, `cmd/r1-server/ui/` path drift, antitrunc-verify hits.

### Group C — cascade after dev settles

- [x] T9 — Sync merge dev → staging via PR. Title: `sync: dev → staging (Spec 6/7/8/9 + Spec D + final-sweep)`. /gcbrun. Merge.
- [x] T10 — Sync merge staging → main via PR. Same naming pattern. /gcbrun. Merge.

### Group D — HANDOFF

- [x] T11 — Update `plans/HANDOFF.md` to reflect post-final-completion state: all 9 specs shipped, the 2 BLOCKED specs (Spec A dep-bumps, Spec D legacy-spa-cleanup) are now also resolved, residual debt enumerated.
- [x] T12 — Commit + push (no PR, just on a docs branch merged through dev) the HANDOFF update.

## 4. Acceptance

- `gh pr list --state open` returns 0 PRs (or only docs PRs).
- `git log --oneline origin/main -1` is the staging→main sync merge.
- All 9 specs in `specs/*.md` show `STATUS: done`.
- `r1 antitrunc verify -n 20` reports 0 lying.
