# Spec Review: stoke-v2-enhancement.md

**Spec:** `specs/stoke-v2-enhancement.md`
**Reviewed:** 2026-04-03
**Reviewer:** Automated spec quality review (10 checks)

---

## Check 1: SELF-CONTAINED ITEMS

**Rating: MINOR**

Most items are well-written and self-contained. A subagent could implement the majority of them with only the item's text. However, several items have cross-references that omit necessary context:

- **A2.1** references "the Stop hook (A1.1)" but does not restate what the Stop hook is or its protocol. A subagent working on A2 would need to read A1 first.
- **A2.2** says "proceed to the retry loop with a targeted retry brief" but does not restate how the retry loop works (it is defined in `workflow.go`'s existing execute+verify loop). A subagent could infer this from reading the code, but the item should say "append to the existing retry loop in workflow.go's execute+verify cycle."
- **A3.2** references "standard scan" without restating what it is. Should specify: "the existing `scan.ScanFiles(handle.Path, scan.DefaultRules(), modifiedFiles)` call."
- **A6.2** references `worktree.DiffLineCount()` which is defined in A6.3. The item says "(new helper)" which is sufficient indication, but a subagent implementing A6.2 before A6.3 would not have the function available.
- **B2** ("Extends A3 with additional patterns") is explicitly a continuation of A3.1. If a subagent picks up B2 without A3 being complete, `SlopRules()` does not exist yet. The item says "Add these additional slop rules to `SlopRules()` in `scan/slop.go`" which implies the file exists. This is correctly handled by dependency ordering (A3 has higher priority), but the item itself does not warn about this prerequisite.

**Recommendation:** Add a `Requires:` line to items that depend on other items being complete. This is especially important for B2 (requires A3) and A2 (requires A1.1).

---

## Check 2: FILE PATHS

**Rating: PASS**

Every item specifies exact file paths. The spec consistently names both the target file and any new files to create. Examples of good practice:

- A1.6: "New files: `stop.sh`, `session-start.sh`, `session-end.sh`, `post-tool-failure.sh`, `pre-compact.sh`"
- B1.1: "Create `stoke/internal/scan/hashline.go`"
- D1.1: "Create `stoke/internal/notify/notify.go`"
- A6.3: "Add `DiffLineCount(...)` function to `worktree/helpers.go`"

All referenced files were verified to exist in the codebase at the paths stated (e.g., `stoke/internal/hooks/hooks.go`, `stoke/internal/config/policy.go`, `stoke/internal/workflow/workflow.go`, `stoke/internal/engine/types.go`).

Two items reference `main.go` without the full path -- this is `stoke/cmd/stoke/main.go` in every case. A subagent could locate it via the `cmd/stoke` convention, but explicit full paths would be cleaner.

---

## Check 3: TESTABILITY

**Rating: PASS**

Every feature group includes explicit test items (A1.8, A2.4, A3.4, A4.5, A5.6, A6.6, A7.4, B1.6, B2.2, B4.2, C1.4, D1.6, D2.5, E1.5, E2.4, F1.3, F2.5, F3.5). Test items describe specific scenarios with expected outcomes:

- A2.4: "mock agent that exits WITHOUT the promise string -> verify workflow detects missing promise and retries"
- B1.6: "Read file -> record hash -> modify file externally -> attempt Edit -> hook blocks"
- A4.5: "verify deterministic rules get confidence 100, slop rules get appropriate confidence, filtering at threshold 80 removes low-confidence findings"

The success criteria are clear enough that a reviewer could determine "done" from the test item alone.

---

## Check 4: DEPENDENCY ORDERING

**Rating: MINOR**

Items within each category are correctly ordered (e.g., A1 before A2, since A2 depends on the Stop hook from A1.1). The priority annotations (P0, P1, P2, P3) also form a reasonable build order.

**Issues:**

- **B2 depends on A3** (B2 adds patterns to `SlopRules()` which A3 creates). B2 is in Category B while A3 is in Category A. The priority both say P0. If a builder processes by category rather than priority, B2 would fail. The spec should either move B2 into Category A or add an explicit `Depends: A3` annotation.
- **A6.2 depends on A6.3** (calls `worktree.DiffLineCount()` defined in A6.3). Within A6, item 2 uses a function from item 3. Should be reordered: A6.3 before A6.2, or merged.
- **A2 depends on A1.1** (uses Stop hook). Cross-category dependency is documented in A2's description but not in the checklist item itself.
- **A3.2** references integration with the verify pipeline which depends on the scan infrastructure from A3.1. This is correctly ordered within A3.

**Recommendation:** Add explicit `Depends:` annotations and consider reordering A6.3 before A6.2.

---

## Check 5: SCOPE CREEP

**Rating: MINOR**

Most items directly serve the stated goal of competitive superiority. A few items are borderline:

- **D2 (HTTP/SSE Server)** is P2 and adds significant surface area (6 endpoints, SSE streaming, EventBus, CORS, authentication). This is a substantial subsystem for a CLI tool. The justification (remote monitoring, web dashboards) is valid for production use but is gold-plating relative to core orchestration quality. The spec correctly marks it P2, so it is not urgent, but the implementation cost is high relative to its anti-laziness impact.
- **E1 (Plugin System)** is P3 and is a large scope item (manifest format, registry, CLI commands, hook aggregation, scan rule loading). This is a significant architectural decision that could be its own spec. Including it here risks under-specifying the security boundaries of plugin execution.
- **D3 (Enhanced TUI)** items D3.3 and D3.4 are incremental polish that could ship independently. Including them in a "competitive superiority" spec is fine but they are not blockers.
- **B3 (Intent Verbalization)** is a prompt-only change with no measurable verification. It is low-risk but its impact is hard to quantify.

**No items are clearly out of scope.** The P2/P3 items are appropriately deprioritized.

---

## Check 6: MISSING EDGE CASES

**Rating: FAIL**

Several features introduce failure modes that are not addressed:

1. **Hook timeout (A1):** The spec adds 5 new hook events but does not specify timeout behavior. The existing hooks run as bash subprocesses. What happens if a `Stop` hook hangs? A `PreCompact` hook that takes 30 seconds? The spec should define:
   - Maximum hook execution time (e.g., 10 seconds)
   - Behavior on timeout (kill + fail-open or fail-closed?)
   - Whether hooks run sequentially or in parallel

2. **Hash verification file corruption (B1.3):** The `$RUNTIME_DIR/file-hashes.json` file is read/written by hook scripts (bash) and potentially by concurrent worktrees. What happens if:
   - Two hooks write to the file simultaneously (race condition)?
   - The JSON file is corrupted mid-write (crash)?
   - The file grows unbounded for a long session with many reads?
   The spec should define locking semantics or use atomic write (which is partially addressed by the existing `safeWrite` pattern, but not mentioned for this new file).

3. **Notification failures (D1):** Item D1.1 says "Log failures but don't block execution" which is good. However:
   - What if the webhook URL is unreachable at startup? Does `stoke notify --test` validate this?
   - What if the webhook returns a 4xx (permanent failure) vs 5xx (transient)? Should transient failures retry more than once?
   - Rate limiting from Discord/Slack webhooks (429) is not addressed.

4. **Context exceeded recovery (F2.2):** The recovery flow "saves current progress (committed changes + plan state)" but does not specify:
   - What if the context exceeds on the first tool call (no progress to save)?
   - What happens to the file-hash map (B1.3) in the old worktree?
   - Does the recovery count toward `maxAttempts` or is it tracked separately? (F2.4 says "separate from retry attempts" but the flow in F2.2 does not check this limit.)

5. **Completion promise false negatives (A2.2):** Exact string matching for the promise text is fragile. What if the agent outputs the promise with minor formatting differences (extra whitespace, markdown formatting around it)? The spec should define whether matching is exact or normalized.

6. **Plugin security boundary (E1):** Item E1.3 says "A blocking hook from any source (built-in or plugin) blocks the action." This means a malicious plugin can deny-of-service the entire workflow by blocking all tool uses. The spec should define whether built-in hooks have priority or whether plugins can only add restrictions (not override built-in allows).

**Recommendation:** Add timeout + failure-mode subsections to A1, B1, D1, and F2. Address the promise matching semantics in A2. Define plugin trust boundaries in E1.

---

## Check 7: BACKWARD COMPATIBILITY

**Rating: PASS**

The spec explicitly calls out backward compatibility as Ship Blocker #4: "New features must not break backward compatibility with existing `stoke.policy.yaml` files."

Analysis of changes to `config.Policy`:

- **A4.3** adds `ConfidenceThreshold int` with default 80. Existing YAML without this field will deserialize to Go's zero value (0), which would filter nothing (all findings pass). The spec says default is 80 but does not state whether the default is applied when the field is absent. **This needs clarification**: the `DefaultPolicy()` function should set it, and YAML parsing should merge with defaults. Currently `DefaultPolicy()` in `config/policy.go` does not have this field.
- **D1.3** adds a `Notifications` section. Existing YAML without this section will have zero-value fields (empty URL, empty platform). Since the notifier is created from config, this means "no notifications" which is correct behavior.
- **A6.4** adds a `--verification-tier` flag. Default is `auto` which preserves existing behavior.

The hook system changes (A1) are all additive -- new events, new scripts. Existing `pre-tool-use.sh` and `post-tool-use.sh` are unchanged. The `HooksConfig()` function adds new entries but existing entries are preserved.

No breaking changes detected. The one caution is A4.3's default handling.

---

## Check 8: SECURITY

**Rating: MINOR**

Most security considerations are well handled, but a few deserve attention:

1. **A7 (Hook Tool Input Modification):** Allowing hooks to rewrite tool inputs is powerful but introduces an injection vector. If a hook returns `{"decision":"modify","tool_input":"<attacker-controlled JSON>"}`, the agent will execute with the modified input. The spec addresses this partially -- A7.4 says invalid JSON should block the tool (fail-safe). However:
   - Who controls what hooks are installed? If plugins (E1) can install hooks that modify inputs, a malicious plugin could redirect file writes to arbitrary paths.
   - The spec should mandate that modified inputs still pass through the existing protected-file checks (i.e., modification happens before the guard checks, not after).

2. **D2 (HTTP/SSE Server):** The spec mentions `--serve-token` for bearer auth (D2.4) and Ship Blocker #6 about not leaking API keys. However:
   - The `/api/tasks/{id}` endpoint returns "detailed task state including ClaimedVsVerified" -- does this include the task prompt, which could contain sensitive business logic?
   - The `/api/pools` endpoint returns "pool snapshot (utilization, status, circuit breaker state)" -- it must NOT return pool credentials (API keys, config dirs).
   - The spec should explicitly list what fields are excluded from API responses.

3. **B1.3 (file-hashes.json):** Stored in `$RUNTIME_DIR` which is outside the worktree. Good. But if `RUNTIME_DIR` is on a shared filesystem (e.g., `/tmp`), another user could tamper with the hash file to allow stale edits. The existing worktree RuntimeDir uses `/tmp/stoke-runtime-*` in dry-run mode. The spec should mandate that `file-hashes.json` has restrictive permissions (0600).

4. **E1 (Plugin System):** Plugin manifests can specify arbitrary script paths for hooks. The spec does not define:
   - Whether plugin hook scripts are validated (e.g., no symlinks, no paths outside the plugin directory)
   - Whether plugins run with the same permissions as the main process
   - Whether plugin hooks have access to pool credentials in the environment

**Recommendation:** Mandate that A7 modifications pass through existing guards. Explicitly exclude credentials from D2 API responses. Define plugin sandboxing in E1.

---

## Check 9: IMPLEMENTATION FEASIBILITY

**Rating: PASS**

All items are technically feasible in Go:

- **B1 (Hash-Anchored Editing):** xxHash32 is straightforward. `github.com/cespare/xxhash/v2` provides xxHash64; truncating to 32 bits is trivial. The spec also offers inline implementation as an alternative. The `go.mod` currently has no xxhash dependency, so B1.5 correctly identifies the dependency addition.
- **D2 (HTTP/SSE Server):** Go's `net/http` stdlib is well-suited for this. SSE is simply long-lived HTTP responses with `text/event-stream` content type. No external dependencies needed.
- **A1 (Hook Lifecycle):** Claude Code supports `Stop`, `PreToolUse`, `PostToolUse` and other hook events natively. The spec correctly references claude-code-1's 9 events. However, **one concern**: the spec says to add `PreCompact` (A1.5) but Stoke drives Claude Code as a subprocess (`claude -p`). Compaction happens inside Claude Code's process, not in Stoke's harness. The spec says to fire PreCompact from `context/context.go Compact()` -- but that is Stoke's internal context manager, not Claude Code's context window. This is feasible but the naming is misleading: it fires before Stoke compacts its own context (retry briefs, session data), not before Claude Code compacts its conversation.
- **F3 (Multi-Provider Expansion):** Adding a Gemini runner follows the same `CommandRunner` interface pattern as Claude and Codex. The interface is simple (Prepare + Run). Feasible.
- **A5 (Fanout-Validate-Filter):** Spawning validation agents in parallel is straightforward with goroutines. The cost concern (using cheaper models for validation) is mentioned but not specified how -- the existing `model.Resolve()` picks models by task type, not by cost tier.

**One concern about A1.5 (PreCompact):** The spec says hooks can "inject critical context that must survive compaction by writing to stdout. The compacted context should include hook-injected content." This means the `Compact()` method must call a bash subprocess, wait for output, parse it, and inject it into the compaction result. The existing `Compact()` is a pure in-memory operation. Adding subprocess execution here is a design change that could cause performance issues if compaction runs frequently.

---

## Check 10: COMPLETENESS

**Rating: MINOR**

The spec references "8 research reports totaling 200+ pages" and sources from 7 competing projects. Comparing the spec against the research findings in RT-8 (best practices):

**Covered well:**
- Tiered verification scaling (RT-8 item 3) -> A6
- AI slop detection (RT-8 item 3) -> A3, B2
- Hook expansion (RT-2, RT-4, RT-6) -> A1, A7
- Notification system (RT-4, RT-5) -> D1
- Container detection (RT-1, RT-6) -> F1
- Session recovery (RT-2) -> F2

**Researched but NOT included (potentially high-impact):**

1. **Agent self-reflection / REFLECTION.md protocol** (RT-8 item 1, rated highest impact in best-practices research): After each task, the agent writes a structured reflection on what worked and what did not. This feeds into future planning. Stoke has a `wisdom.Store` but the spec does not enhance it with structured task-level reflection. Given it was rated #1 in RT-8's gap analysis, its omission is notable.

2. **Complexity-based model routing within task types** (RT-8 item 2): The research recommends using frontier models for complex tasks and mid-tier models for simple ones within the same task type. The spec adds `A6 (tiered verification)` but not tiered model selection based on complexity. The existing `model.Resolve()` routes by task type, not by complexity.

3. **Compound learning validation** (RT-8 item 4): Research shows LLM-generated wisdom can degrade performance over time. The spec does not add validation or pruning to the existing `wisdom.Store`.

4. **Background task execution** (RT-8 item 7): Running build/test in the background (non-blocking) while the agent continues. Not addressed in the spec.

5. **MCP-based tool extension** (RT-8 item 8): Using MCP as the standard for tool extension instead of bash-script plugins. The spec uses E1's manifest-based plugin system instead. The research notes MCP is the emerging industry standard.

6. **Inter-agent messaging** (RT-8 gap analysis for Claude Code Agent Teams): Agents can't ask each other questions mid-task. Rated low priority in RT-8, so its absence is defensible.

**Recommendation:** The self-reflection protocol (RT-8 #1) and complexity-based model routing (RT-8 #2) are the highest-impact omissions. Consider adding them as P1 items or documenting them as explicit non-goals with justification.

---

## Summary

| Check | Rating | Notes |
|-------|--------|-------|
| 1. Self-Contained Items | MINOR | Cross-references in A2, A3.2, B2 need `Requires:` annotations |
| 2. File Paths | PASS | All items specify exact files |
| 3. Testability | PASS | Every feature has explicit test items with scenarios |
| 4. Dependency Ordering | MINOR | B2->A3 cross-category dep; A6.3 should precede A6.2 |
| 5. Scope Creep | MINOR | D2 and E1 are large; correctly deprioritized |
| 6. Missing Edge Cases | FAIL | Hook timeouts, hash file races, promise matching, plugin trust boundaries |
| 7. Backward Compatibility | PASS | One caution on A4.3 default handling |
| 8. Security | MINOR | A7 injection via modified inputs, D2 response filtering, E1 plugin sandboxing |
| 9. Implementation Feasibility | PASS | A1.5 PreCompact subprocess concern is minor |
| 10. Completeness | MINOR | Self-reflection protocol and complexity routing are high-impact omissions |

## Overall Readiness Assessment

**NOT READY -- 1 blocking issue (Check 6: Missing Edge Cases)**

The spec is well-structured, thoroughly sourced, and covers an impressive scope. File paths are precise, test items are specific, and priority ordering is sound. The single FAIL is Check 6: several features introduce failure modes (hook timeouts, hash file concurrency, promise matching semantics, plugin trust boundaries) that must be addressed before implementation begins.

**To reach READY status:**
1. Add hook timeout behavior to A1 (max duration, kill semantics, fail-open vs fail-closed)
2. Add concurrency/corruption handling to B1.3 (file locking or atomic writes for file-hashes.json)
3. Specify promise matching semantics in A2.2 (exact vs normalized)
4. Define plugin trust boundaries in E1 (what can plugins block/modify, credential access)
5. Specify context-exceeded recovery interaction with max attempts in F2.2

The 4 MINOR findings are non-blocking but should be addressed for implementation quality: add `Requires:` annotations for cross-references, reorder A6.3 before A6.2, specify A4.3 default field handling, and consider adding self-reflection protocol from RT-8's #1 recommendation.
