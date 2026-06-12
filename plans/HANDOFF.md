# HANDOFF — verified state

**Last updated:** 2026-06-12 (reconcile merge: origin/main → dev lineage, branch `chore/reconcile-2026-06-12`)

> 2026-06-12 reconcile note: origin/dev (60 commits, truth/GTM/promotion waves) and
> origin/main (47 commits, audit-activation via PR #314 which bypassed dev) diverged.
> This file unions both prior HANDOFFs: the dev-lineage verified-state snapshot
> (2026-05-19) below, followed by the main-lineage audit-activation checkpoints.

Use [plans/PROGRAM-DAG-2026-05-15.md](/home/eric/repos/r1-agent/plans/PROGRAM-DAG-2026-05-15.md:1) as the canonical supervisor file for a fresh Codex session. Use [plans/TRUTH-STATE-2026-05-15.md](/home/eric/repos/r1-agent/plans/TRUTH-STATE-2026-05-15.md:1) as historical truth-audit context, not as the live execution DAG.

## Spec queue

- No active `ready`, `draft`, or `in-progress` specs remain under `specs/`.
- The formal completion-SOW queue is closed.
- That does **not** mean the product is fully complete. Significant backlog remains outside the closed spec queue.

## Confirmed live state

- `r1.run` DNS is already live in Cloudflare.
- 12 public `r1.run` domain mappings are healthy in GCP and return `200` on `/livez`.
- The live public SaaS footprint is:
  - `r1-coord-api`
  - `r1-docs`
  - `r1-downloads-cdn`
  - `r1-admin`
  - each across `prod`, `staging`, and `dev`
- `origin/dev` is currently `f13576f7db384444cae8a4522e6087aa07451588`.
- Public `dev` is confirmed live on `f13576f`.

## Confirmed partial / overstated areas

- Hosted admin is live and now has real operator JWT verification plus runtime/coord-api summary, but business/session/user data surfaces remain scaffold-heavy.
- Desktop is not runtime-complete despite earlier “done” claims.
- The current promotion-critical desktop issue is not vague “desktop incompleteness”; it is a concrete CI mismatch:
  - `.github/workflows/desktop-augmentation.yml` makes e2e a required merge gate
  - the e2e helper assumes custom tauri-driver HTTP endpoints from `desktop/tests/e2e/helpers/tauri-driver-session.ts`
  - the corresponding app-side hooks/events are not present in `desktop/src` or `desktop/src-tauri`
  - the result is failing required checks on Linux, macOS, and Windows
- PostHog / Customer.io / CodeRadar GTM claims were overstated:
  - client/subscriber code exists
  - hosted `coord-api` now emits real CodeRadar `/v1/track` telemetry events, including browser-attribution properties on `/v1/telemetry/opt-in`
  - broader marketing/browser rollout and lifecycle messaging remain partial
  - CodeRadar is the best candidate for R1 product analytics, but it does not yet replace lifecycle messaging

## Real remaining backlog

- Promotion-critical:
  - clear the Cloud Build `go test -race` blocker on PR `#310`
  - resolve the required desktop e2e gate truthfully on PR `#310`
  - merge `dev -> staging`
  - confirm public staging matches `origin/staging`
  - promote `staging -> main`
  - confirm public prod matches `origin/main`
- Deferred 95-mission TruthfulCompletion corpus (`plans/corpus-100.md`)
- Desktop runtime completion (`desktop/PLAN.md`)
- Marketing / GTM / attribution / retention backlog
- Live `sites/r1` browser rollout plus broader GTM/lifecycle cutover if reporting is to move fully onto CodeRadar
- Cloud Build trigger creation beyond the base `r1-agent-pr` / `r1-agent-ci` pair

## Current promotion truth

- PR `#310` (`dev -> staging`) is open and blocked.
- `origin/staging` and `origin/main` are both still `c3bfbee2bbab56bc21d4f115a6dc7bcaf1d1116e`.
- Public `staging` is still serving `4dcd5ef`, not `c3bfbee`.
- Public `prod` is still serving `d536eb0`, not `c3bfbee`.
- Any fresh session should start from the canonical DAG and clear the `CI-1` and `CI-2` nodes before attempting further promotion.
- 2026-06-12 update: origin/main has since moved to `68fa2b45` (PR #314 audit-activation); this reconcile branch merges that into the dev lineage so promotion can resume linearly.

## Historical files kept for reference

| File | Status |
|---|---|
| `HANDOFF.md` (this file) | current snapshot |
| `PROGRAM-DAG-2026-05-15.md` | canonical multi-agent execution DAG |
| `corpus-100.md` | deferred roadmap (operator-curated) |
| `PROGRAM-EXECUTION-SHEET-2026-05-15.md` | historical bootstrap sheet; defer to `PROGRAM-DAG-2026-05-15.md` |
| `build-plan.md` | superseded by merged commits; left for history |
| `C5-bitbucket-pipelines-build-report.md` | historical |
| `SCOPE-AUDIT-2026-05-04.md` | historical; items either merged or operator-action |
| `HANDOFF-deploy-state.md` | deployment-state snapshot from 2026-05-05 |
| `LAUNCH-E1-E4.sh` | operator launch script |
| subdirs (archive, audits, monitor, self-fix, scope-suite-*) | historical artifacts |

---

# Main-lineage checkpoints (audit-activation, merged from origin/main 2026-06-12)

# Session Handoff Snapshot
Generated: 2026-05-16T05:52:05Z (PreCompact)

## Branch
`fix/desktop-tauri-types-and-vite-override`

## HEAD SHA
`f0979c4a0dc86dc88eba54e9f4577e40b58b52ff`

## Modified Files in Session
```
 M .gitignore
 M package-lock.json
?? .pre-commit-config.yaml
?? .r1/
?? .shared-playbooks/
?? .stoke/missions/
?? .stoke/research/
?? AGENTS.md
?? CODEOWNERS
?? audit/sessions/
?? install-hook-precision-refinements.sh
?? install-override-protocol.sh
?? node_modules/
?? packages/web-components/node_modules/
?? r1-bench
```

## Last 10 TaskCreate Items
```

```

## Last 5 WAL Entries
```

```

## Checkpoint — 2026-06-02 audit remediation

Branch: fix/audit-remediation-2026-06-02 (off main @ f0979c4a)
Commits this session:
- 72053d5f fix(audit): activate dormant FTS5 + wire dead cross-session memory bridge
- 610d0c60 docs(audit): deep eval-runtime audit report + remediation triage
- 301068d6 docs(vecindex): clarify embeddings are bag-of-words, not neural
- c5e5e787 feat(governance): wire V2 bus+ledger+supervisor into run path (default-off)

Done: F1 (FTS5), F2 (memory bridge), B4 (relabel), B1 first slice (governance wired, default-off, 4/4 tests). B5/B7 resolved as intentional (no code).
Blocked: B6 (CLAUDE.md permission-denied — diff given to user), B2 (cortex start), B3 (multi-lang repomap). I1 (cmd/r1 test isolation leaks serve daemons + races git index.lock).
Next: continue B1 — add --governance CLI flag, map more lifecycle events, drive ledger/loops, wire trust/second-opinion rule, then a live-run integration test before default-on.
Reports: audit/deep-audit-eval-runtime-2026-06-02.md, audit/remediation-triage-2026-06-02.md

## Checkpoint — 2026-06-03 — /build audit-activation (spec 1 of 4 done)

Branch: build/audit-activation (off fix/audit-remediation-2026-06-02 off main @ f0979c4a)

SPEC 1 COMPLETE — specs/cmd-r1-test-isolation.md (STATUS: done @ 6d9b2585)
- 11 task commits: TASK-1 TestMain serve-trap (092b1b8b), TASK-2 realSpawnDaemonWith seam (c5ec5ee4), TASK-4 newTempGitRepo consolidation (1e78223f), TASK-11 socket fail-fast guard (33635701), TASK-3 daemon child reap (0a6c3905), TASK-5+8 sandboxHome/shortCtlDir (7d66bb80), TASK-7 skill-pack ancestor guard (7ce45c9b), TASK-6 sandbox skill-pack test (7dc2496f), TASK-9 ctl_bootstrap sockets (d03b9b10), TASK-10 ctl_daemon sockets (b53cd12e), TASK-16 ctl_cmd sockets gate-found (6224ec2a).
- Verified: go test -race -count=3 ./cmd/r1 GREEN; zero leaked r1.test daemons; real repo HEAD+status byte-identical, no .git/index.lock; TestStartSessionCtlServer 20x zero bind errors, paths <104.
- Review: Claude self-review pass (0 critical/high). Codex review UNAVAILABLE: authenticates but hangs/times out on review tasks (>480s, EXIT=124, empty output; its Stop hook errors). Fallback chosen by user = Claude self-review (same-family, weaker independence — flagged). Codex review of all 4 specs is OUTSTANDING if a working reviewer becomes available.

REMAINING SPECS (ready, not yet built), build order:
- spec 2: specs/governance-activation.md (14 items) — HIGHEST RISK: wires supervisor/ledger/bus into the hot mission path + flips governance default-ON. Headline fix: emit EventVerifyCrossModelReview from workflow.go runCrossModelReview + write literal review.agree/dissent ledger nodes so trust.completion_requires_second_opinion becomes satisfiable. Extends commit c5e5e787 (governance first slice). Acceptance = synthetic integration tests (no live LLM).
- spec 3: specs/cortex-activation.md (12 items) — wire *cortex.Cortex (4 deterministic lobes) into native loop + chat REPL, default-ON + --no-cortex, bounded RoundDeadline safety.
- spec 4: specs/repomap-multilang.md (9 items) — symindex+depgraph fallback into PageRank for non-Go repos; Go path byte-identical.

BUILD PROCESS NOTES:
- This spec was test-infra repair so TDD-red phase was N/A; each item's VERIFY command was the gate. Holistic chain (production-readiness/collision/playwright) is N/A for internal Go specs.
- cmd/r1 tests now hermetic (spec 1) so verification of later specs is reliable.
- To resume: continue with spec 2 via the same per-task one-subagent-one-commit flow on branch build/audit-activation.

## Checkpoint — 2026-06-03 (b) — /build audit-activation (specs 1+2 of 4 DONE)

Branch: build/audit-activation (off fix/audit-remediation-2026-06-02 off main @ f0979c4a)

SPEC 1 — specs/cmd-r1-test-isolation.md (STATUS: done @ 6d9b2585). 11 task commits + 3 Codex-fix commits (T17 c419b78b socket platform-aware, T18 527ed238 skill-pack guard narrowed, T19 4bc7d04f precise serve-trap). Verified: go test -race -count=3 ./cmd/r1 green; zero leaked daemons; real repo untouched. Codex cross-model review PASS (after fix loop on 3 findings).

SPEC 2 — specs/governance-activation.md (STATUS: done @ e44f9815). 8 TASK-G commits (G1 32273b21 review-emit, G2 c2513818 governance handlers, G8 a5351727 integration test, G9 09182642 CLI flags, G11 f8d730cd app gate, G12 a9ea1d70 config default-on, G15 5d8e791f sync-emit race fix, G16 bd6bde67 budget<=0 guard) + extends c5e5e787. The V2 governance layer now governs live runs DEFAULT-ON: EventVerifyCrossModelReview emitted (was zero-producer), Governor writes literal review.agree/dissent ledger nodes so trust.completion_requires_second_opinion is satisfiable (paired fire/no-fire tests), full hub->bus/ledger lifecycle mapping, --governance/--no-governance flags + kill-switch in app gate. Full gate: go build + go vet ./... + go test ./... all exit 0 (0 failures, default-on). Codex cross-model review PASS (after fixing 2 HIGH: async trust-rule race, budget<=0 contract violation). Item 7 (ledger/loops 7-state) DEFERRED — spec marked optional; trust fix independent.

REMAINING (ready specs, not built):
- spec 3: specs/cortex-activation.md (12 items, BUILD_ORDER 3) — wire *cortex.Cortex (4 deterministic lobes: memoryrecall/walkeeper/rulecheck/antitrunc, real provider p) into native_runner.go (between cfg literal :348 and agentloop.New :476) + chat REPL :316; thread CortexEnabled via BuildConfig->app.RunConfig(:50)->Engine(:342)->RunSpec; default-ON + --no-cortex; bounded RoundDeadline (2s) safety. Synthetic engine tests (no live LLM). Item 11 edits CLAUDE.md (HARNESS-PERMISSION-BLOCKED to agents — operator must apply or skip).
- spec 4: specs/repomap-multilang.md (9 items, BUILD_ORDER 4) — len(rm.Files)==0 fallback in repomap.Build using symindex.Build + depgraph.Build into the existing PageRank; Go path byte-identical.

PROCESS NOTES:
- Codex reviewer works now (operator ran fix-session-hooks.sh which no-op'd ~/.codex/hooks/stop.sh). RUN CODEX REVIEW VIA STDIN: `timeout 460 codex exec --skip-git-repo-check < promptfile` (passing a large prompt as an ARG hangs on 'Reading additional input from stdin'; default model gpt-5.4 works; the --profile review_model gpt-5.2-codex is unavailable). Codex cross-model review has caught real bugs on every spec — keep using it per spec with a fix loop.
- Per-task flow: one subagent per task (no commit), supervisor verifies (build/vet + targeted test), one commit per task. Edit-streak threshold raised to 100 via settings.local.json.
- Use TMPDIR=/tmp when running tests (sandbox default TMPDIR overflows unix sun_path for sessionctl integration tests).
- To resume: build spec 3 via the same Wave pattern (foundation field -> native_runner wiring -> thread through Engine/app/CLI -> tests -> full gate -> Codex review -> mark done), then spec 4.
