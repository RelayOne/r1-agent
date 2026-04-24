# S6 Deprecation-Window Closures -- Operator Runbook (R1 repo)

**Scope:** governs the scheduled SHUTDOWN dates of the S1-* dual-accept
windows shipped in the R1 rename launched 2026-04-23. Each window has a
hard calendar date announced portfolio-wide.

**Governing plan:** `/home/eric/repos/plans/work-orders/work-r1-rename.md`
Phase S6.

**Chain of precedence:**

- S1 shipped dual-accept on 2026-04-23 (env vars, headers, NATS, MCP
  tool names, data-dir path, audit metadata key).
- S2 renamed the Go module, binaries, Docker images, Homebrew tap.
- S3 swept product-name prose and dual-emitted error codes.
- S4 wired portfolio consumers (RelayGate / CloudSwarm / Veritize /
  RelayOne / etc.) for dual-accept.
- **S6 is the scheduled shutdown of those S1 dual-accept surfaces.**
  Each S6-N sub-phase ships as a pre-scaffolded branch that sits
  dormant until its cutover date.

## Summary table (this repo)

| Sub-phase | Date | Branch | Surfaces dropped |
|-----------|------|--------|------------------|
| S6-1 | 2026-05-23 (30d) | `claude/r1-s6-1-headers-drop-stoke` | Legacy `X-Stoke-*` outbound header emission in `internal/correlation` + `internal/provider`. |
| S6-3 | 2026-07-23 (90d) | `claude/r1-s6-3-env-drop-stoke` | Legacy `STOKE_*` env var fallback in `internal/r1rename/env.go`. |
| S6-4 | 2026-07-23 | `claude/r1-s6-4-symlink-drop-stoke` | `stoke` binary install step in `install.sh` + `stoke` Homebrew formula in `.goreleaser.yml`. |
| S6-6 | TBD (>=2w notice) | `claude/r1-s6-6-mcp-v2-stoke` | Legacy `stoke_*` MCP tool registrations in `cmd/stoke-mcp/`. |

S6-2 stoke NDJSON event-type flip is scoped but BLOCKED at-dispatch-time
-- see §S6-2 below for the honest reason.

---

## S6-1 -- Drop X-Stoke-* legacy headers (2026-05-23)

**Surfaces dropped:**

- `internal/correlation/correlation.go`:
  - `ApplyHeaders` legacy branch: `req.Header.Set("X-Stoke-Session-ID", ...)` and
    the two companion AgentID/TaskID legacy setters.
  - Package + function docstrings updated to reflect canonical-only surface.
- `internal/provider/correlation_wire.go`:
  - `applyStokeCorrelationHeaders` legacy branch: same three
    `req.Header.Set("X-Stoke-*", ...)` lines removed.
- Tests:
  - `internal/correlation/correlation_test.go`: `TestApplyHeaders_Full` +
    `TestApplyHeaders_OmitsEmpty` + `TestApplyHeaders_NoIDs_NoHeaders`
    flipped to canonical-only expectations. `TestApplyHeaders_DualSendR1AndStoke`
    replaced with `TestApplyHeaders_S61_NoLegacyStokeHeaders` regression
    guard asserting legacy headers are absent post-cutover.
  - `internal/provider/correlation_wire_test.go`: same transform.

**Pre-cutover checklist (run the week of 2026-05-16):**

- [ ] Confirm RelayGate S6-1 branch (`claude/r1-s6-1-headers-drop-relaygate`)
      has landed OR is ready to land on the same day. Headers-side drop
      must happen atomically (stoke stops emitting, RelayGate stops reading).
- [ ] Spot-check any downstream consumer logs for `X-Stoke-` header
      occurrences; announce cutover date to integrator channel one
      week ahead; absence of "we still need it" replies is the go-signal.
- [ ] Build matrix green for this branch: `go build ./...`,
      `go vet ./...`, `go test -count=1 -short ./...`.

**Cutover commands:**

```bash
# 2026-05-23 cutover -- merge S6-1 stoke + relaygate branches.
cd /home/eric/repos/stoke
git checkout main
git pull --ff-only origin main
git merge --no-ff claude/r1-s6-1-headers-drop-stoke \
  -m "chore(S6-1): drop X-Stoke-* legacy headers (30d window elapsed)"
git push origin main

cd /home/eric/repos/router-core
git checkout main
git pull --ff-only origin main
git merge --no-ff claude/r1-s6-1-headers-drop-relaygate \
  -m "chore(S6-1): drop X-Stoke-* legacy header ingress (30d window elapsed)"
git push origin main

# Roll the services: stoke binary release + relaygate deploy.
# Stoke CLI ships via goreleaser from the tag; RelayGate via existing
# deploy pipeline.
```

**Rollback procedure (if any integrator surfaces a legacy-header-only
client after cutover):**

```bash
cd /home/eric/repos/stoke
git checkout main
git revert --no-ff <S6-1-stoke-merge-sha> -m 1 \
  -m "revert(S6-1): reinstate X-Stoke-* dual-send -- integrator regression"
git push origin main
# Repeat symmetrically in router-core.
```

Negotiate a revised cutover date with the un-migrated integrator;
update this doc.

---

## S6-2 -- BLOCKED at-dispatch-time (stoke NDJSON event-type flip)

**Status:** BLOCKED. The S6-2 work-order item listed "flip the 20
NDJSON event-type strings from `stoke.*` to `r1.*` in
`internal/streamjson/emitter.go`", but per S1-3 (commit `84b9515`,
PR #49) the NDJSON `stoke.*` strings are **not NATS subjects** and
no prior dual-emit canonical-addition window was shipped for them.
The strings feed downstream NATS bridges (RelayOne
`nats-audit-ingest.service.ts` subscribes to `stoke.agent.*` only at
at-dispatch-time; RelayGate / CloudSwarm audit consumers read them as
NDJSON `type` fields). Flipping them on a "30d/60d elapsed" schedule
without a prior dual-emit window would be a breaking change, not a
deprecation closure.

**Resolution path:** requires a new phased work-order (canonical-addition
window -> window elapsed -> legacy drop), not a closure-style S6 branch.
Raised to user for triage.

The RelayGate `stoke_session_id` audit-key drop and the CloudSwarm
`stoke_sessions` / `stoke_events` VIEW drop are each properly preceded
by their S4-2 / S4-1 dual-write windows and ship as separate S6-2
branches in their respective repos.

---

## S6-3 -- Drop STOKE_* legacy env vars (2026-07-23)

**Surfaces dropped:**

- `internal/r1rename/env.go`:
  - `LookupEnv` legacy-fallback branch (lines that delegate to
    `r1env.Get(canonical, legacy)`) removed.
  - `EnvLegacyDropEnv` + `EnvLegacyDropEnabled` feature-flag gate
    removed (canonical-only becomes unconditional).
  - Function doc flipped to canonical-only semantics.
- `internal/r1rename/env_test.go`:
  - `TestLookupEnv_LegacyFallback` removed.
  - `TestLookupEnv_DropFlag*` removed.
  - Canonical-only tests retained + tightened.
- Boot with only legacy env vars fails explicitly via the
  "missing required env" failure at each call site (no silent fallback).

**Pre-cutover checklist (run the week of 2026-07-16):**

- [ ] Confirm S6-3 CloudSwarm branch (`claude/r1-s6-3-env-drop-cloudswarm`)
      is coordinated for the same cutover.
- [ ] Scan any deployment Helm values / compose files / systemd units
      for `STOKE_*` env entries -- any that remain will start returning
      empty-string on cutover. Migrate them to the canonical `R1_*`
      names first.
- [ ] Build matrix green on this branch.

**Cutover commands:**

```bash
# 2026-07-23 cutover.
cd /home/eric/repos/stoke
git checkout main
git pull --ff-only origin main
git merge --no-ff claude/r1-s6-3-env-drop-stoke \
  -m "chore(S6-3): drop STOKE_* legacy env vars (90d window elapsed)"
git push origin main
```

**Rollback:** revert the merge and ship a hotfix release that re-adds
the legacy fallback. Announce in integrator channel.

---

## S6-4 -- Drop stoke binary symlinks + Homebrew/apt stoke package (2026-07-23)

**Surfaces dropped:**

- `install.sh`:
  - `install_one stoke "${BINARY}" required` line removed (prebuilt path).
  - `stoke-bin` + `stoke-acp-bin` go build loops removed from
    `build_from_source` (canonical `r1` build only).
  - `BINARY` default + the stoke-facing info lines refactored to
    advertise `r1` canonical.
- `.goreleaser.yml`:
  - `- id: stoke` build entry removed.
  - `- id: stoke-acp` build entry kept if still required by integrators
    (ACP is a separate protocol; not product-named prose). See note
    in brews block.
  - `- name: stoke` brew formula entry deleted (tap consumers migrate
    to `r1` formula; the retired `stoke` tap is marked `retracted` in
    the `homebrew-stoke` repo via a separate operator commit on
    cutover day -- that step is not part of this branch since it
    lives in a separate repo).
- No `nfpms` / apt surface exists in `.goreleaser.yml` at-dispatch-time,
  so the "apt repo stoke package mark retracted" step is a doc-only
  instruction for whatever apt publisher is wired at that time.

**Pre-cutover checklist (run the week of 2026-07-16):**

- [ ] Confirm brew install counts for `ericmacdougall/stoke/stoke`
      have migrated to `ericmacdougall/stoke/r1` via tap analytics.
- [ ] Post final "stoke binary install retires 2026-07-23" notice
      to the install.sh output + README install section.

**Cutover commands:**

```bash
cd /home/eric/repos/stoke
git checkout main
git pull --ff-only origin main
git merge --no-ff claude/r1-s6-4-symlink-drop-stoke \
  -m "chore(S6-4): drop stoke binary symlinks + retract Homebrew/apt stoke package"
git push origin main
# Tag + release via goreleaser; the stoke brew formula will no
# longer update.
#
# In the homebrew-stoke tap repo, edit the tap's stoke.rb to add
# `deprecate! date: "2026-07-23", because: "renamed to r1"` and push.
```

**Rollback:** revert, re-tag, re-release. Brew tap reinstates stoke.rb.

---

## S6-6 -- MCP tool v2.0.0 (legacy name drop, >=2w external notice)

**Surfaces dropped:**

- `cmd/stoke-mcp/main.go`:
  - `baseTools` renamed to `canonicalTools` and uses `r1_*` names
    directly (not `stoke_*` with a build-time doubling).
  - `buildDualTools` helper removed; `tools` is assigned from the
    canonical list directly.
  - `canonicalToolName` + `legacyToolName` helpers removed.
  - Dispatch switch cases + docstrings updated canonical-only.
- `internal/r1rename/mcp.go`:
  - `MCPLegacyDropEnv`, `MCPLegacyToolPrefix`, `CanonicalToolName`,
    `LegacyToolName`, `MCPLegacyDropEnabled` all removed
    (canonical-only becomes unconditional).
  - `MCPCanonicalToolPrefix` retained as a documented constant.
- Test surface: drop dual-registration tests, add
  `TestMCPServerTools_CanonicalOnly` + `TestMCPServerTools_S66_NoLegacyStokeTools`
  regression guards.

**Pre-cutover (at-discretion, >=2 weeks after integrator notice):**

- [ ] Announce v2.0.0 cutover in the release channel + CHANGELOG.
- [ ] Confirm `r1-mcp` published binary name is available (S2-3)
      or explicitly retain the binary name `stoke-mcp` with canonical
      tool names inside.
- [ ] Build + test green.

**Cutover:** standard `git merge --no-ff`, tag v2.0.0.

**Rollback:** standard revert + release bump.

---

## Status at-dispatch-time (2026-04-24)

All branches scaffolded but NOT pushed and NOT merged. Each branch sits
on top of `main` (S1/S2/S3/S4 merged) and carries exactly the diff
listed in the §Surfaces-dropped bullets above plus its regression-guard
test.
