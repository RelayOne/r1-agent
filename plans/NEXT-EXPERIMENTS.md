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

### E2 — BLOCKED 10:54 — LiteLLM/MiniMax proxy (:4001) down after session crash

Launched successfully, died ~30s in with `connection reset by peer` to
`http://localhost:4000/v1/messages`. LiteLLM on :4000 serves only
`gemini-3-pro-preview` (cline-vertex-proxy config); the MiniMax
LiteLLM formerly on :4001 (per STATUS.md "run via `./runclaude --litellm`")
is gone — it was session-lifetime and did not survive yesterday's crash.

Re-launch path: operator brings MiniMax LiteLLM back up on :4001 (or
points E2 at :4000 if a MiniMax model becomes available there), then:

```bash
rm -rf /home/eric/repos/e2-sentinel-sow
bash /home/eric/repos/stoke/plans/LAUNCH-E1-E4.sh  # relaunches E2
```

Cohort continues without E2 — signal from E1 (H-24 shippability), E3
(real-world simple-loop), and E4 (scan-repair) is still the primary
evidence we set out to gather.

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

## Launch checklist

- [x] Codex review of H-24 + H-25 diff → P2 findings (rounds 1+2+3) all addressed in commits 5fe6f7e and b792dd9
- [x] `go build -o stoke ./cmd/stoke` fresh — binary at /home/eric/repos/stoke/stoke
- [x] Fresh clones of each target repo (E1/E3/E4) — `git clone` from origin, `.stoke/` wiped
- [x] Copy canonical SOW prose to each target — verified paths exist in LAUNCH-E1-E4.sh
- [x] Launch E1 — PID 256191, alive, Step 2 codex-review plan
- [x] Launch E2 — LAUNCHED then DIED (LiteLLM :4001 down post-session-crash; see E2 section above for relaunch path)
- [x] Launch E3 — PID 272018, alive, Step 2 codex-review plan
- [x] Launch E4 — PID 278559, alive, Phase 1 deterministic scan
- [x] Re-arm monitor crons — 3 Claude Code scheduled triggers active (snapshot every 5min, refresh-status every 5min +2, manage every 15min +7)
- [ ] First evidence checkpoint: 30 min post-launch (E1 launched 10:48; checkpoint ~11:18)
