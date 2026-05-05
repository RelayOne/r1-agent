# Scope audit — operator's "finish all + scope" checklist

Honest status against each item. Items marked **DONE** require no operator action. Items marked **OPERATOR** are blocked on action only you can take. Items marked **DESIGN** require design decisions before any work; effort estimates are conservative.

---

## ✅ Item: Branch survey across last 14 days, all unique work merged into dev or this PR

**Status: PARTIAL** — survey done; non-superseded branches need decisions.

Surveyed 25 branches with activity in last 14 days. The 9 spec-related branches are merged (specs 6/7/8/9 + earlier). The remaining 15 branches with unique commits group into:

| Branch | Unique commits | What it adds | Action |
|---|---|---|---|
| `claude/r1-maxturns-worktree-cleanup-2026-05-01` | 5 | tier 2/5 daemon executors + worktree maxturns + canonical-doc refresh | merge into PR (no conflicts expected; superset of `codex/w248-udrel-wire-http-routes` + `feat/tier5-daemon-executors`) |
| `codex/w248-udrel-wire-http-routes` | 5 | subset of above | cap (history preserved by the merged branch) |
| `feat/tier5-daemon-executors` | 2 | subset of above | cap |
| `feat/r1-tier2-2026-04-30` | 2 | tier 2 retry + CodeRadar startup hooks | merge into PR |
| `feat/r1-tier3-2026-04-30` | 2 | tier 3 enhancement validators | merge into PR |
| `feat/r1-trust-layer` | 2 | beacon hub trust layer | merge into PR |
| `feat/r1-beacon-protocol` | 2 | beacon protocol foundation | merge into PR |
| `feat/r1-agent-interaction-mode-2026-04-30` | 2 | daemon agent interaction mode | merge into PR |
| `codex/w249-chat-rules` | 3 | TUI /rules subcommands | merge into PR |
| `sweep/actium-studio-skill-pack-20260429` | 5 | studioclient pack (HTTP/MCP transport, integration tests) | merge into PR |
| `feat/r1-skill-deterministic-next` | 1 | recursive pack install | merge into PR |
| `feat/r1-parity-wave-{a,b,c,d}` | 4 × 1 | wave a/b/c/d primitives (artifact, plan-ledger, receipts, wizard, registry) | merge — small surface |
| `feat/skill-wizard` | 1 | skill-wizard operator on-ramp | merge |
| `claude/r1-s6-{1,3,4}-drop-stoke` | 3 × 1 | stoke removal sweeps (headers/env/symlinks) | superseded by spec 6 stoke cleanup; cap |
| `sweep/s6-6-mcp-v2-stoke-20260429` | 1 | MCP v2.0.0 stoke registration drop | superseded; cap |

**Recommended action:** merge the 11 "merge into PR" branches into a new integration branch `claude/w522-tier-and-beacon-integration-2026-05-04`, resolve conflicts, run tests, then merge that into the current PR. **Effort: ~1-2 hours.**

For the "cap" branches: tag each tip as `archive/<branch-name>` and delete the branch ref. History preserved, branch list cleaned. **Effort: 10 min.**

---

## ✅ Item: Proper environmental splits (dev/staging/main) with branch protection

**Status: NOT-STARTED** for r1-agent; sister repos already conform.

`r1-agent` only has `main` on origin. Sister repos (coderadar-admin, relaygate-admin, wellytic-admin, coder1, etc.) all have `dev / staging / main`.

**Action plan (~30 min):**
1. After this PR merges to `main`, create `staging` and `dev` from the merge commit.
2. Apply branch protection to all three:
   - Required PR reviews (1+)
   - Required status checks (`r1-agent-pr` + future `r1-services-pr`)
   - No force-pushes
   - No direct commits to `main`/`staging`
   - `dev` allows direct commits for "minor one-off bug fixes" per the standing rules.

**Operator action required:** Branch protection settings need org-admin permission via `gh api repos/.../branches/.../protection`. Can be scripted; need confirmation before applying.

---

## 🟡 Item: Cloud Build triggers + SQL + hosting + secrets + DNS for all 3 envs

**Status: PARTIAL** — code/infra ready, DNS pending.

| Component | dev | staging | prod |
|---|---|---|---|
| Cloud SQL | ✅ r1-dev-pg | ✅ r1-staging-pg | ✅ r1-prod-pg |
| Cloud Run services | ✅ 3 services | ✅ 3 services | ✅ 3 services |
| Secret Manager skeleton | ✅ 2 secrets | ✅ 2 secrets | ✅ 2 secrets |
| Domain mapping created | ✅ 3 domains | ✅ 3 domains | ✅ 3 domains |
| Cloudflare CNAMEs | ❌ NXDOMAIN | ❌ NXDOMAIN | ❌ NXDOMAIN |
| TLS cert provisioned | ❌ blocked on DNS | ❌ blocked on DNS | ❌ blocked on DNS |
| Cloud Build deploy trigger | ❌ yaml ready, trigger not created | ❌ | ❌ |

**OPERATOR ACTION:**
1. Add 9 CNAME records to Cloudflare `r1.run` zone (list in `plans/HANDOFF-deploy-state.md`). Proxy must be **OFF** (gray cloud).
2. Set real values for the 6 secret placeholders.
3. Create 3 Cloud Build triggers (one per env) using `services/cloudbuild-deploy.yaml`.

I can write a `gcloud builds triggers create ...` script for #3.

---

## 🔴 Item: System supports own JWT login + RelayOne MSP SSO

**Status: NOT-STARTED.** Major scope.

`@relayone/auth-core` (Node/TS package) already provides `JwtService`, `RelayOneSsoClient`, `PasswordAuth`, `MagicLinkAuth`, OIDC clients. Operator's "JWT login + RelayOne MSP SSO" should consume this, not reimplement.

But my SaaS services are Go, not Node. Two paths:

**Path A — port to Go** (~3-5 days)
- Reimplement JwtService + RelayOneSsoClient in Go
- Drift risk: two impls of the same auth contract
- Faster runtime (no extra hop)

**Path B — auth-frontend Node service** (~1-2 days)
- New `services/r1-auth/` — Node/TS, consumes `@relayone/auth-core`
- Issues JWTs that Go services verify with the public RS256 key
- Single source of truth for auth logic
- Adds one Cloud Run service per env (3 more services)

**DECISION NEEDED:** A or B? Both require new services scaffolded + tested + deployed.

---

## 🔴 Item: Admin panel for operators (user / revenue / usage / control)

**Status: NOT-STARTED.** Multi-day scope.

Pattern from sister repos: each product has `<product>-admin` (e.g., `coderadar-admin`, `relaygate-admin`). These are Next.js admin apps with shared shadcn components.

**Path forward (~3-5 days):**
1. Create new repo `RelayOne/r1-admin` from the standard admin template.
2. Routes: `/users`, `/sessions`, `/revenue`, `/usage`, `/license-keys`, `/lanes-overview`.
3. Auth via `@relayone/auth-core` RelayOneSsoClient (operator-only).
4. Cloud Run service `r1-admin-{prod,staging,dev}` at `admin.r1.run` / `admin.{staging,dev}.r1.run`.

**DECISION NEEDED:** Do we use one of the existing `*-admin` templates as the base, or scaffold from scratch?

---

## 🔴 Item: User behavior tracking, conversion, falloff, marketing/retention/support hooks

**Status: NOT-STARTED.** Multi-week scope if "all major tooling".

Concrete vendors per category (each is hours-per-integration):

| Category | Common vendor | Effort |
|---|---|---|
| Product analytics | PostHog / Segment / Mixpanel | 4-8 hours per |
| Conversion + funnel | PostHog / Amplitude | 4-8 hours per |
| Retention / lifecycle email | Customer.io / Loops | 8-16 hours per |
| Support | Intercom / HelpScout / Zendesk | 8-16 hours per |
| Affiliate / referral | PartnerStack / Rewardful | 16-24 hours per |
| Attribution | Dreamdata / June | 8-16 hours per |
| SEO / CRO | Ahrefs / Hotjar / FullStory | 4-8 hours per |
| Error tracking (already in scope) | CodeRadar (in-house) | already designed |

**DECISION NEEDED:** Pick a vendor per category — without a choice each, "integrate with all major tooling" is a 6-8 week project. Recommend starting with PostHog + Customer.io + CodeRadar (already in-house) as v1.

---

## 🟡 Item: CodeRadar dogfood — events streaming

**Status: PARTIAL.** `internal/coderadar/` package exists in the repo (verified by the test fix earlier). DSN parser, `parseDSN()`, captureError. Tier-2 startup hooks live on `feat/r1-tier2-2026-04-30` (NOT in our PR yet — see branch survey above).

**Action plan (~2 hours):**
1. Merge `feat/r1-tier2-2026-04-30` into the integration branch (per the branch survey).
2. Add CodeRadar DSN secret to all 3 envs:
   - `r1-{prod,staging,dev}-shared-CODERADAR_DSN`
3. Wire each new SaaS service (r1-coord-api, r1-docs, r1-downloads-cdn) to call the CodeRadar SDK at boot + on panic.
4. Smoke-test by sending a synthetic error from each service's `/v1/test/coderadar` endpoint (dev only).

**DECISION NEEDED:** Which CodeRadar DSN to use per env (presumably one already exists for r1's existing usage)?

---

## ✅ Item: Branch hygiene — no messy unmerged branches; archive what's stale

**Status: PARTIAL.** Survey done above. Plan:

For each "cap" branch in the survey:
```bash
git tag archive/<branch-name> origin/<branch-name>
git push origin archive/<branch-name>
git push origin --delete <branch-name>
```

History preserved as a tag; branch list cleaned. Can run as one script after operator approves the cap list.

---

## 🔴 Item: Docs 100% accurate and up to date

**Status: NEEDS-AUDIT.** Hours-of-work scope.

Docs at risk of staleness after the 4-spec merge:
- `docs/README.md` (root) — references the old "scoped not yet built" status
- `docs/ARCHITECTURE.md` — needs sections for cortex/lanes/web/desktop/agentic
- `docs/HOW-IT-WORKS.md` — same
- `docs/FEATURE-MAP.md` — partial updates from spec merges
- `docs/DEPLOYMENT.md` — does NOT yet describe the r1.run SaaS deploy
- `docs/BUSINESS-VALUE.md` — pre-cortex framing
- `docs/AGENTIC-API.md` — partial updates

Spec 6/7/8/9 each touched their sections; combined coherence pass needed.

**Action plan (~3-4 hours):** Run `/update-docs` slash command (it's in the available skills list) to do the holistic refresh from the actual code state.

---

## 🟡 Item: Test coverage full and up to date; tests pass

**Status: PARTIAL.**

- ✅ `go test ./... -count=1` — all green now (after the 2 pre-existing fixes)
- ✅ `go vet ./...` — clean
- ✅ web/ `tsc --noEmit` — clean
- ✅ desktop/ `cargo test` — 110 pass / 0 fail (per spec 7 agent verification)
- ❌ live testing — blocked on DNS (the new SaaS services have only been smoke-tested via *.run.app URLs)
- ❌ "test coverage full" — depends on definition. Vitest coverage threshold is 80% per spec 6 item 5; never been measured against the merged tree because Storybook+Vitest aren't fully wired in CI yet.

**Action plan (~1-2 hours):**
1. Run vitest coverage for web/ once node_modules is fresh; surface the actual %.
2. Run Playwright e2e suite against the live (or local) services.
3. Add CI step to enforce coverage threshold on PR.

---

## 🔴 Item: Marketing site integration — affiliate, SEO, CRO, attribution, retention

**Status: NOT-STARTED.** Multi-week scope.

The org has `RelayOne/relayone-web-landing` (and `truecom-web-landing`, `Actium-web-landing`, etc.) — that's the marketing site pattern. There's no `r1-web-landing` repo yet — it'd need to be created OR `r1-docs` (the platform.r1.run service we just built) could absorb the marketing-page role.

Concrete deliverables this would imply:
- `r1-landing` site (marketing copy + hero + signup)
- Affiliate signup flow + tracking script (PartnerStack / Rewardful)
- SEO: meta tags, OG cards, sitemap, robots.txt, schema.org
- CRO: experiment framework (PostHog A/B / GrowthBook)
- Attribution: UTM capture + first-touch / last-touch model into CodeRadar or PostHog
- Drop-off: funnel events on signup → license-verify → first-session
- Retention: Customer.io campaigns hooked off the funnel events

**DECISION NEEDED:** Build a new `r1-landing` repo or absorb into `r1-docs`? With which vendors? The honest estimate for "all of the above" is **3-4 weeks of focused engineering**, not a one-session task.

---

# What I can do in the next ~2 hours autonomously

Without further operator decisions:

1. ✅ Cap the stale branches via archive tags (10 min)
2. ✅ Create integration branch + merge the 11 "merge into PR" branches; resolve conflicts (1-2 hr)
3. ✅ Run full test suite on the integration result (15 min)
4. ✅ Update docs holistically via /update-docs (1 hr)
5. ✅ Write the `gcloud builds triggers create` script for the 3 deploy triggers
6. ✅ Write the dev/staging branch creation + protection script (operator runs after merge)

Pushing past that needs your decisions on:
- **A or B for auth?** (Node service in front of Go vs. Go reimplement)
- **r1-admin scaffold base?** (clone existing admin template vs. fresh)
- **Vendor picks** for analytics / retention / support / affiliate
- **r1-landing strategy** (new repo vs. absorb into r1-docs)
- **CodeRadar DSN** per env

If you want, I'll start running items 1-6 above in order while you triage the rest.
