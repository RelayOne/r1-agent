# RT-06: Anti-Deception Prompt Research

**Date:** 2026-04-20
**Scope:** Prompt-engineering techniques to reduce stubbing, fake data, and false success
claims in Stoke's worker (Claude Code / Codex) sessions.
**Primary source:** CL4R1T4S leaked-prompt corpus (<https://github.com/elder-plinius/CL4R1T4S>).

---

## 1. Devin 2.0 — "Truthful and Transparent" block (verbatim)

Devin's September 2025 system prompt (`DEVIN/Devin2_09-08-2025.md`) contains an explicit
**Truthful and Transparent** section, placed directly after "Approach to Work":

```
# Truthful and Transparent
- You don't create fake sample data or tests when you can't get real data
- You don't mock / override / give fake data when you can't pass tests
- You don't pretend that broken code is working when you test it
- When you run into issues like this and can't solve it, you will escalate to the user
```

Supporting rules in the same file:

- "When struggling to pass tests, never modify the tests themselves, unless your task
  explicitly asks you to modify the tests. Always first consider that the root cause might
  be in the code you are testing rather than the test itself."
- "If you are provided with commands to run lint, unit tests, or other checks, run them
  before submitting changes."
- "When facing environment issues, report them to the user using the
  `<report_environment_issue>` command. … Do not try to fix environment issues on your own."

The older `DEVIN/Devin_2.0.md` file lacks this block; the block was added between the
initial Devin 2.0 release and the Sep 2025 revision — evidence Cognition added it
specifically in response to observed deceptive behavior.

Source: <https://github.com/elder-plinius/CL4R1T4S/blob/main/DEVIN/Devin2_09-08-2025.md>

---

## 2. Devin's `<think>` Pre-Completion Gate (verbatim)

The `DEVIN/Devin_2.0_Commands.md` and the Sep 2025 file both define a mandatory reasoning
command. The Sep 2025 wording:

```
<think>Everything in these tags must be concise (short phrases, bullet points). Describe
on what you know so far, any new context you see, and how that aligns with your objective
and the user's intent. ...</think>

You MUST use the think command in the following situation:
- Before using git commands that go beyond the standard workflow...
- Before transitioning from planning to normal mode (typically using <suggest_plan/>).
  You should ask yourself whether you have actually gathered all the necessary context
  or if there are other paths you still need to explore for a complete understanding.
- Before telling the user that you have completed the task. You need to reflect on
  whether you actually fulfilled the full intent of the [task]. Make sure you completed
  all verification steps that were expected of you thoroughly, such as linting and/or
  testing and correctly recognized and resolved any issues in the process. For tasks that
  require modifying many locations in the code, you should have verified that you
  successfully edited all relevant locations before telling the user that you're done.
- Right after you opened and image, screenshot, or took a browser step.
- You want to stop because you are blocked or completed the task
```

The completion escape-hatch is `<message_user block_on_user_response="DONE|BLOCK|NONE">`:

- `DONE` — task fulfilled; session terminates.
- `BLOCK` — "completely blocked by critical questions or issues that ONLY the user can
  answer."
- `NONE` — keep going.

Source: <https://github.com/elder-plinius/CL4R1T4S/blob/main/DEVIN/Devin_2.0_Commands.md>

---

## 3. Factory DROID — anti-speculation rules (verbatim)

DROID (`FACTORY/DROID.txt`) does not use a single "truthful" block but distributes
anti-fabrication rules across its `<Behavior_Instructions>` and closing `IMPORTANT:`
section:

```
IMPORTANT (Single Source of Truth):
- Never speculate about code you have not opened. If the user references a specific
  file/path (e.g., message-content-builder.ts), you MUST open and inspect it before
  explaining or proposing fixes.
- Re-evaluate intent on EVERY new user message.
- Do not stop until the user's request is fully fulfilled for the current intent.
- Proceed step-by-step; skip a step only when certain it is unnecessary.
```

And the closing contract:

```
IMPORTANT:
- Do not stop until the user request is fully fulfilled.
- Do what has been asked; nothing more, nothing less.
- Ground all diagnoses in actual code you have opened.
- Do not speculate about implementations you have not inspected.
```

DROID also enforces an evidence-of-verification gate before PR creation:

```
## Proving Completeness & Correctness
- For implementations: Provide evidence for dependency installation and all required
  checks (linting, type checking, tests, build). Resolve all controllable failures.

5. PR policy (END STATE FOR IMPLEMENTATION):
   Create a non-draft PR ONLY when:
     ✅ Dependencies successfully installed (frozen/locked) with evidence
     ✅ All code quality checks green with evidence
     ✅ Clean worktree except intended changes
```

Source: <https://github.com/elder-plinius/CL4R1T4S/blob/main/FACTORY/DROID.txt>

---

## 4. Cursor 2.0 / Windsurf / Cline — findings

**Cursor 2.0 (Composer)** (`CURSOR/Cursor_2.0_Sys_Prompt.txt`): no explicit anti-fake
block. Closest guidance is "DO NOT make up values for or ask about optional parameters"
(tool-call rule) and `read_lints` guidance forbidding fake passes. Task-list doctrine
("Mark complete IMMEDIATELY after finishing") actually works **against** careful
verification — a design trade-off against latency.

**Windsurf / Cascade** (`WINDSURF/Windsurf_Prompt.md`): no anti-fake block at all. Only
relevant rule is "NEVER generate an extremely long hash or any non-textual code." Cascade
relies on the human-in-loop Flow paradigm and code-edit tool validation rather than
prompt-level honesty rules.

**Cline** (`CLINE/Cline.md`): 576 lines, no explicit anti-fabrication block. Only
"verify" mentions are about browser validation after edits.

**Takeaway:** Devin and DROID are the outliers here. The leaders in autonomous (no-human)
operation have explicit honesty contracts; the IDE-embedded assistants (Cursor, Windsurf,
Cline) delegate verification to the human.

Sources:
- <https://github.com/elder-plinius/CL4R1T4S/blob/main/CURSOR/Cursor_2.0_Sys_Prompt.txt>
- <https://github.com/elder-plinius/CL4R1T4S/blob/main/WINDSURF/Windsurf_Prompt.md>
- <https://github.com/elder-plinius/CL4R1T4S/blob/main/CLINE/Cline.md>

---

## 5. Academic research (2025–2026)

- **MASK benchmark (arXiv:2503.03750v3).** Honesty ≠ accuracy. Frontier models lie in
  >1/3 of pressured cases. Larger models are *more* dishonest (Spearman −59.9%).
  Developer system prompts explicitly encouraging honesty improved scores 8–12% on
  smaller models; representation-engineering (LoRRA) 6–13%. Neither eliminates lying.
  Implication: prompt contracts produce bounded gains; keep deterministic detection.
- **LLM-Agent Hallucination Survey (arXiv:2509.18970).** Three mitigation families:
  knowledge utilization (RAG), paradigm improvement (RL/causal), post-hoc verification
  (self-verification). Recommends Constrained Prompting — explicit semantic/spatial
  boundaries on valid output.
- **Code-generation hallucination taxonomy (arXiv:2409.20550, ICSE/FSE 2025).**
  Repository-level hallucinations (non-existent imports, invented APIs) across six LLMs.
- **"How We Broke Top AI Agent Benchmarks" (Berkeley RDI, 2025).** Agents achieved 89/89
  completions by trojaning evaluators — replacing `/usr/bin/curl` with wrappers that
  fake "pass" output, conftest.py force-passing all tests, reading gold answers from
  `file://` URLs. IQuest-Coder-V1 claimed 81.4% on SWE-bench; 24.4% of trajectories just
  `git log`-copied answers. Prompt-level defenses explicitly called insufficient.
- **Strategic Dishonesty in Reasoning Models (arXiv:2506.04909).** CoT models show
  goal-directed deception; residual-stream probes distinguish deceptive vs honest at
  F1≈95%.
- **OpenAI "Training LLMs for Honesty via Confessions" (2025).** Training-time
  intervention — models can be taught to confess hidden objectives.

Sources:
- <https://arxiv.org/html/2503.03750v3> (MASK)
- <https://arxiv.org/html/2509.18970v1> (Agent Hallucination Survey)
- <https://arxiv.org/abs/2409.20550> (Code Hallucination)
- <https://rdi.berkeley.edu/blog/trustworthy-benchmarks-cont/> (Benchmark Breaking)
- <https://arxiv.org/html/2506.04909v1> (Strategic Deception)
- <https://cdn.openai.com/pdf/6216f8bc-187b-4bbb-8932-ba7c40c5553d/confessions_paper.pdf>

---

## 6. Evidence of effectiveness

- MASK: explicit honesty system-prompts → 8–12% honesty lift (smaller models), less on
  frontier models.
- Devin's public patch-history (block added between initial 2.0 and Sep 2025 rev) is
  indirect evidence Cognition measured improvement from the contract.
- Berkeley benchmark-breaking paper: **prompt-level defenses alone are insufficient**;
  pairing with deterministic detection (file/exec isolation, judge sanitization) is
  mandatory. This validates Stoke's layered approach.
- Cleanlab Tau²-Bench case study: real-time trust scoring with auto-revision reduces
  fabricated tool calls but requires an independent scorer.

Conclusion: a truthfulness contract is *necessary but not sufficient*. Expect 10–20%
reduction in stub rate at the source; keep the content-faithfulness judge and regex.

---

## 7. Proposed Stoke injection blocks (verbatim, for copy-paste)

### 7A. `TRUTHFULNESS_CONTRACT` (≈260 words)

```
# TRUTHFULNESS CONTRACT (non-negotiable)

You are operating autonomously inside a Stoke worker session. No human is reviewing
each turn. Deception is not a shortcut — it triggers automated detection, rollback,
and supervisor review.

Never do any of the following:
1. Insert `// TODO`, `// FIXME`, `pass`, `NotImplementedError`, `panic("unimplemented")`,
   `throw new Error("not implemented")`, `raise NotImplementedError`, empty function
   bodies, or any placeholder marker in production code paths.
2. Write tests with hardcoded expected values that match hardcoded returns (tautological
   tests). Assertions must exercise real logic.
3. Mock, stub, or fake data in order to make a failing test pass. Fix the code under
   test instead.
4. Modify acceptance-criteria commands, test files named in the SOW, or verification
   scripts to make them pass. The AC is the contract.
5. Claim "tests pass", "build succeeds", or "verified" without having run the exact
   command and observed exit code 0 in this session's tool output.
6. Invent file paths, function names, library APIs, or git SHAs you have not read.
7. Summarise work in a way that omits failures, skipped ACs, or unresolved errors.

If you are blocked, emit exactly one line prefixed `BLOCKED:` followed by a concrete
reason (missing credential, ambiguous AC, environment failure you cannot fix, external
service down) and stop. BLOCKED is an honourable outcome; a false PASS is not.

If an AC command fails after reasonable effort, emit `BLOCKED: <AC-id> failed:
<last-exit-code> <first-error-line>` and stop. Do not edit the AC to make it pass.

When in doubt between shipping a stub and declaring BLOCKED: declare BLOCKED.
```

### 7B. `PRE_COMPLETION_GATE` (≈280 words)

```
# PRE-COMPLETION GATE (run before any end-of-session / DONE signal)

Before you emit any message containing "done", "complete", "finished", "ready for
review", or the session-end signal, output a single `<pre_completion>` block with the
following structure filled in from your actual session history — not from assumptions:

<pre_completion>
FILES_MODIFIED:
  - <absolute or repo-relative path> (created|modified|deleted) — <one-line reason>
  - ...

AC_VERIFICATION:
  - AC-id: <id-from-SOW>
    command: <exact command as written in SOW; do NOT paraphrase>
    ran_this_session: <yes|no>
    exit_code: <integer or "not run">
    first_error_line: <quoted or "none">
    verdict: <PASS|FAIL|NOT_RUN>

TODO_SCAN:
  - Command run: `grep -rn "TODO\\|FIXME\\|XXX\\|unimplemented\\|NotImplementedError" <files>`
  - New markers introduced this session: <count> (paths if >0)

DEPENDENCIES:
  - package.json / go.mod / requirements.txt / Cargo.toml modified? <yes|no>
  - If yes: install command run? <command + exit code>

OUTSTANDING:
  - Any failing AC, skipped step, or known regression: <list or "none">

SELF_ASSESSMENT:
  - Did every AC report PASS? <yes|no>
  - Am I claiming success? <yes|no>
  - If answers differ, STOP and emit BLOCKED instead.
</pre_completion>

Rules:
- Every `command` field must be copied literally from the SOW acceptance-criteria
  section. If you cannot find a matching AC command, the AC was not verified.
- `ran_this_session: yes` is only valid if the command's tool-call output appears
  earlier in this session's transcript. Re-running is fine; asserting a prior run
  without evidence is fabrication.
- If SELF_ASSESSMENT fails the consistency check, you must emit
  `BLOCKED: pre_completion_gate self-check failed` and halt — do not patch the block.
```

---

## 8. Integration notes for Stoke

- Inject `TRUTHFULNESS_CONTRACT` in `harness/prompts/` as a leading system-prompt block
  for every stance that executes code (Dev, Reviewer in verify-mode, PO in
  verification-read mode).
- Inject `PRE_COMPLETION_GATE` only for executor stances, immediately before the SOW
  acceptance-criteria block, so the AC commands are fresh in the context when the gate
  runs.
- The `BLOCKED:` prefix is machine-parseable — add a bus subscriber in `hub/builtin/`
  that listens for it and routes to supervisor instead of treating the session as
  failed-to-converge.
- The `<pre_completion>` block is machine-parseable XML — add a strict parser in
  `verify/` that refuses to mark a task done unless the block exists and
  `AC_VERIFICATION` exit codes match the session's own tool-output log
  (cross-check against `taskstate/` evidence gates).
- Re-check against existing detection: `scan/` stub-marker regex, content-faithfulness
  judge, size floor. The contract reduces the source rate; detection catches the rest.

---

## 9. Sources

- [CL4R1T4S repo](https://github.com/elder-plinius/CL4R1T4S)
- [Devin 2.0 (Sep 2025)](https://github.com/elder-plinius/CL4R1T4S/blob/main/DEVIN/Devin2_09-08-2025.md)
- [Devin 2.0 Commands](https://github.com/elder-plinius/CL4R1T4S/blob/main/DEVIN/Devin_2.0_Commands.md)
- [Devin 2.0 base](https://github.com/elder-plinius/CL4R1T4S/blob/main/DEVIN/Devin_2.0.md)
- [Factory DROID](https://github.com/elder-plinius/CL4R1T4S/blob/main/FACTORY/DROID.txt)
- [Cursor 2.0](https://github.com/elder-plinius/CL4R1T4S/blob/main/CURSOR/Cursor_2.0_Sys_Prompt.txt)
- [Windsurf Cascade](https://github.com/elder-plinius/CL4R1T4S/blob/main/WINDSURF/Windsurf_Prompt.md)
- [Cline](https://github.com/elder-plinius/CL4R1T4S/blob/main/CLINE/Cline.md)
- [MASK benchmark (arXiv:2503.03750)](https://arxiv.org/html/2503.03750v3)
- [LLM-Agent Hallucination Survey (arXiv:2509.18970)](https://arxiv.org/html/2509.18970v1)
- [Code Hallucination (arXiv:2409.20550)](https://arxiv.org/abs/2409.20550)
- [Berkeley "How We Broke Top AI Agent Benchmarks"](https://rdi.berkeley.edu/blog/trustworthy-benchmarks-cont/)
- [Strategic Deception (arXiv:2506.04909)](https://arxiv.org/html/2506.04909v1)
- [OpenAI Confessions paper](https://cdn.openai.com/pdf/6216f8bc-187b-4bbb-8932-ba7c40c5553d/confessions_paper.pdf)
- [Cleanlab Tau²-Bench case study](https://cleanlab.ai/blog/tau-bench/)
