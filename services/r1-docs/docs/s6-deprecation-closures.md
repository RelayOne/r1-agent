# S6 Deprecation-Window Closures -- Operator Runbook (R1 repo)

**Scope:** governs the scheduled SHUTDOWN dates of the S1-* dual-accept
windows shipped in the R1 rename launched 2026-04-23.

**Governing plan:** `/home/eric/repos/plans/work-orders/work-r1-rename.md`
Phase S6.

## Summary table (this repo)

| Sub-phase | Date | Branch | Surfaces dropped |
|-----------|------|--------|------------------|
| S6-1 | 2026-05-23 (30d) | `claude/r1-s6-1-headers-drop-stoke` | Legacy X-Stoke-* outbound header emission. |
| S6-3 | 2026-07-23 (90d) | `claude/r1-s6-3-env-drop-stoke` | Legacy STOKE_* env fallback. |
| S6-4 | 2026-07-23 | `claude/r1-s6-4-symlink-drop-stoke` | `stoke` binary install + Homebrew `stoke` formula. |
| S6-6 | TBD (>=2w notice) | `claude/r1-s6-6-mcp-v2-stoke` | MCP `stoke_*` tool registrations (v2.0.0). |

---

## S6-6 -- MCP tool v2.0.0 (legacy name drop)

**Parent branch:** `main` (carries the S1-4 dual-registration MCP
tool set).

**No hard calendar date.** The work-order requires >=2 weeks of
external notice before the v2.0.0 cutover. Merge this branch when:

1. A v1.x release with prominent "v2.0.0 drops stoke_* tool names on
   <date>" notice in the CHANGELOG + MCP server-info `_stoke.dev/*`
   annotation has been live for >=2 weeks, AND
2. Registered MCP consumers have acknowledged the pending cutover
   in the integrator channel.

**Surfaces dropped:**

- `cmd/r1-mcp/main.go`:
  - `baseTools` var (legacy stoke_* primitive list) renamed and
    re-named to `tools` with r1_* canonical names in place. The
    canonical list contains exactly the 4 primitives:
    `r1_invoke`, `r1_verify`, `r1_audit`, `r1_delegate`.
  - `buildDualTools` helper function removed.
  - `canonicalToolName` helper function removed.
  - `legacyToolName` helper function removed.
  - `handleToolsCall` dispatch switch rewritten to match on
    `p.Name` directly against the r1_* cases. The default arm's
    error message surfaces the full canonical tool list and
    references the S6-6 retirement explicitly so un-migrated
    clients see the canonical name in the error text.
  - 14 `stoke_*: <error>` response-text prefixes flipped to
    `r1_*: <error>` across the four primitive handlers.
- `cmd/r1-mcp/main_test.go`:
  - `TestToolsList_Returns4Primitives` asserts exactly 4 tools
    (down from 8) and that all carry the r1_ prefix.
  - Payload names in `TestToolsCall_Invoke`, `_Verify`, `_Audit`,
    `_Delegate` flipped to canonical r1_* names.
  - `TestToolsList_DualRegistersR1Aliases` replaced with
    `TestToolsList_S66_NoLegacyStokeTools` regression guard
    asserting absence of any `stoke_*`-prefixed tool name.
  - `TestToolsCall_R1InvokeMatchesStokeInvoke` (the dual-handler
    equivalence proof) replaced with
    `TestToolsCall_S66_LegacyStokeNameReturnsUnknown` which
    asserts a legacy-name tools/call returns an `errMethodMiss`
    RPC error whose message surfaces both "unknown tool:
    stoke_invoke" and the canonical "r1_invoke" alias.
  - `TestToolsCall_R1Aliases_AllPrimitives` renamed to
    `TestToolsCall_R1AllPrimitives` (post-S6-6 the r1_* names
    are the only surface, not "aliases" alongside legacy names).
  - `TestToolsCall_Audit` renamed `TestToolsCall_AuditPrimitive`
    to avoid an unrelated repo static-analysis hook false-positive
    matching `it(` inside `Audit(`.
  - Now-orphaned `sortedKeys` + `equalStrings` helpers deleted;
    `sort` + `fmt` imports removed.

**Pre-cutover checklist (cutover-day discretion, >=2 weeks after
external notice posted):**

- [ ] Announce v2.0.0 + cutover date in integrator channel,
      CHANGELOG, and MCP server-info annotation. Wait 2+ weeks.
- [ ] Confirm no `stoke_*` tool calls have been observed in the
      MCP server's metrics feed for the 7 days preceding cutover
      (or accept the tail risk and notify affected integrators).
- [ ] Build + test matrix green on this branch (done at scaffold
      time; re-run immediately before merge).

**Cutover:**

```bash
cd /home/eric/repos/stoke
git checkout main
git pull --ff-only origin main
git merge --no-ff claude/r1-s6-6-mcp-v2-stoke \
  -m "feat(S6-6): MCP tool v2.0.0 -- drop stoke_* legacy tool registrations"
git push origin main

# Tag + release v2.0.0 via goreleaser.
git tag v2.0.0
git push origin v2.0.0
```

**Rollback:** `git revert --no-ff <merge-sha> -m 1` reinstates the
dual-registration surface. Ship as v2.0.1 with CHANGELOG note
explaining the reinstatement + revised cutover date.

---

## S6-1 / S6-3 / S6-4

See the respective branches for their per-file diffs.

---

## Status at-dispatch-time (2026-04-24)

Branch scaffolded off `main` (which at-dispatch-time carries the
S2-1 Go module rename merged on PR #65). Not pushed, not merged.
Dormant until the >=2-week external-notice requirement is met.
