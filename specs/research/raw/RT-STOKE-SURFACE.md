# RT-STOKE-SURFACE: Current State Map (Commit 8611d48)

Dense reference document mapping the current Stoke codebase state for 7 upcoming specs.

## 1. VERIFICATION DESCENT ENGINE

**Location**: `internal/plan/verification_descent.go:26-1017`  
**Status**: FULLY IMPLEMENTED AND WIRED  
**Feature flag**: `STOKE_DESCENT=1`

### 8-Tier Ladder

| Tier | Name | Lines | Purpose | Condition |
|------|------|-------|---------|-----------|
| T1 | Intent Match | 405-428 | Reviewer confirms code matches spec intent | `IntentCheckFunc` callback |
| T2 | Run AC | 430-444 | Execute AC command; if exit 0 → PASS | Initial check via `runACCommand` |
| T3 | Classify Failure | 446-512 | Deterministic stderr + multi-analyst reasoning | `ClassifyStderr` + optional LLM |
| T4 | Code Repair | 514-608 | Dispatch repair worker, re-run AC, loop up to MaxCodeRepairs (default 3) | `RepairFunc` callback |
| T5 | Environment Fix | 610-655 | Attempt pnpm install / apt-get for missing deps | `EnvFixFunc` callback |
| T6 | AC Rewrite | 657-685 | Apply rewritten AC command from T3 analyst | `analysisACRewrite` non-empty |
| T7 | Refactor | 687-734 | Dispatch refactor worker for verifiability; re-check intent | `RefactorAttempted` flag |
| T8 | Soft-Pass | 736-820 | All tiers exhausted, intent confirmed, category ≠ code_bug → approve | 6-gate evaluation |

### DescentConfig (lines 232-291)

**ALL FIELDS WITH DEFAULTS:**

```go
Provider provider.Provider              // LLM for multi-analyst; nil = skip
Model string                            // reasoning model (default "claude-sonnet-4-6")
RepoRoot string                         // workspace root
Session Session                         // current session
MaxCodeRepairs int                      // T4 loop limit (default 3, resets if ≤0 or >5)
RepairFunc func(ctx, directive) error  // T4/T7 dispatch; nil = skip T4/T7
EnvFixFunc func(ctx, cause, stderr) bool // T5 environment fix; nil = skip T5
IntentCheckFunc func(ctx, ac) (bool, string) // T1 intent approval; nil = assume true
BuildCleanFunc func(ctx) bool           // T8 prerequisite; nil = assume true
StubScanCleanFunc func(ctx) bool        // T8 prerequisite; nil = assume true
AllOtherACsPassedFunc func(acID) bool   // T8 prerequisite; nil = assume true
UniversalPromptBlock string             // coding-standards context
OnLog func(msg string)                  // progress logging; nil = discard
```

### Key Functions

**runACCommand** (lines 829-832):
```go
func runACCommand(ctx context.Context, projectRoot string, ac AcceptanceCriterion) (string, bool)
```
Wrapper over `checkOneCriterion`; called from T2, T4, T5, T6, T7.

**runDescentReasoning** (lines 834-879):  
Dispatches multi-analyst via `ReasonAboutFailure` (sow_reason.go:105).

**Multi-analyst reasoning** (5 LLM calls):
- A1 code-review (sow_reason.go:133)
- A2 ac-hygiene (sow_reason.go:139)
- A3 root-cause (sow_reason.go:145)
- A4 ac-rewrite (sow_reason.go:150+)
- Judge synthesis (sow_reason.go:155+)

**ReasoningVerdict fields** (sow_reason.go:20-51):
- `Category` (code_bug | ac_bug | both | acceptable_as_is)
- `Reasoning` (synthesis explanation)
- `CodeFix` (T4 directive, optional)
- `ACRewrite` (T6 replacement command, optional)
- `AnalystNotes` (map[string]string with raw analyst outputs)

### Soft-Pass Logic (lines 736-820)

**ALL 6 GATES MUST PASS:**

1. At least one active resolution (T4|T5|T6|T7) — lines 748-758
2. Intent confirmed (`IntentConfirmed == true`) — lines 761-767
3. **Category ≠ code_bug** (HARD RULE: code_bug NEVER soft-passes) — lines 769-775
4. Build clean — lines 777-784
5. Stub scan clean — lines 786-793
6. Other ACs passed — lines 795-802

**No approval hook beyond T1.** Soft-pass automatic when all gates pass.

### AcceptanceCriterion Struct (sow.go:87-96)

```go
type AcceptanceCriterion struct {
    ID          string                   // unique identifier
    Description string                   // human-readable requirement
    Command     string                   // optional: shell command (exit 0 = pass)
    FileExists  string                   // optional: path must exist
    ContentMatch *ContentMatchCriterion  // optional: file + regex
}

type ContentMatchCriterion struct {
    File    string  // file path
    Pattern string  // substring or regex
}
```

Custom UnmarshalJSON (sow.go:98-146) handles LLM-emitted malformed shapes.

### OnLog Hook Pattern (lines 290-297)

**Signature**: `OnLog func(msg string)`  
**Called at**: Every tier decision (lines 424, 440, 444, 454, 460, 478, 511, 527, 546, 554, 560, 566, 579, 598, 620, 630, 633, 645, 664, 678, 681, 697, 716, 720, 727, 732, 746-802)

### Integration with descent_bridge.go

**Entry**: `runDescentRepairLoop` (descent_bridge.go:295-400+) called when `STOKE_DESCENT=1`

**buildDescentConfig** (descent_bridge.go:37-287):
- RepairFunc (lines 74-99): Dispatch repair via `execNativeTask` with descent prompts
- EnvFixFunc (lines 104-150): pnpm install (2min) or apt-get (1min)
- OnLog (lines 282-284): Print `[descent SESSION_ID] message`

**AC re-run loop** (lines 326-369):
- Multiple attempts (configurable `maxRepairs` parameter)
- Each runs `CheckAcceptanceCriteriaWithJudge` with semantic judge
- Logs pass/fail per attempt
- Breaks on all-pass or max-attempts

---

## 2. WORKER PROMPT PIPELINE

**Location**: `cmd/stoke/sow_native.go`

### buildSOWNativePromptsWithOpts (lines 3729-3950+)

**Signature**: `func buildSOWNativePromptsWithOpts(sowDoc *plan.SOW, session plan.Session, task plan.Task, opts promptOpts) (string, string)`

**System Prompt Injection Points:**

1. **Repair Mode Signal** (lines 3737-3764)
   - When `opts.Repair != nil`: "You are in REPAIR mode" + COMMON FAILURE CLASSES + TARGET-FILE DISCIPLINE
   - Else: Standard task preamble + self-check + NODE discipline (conditional on `isNodeStack`)

2. **Common Failure Classes** (lines 3740-3749)
   - X: not found → install to devDeps
   - Cannot find module → import/dep issue
   - missing script → add to package.json.scripts
   - node_modules missing → pnpm install
   - file not found → create with real content
   - test collector 0 → test runner not configured
   - Type errors → add @types/<pkg>

3. **Target-File Discipline** (lines 3750-3751)
   - Points to what AC command actually reads
   - Edit target file, not adjacent configs

4. **Node/TS Ecosystem** (lines 3769-3778, conditional)
   - node_modules/.bin on PATH
   - Per-package dependencies
   - Workspace: `"@sentinel/types": "workspace:*"`
   - tsconfig extends `@sentinel/tsconfig/base.json`
   - pnpm `--filter <pkg>`
   - No external network access

5. **Working Directory Anchor** (lines 3787-3790)
   - Absolute path disambiguates relative paths

6. **Project Context** (lines 3792-3831)
   - SOW: name, description, stack, monorepo, infra
   - Session: title, description, inputs, outputs
   - Acceptance criteria list

7. **Repository Map** (lines 3853-3871)
   - Conditional on `opts.RepoMap != nil`
   - Budget: `opts.RepoMapBudget` (default 3000)

8. **Public API Surface** (lines 3879-3885)
   - `sowAPISurface(opts.RepoRoot, 30000)`
   - Exports from prior sessions

9. **Cross-Session Wisdom** (lines 3891-3896)
   - Conditional on `wisdomStore != nil`
   - `wisdomStore.ForPrompt()`

10. **Canonical Names Block** (lines 3906-3909)
    - `buildCanonicalNamesBlock(sowDoc, session, task)`
    - Reinforces exact identifiers from SOW

11. **Skill Injection** (lines 3917-3950+)
    - `skill.DefaultRegistry(opts.RepoRoot)`
    - Match against stack tags + task description + session title
    - Ecosystem playbooks (pnpm-monorepo, react-native, etc.)

### autoExtractTaskSupervisor

**Called**: Lines 939, 1395, 1431, 2580, 3277, 5104, 5434  
**Purpose**: Extract supervisor rules from SOW scoped to (session, task)  
**Output**: Wired to `toEngineSupervisor()` for agentloop format

### TRUTHFULNESS CONTRACT

**Status**: NOT YET IMPLEMENTED  
**Where**: After canonical-names (line 3909), before skills (line 3918)

### PRE_COMPLETION_GATE

**Status**: NOT YET IMPLEMENTED  
**Options**:
1. agentloop.Config.PreEndTurnCheckFn (agentloop/loop.go:77) — hook before end_turn accepted
2. Supervisor rule via autoExtractTaskSupervisor + MidturnCheckFn

### Repair-Dispatch Prompt (lines 3737-3764)

**When**: `opts.Repair != nil` (pointer to repair directive string)  
**System**: Repair preamble + COMMON FAILURE CLASSES + TARGET-FILE DISCIPLINE + rest  
**User**: Wraps directive string  
**Example** (descent_bridge.go:80-89): Repair worker for descent AC fix

---

## 3. TASK JUDGING / REVIEWER

**Location**: `internal/plan/task_judge.go`

### ReviewTaskWork (lines 184-367)

**Signature**: `func ReviewTaskWork(ctx context.Context, prov provider.Provider, model string, in TaskReviewInput) (*TaskWorkVerdict, error)`

**CHECKS (7 TOTAL):**

1. Expected files exist + real content (not stubs) — prompt line 503-504
2. Declared dependencies in manifests — prompt line 502
3. Required scripts (test/build/typecheck/lint) — prompt line 503
4. Live compile errors addressed (GROUND TRUTH, SCOPE EXCEPTION) — lines 296-311
5. Worker summary backed by code — prompt line 504
6. No unfinished-work markers (TODO/FIXME) — prompt line 514
7. Imports reference existing identifiers — prompt line 512

### TaskReviewInput (lines 36-128)

All fields documented with purposes and usage in reviewer logic.

### TaskWorkVerdict (lines 14-32)

```go
type TaskWorkVerdict struct {
    Complete            bool
    Reasoning           string        // with file:line references
    GapsFound           []string      // one sentence per gap
    FollowupDirective   string        // next step (only if Complete=false)
}
```

### Adaptive Bar Raising (lines 314-335)

- PriorAttempts 0-2: Normal bar
- 3+: "Only concrete blockers"
- 5+: "Raise bar, already tried"
- 10+: "About to give up"

---

## 4. CONTENT-FAITHFULNESS JUDGE

**Location**: `internal/plan/content_judge.go` (FULLY IMPLEMENTED)

### JudgeDeclaredContent (lines 54-62)

**Purpose**: Catch hardcoded responses, empty schemas, trivial re-exports, copy-pasted sibling code

**When called** (lines 18-22): Defensively, only "zombie-already-done" state (zero writes, all files pre-exist)

### ContentJudgeVerdict (lines 37-52)

```go
type ContentJudgeVerdict struct {
    Real      bool    // true = substantive, false = placeholder
    Reason    string  // one or two sentences
    FakeFile  string  // most obvious fake (when Real=false)
}
```

**Default**: Real=true when uncertain (non-gating)

### Detection Rules (lines 109-112)

- real=true: Substantive logic matching spec
- real=false: Single-line, hardcoded, empty body, copy-pasted identical, re-export pretending
- DEFAULT: real=true (non-gating philosophy)

### Per-File Budget (line 82)

6000 chars per file; truncated with `... (truncated)` marker

---

## 5. DECLARED-SYMBOL SCANNER

**Location**: `internal/plan/declared_symbols.go` + `internal/plan/declared_symbols_treesitter.go`

### ScanDeclaredSymbolsNotImplemented (lines 81-82)

**Purpose** (H-27): Extract named deliverables from SOW prose, verify they appear as defined symbols in code

**Patterns** (lines 62-68):
- "the acknowledgeAlarm handler"
- "a XxxSchema class|struct|type|enum|interface|trait"
- "an AuthContext component|provider|hook"
- "export(s) ... function|class|type|const XxxFoo"
- Backtick-quoted: `` `doSomething` ``

**Scope** (H-51, lines 103-115):
- Scans WHOLE repo's tracked source files, not just diff
- "not implemented" = missing from REPO, not missing from commit
- Falls back to changed-files-only when git unavailable

**Verdict Level** (H-83, lines 144-150):
- Downgraded to Major (advisory), not Blocking
- Reason: Prose false-positives (task.Files and ACs are authoritative)
- Override: `STOKE_DECLARED_SYMBOL_BLOCKING=1`

### Symindex Integration (lines 34-39)

- Fallback regex extractor for non-TS/JS/Python files
- Function-var: `ExtractDeclaredSymbolsFallback = extractSymbolsViaSymindex`
- Tree-sitter path (declared_symbols_treesitter.go) for TS/JS/Python AST

---

## 6. INTERACTIVE CHAT

**Location**: `internal/chat/` + `cmd/stoke/chat.go`

### Status: FULLY WIRED AND OPERATIONAL

**Files**:
- `session.go`: Chat session + message history + streaming
- `dispatcher.go`: Dispatcher interface
- `intent.go`: Intent extraction
- `clarify_responder.go`: Q&A handling
- `antisycophancy.go`: Bias mitigation
- `topic_shift.go`: Topic transitions
- `image.go`: Image attachments
- `provider.go`: Provider abstraction

### Wire-up

- `cmd/stoke/chat.go:398` — `buildChatSession(defaults)`
- `cmd/stoke/chat.go:431` — `chatOnceREPL` (streaming REPL)
- `cmd/stoke/chat.go:485` — `chatOnceShell` (TUI shell)
- `cmd/stoke/main.go:5279` — `launchREPL` creates session
- `cmd/stoke/main.go:5571` — `launchShell` creates session

### Functionality

Users type naturally at REPL/shell; chat replies conversationally and dispatches to /scope, /build, /ship, /plan, /audit, /scan, /sow, /status when user agrees.

---

## 7. INTERNAL/MEMORY

**Location**: `internal/memory/`

**Files**: `memory.go`, `tiers.go` (H-47), `contradiction.go` (H-19), tests

**Depth**: Real logic for tiers + contradiction detection

**Integration Status**: NOT WIRED — no imports in sow_native.go, simple_loop.go, main.go

**Retrieval**: NOT IMPLEMENTED — no query/lookup APIs

---

## 8. INTERNAL/DELEGATION

**Location**: `internal/delegation/`

**Files**: `saga.go` (H-26), `delegation.go`, `scoping.go`, tests

**Depth**: Real saga pattern + delegation types

**Integration Status**: NOT WIRED — no calls in session_scheduler.go or task dispatch

---

## 9. INTERNAL/TRUSTPLANE

**Location**: `internal/trustplane/`

**Files**: `client.go` (H-6), `real.go` (H-15), `factory.go`, `dpop/`, `openapi/`, tests

**Depth**: Full OAuth 2.0 DPoP authentication ready

**Integration Status**: NOT WIRED — no imports in core loop

---

## 10. INTERNAL/MCP

**Location**: `internal/mcp/`

**Files**: `client.go`, `codebase_server.go`, `stoke_server.go`, `memory.go`, tests

**Server-side**: FULLY IMPLEMENTED — Stoke exposes MCP interface for Claude Code / other clients

**Client-side**: ABSTRACT ONLY — no actual HTTP/subprocess invocation

**Integration Status**: Server wired, client-side stub

---

## 11. INTERNAL/RESEARCH

**Location**: `internal/research/store.go` + tests

**Depth**: In-memory + disk-persisted research facts; no executor

**Executor**: MISSING — no web-search dispatch, no API calls

**Integration**: NOT WIRED

---

## 12. INTERNAL/BUS

**Location**: `internal/bus/bus.go` + `wal.go`

**Event Types** (lines 31-69):
- Worker: spawned, action.started, action.completed, declaration.done/fix/problem, paused, resumed, terminated
- Ledger: node.added, edge.added
- Supervisor: rule.fired, hook.injected, checkpoint
- Skill: loaded, applied, extraction.requested
- Mission: started, completed, aborted
- Observability: handler.panic, subscriber.overflow, hook.action_failed, hook.injection_failed

**Depth**: COMPLETE — publish/subscribe, hooks, pattern matching, WAL

**Active Publishers**: NONE VISIBLE — no `bus.Publish()` calls in sow_native.go, simple_loop.go

---

## 13. INTERNAL/AGENTLOOP

**Location**: `internal/agentloop/loop.go` + `circuit.go`, `cache.go`, tests

**Depth**: FULLY IMPLEMENTED — native Messages API loop per P61 spec

**Config Fields** (lines 28-79):
- `MaxTurns` (int, default 25)
- `MaxConsecutiveErrs` (int, default 3)
- `MaxTokens` (int, default 16000)
- `SystemPrompt` (string, cached)
- `ThinkingBudget` (int, default 0)
- `CompactThreshold` (int)
- `CompactFn` (message history rewrite hook)
- `MidturnCheckFn` (between-turn supervisor hook)
- `PreEndTurnCheckFn` (build-error check before end_turn)

**Extended thinking**: Signature preserved, defensive bounds, token accumulation

**Integration**: Called by `execNativeTask` via `NativeRunner.Run`

---

## 14. STREAMJSON EVENT EMISSION

**Location**: `internal/streamjson/emitter.go`

**5 Event Types** (Claude Code-compatible NDJSON):

1. EmitSystem (line 63): init, api_retry, compact_boundary
2. EmitAssistant (line 83): LLM reply with message.content[] (text/tool_use/thinking)
3. EmitUser (line 102): Tool results
4. EmitResult (line 126): Terminal (success|error_max_turns|error_during_execution|error_max_budget_usd)
5. EmitStreamEvent (line 151): Delta text during streaming

**Fields**: type, uuid, session_id, subtype, event-specific, `_stoke.dev/*` extensions

**Wiring** (main.go):
- Line 1476-1477: Construct emitter via `--output-format=stream-json`
- Line 1480: EmitSystem("init")
- Line 1304: EmitResult at session end

**Status**: PARTIAL — emitter wired at SOW level, worker/task emit points sparse

---

## 15. BOOTSTRAP / INSTALL LOGIC

**Location**: `internal/plan/acceptance.go:348-404`

### ensureWorkspaceInstalled

**Called from**: `CheckAcceptanceCriteria` (line 264), `CheckAcceptanceCriteriaWithJudge` (line 264)

**Logic**:
1. Guard: `installedOnceMu` + `installedOnce[projectRoot]` per-project
2. Node-only: Return if no package.json
3. Registry validation: `runDepCheck(ctx, projectRoot)` validates declared deps (npm/PyPI/crates/Go)
4. node_modules check: Return if exists
5. Install attempts:
   - Prefer pnpm when pnpm-workspace.yaml (lines 391-395)
   - Fall back pnpm (lines 397-398)
   - Fall back npm (lines 400-401)
   - Each: 3-minute timeout (line 383)
6. Silent on success: Errors ignored

### Per-Descent Install

**Status**: NOT YET IMPLEMENTED

Descent relies on pre-run install. RepairFunc runs in same context, so install must have happened already.

---

## 16. PER-FILE REPAIR COUNTER STATE

### Current Tracking (DescentResult)

**Fields** (verification_descent.go:129-173):
- `CodeRepairAttempts` (int): How many T4 rounds for ONE AC
- No per-file counters; session-level `MaxCodeRepairs` (default 3) only

### Design Option (Not Yet Implemented)

Add to DescentConfig:
```go
FileRepairCounts map[string]int  // file path -> attempt count
MaxRepairsPerFile int            // hard limit per file
```

Usage in T4 (lines 519-586): Check `FileRepairCounts[file] < MaxRepairsPerFile` before RepairFunc

---

## SUMMARY TABLE

| Component | Location | Exists? | Integrated? | Notes |
|-----------|----------|---------|-------------|-------|
| Verification Descent | verification_descent.go:26-1017 | ✓ | ✓ WIRED | STOKE_DESCENT=1 flag |
| Worker Prompts | sow_native.go:3729-3950+ | ✓ | ✓ ACTIVE | 11+ injection points |
| Task Judge | task_judge.go:184-367 | ✓ | ✓ ACTIVE | 7-check logic + bar scaling |
| Content Judge | content_judge.go:54-154 | ✓ | ✓ AVAILABLE | JudgeDeclaredContent function |
| Declared-Symbol Scanner | declared_symbols.go:81+ | ✓ | ✓ ACTIVE | H-27 gate, 11 languages |
| Interactive Chat | chat/ + chat.go | ✓ | ✓ WIRED | Full REPL/TUI integration |
| Memory System | memory/ | ✓ | ✗ NOT WIRED | Stub integration |
| Delegation Framework | delegation/ | ✓ | ✗ NOT WIRED | Stub integration |
| TrustPlane A2A | trustplane/ | ✓ | ✗ NOT WIRED | Real client ready |
| MCP | mcp/ | ✓ | ✓ SERVER, ✗ CLIENT | Server wired, client stub |
| Research Store | research/store.go | ✓ | ✗ NOT WIRED | Storage only, no executor |
| Event Bus | bus/ | ✓ | ✗ UNUSED | Infrastructure complete, no publishers |
| AgentLoop | agentloop/loop.go | ✓ | ✓ ACTIVE | Native Messages API |
| StreamJSON Events | streamjson/emitter.go | ✓ | ✓ PARTIAL | Wired at SOW level, sparse in workers |
| Bootstrap Install | acceptance.go:348-404 | ✓ | ✓ ACTIVE | 3-min timeout, registry validation |
| Per-File Repair Counter | — | ✗ | — | Not in current design |

