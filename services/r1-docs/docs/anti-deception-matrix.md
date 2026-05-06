# Anti-deception enforcement matrix

**Audit deliverable per S-U-012.** Every SOW-level phase transition,
the evidence it claims to require, and whether that evidence is
actually enforced by code today. Gaps are flagged so a follow-up
commit can close them.

## Executing-model context boundary

The architectural invariant the matrix below exists to protect:

> No commit, attestation, or acceptance signoff may be produced by
> the same agent context that wrote the code under review.

The existing cross-model reviewer covers half of this (Claude writes,
Codex reviews). The missing half — cryptographic attribution of
commits — is what `internal/stancesign/` now provides. A commit
signed by the `reviewer` stance key cannot have been authored by a
worker stance even if a supply-chain attacker compromised the worker
model's context, because the signing key never enters the worker's
execution environment.

## Matrix

| Transition | Required evidence | Enforcement status | Notes |
|---|---|---|---|
| **Task dispatch → task complete** | Worker declares `tr.Success=true` AND reviewer verdict `Complete=true` | ✅ `reviewAndFollowupRecursive` ([sow_native.go:3127](../cmd/r1/sow_native.go)); LLM reviewer required | Since `866fe57` the LLM verdict is cross-checked by the zombie classifier (3-state) and content-faithfulness judge on already-done cases. |
| **Zombie override** | Task declared ≥1 file AND 0 writes attributed this dispatch AND ≥1 declared file missing/empty | ✅ `classifyZombie` deterministic check cannot be overridden by the LLM | No LLM can talk its way past the stat-based file check. |
| **Declared-files-present but LLM verdict=Complete** | Deterministic stub scan + LLM content-faithfulness judge agree it's real | ✅ `taskOutputsLookComplete` + `JudgeDeclaredContent` both consulted | Fake content must pass both a regex stub scan and a second-opinion LLM. |
| **Decomposer abandon** | Decomposer returns `Abandon=true` with reason | ✅ `⛔ BLOCKED` marker surfaced; no silent accept | Changed in `d1872a2`: was `⏹` (neutral); now `⛔ BLOCKED, not complete`. |
| **Session AC pass** | `CheckAcceptanceCriteria` mechanical command exits 0 OR semantic judge overrides with reasoning | ✅ `acceptance.go` + `ac_judge.go` | Semantic override requires a stored reasoning string per criterion. |
| **Session advance (continueOnFailure)** | Upstream session success OR explicit ContinueOnFailure=true | ✅ `runParallel` + legacy `Run` both enforce | Dependent sessions under a failed upstream are marked BLOCKED, not silently run. |
| **Integration review verdict** | JSON-structured `IntegrationReport` OR synthetic `reviewer-noncompliant` gap | ✅ Prior non-JSON verdicts silently passed as "clean"; now forced to surface a synthetic gap | Fixed in the same commit as the env-var classifier work. |
| **Env-var gating** | Classifier verdict `build-required` AND OS env unset | ✅ Runtime-only vars no longer gate builds | `env_var_classifier.go` + classifier cache. |
| **Declared dependency resolution** | Every npm/PyPI/crates.io/Go module named in a manifest resolves against its registry | ✅ `depcheck.Validate` runs pre-install | Catches the `@nativewind/style` hallucination class. |
| **Prompt-injection intake scan** | Project-supplied skill content passes threat-pattern scan | ✅ `promptguard` in `Warn` mode at ingestion | Default Warn; policy flag will promote to Strip once telemetry accrues. |
| **Secret egress** | Log/replay/bus output passes through `redact.Writer` | ✅ `logging.Init` wraps the output writer | Can be disabled via `STOKE_LOG_REDACT=0` for debugging. |
| **Dangling task dependency** | Every `Task.Dependencies` entry resolves to an existing task ID | ✅ `plan.CleanTaskDependencies` before dispatch | Prior behavior printed a warning and dispatched anyway. |
| **Commit attribution** | Author + committer match stance; signature verifies against stance's public key | ⚠ **Gap** — primitives exist (`stancesign.Identity.ApplyTo`), not yet called from the commit sites | Follow-up: thread stance identity into `worktree` merge commit + any `git commit` invocation the harness produces. |
| **Stance separation at model level** | Reviewer model ≠ builder model | ✅ `modelsource` resolves per-role (builder vs reviewer) | Can use Gemini reviewer + Claude builder; OpenRouter any-vendor. |
| **Critic approval references evidence** | Critic `Verdict` carries artifact/log hashes of what it examined | ⚠ **Gap** — `critic.Verdict` today carries findings + severity but no explicit evidence-hash field | Follow-up: add `EvidenceRefs []string` to `Verdict` struct. |
| **Supervisor rule verdicts carry evidence** | Supervisor rule decisions record the inputs that justified them | ✅ `supervisor` rules emit bus events with payloads | Verify per-rule that payload is sufficient for replay. |
| **Merge to main** | Branch is clean OR conflict resolved deterministically | ✅ `worktree.Merge` uses `git merge-tree` + `mergeMu` | Pre-merge snapshot + restore on failure. |

## Remaining gaps (ordered by blast radius)

1. **Commit attribution is not yet stance-signed.** The signing
   primitive exists (`internal/stancesign/`). Wiring it into the
   actual commit sites is a follow-up that touches `internal/worktree`
   and any direct `exec.Command("git", "commit", ...)` in
   `cmd/r1/sow_native.go`. When that lands, a commit with author
   `stoke-reviewer` and a valid signature from the reviewer key
   becomes a cryptographic attestation; a commit with that author
   but no signature or a mismatching signature is immediately
   rejectable.

2. **Critic evidence references are prose, not hashes.** The
   existing `Verdict.Findings[].Message` is a human-readable string.
   Extending to `EvidenceRefs []string{"sha256:<artifact>",
   "log:<span-id>"}` lets an auditor replay exactly what the critic
   saw. Low-risk schema change; callers append a hash list alongside
   their existing prose.

3. **Supervisor rule payloads should be schema-validated.** Bus
   events carry `map[string]any`; a drift-detection rule that emits
   an under-populated payload today fails silently at replay. Not
   urgent (the existing rules are well-tested) but worth a single
   schema-validation pass during the next supervisor-rule refactor.

## Validation procedure

When a follow-up commit closes one of the gaps above:

- Update the matrix row's **Enforcement status** to ✅.
- Add a test in the matching package that trips the check with a
  crafted failure case (e.g. a commit with mismatched stance
  signature should fail verification).
- Reference the matrix row in the commit message so the audit trail
  is traceable: "closes anti-deception matrix row: commit
  attribution is not yet stance-signed."

## What this matrix does not cover

- User-supplied post-processing scripts (e.g. `skills/` `scripts/`
  executed by workers). Sandboxing those is an env-backend concern
  (Firecracker, S-U-010) rather than an evidence-chain one.
- Merge-time policy rules (CODEOWNERS, branch protection) — out of
  scope; the harness does not push to protected refs.
- Humans bypassing the harness entirely and editing the repo by
  hand. The signing separation catches an attacker who gets inside
  the harness; it does not catch an operator who removes the harness
  from the loop.
