# Next experiments — post-H-24/H-25 binary

Date: 2026-04-18
Branch: `feat/smart-chat-mode`
Binary commit target: this batch (H-24 monorepo-normalizer + H-25 simple-loop resume + self-scan FP exclusion)

## What the last 14h of runs taught us

1. **Config that won.** `sow + MiniMax/LiteLLM worker + Codex reviewer + per-task-worktree` (H3-sow-mm) was the cohort score leader at 12 with 65 commits / 69 TS in 2h46m. `simple-loop + CC-sonnet + Codex + DEFAULT gates` (H1-v2) was the only hardened-v2 simple-loop to die by *design-intended* H-6 cap — clean kill, not a crash.
2. **Config that lost.** Pure-native LiteLLM-reviewing-LiteLLM (M1c) rubber-stamped everything; file count bounced ±20% while TS stayed flat. Concurrent fix-mode (D2) logged 0/20 merges. Opus-only workers (D-opus) accumulated 35 unresolved gate-hits.
3. **Real-world scaled.** R1F-feat reached 2172 TS on RelayOne at crash time — cohort winner. A-deep ran scan-repair end-to-end through 4 phases on ActiumChat (1792 files). Production codebases behave the same as synthetic Sentinel SOWs under hardened-v2.
4. **The token-burn shipstopper was H-24.** H1-v2 + H2-v2 each logged 200+ gate-hits on the same 8 findings for 6h+ because the declared-file gate didn't strip `apps/<name>/src/` prefixes. Fixed this pass.
5. **Codex unavailability is a real failure mode.** The 3 dormant sow variants (M2x-OPTB, H3, H4) all hit `codex: 3 retries exhausted` and degraded to deterministic-only while heartbeating. Not a crash — a silent degradation. FallbackPair (H-11) was dormant all session because no variant was configured with a CC fallback for the sow reviewer slot. Launch-config tweak, not a code fix.
6. **Resume was missing.** Simple-loop persisted zero state. After the 09:51 crash the entire cohort was unresumable. Fixed this pass (H-25).

## Cohort design — four focused runs, not fifteen

Each answers one open question. No filler.

### E1 — Does H-24 break the 200-gate loop? (simple-loop shippability A/B)

- **Repo**: fresh clone of Sentinel Web + Mobile (destination `/home/eric/repos/e1-sentinel`).
- **Config**: `stoke simple-loop --file SOW.md --reviewer codex --max-rounds 5 --fix-mode sequential`.
- **Binary**: fresh build with H-24 + H-25.
- **Expected outcome**: H-24 fires on the first commit's `apps/web/src/app/...` file and *doesn't re-fire next round*. Total gate-hits on the run should be small (bounded by gaps the worker actually didn't create, not the monorepo-prefix mismatch). Must reach SIMPLE LOOP COMPLETE or hit a different termination class (max-rounds, compliance pass). If it dies by H-6 regression cap on the same gates H1-v2 died on, H-24 didn't land.
- **Kill condition**: 6h wall-clock, OR H-6 abort.

### E2 — Does the winner config produce clean SOW completion? (sow shippability)

- **Repo**: fresh clone of Sentinel (`/home/eric/repos/e2-sentinel-sow`).
- **Config**: `stoke sow --file SOW.md --native-base-url http://localhost:4000 --native-api-key sk-... --native-model minimax-m2 --reviewer-source codex --per-task-worktree --parallel 2`.
- **Binary**: same fresh build.
- **Expected outcome**: sow mission-end compliance gate passes OR terminates with concrete missing-deliverable list. This is the budget-dev pitch — cheap worker, frontier reviewer, per-task worktree commits.
- **Kill condition**: 8h wall-clock, OR 30 min with no git-rev-count growth.

### E3 — Does the real-world simple-loop finish this time? (R1F-feat replay)

- **Repo**: fresh clone of RelayOne (`/home/eric/repos/e3-relayone-feat`).
- **SOW**: the relayone-feat-exp SOW prose from last run (preserved via `git show` or re-authored — same feature, notification-preferences).
- **Config**: `stoke simple-loop --file relayone-SOW.md --reviewer codex --max-rounds 5 --fix-mode sequential`.
- **Binary**: same fresh build.
- **Expected outcome**: 7.6h-stale Step 5 build-verify was last seen at crash. With H-24, the monorepo-prefix false-fires that kept it cycling should be gone. Should either SIMPLE LOOP COMPLETE or escalate to a *different* (genuine) termination class.
- **Kill condition**: 8h, OR H-6 abort, OR 30 min no log growth AND no commits.

### E4 — Does scan-repair converge on fresh binary? (A-deep replay)

- **Repo**: fresh clone of ActiumChat (`/home/eric/repos/e4-actium-scan`).
- **Config**: `stoke scan-repair --repo /home/eric/repos/e4-actium-scan --mode simple-loop --max-sections 0 --max-patterns 0`.
- **Binary**: same fresh build (includes H-21 ARG_MAX + H-22 flag-forwarding + H-24 monorepo fix).
- **Expected outcome**: Phase 1 (deterministic) + Phase 2 (semantic) + Phase 3 (FIX_SOW) + Phase 4 (simple-loop build) complete. A-deep died at ROUND 2 by H-6 last run — that was designed behavior; rerun confirms the full pipeline is healthy on real production scale.
- **Kill condition**: 10h, OR H-6 abort on ROUND 2+.

## Stagger + monitoring

- **Launch with 60s stagger** per STATUS open-decision #10 + #13. Avoids the 10-14min codex Step-2 latency hit that killed the original parallel launch at 11:45 yesterday.
- **Cron 1 — monitor** (re-arm): every 5 min, snapshot each variant's PID state, git rev-count, TS count, log mtime, `[gate-hit]` count, cerr count, phase line, pause/fb counters. Write to `monitor-log.md`.
- **Cron 2 — STATUS refresh** (re-arm): every 5 min offset +3, regenerate this file's "live state" section.
- **Telemetry dashboard**: after 2h of running we want one look to tell us (per variant): phase, commits, TS, [gate-hit] count, last log activity. Same format as yesterday.

## Launch checklist (execute after codex verification signs off)

- [ ] Codex review of H-24 + H-25 diff → no major/critical findings
- [ ] `go build -o stoke ./cmd/stoke` fresh
- [ ] Fresh clones of each target repo (E1-E4) — `git clone` from origin, wipe `.stoke/`
- [ ] Copy canonical SOW prose to each target
- [ ] Launch E1 (60s)
- [ ] Launch E2 (60s)
- [ ] Launch E3 (60s)
- [ ] Launch E4
- [ ] Re-arm monitor cron + STATUS refresh cron
- [ ] First evidence checkpoint: 30 min post-launch
