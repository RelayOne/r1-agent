<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: (none — builds on H-91) -->
<!-- BUILD_ORDER: 1 -->

# Descent Hardening — Implementation Spec

## Overview

The H-91 verification descent engine shipped in commit `8611d48` (`internal/plan/verification_descent.go:26-1017`) gates acceptance-criteria failure through an 8-tier ladder (T1 intent → T2 run AC → T3 classify → T4 code repair → T5 env fix → T6 AC rewrite → T7 refactor → T8 soft-pass). It is opt-in via `STOKE_DESCENT=1`. This spec hardens it against six source-rate deception/runaway failure modes: (1) worker stubbing/fake-pass, (2) no pre-completion self-check, (3) unbounded per-file repair, (4) stale workspace after dep manifest edits, (5) no fast-path for environment blockers, (6) ghost-writes (tool reports success, file empty). Everything shipped here stays behind `STOKE_DESCENT=1` (decision D-2026-04-20-02).

## Stack & Versions

- Go 1.22+
- Anthropic Messages API via `internal/agentloop` (Opus 4.7 / Sonnet 4.6)
- Worker tool registry: `internal/harness/tools/`
- Bus: `internal/bus/bus.go`
- Event emitter: `internal/streamjson/emitter.go`

## Existing Patterns to Follow

- Descent engine + DescentConfig: `internal/plan/verification_descent.go:26-1017`
- Descent bridge wiring: `cmd/r1/descent_bridge.go:37-400`
- Worker prompt builder: `cmd/r1/sow_native.go:3729-3950+`
- Repair-mode dispatch prompt: `cmd/r1/sow_native.go:3737-3764`
- Anti-deception injection point: `cmd/r1/sow_native.go:3906-3918` (after canonical-names, before skills)
- PreEndTurn hook: `internal/agentloop/loop.go:77` (`PreEndTurnCheckFn`)
- Bootstrap/install: `internal/plan/acceptance.go:348-404` (`ensureWorkspaceInstalled`)
- Supervisor extraction hook: `autoExtractTaskSupervisor` (`sow_native.go` callsites 939, 1395, 1431, 2580, 3277, 5104, 5434)
- Bus event conventions: `internal/bus/bus.go:31-69`

## Library Preferences

- No new third-party libs. All hooks plug into existing package seams.
- XML parsing for `<pre_completion>`: stdlib `encoding/xml`.
- Git diff inspection: reuse existing `os/exec` wrappers in `worktree/` or `atomicfs/`.

## Data Models

### DescentConfig additions (internal/plan/verification_descent.go:232-291)

| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `FileRepairCounts` | `map[string]int` | nil-safe; lazily initialized | `nil` |
| `MaxRepairsPerFile` | `int` | values ≤0 replaced by default | `3` |
| `PreEndTurnCheckFn` | `func(ctx, finalText) (retry bool, reason string)` | forwarded to agentloop | `nil` |

### Bus event payloads (new)

- `descent.file_cap_exceeded` → `{session_id, task_id, ac_id, file, attempts, last_errors[]}`
- `worker.env_blocked` → `{session_id, task_id, ac_id, issue, workaround_attempted, suggestion}`
- `descent.ghost_write_detected` → `{session_id, task_id, tool_call_id, path, size_bytes}`
- `descent.bootstrap_reinstalled` → `{session_id, manifest_touched, frozen, duration_ms}`
- `descent.pre_completion_gate_failed` → `{session_id, task_id, mismatch_kind, claim, observed}`

All payloads route through `bus.Publish` and mirror onto streamjson as `subtype:"stoke.descent.*"` with `_stoke.dev/*` extensions (C1).

## Business Logic

### 1. TRUTHFULNESS_CONTRACT injection

1. Build constant `const truthfulnessContract = "..."` in `cmd/r1/sow_native.go`.
2. In `buildSOWNativePromptsWithOpts`:
   - **Standard path:** inject between canonical-names (line 3909) and skills (line 3918).
   - **Repair path:** inject inside the repair preamble at line 3737-3764 (before COMMON FAILURE CLASSES).
3. No opt-out, no flag. Always present.

### 2. PRE_COMPLETION_GATE via `PreEndTurnCheckFn`

1. Build constant `const preCompletionGate = "..."` (verbatim block below).
2. Inject in same two sites as contract, immediately after `TRUTHFULNESS_CONTRACT` but only for executor stances (skip for DIAGNOSE/read-only).
3. Implement `PreEndTurnCheckFn` in a new file `internal/plan/pre_completion_check.go`:
   - Scan final assistant message for `<pre_completion>...</pre_completion>`.
   - If absent AND message contains any of `done|complete|finished|ready for review` (case-insensitive) → `retry=true`, reason `pre_completion_gate missing`.
   - If present, parse with `encoding/xml`.
   - For each `FILES_MODIFIED` entry: run `git status --porcelain <path>` in session cwd. Claim says created/modified but git shows nothing → mismatch.
   - For each `AC_VERIFICATION` entry claiming `exit_code: 0`: verify the exact `command` appeared in the session's tool-call transcript (passed via `SessionTranscript []ToolCall` in the check context). No evidence → mismatch.
   - Any mismatch → publish `descent.pre_completion_gate_failed` + return `retry=false, reason` and flag the descent run with `analysisCategory = "code_bug"` so the ladder skips straight to T4 on this AC.
4. Wire via `agentloop.Config.PreEndTurnCheckFn` (loop.go:77) in `execNativeTask` construction. Descent engine sets the hook on its repair dispatches.

### 3. Per-file repair cap

1. Extend `DescentConfig` with `FileRepairCounts` + `MaxRepairsPerFile` (defaults above).
2. In `runDescent` T4 logic (`verification_descent.go:514-608`), before every `RepairFunc` call:
   - Resolve the repair's target file list. Prefer `analysisCodeFix.TargetFiles` from `ReasoningVerdict`; fall back to `AcceptanceCriterion.ContentMatch.File` or parsing stderr pathname.
   - For each file: `if cfg.FileRepairCounts[file] >= cfg.MaxRepairsPerFile` → cap hit.
   - On cap hit: publish `descent.file_cap_exceeded`, call `cfg.OnLog`, mark the AC result `Passed=false, Reason="per-file repair cap exceeded"`, skip RepairFunc for THIS AC, exit T4 (do NOT fall through to T5).
   - Otherwise: increment `FileRepairCounts[file]++` for each targeted file before calling RepairFunc. Reset to 0 only on an all-passing re-run of the AC against those files.
3. Map is session-scoped (one DescentConfig per session). Concurrent access not expected in current callsites — no mutex unless the race detector flags one.

### 4. Bootstrap per descent cycle

1. In `cmd/r1/descent_bridge.go:74-99` (current RepairFunc), wrap the existing dispatch:

   ```go
   originalRepair := cfg.RepairFunc
   cfg.RepairFunc = func(ctx context.Context, directive string) error {
       preSHA := gitHead(ctx, root)
       if err := originalRepair(ctx, directive); err != nil {
           return err
       }
       changed := gitDiffNames(ctx, root, preSHA, "HEAD")
       manifests := []string{"package.json","pnpm-lock.yaml","go.mod","Cargo.toml","requirements.txt","pyproject.toml","uv.lock"}
       touched := intersect(changed, manifests)
       if len(touched) > 0 {
           frozen := lockfilePresent(root)
           start := time.Now()
           _ = ensureWorkspaceInstalledOpts(ctx, root, installOpts{Frozen: frozen, Force: true})
           bus.Publish(bus.Event{Kind:"descent.bootstrap_reinstalled", Data: map[string]any{
               "session_id": sess.ID, "manifest_touched": touched, "frozen": frozen,
               "duration_ms": time.Since(start).Milliseconds(),
           }})
       }
       return nil
   }
   ```

2. Extend `ensureWorkspaceInstalled` (`internal/plan/acceptance.go:348-404`) with a sibling `ensureWorkspaceInstalledOpts(ctx, root, opts)` that:
   - Bypasses the `installedOnce` guard when `opts.Force`.
   - Appends `--frozen-lockfile` to pnpm, `--frozen-lockfile` to npm ci, `--locked` to `cargo` when `opts.Frozen`.
   - Keeps existing registry validation (`runDepCheck`) path.
3. Frozen-lockfile mode catches hallucinated deps: lockfile mismatch → non-zero exit → T5 env-fix (existing).

### 5. `report_env_issue` worker tool

1. Add tool definition to worker tool registry. Verify location with: `grep -rn "report_environment_issue\|ToolDefinition\|tool_name" internal/harness/tools/ cmd/r1/sow_native.go`. Register alongside existing tools (edit/read/shell).
2. Schema:

   ```json
   {
     "name": "report_env_issue",
     "description": "Report an environment blocker the worker cannot fix (missing binary, network outage, credential, protected file). The descent engine classifies this AC as 'environment' and skips multi-analyst reasoning.",
     "input_schema": {
       "type": "object",
       "required": ["issue"],
       "properties": {
         "issue": {"type":"string","description":"One-line concrete problem (e.g., 'pnpm not on PATH', 'npm registry 503')."},
         "workaround_attempted": {"type":"string","description":"What you tried before giving up."},
         "suggestion": {"type":"string","description":"Actionable next step for the human operator."}
       }
     }
   }
   ```

3. On invocation: handler in `harness/tools/` publishes `worker.env_blocked` on the bus and writes a marker `{"env_blocked": true, ...}` into the session's shared scratch. The tool returns a terse `"reported"` result so the worker ends its turn cleanly.
4. In descent T3 (`verification_descent.go:446-512`), before dispatching multi-analyst: if the session scratch has an `env_blocked` marker for the current AC → set `verdict.Category = "environment"`, `verdict.Reasoning = "report_env_issue invoked: <issue>"`, skip to T5. Saves ~$0.10/AC (multi-analyst = 5 LLM calls).

### 6. Ghost-write detection

1. Extend `autoExtractTaskSupervisor` (sow_native.go callsites 939, 1395, etc.) to include a post-tool-use rule: after any `edit`/`write`/`str_replace`/`create_file` tool returns success, inject a bash check.
2. The supervisor rule emits a `PostToolUseHook` in the agentloop transcript: `bash -c 'test -f "$P" && [ -s "$P" ]'` with `P=<path>` from the tool result. Exit non-zero → force the worker to retry the write.
3. Wire in `internal/agentloop/loop.go` `MidturnCheckFn` path (already called between turns). If the check fails, publish `descent.ghost_write_detected` and inject a user-role reminder: `"Ghost-write detected: <path> is empty/missing after your edit tool reported success. Re-run the edit with full file contents, or declare BLOCKED."`
4. Retry is a natural part of the loop; cap via existing `MaxConsecutiveErrs` (default 3).

## Verbatim prompt blocks (MUST COPY INTO CODE)

### TRUTHFULNESS_CONTRACT (inject as-is)

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

### PRE_COMPLETION_GATE (inject as-is)

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
  - Command run: `grep -rn "TODO\|FIXME\|XXX\|unimplemented\|NotImplementedError" <files>`
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

## Error Handling

| Failure | Strategy | User Sees |
|---------|----------|-----------|
| Pre-completion gate mismatch | Publish event, force T4 | `[descent] pre_completion: claimed PASS on AC-X but no exit-0 evidence` |
| Per-file cap exceeded | Publish event, fail AC, do not fall through | `[descent] file cap: src/x.ts reached 3 repair attempts` |
| Frozen-lockfile install fails | Fall through to T5 env fix | `[descent] install --frozen-lockfile failed; trying env fix` |
| `report_env_issue` called | Skip to T5 with env category | `[descent] worker reported env blocker: <issue>` |
| Ghost write | Reminder injection + worker retry | `[descent] ghost-write: <path> empty after edit` |
| Contract violation (stub detected post-run) | Existing `scan/` path | unchanged |

## Boundaries — What NOT To Do

- Do NOT flip `STOKE_DESCENT` default (D-2026-04-20-02).
- Do NOT modify `AcceptanceCriterion` struct (`sow.go:87-96`) — spec-3 owns `VerifyFunc`.
- Do NOT rewrite the descent ladder; additive changes only.
- Do NOT touch `internal/bus/bus.go`; only publish to it.
- Do NOT regress soft-pass behavior; code_bug never soft-passes (gate 3, lines 769-775).
- Do NOT add opt-out flags for the truthfulness contract (decision C4).
- Do NOT skip `report_env_issue`'s bus publish (operator observability depends on it).

## Testing

### TRUTHFULNESS_CONTRACT injection
- [ ] `grep -q "TRUTHFULNESS CONTRACT" cmd/r1/sow_native.go`
- [ ] `go test ./cmd/r1/... -run TestBuildSOWPromptsContainsContract` (new)
- [ ] Repair path also contains the contract (same test, `opts.Repair != nil` branch).

### PRE_COMPLETION_GATE
- [ ] Unit: `pre_completion_check_test.go` with three fixtures: missing block, mismatched FILES_MODIFIED, mismatched AC exit code. Each fixture → `retry=false, reason != ""`.
- [ ] Unit: passing fixture → `retry=false, reason == ""`.
- [ ] Integration: `go test ./internal/agentloop/... -run TestPreEndTurnCheckFn` verifies the hook fires before end_turn.

### Per-file repair cap
- [ ] `go test ./internal/plan/... -run TestPerFileRepairCap` — fake RepairFunc that always fails; after 3 rounds on `src/a.ts` the cap event fires and AC fails.
- [ ] Cap event payload includes `file` and `attempts=3`.
- [ ] Default population: `MaxRepairsPerFile<=0` → 3 (guarded in `DescentConfig.Normalize` or equivalent init).

### Bootstrap per descent cycle
- [ ] `go test ./internal/plan/... -run TestBootstrapReinstallOnManifestChange` — fake git diff returns `package.json`; wrapped RepairFunc triggers install; bus event emitted.
- [ ] No-op when no manifest changed (no bus event, no install).
- [ ] Frozen-lockfile flag passed when `pnpm-lock.yaml` exists.

### `report_env_issue` tool
- [ ] Tool is registered and advertised to the worker (assert tool schema present).
- [ ] Invocation publishes `worker.env_blocked` with required fields.
- [ ] Descent T3 picks up the scratch marker and sets `Category="environment"` without calling multi-analyst (assert 0 LLM calls).

### Ghost-write detection
- [ ] Fixture: fake edit tool returns success but path is empty → supervisor injects retry reminder, bus event fires.
- [ ] Normal non-empty write → no event, no reminder.

## Acceptance Criteria (bash-runnable)

```bash
# 1. Contract injection sites exist.
grep -q "TRUTHFULNESS CONTRACT" cmd/r1/sow_native.go
grep -q "PRE-COMPLETION GATE" cmd/r1/sow_native.go

# 2. PreEndTurnCheckFn hook wired to descent executor.
grep -q "PreEndTurnCheckFn" internal/plan/pre_completion_check.go
go test ./internal/agentloop/... -run TestPreEndTurnCheckFn

# 3. Per-file cap defaults + enforcement.
grep -q "MaxRepairsPerFile" internal/plan/verification_descent.go
grep -q "FileRepairCounts"  internal/plan/verification_descent.go
go test ./internal/plan/... -run TestPerFileRepairCap

# 4. Bootstrap wrapper + frozen mode.
grep -q "descent.bootstrap_reinstalled" cmd/r1/descent_bridge.go
go test ./internal/plan/... -run TestBootstrapReinstallOnManifestChange

# 5. Env tool registered + fast-path.
grep -q "report_env_issue" internal/harness/tools
go test ./internal/plan/... -run TestEnvBlockerFastPath

# 6. Ghost-write hook.
grep -q "descent.ghost_write_detected" cmd/r1/sow_native.go
go test ./cmd/r1/... -run TestGhostWriteRetry

# 7. Build + vet + full test suite green.
go build ./cmd/r1 && ./stoke version
go vet ./...
go test ./...
```

## Implementation Checklist

Each item is self-contained. The implementer gets ONLY that item plus this spec.

1. [ ] **Inject TRUTHFULNESS_CONTRACT.** File: `cmd/r1/sow_native.go`. Add top-level `const truthfulnessContract = ` with the verbatim block (see "Verbatim prompt blocks" above — ~260 words, preserve ASCII hyphens and backticks exactly). In `buildSOWNativePromptsWithOpts`, insert the constant into the system prompt in TWO places:
   - Standard path: after `buildCanonicalNamesBlock` output (line 3909) and before the skill-injection section (line 3918). Use a preceding `\n\n` separator.
   - Repair path: inside the `if opts.Repair != nil` branch at line 3737, placed FIRST before the existing "You are in REPAIR mode" preamble. Preserve `\n\n` separators.

   AC: `grep -q "TRUTHFULNESS CONTRACT" cmd/r1/sow_native.go`; new unit test `TestBuildSOWPromptsContainsContract` asserts contract substring in both branches' system prompt output.

2. [ ] **Inject PRE_COMPLETION_GATE.** Same file. Add top-level `const preCompletionGate = ` with the verbatim ~280-word block. Inject immediately after `truthfulnessContract` in both the standard and repair paths. Gate ONLY fires for executor stances — repair IS executor; also allow for the default Dev stance. Do NOT inject on reviewer-only or diagnostic dispatches (check with `opts.Stance` or equivalent; if no stance field today, add one or inject unconditionally since DIAGNOSE mode is spec-7).

   AC: `grep -q "PRE-COMPLETION GATE" cmd/r1/sow_native.go`; assert both standard + repair system prompt contain the block.

3. [ ] **Implement `PreEndTurnCheckFn` parser.** New file `internal/plan/pre_completion_check.go`:
   - Exported `func NewPreEndTurnCheck(ctx PreCheckContext) agentloop.PreEndTurnCheckFn`.
   - Define `PreCheckContext { RepoRoot string; SowACs []AcceptanceCriterion; SessionTranscript []ToolCall }`.
   - Inside returned func: if the final assistant text matches `/\b(done|complete|finished|ready for review)\b/i` AND no `<pre_completion>` tag → return `(false, "pre_completion_gate missing")` and publish `descent.pre_completion_gate_failed`.
   - If gate present, parse with `encoding/xml`. Struct mirrors the block (FILES_MODIFIED list, AC_VERIFICATION list with AC-id/command/ran_this_session/exit_code/first_error_line/verdict).
   - Cross-check FILES_MODIFIED: for each entry, run `git status --porcelain -- <path>` via `exec.CommandContext`; empty output on a "created|modified" claim → mismatch.
   - Cross-check AC_VERIFICATION: for each entry claiming `exit_code: 0`, verify exact `command` string appears in `SessionTranscript` as a bash tool input. No match → mismatch.
   - On mismatch: publish `descent.pre_completion_gate_failed` with `mismatch_kind` ∈ `{files_missing, ac_no_evidence, self_assessment_inconsistent}`, return `(false, <short reason>)`.
   - Wire in descent via setting `cfg.PreEndTurnCheckFn` in `descent_bridge.go:buildDescentConfig`. The agentloop `RepairFunc` wrapper passes it to its Anthropic `agentloop.Config`.

   AC: `go test ./internal/plan/... -run TestPreCompletionGateParsing` (four fixtures: pass, missing block, FILES mismatch, AC mismatch); `go test ./internal/agentloop/... -run TestPreEndTurnCheckFn` verifies it is invoked exactly once before `end_turn` is finalized.

4. [ ] **Add per-file repair cap fields + enforcement.** File: `internal/plan/verification_descent.go`:
   - In `DescentConfig` struct (lines 232-291) add: `FileRepairCounts map[string]int` and `MaxRepairsPerFile int`.
   - In config normalization (there is a block near lines 280-291 that defaults `MaxCodeRepairs`; add the same pattern): if `MaxRepairsPerFile <= 0` set to `3`; if `FileRepairCounts == nil` set to `make(map[string]int)`.
   - In T4 loop (lines 519-586), BEFORE `cfg.RepairFunc(...)`:
     1. Derive `targetFiles []string` from `verdict.CodeFix.TargetFiles` (add this field to `ReasoningVerdict` if not present) or fallback parser on stderr.
     2. For each file: `if cfg.FileRepairCounts[f] >= cfg.MaxRepairsPerFile` → publish `descent.file_cap_exceeded{file:f, attempts: cfg.FileRepairCounts[f]}` via `bus.Publish`; call `cfg.OnLog(fmt.Sprintf("[descent] file cap hit: %s", f))`; set `result.Passed = false`, `result.Reason = "per-file repair cap exceeded: "+f`; `return result, nil` (skip T5+).
     3. Otherwise increment `cfg.FileRepairCounts[f]++` for every file in `targetFiles` and proceed.
   - After a successful re-run of the AC (existing pass branch), reset `cfg.FileRepairCounts[f] = 0` for those files.
   - Add bus subscriber test: event payload shape `{session_id, task_id, ac_id, file, attempts, last_errors[]}`.

   AC: `grep -q "MaxRepairsPerFile" internal/plan/verification_descent.go`; `go test ./internal/plan/... -run TestPerFileRepairCap` passes — cap-hit fixture never reaches T5.

5. [ ] **Bootstrap-per-cycle wrapper.** File: `cmd/r1/descent_bridge.go` (lines 74-99 host current RepairFunc):
   - Wrap the existing `cfg.RepairFunc`: capture `preSHA := gitHead(ctx, sess.RepoRoot)` before calling the inner func.
   - After inner returns (success or error), compute `changed := gitDiffNames(ctx, sess.RepoRoot, preSHA, "HEAD")`. Helpers go in `cmd/r1/descent_bridge.go` or `internal/worktree/` (reuse if present).
   - Manifest list: `["package.json","pnpm-lock.yaml","go.mod","go.sum","Cargo.toml","Cargo.lock","requirements.txt","pyproject.toml","uv.lock","poetry.lock"]`.
   - Intersect; if non-empty, call new `plan.EnsureWorkspaceInstalledOpts(ctx, sess.RepoRoot, plan.InstallOpts{Force:true, Frozen: lockfilePresent(sess.RepoRoot)})` and publish `descent.bootstrap_reinstalled`.
   - Extend `internal/plan/acceptance.go` with `EnsureWorkspaceInstalledOpts(ctx, root, opts InstallOpts)`:
     - `opts.Force` bypasses `installedOnceMu`+`installedOnce` guard.
     - `opts.Frozen` switches pnpm to `pnpm install --frozen-lockfile`, npm to `npm ci`, cargo to `cargo fetch --locked`, uv to `uv sync --frozen`.
     - All other logic (registry validation, 3-min timeout, fallback order) unchanged.
   - `lockfilePresent(root)`: returns true if any of `pnpm-lock.yaml / package-lock.json / yarn.lock / Cargo.lock / uv.lock / poetry.lock / go.sum` is present.

   AC: `grep -q "descent.bootstrap_reinstalled" cmd/r1/descent_bridge.go`; `go test ./internal/plan/... -run TestBootstrapReinstallOnManifestChange` — stub git diff returns `package.json` → install invoked + event emitted; empty diff → no invoke.

6. [ ] **`report_env_issue` tool.** Locate the worker tool registry — run `grep -rn "tool_name\|ToolDefinition\|registerTool" internal/harness/tools/` before editing. Add the JSON-schema-backed tool (schema in Business Logic §5). Handler:
   - Accept `{issue, workaround_attempted, suggestion}`; require `issue`.
   - Publish `bus.Event{Kind:"worker.env_blocked", Data: map[string]any{"session_id":sess.ID,"task_id":task.ID,"ac_id":currentACID,"issue":...,"workaround_attempted":...,"suggestion":...}}`.
   - Write marker to session scratch (e.g., `sess.Scratch["env_blocked:"+acID] = issue`) so T3 reads it.
   - Return tool-result `"reported"`.
   - In `internal/plan/verification_descent.go` T3 (lines 446-512), at the top: `if issue, ok := sess.Scratch["env_blocked:"+ac.ID]; ok { verdict.Category = "environment"; verdict.Reasoning = "report_env_issue: "+issue; return verdict, nil }` — short-circuits the 5-LLM multi-analyst path.
   - Document the tool in worker prompt's tool list (if such a list is rendered into the system prompt).

   AC: `grep -q "report_env_issue" internal/harness/tools`; `go test ./internal/plan/... -run TestEnvBlockerFastPath` — invoking the tool then running descent on the AC produces `Category=environment` with 0 provider calls made (inject a counting provider).

7. [ ] **Ghost-write detector.** Extend `autoExtractTaskSupervisor` (callsites listed in RT-STOKE-SURFACE §2) to emit a post-tool rule. Rule semantics:
   - After any tool whose name matches `/^(edit|write|str_replace|create_file|apply_patch)$/` returns success with a `path` (or `file_path`, `target_file`) field in its input or output → run `test -f "$P" && [ -s "$P" ]` via the existing bash tool in a "pre-next-turn" MidturnCheckFn injection.
   - If the check exits non-zero: publish `descent.ghost_write_detected{path:P, size_bytes:0}`, inject a user-role reminder (see Business Logic §6 text), and DO NOT increment any retry counter beyond `MaxConsecutiveErrs`.
   - Wire via `agentloop.Config.MidturnCheckFn` (loop.go existing hook). The supervisor-rule machinery already funnels through that seam — extend `toEngineSupervisor` to translate this rule into a `MidturnCheckFn` closure.

   AC: `grep -q "descent.ghost_write_detected" cmd/r1` (either sow_native.go or harness/tools); `go test ./cmd/r1/... -run TestGhostWriteRetry` — fake edit tool that creates an empty file triggers reminder + event; normal edit is silent.

8. [ ] **Bus + streamjson plumbing.** Ensure every new event kind (`descent.file_cap_exceeded`, `worker.env_blocked`, `descent.ghost_write_detected`, `descent.bootstrap_reinstalled`, `descent.pre_completion_gate_failed`) is:
   - Added to the documented event catalog in `internal/bus/bus.go:31-69` comments.
   - Mirrored onto streamjson emitter by adding a subscriber in `cmd/r1/descent_bridge.go` (or a small new `cmd/r1/descent_bus_bridge.go`) that translates bus events to `emitter.EmitSystem(subtype:"stoke.descent.<kind>", data: ...)`. Matches decision C1 (extend streamjson, no new package).
   - Test: `go test ./internal/bus/... -run TestDescentEventNames` asserts every event kind has a catalog entry.

   AC: `go test ./internal/bus/... -run TestDescentEventNames`.

9. [ ] **Run the CI gate.** `go build ./cmd/r1 && go vet ./... && go test ./...`. Any failure → fix; new code must not break existing packages.

## Rollout

- `STOKE_DESCENT=1` remains opt-in (decision D-2026-04-20-02). All six hardenings are gated behind this flag — they only activate when descent runs.
- `TRUTHFULNESS_CONTRACT` and `PRE_COMPLETION_GATE` injection is **always-on** regardless of `STOKE_DESCENT` (decision C4: no opt-out). The supporting gate *enforcement* (parser + descent T4 forcing) only runs when descent is engaged, so unflagged sessions see the contract but don't get the post-hoc check.
- Flip `STOKE_DESCENT=1` to default-on when: 14 consecutive days of ladder runs regression-free against the bench golden set (`bench/`), plus operator sign-off. Follow-up PR owns the flag flip.

## Metrics

| Item | Metric | How measured | Target |
|------|--------|--------------|--------|
| TRUTHFULNESS_CONTRACT | Stub-rate at source (TODO/FIXME/NotImplementedError tokens introduced per 100 sessions) | Tag each session with `scan/` stub-count delta; compare 14-day windows pre/post | −10-20% (RT-06 MASK 8-12% lift baseline) |
| PRE_COMPLETION_GATE | Count of `descent.pre_completion_gate_failed` events per 100 sessions; false-PASS rate on AC | Bus event counter + downstream verify/build re-run | >0 detections week 1 (proves wiring); declining trend thereafter |
| Per-file cap | `descent.file_cap_exceeded` rate; stuck-loop session duration p95 | Bus event counter; session duration histogram | Duration p95 drops by the cap-hit count × avg loop cost (~30s × 3) |
| Bootstrap per cycle | `descent.bootstrap_reinstalled` count; install-miss induced AC fails (baseline vs now) | Bus counter + AC verdict diff | Install-miss AC failures → 0 after rollout |
| `report_env_issue` | Tool invocation count; T3 multi-analyst calls avoided | Tool usage counter × $0.10/AC | ~$0.10 × invocations saved per 14-day window |
| Ghost-write | `descent.ghost_write_detected` count; empty-file commits reaching merge | Bus counter + `atomicfs/` size-zero commit check | >0 detections week 1; 0 empty-file commits merged post-rollout |

Emit a weekly report via `bench/` integration summarizing all six metrics.

## Evidence for reviewer

- **MASK benchmark (arXiv:2503.03750v3):** "Developer system prompts explicitly encouraging honesty improved scores 8-12% on smaller models." Conclusion: prompt contracts produce bounded but real gains; deterministic detection (stub scan, content-faithfulness judge, per-file cap, ghost-write check) is still required. Justifies items 1-2 and the "always-on" no-opt-out policy (C4).
- **Devin 2.0 "Truthful and Transparent" (Sep 2025 revision, CL4R1T4S/DEVIN/Devin2_09-08-2025.md):** The Truthful and Transparent block was added between Devin 2.0 initial release and the Sep 2025 revision — Cognition patched it in *because* they measured deceptive behavior. Our `TRUTHFULNESS_CONTRACT` copies the same anti-fake/anti-stub spirit verbatim.
- **Devin `<think>` pre-completion rule (DEVIN/Devin_2.0_Commands.md):** "Before telling the user that you have completed the task. You need to reflect on whether you actually fulfilled the full intent … you should have verified that you successfully edited all relevant locations …". Our `PRE_COMPLETION_GATE` is the machine-parseable evolution of this.
- **Factory DROID "Proving Completeness & Correctness" (FACTORY/DROID.txt):** "Create a non-draft PR ONLY when: Dependencies successfully installed (frozen/locked) with evidence". Justifies the `--frozen-lockfile` default in item 5 when a lockfile exists.
- **Berkeley "How We Broke Top AI Agent Benchmarks" (2025):** agents achieved 89/89 by trojaning evaluators and reading gold answers. "Prompt-level defenses alone are insufficient." Validates the layered approach — contract + parser + cap + bootstrap + env tool + ghost-write.
- **Cursor 2.0 per-file repair cap (CURSOR/Cursor_Prompt.md verbatim):** "DO NOT loop more than 3 times on fixing linter errors on the same file. On the third time, you should stop and ask the user what to do next." Our `MaxRepairsPerFile=3` is the direct adoption; no silent skip on cap-hit — we fail AC explicitly.
