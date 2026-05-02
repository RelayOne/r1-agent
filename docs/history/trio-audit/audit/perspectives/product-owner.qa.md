# Product Owner QA Audit — Trio

**Date:** 2026-04-01
**Auditor:** Product Owner perspective
**Scope:** ember/devbox, flare, stoke — claims vs. delivered, broken user flows, UX gaps

---

## Executive Summary

All three repos ship real, working code. The core happy paths are delivered. Findings below are areas where documented claims outrun implementation, feature flags are undocumented, or UX breaks without explanation.

---

## Stoke

### README / Architecture Drift

- [ ] **MEDIUM** [stoke/README.md:123 vs stoke/cmd/r1/main.go:327] **README says "9 commands" but 16 are implemented** — README command table lists only `run, build, plan, scan, audit, status, pool, doctor, version`. The binary actually ships `yolo, scope, repair, ship, add-claude, add-codex, pools, remove-pool` as callable subcommands, plus an interactive REPL on zero-args. None of these are documented in the README. Users who discover `stoke --help` will see commands that have no entry in the public docs. — fix: Add all 16 commands to the README command table with a one-line description each. — effort: trivial

- [ ] **MEDIUM** [stoke/README.md:146] **README says "19 packages" — actual count is 26** — The architecture section and CLAUDE.md both claim "19 packages. 6,500+ lines source. 3,400+ lines tests. 182 test functions. 60 Go files." Actual internal package count: 26 (`compute`, `managed`, `pools`, `prompts`, `repl`, `remote`, `taskstate` are present but not listed in README architecture table). Actual source is ~11,800 lines, tests ~4,300 lines, test functions ~219. — fix: Update README architecture section with accurate counts and add the missing 7 packages to the package map. — effort: trivial

- [ ] **MEDIUM** [stoke/README.md:160-162] **Install script references a non-existent GitHub org** — `curl -fsSL https://stoke.dev/install | bash` clones `https://github.com/good-ventures/stoke.git` but the module is `github.com/ericmacdougall/stoke`. The URL will 404. There are no release binaries. — fix: Update install.sh to clone from the correct repo URL, or remove the `curl` install option from the README until the domain and org are set up. — effort: small

- [ ] **LOW** [stoke/README.md:140] **TUI `Focus/Dashboard/Detail` modes are real but undescribed** — README mentions "Bubble Tea interactive (Focus/Dashboard/Detail)" but gives users no information about how to switch modes or what each does. The `--interactive` flag is documented only in the build flags table with no description of what the TUI shows or how to navigate it. — fix: Add a `## Interactive TUI` section to the README describing keyboard navigation and the three modes. — effort: small

### Missing User-Facing Errors / UX Gaps

- [ ] **HIGH** [stoke/cmd/r1/main.go:327-378] **`stoke` with no args launches an undocumented interactive REPL** — The README Quick Start says `go build ./cmd/r1` then `stoke run --task ...`. A user who runs just `stoke` gets an interactive REPL (`⚡ STOKE` prompt with slash commands). This REPL is not described anywhere in the README or docs. The slash commands it exposes (`/ship`, `/scope`, `/repair`, `/yolo`, `/add-claude`, etc.) are entirely undocumented. — fix: Document the REPL in README or add a `stoke help` output that covers both CLI mode and REPL mode. — effort: small

- [ ] **MEDIUM** [stoke/cmd/r1/main.go:1633-1719 `shipCmd`] **`stoke ship` is undocumented but is a core convergence loop** — `ship` is arguably the highest-value command (plan → build → review → fix loop) but does not appear in the README commands table. Users running `stoke --help` will see it listed but find no documentation. — fix: Add `stoke ship` to the README commands table with a description of the convergence loop. — effort: trivial

- [ ] **MEDIUM** [stoke/internal/compute/ember.go:59] **`EmberBackend.Spawn` calls `/v1/workers` but ember's workers route requires `ENABLE_V1_WORKERS=true`** — Stoke's compute layer spawns burst workers by calling `POST {endpoint}/v1/workers`. This endpoint returns 501 unless `ENABLE_V1_WORKERS=true` in ember. If a user configures `EMBER_API_KEY` + `EMBER_API_URL` in stoke but the ember instance hasn't enabled the flag, the error message is `"Workers API not enabled. Set ENABLE_V1_WORKERS=true."` — but stoke surfaces this as `spawn worker: HTTP 501: ...`. Users will not know to look at the ember environment. — fix: In `EmberBackend.Spawn`, detect HTTP 501 and return a user-friendly error: "Ember worker API is disabled on this server. Set ENABLE_V1_WORKERS=true in the ember deployment." — effort: trivial

---

## Ember (devbox)

### Undocumented Features / Configuration Gaps

- [ ] **HIGH** [ember/devbox/README.md (env vars table)] **Workers API and Managed AI feature flags are not documented** — Ember ships two opt-in APIs: the burst workers API (`ENABLE_V1_WORKERS`) and the managed AI proxy (`ENABLE_MANAGED_AI`). The README's environment variables table has 23 entries but neither `ENABLE_V1_WORKERS`, `ENABLE_MANAGED_AI`, `OPENROUTER_API_KEY`, `AI_MARKUP_PERCENT`, nor `AI_MONTHLY_CAP_USD` appears anywhere in the README. An operator deploying ember for stoke integration has no way to know these flags exist or what they control. — fix: Add `ENABLE_V1_WORKERS`, `ENABLE_MANAGED_AI`, `OPENROUTER_API_KEY`, `AI_MARKUP_PERCENT`, and `AI_MONTHLY_CAP_USD` to the environment variables table in the README. — effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/routes/ai.ts:24, ember/devbox/README.md] **Managed AI endpoint is implemented but completely undocumented** — `POST /v1/ai/chat` is a working OpenRouter proxy with metering and a monthly spend cap. It is not mentioned in the README, has no API documentation, and has no description of the $50/month default cap or the markup behavior. A Stoke user trying to use the managed AI fallback (`managed/proxy.go`) has no documentation to refer to. — fix: Add a "Managed AI API" section to the README documenting the `/v1/ai/chat` endpoint, auth (Ember API key), model routing, spend cap, and markup. — effort: small

- [ ] **MEDIUM** [ember/devbox/src/routes/workers.ts:56] **Worker count cap (MAX_WORKERS=20) is not documented** — The workers API enforces a hard cap of 20 concurrent burst workers. This is a user-visible limit that returns HTTP 429 with `"Worker limit reached (20)"` but is not mentioned in any documentation. An operator cannot configure this limit. — fix: Document the 20-worker cap in the README workers section and consider making it configurable via env var. — effort: small

- [ ] **LOW** [ember/devbox/src/routes/machines.ts:393-425] **File upload endpoint (`POST /api/machines/:id/upload`) is not documented** — A 100MB upload endpoint exists but appears nowhere in the README. Users and integrators have no way to discover this feature. — fix: Add `POST /api/machines/:id/upload` to the README's API documentation or add a "Machine File Upload" section. — effort: trivial

### Broken/Missing UX

- [ ] **MEDIUM** [ember/devbox/src/routes/billing.ts:30 `ACCESS_STATUSES`] **User with `trialing` subscription can create machines but start entitlement check only passes `active` subscriptions** — `ACCESS_STATUSES = new Set(["active", "trialing"])` is used for machine start entitlement. However the slot query in `startMachine` does: `slot.sub_status IS NULL OR sub.status IN ('active', 'trialing')`. This is consistent. But if a trialing user's sub transitions to `past_due`, existing running machines can be stopped by the stop-reconciler with no user-visible explanation beyond "Subscription slot is no longer active" on the next terminal call. There is no proactive notification to users before their machines are stopped. — fix: When reconciler stops machines due to subscription changes, surface a reason in the machine state that the dashboard can display. Consider an email notification on subscription state change. — effort: medium

---

## Flare

### Claims vs. Delivered

- [ ] **LOW** [flare/README.md:163-165] **README says "Health loop marks hosts dead after 90s" but reconciler config is 45s** — The README states "Health loop marks hosts dead after 90s without heartbeat." The actual reconciler is started with `HostTimeout: 45 * time.Second` in `cmd/control-plane/main.go:143`. The documented 90s timeout does not match the 45s implementation. — fix: Update the README to say 45s, or update the reconciler config to match the documented 90s. — effort: trivial

- [ ] **LOW** [flare/README.md:163-165] **"Machines on dead hosts should be marked lost by reconciliation loop" — this IS implemented** — The README hedges with "should be marked" as if it's aspirational, but `reconciler.go:80-84` and `store.go:499-518` fully implement `MarkMachinesLostOnDeadHosts`. The documentation undersells a delivered capability. — fix: Change "should be marked" to "are marked" in the README. — effort: trivial

- [ ] **MEDIUM** [flare/README.md] **No documented auth for the Cloudflare tunnel** — The README describes `Cloudflare Tunnel (TLS)` as the ingress path but provides no documentation on how to configure or secure the Cloudflare tunnel connection. An operator following the README to deploy flare has no guidance on the Cloudflare side. — fix: Add a "Cloudflare Tunnel setup" section to the README or docs with minimal configuration steps. — effort: small

---

## Cross-Repo Integration

- [ ] **HIGH** [stoke/README.md, ember/devbox/README.md, flare/README.md] **The trio integration story is entirely undocumented** — Stoke has a `compute/ember.go` that spawns workers via the Ember API. Ember has a `workers.ts` that provisions Fly machines. Ember has a `managed/proxy.go` for AI routing. None of the three repos' READMEs mention each other or describe the integrated architecture. A user who wants to run stoke with ember-hosted burst workers has no documentation explaining: (1) what env vars to set in stoke (`EMBER_API_KEY`, `EMBER_API_URL`), (2) that ember must have `ENABLE_V1_WORKERS=true`, (3) that the workers route provisions Fly machines via the same ember fly.ts, (4) how stoke's `managed/proxy.go` maps to ember's `/v1/ai/chat`. — fix: Add a `## Integration: Stoke + Ember + Flare` section to the root `README.md` (currently a blank template) describing the integrated deployment model and required configuration. — effort: medium

- [ ] **HIGH** [/home/eric/repos/trio/README.md] **Root README is an unfilled template** — The root `README.md` is entirely placeholder text (`[Project Name]`, `<!-- 2-3 sentence elevator pitch -->`, etc.). This is the document that GitHub shows first. Any user or investor landing on the repo sees a template. — fix: Fill the root README with the actual product description, architecture overview, and quick links to the three sub-repos. — effort: small

- [ ] **MEDIUM** [docs/ARCHITECTURE.md, docs/HOW-IT-WORKS.md, docs/FEATURE-MAP.md] **All trio-level docs are empty templates** — `docs/ARCHITECTURE.md`, `docs/HOW-IT-WORKS.md`, and `docs/FEATURE-MAP.md` contain only template comment blocks. Per the project CLAUDE.md, these docs "must be updated at every phase transition." They have never been written. — fix: Write actual content for all three documents describing the integrated trio system. — effort: large

---

## Summary by Priority

| Priority | Count |
|----------|-------|
| CRITICAL | 0 |
| HIGH     | 4 |
| MEDIUM   | 9 |
| LOW      | 4 |

No functionality is completely broken. The three repos deliver working systems. The gaps are: undocumented commands and configuration, a broken install URL, mismatched host timeout documentation, and a completely unfilled integration story at the trio level.
