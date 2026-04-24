# S6 Deprecation-Window Closures -- Operator Runbook (R1 repo)

**Scope:** governs the scheduled SHUTDOWN dates of the S1-* dual-accept
windows shipped in the R1 rename launched 2026-04-23.

**Governing plan:** `/home/eric/repos/plans/work-orders/work-r1-rename.md`
Phase S6.

## Summary table (this repo)

| Sub-phase | Date | Branch | Surfaces dropped |
|-----------|------|--------|------------------|
| S6-1 | 2026-05-23 (30d) | `claude/r1-s6-1-headers-drop-stoke` | Legacy X-Stoke-* outbound header emission. |
| S6-3 | 2026-07-23 (90d) | `claude/r1-s6-3-env-drop-stoke` | Legacy STOKE_* env fallback in `internal/r1env` + `internal/r1rename/env.go`. |
| S6-4 | 2026-07-23 | `claude/r1-s6-4-symlink-drop-stoke` | stoke binary install + Homebrew formula. |
| S6-6 | TBD | `claude/r1-s6-6-mcp-v2-stoke` | MCP stoke_* tool registrations (v2.0.0). |

---

## S6-3 -- Drop STOKE_* legacy env vars (2026-07-23)

**Parent branch:** `main` (carries the r1env + r1rename packages).

**Surfaces dropped:**

- `internal/r1env/r1env.go`:
  - `Get(canonical, legacy)` legacy-fallback branch removed. The
    function now returns `os.Getenv(canonical)` unconditionally.
    The `legacy` parameter is retained for call-site compatibility
    but ignored.
  - `logLegacyUsedOnce` helper + `warnOnceMu` + `warnOnce` map
    removed (no longer any legacy read to warn on).
  - `ResetWarnOnceForTests` retained as a no-op shim so existing
    test helpers that call it keep compiling.
  - Package doc flipped to canonical-only semantics.
- `internal/r1rename/env.go`:
  - `LookupEnv` legacy-fallback branch removed (same treatment).
  - `EnvLegacyDropEnv` / `EnvLegacyDropEnabled` feature-flag
    surface removed (canonical-only is now unconditional).
  - Package doc updated.
- `internal/r1rename/doc.go`:
  - Env-var bullet flipped from "reads R1_* first, falls back to
    the legacy STOKE_*" to "reads R1_* only".
- Test surface:
  - `internal/r1env/r1env_test.go`: dual-accept/WARN-once tests
    replaced with canonical-only tests + S6-3 regression guard
    `TestGet_S63_LegacyIgnored` asserting legacy-only env returns
    "".
  - `internal/r1rename/env_test.go`: same transform +
    `TestLookupEnv_S63_LegacyIgnored` + `TestLookupEnv_S63_CanonicalWinsOverLegacy`.
- Test-only STOKE_* `t.Setenv` / `os.Setenv` sites migrated to
  canonical R1_* across 10 test files
  (`cmd/stoke-server/main_test.go`, `cmd/stoke/run_cmd_test.go`,
  `cmd/stoke/agent_serve_cmd_test.go`,
  `cmd/stoke/ctl_bootstrap_test.go`,
  `internal/cloud/client_test.go`,
  `internal/deploy/cloudflare/cloudflare_test.go`,
  `internal/engine/policy_gate_test.go`,
  `internal/plan/content_judge_mcp_test.go`,
  `internal/plan/declared_symbols_harness_test.go`,
  `internal/plan/declared_symbols_treesitter_test.go`,
  `internal/provider/pool_test.go`,
  `internal/runtrack/runtrack_test.go`,
  `internal/skill/registry_env_test.go`). Production-code error
  messages that still mention `STOKE_PROVIDERS parse` are
  preserved as-is; those are prose-sweep surfaces, not env-read
  surfaces, and are untouched by this S6-3 branch.

**Pre-cutover checklist (run the week of 2026-07-16):**

- [ ] Coordinate with CloudSwarm S6-3 branch
      (`claude/r1-s6-3-env-drop-cloudswarm`) so both sides cut on
      the same day.
- [ ] Scan all deployment Helm values / systemd unit files / docker
      compose files for `STOKE_*` env entries. Any that remain
      will read as empty on cutover -- migrate to `R1_*` first.
- [ ] Build + test matrix green:
      `go build ./...`, `go build -tags cloud`, `go build -tags local`,
      `go build -tags desktop`, `go test -count=1 -short ./...`.

**Cutover:**

```bash
cd /home/eric/repos/stoke
git checkout main
git pull --ff-only origin main
git merge --no-ff claude/r1-s6-3-env-drop-stoke \
  -m "chore(S6-3): drop STOKE_* legacy env vars (90d window elapsed)"
git push origin main
# Standard goreleaser release flow.
```

**Rollback:** `git revert --no-ff <merge-sha> -m 1` reinstates the
legacy-fallback branch (one revert commit, no operator steps
required beyond redeploy).

---

## S6-1 / S6-4 / S6-6

See the respective branches for their per-file diffs.

---

## Status at-dispatch-time (2026-04-24)

Branches scaffolded off `main`. None pushed, none merged.
