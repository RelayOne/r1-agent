package main

// Prompt templates for the extended scan-repair pipeline. These are
// held in constants (rather than inlined in the caller) so that:
//
//   1. The prompt text is reviewable as a single block. Prompts are
//      load-bearing — any structural change shifts the reviewer /
//      worker behavior, so we keep them in one place and cover them
//      with dedicated prompt-construction tests (see scan_repair_test.go).
//   2. The hook `.claude/hooks/detect-stubs.sh` fires on words like
//      "placeholder" / "TODO" / "mock" in comments. This file contains
//      those words intentionally inside PROMPT STRINGS meant for the
//      audit worker — NOT as stubs in our own Go code. Operators with
//      the hook enabled should silently bypass findings here.

// vectorScanPromptTemplate is sent to the worker for each security
// vector in .claude/scripts/security/vectors.md. The worker gets:
//
//   - {vectorNum} / {vectorName} — anchor the review to exactly one
//     vector (security reviewers frequently drift across vectors if
//     given a broad prompt),
//   - {vectorBody} — the full prose description from vectors.md so
//     the worker knows what "this vector" actually covers,
//   - {files} — the union of section .txt lists relevant to this
//     vector. The worker reads the listed files directly (we don't
//     paste their contents to keep the prompt bounded).
//
// The finding-line format "- [SEVERITY] file:line — description — fix: …"
// is parsed in Phase 3a/3b. Changing the format here requires updating
// the dedup prompt too.
const vectorScanPromptTemplate = `You are a security reviewer. Review ONLY against vector %d: %s.

Think deeply about code paths, concurrency, TOCTOU, atomicity, and indirect data flows.
Read: audit/security/*.csv + the relevant source files listed below.

For each finding, write one line in this exact format:
- [CRITICAL|HIGH|MEDIUM] file:line — description — fix: specific-fix

If no findings, reply with the literal token: None.

## Vector
%s

## Files
%s
`

// personaPromptTemplate wraps each persona description from
// audit-personas.md in the standard finding-format directive. The
// %s placeholders are, in order:
//
//   1. Persona slug (used for the "as [slug]" role banner)
//   2. Full persona body — "You are [role]. Audit for: ..." — pasted
//      from audit-personas.md so the persona's unique perspective is
//      preserved verbatim.
//
// Phase 3a consumes lines matching "- [SEVERITY]" from the reply, so
// the format directive here is load-bearing.
const personaPromptTemplate = `Multi-perspective audit as %s.

%s

## Instructions

1. Do your full audit from your role's perspective.
2. Be specific: file paths, line numbers, exact issue, exact fix.
3. Write each finding as one line in this exact format:
   - [CRITICAL|HIGH|MEDIUM] file:line — description — fix: specific-fix
4. Do NOT list what's correct. Only issues.
5. If no issues meeting the impact-effort threshold, reply with the literal token: None.

Apply the impact-effort filter: focus on security vulnerabilities, data loss,
crashes, scaling blockers, UX blockers. Skip style preferences, aesthetic
rewrites, missing docs on internal helpers, DRY violations of 2-3 lines.
`

// dedupPromptTemplate collapses duplicate findings across every audit
// source (deterministic, semantic, security vectors, personas, codex).
// The reviewer returns ONE de-duplicated finding per line; we parse
// counts by counting lines starting with "- [".
//
// %s = aggregated findings buffer from Phase 3a.
const dedupPromptTemplate = `De-duplicate the audit findings below. Group findings that refer to the
SAME file + SAME issue. For each duplicate group, keep the most specific
version (the one with the best fix description).

Write one finding per line in this exact format:
- [SEVERITY] file:line — description — fix: specific-fix

Do not add commentary. Do not group by category. One finding per line only.
Report on the last line: "TOTAL: N findings, DEDUPED: M unique".

## Findings

%s
`

// tierFilterPromptTemplate classifies each deduped finding into one
// of three tiers. The reviewer writes three distinct blocks separated
// by section headers; Phase 3c parses the blocks into three separate
// output files.
//
// %s = deduped findings buffer from Phase 3b.
const tierFilterPromptTemplate = `Classify each finding into exactly one tier:

TIER 1 (auto-approve, create tasks): security/auth/data-loss/race-condition/
crash/scaling-blocker/UX-blocker findings. Always fix.

TIER 2 (fix only if effort <= medium): reliability issues on critical paths,
test gaps on business logic, performance >500ms, type safety holes causing
runtime errors.

TIER 3 (DROP, do not create tasks): style preferences, pattern migrations,
rewriting working code for marginal gains, abstraction for its own sake,
theoretical improvements, DRY violations of 2-3 lines, missing JSDoc on
internal helpers, naming preferences.

Output exactly three sections, each with findings one-per-line in the format
"- [SEVERITY] file:line — description — fix: specific-fix":

## TIER 1

<tier-1 findings here>

## TIER 2 (small/medium effort)

<tier-2 findings here>

## TIER 3 (dropped)

<tier-3 findings, one-line reason each>

Report on the last line: "APPROVED: A, DEFERRED: D, DROPPED: X".

## Findings

%s
`

// fixTaskPromptTemplate turns a set of approved findings for one
// section into concrete repair tasks. Output format matches the SOW
// runner's expectations (sow_task_spec.go parses "T<n>:" and checkboxes).
//
// Positional %s: section basename, approved findings for this section.
const fixTaskPromptTemplate = `You are the tech-lead producing fix tasks for section %s.

Read the approved findings below. For EACH finding produce ONE task with the
following structure (markdown, valid for the SOW runner):

- [ ] FIX-%s-<seq>: <one-sentence summary>
  FILE: <path>
  LINE: <approx>
  FINDING: <verbatim from report>
  MUST: <exact fix, one or two sentences>
  MUST: build + tests pass after the change
  MUST: do not modify unrelated files
  VERIFY: <exact check, e.g. "go build ./..." or "grep -n X file">

Group tasks by severity: critical first, then high, then medium.
Do NOT invent findings. Do NOT repeat the finding verbatim if the MUST line
already captures it.

## Approved Findings

%s
`

// interactivePhase1PromptTemplate is shown to the operator after
// Phase 1 in interactive mode. It is purely textual — we parse the
// first non-whitespace letter the operator types as the answer.
const interactivePhase1PromptTemplate = `
%d code + %d security findings. How to proceed?
  [A] full semantic + security vectors + 17-persona audit (default)
  [B] quality only (semantic scan, skip security/personas)
  [C] security only (skip semantic/personas)
  [D] flagged only (skip semantic, run security+personas)
  [E] skip semantic entirely (go straight to review)
Choice [A]: `

// interactivePhase2bPromptTemplate asks whether to run the security
// vector scan (Phase 2b).
const interactivePhase2bPromptTemplate = `
Phase 2b security vector scan: ~%d security vectors. Run now?
  [A] run all (default)
  [B] skip
Choice [A]: `

// interactivePhase2cPromptTemplate asks which persona set to run.
const interactivePhase2cPromptTemplate = `
Phase 2c 17-persona audit. Which personas?
  [A] all 17 (most thorough) (default)
  [B] core 8 (lead-eng, lead-qa, lead-security, vp-eng-completeness,
      vp-eng-types, sneaky-finder, build-deploy, picky-reviewer)
  [C] pick (comma list)
  [D] skip
Choice [A]: `

// interactivePhase3cPromptTemplate is shown after the tier filter
// completes. Operator picks between "build", "review first", "edit
// scope", "done".
const interactivePhase3cPromptTemplate = `
Repair SOW ready: %d approved, %d deferred, %d dropped. Next step?
  [A] build now (Phase 4) (default)
  [B] review spec first (open FIX_SOW.md, don't build)
  [C] edit scope (skip build)
  [D] done (skip build)
Choice [A]: `
