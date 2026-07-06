# Gap discovery round 2 — confirmed findings (2026-07-06)

Second audit over subsystems round 1 didn't cover (LLM/provider, MCP/patch, cortex, knowledge, code-analysis, governance/bench). 6 scouts, double-verified. **26 of 30 raw confirmed.** 7 high, 15 medium, 4 low. Concentrated in core primitives: cost accounting (budget bypass), patch/edit application (corruption), and ledger integrity.

## [HIGH] apiclient SSE parser drops input tokens (Input always 0) → cost undercount & budget bypass on native runner
`internal/apiclient/client.go:459` — data-loss — LLM/provider layer (internal/provider, internal/apiclient, internal/model, internal/costtrack, internal/promptcache/microcompact)

**Detail:** parseSSE overwrites finalUsage wholesale on every usage-bearing event instead of merging fields. Anthropic streams input tokens only in `message_start` (parseAnthropicSSE line 510-518 sets Usage.InputTokens) and output tokens only in `message_delta` (line 493-506 sets Usage.OutputTokens). Because `message_delta` arrives last and carries InputTokens=0, `finalUsage = *usage` clobbers the earlier input count. The returned Usage therefore always has InputTokens=0. The native runner consumes this: engine/api_runner.go:196-213 does `if usage.InputTokens > 0 { lastUsage.InputTokens = ... }` — since it's 0, input stays 0 — then computes CostUSD from lastUsage. For agent calls (large context prompts), input tokens are the dominant cost, so CostUSD is undercounted by most of the true spend. Note also that parseSSE never passes `usage` to the handler (only `event`), so the handler's `ev.Usage` in api_runner.go:152 is always nil and can't compensate.

**Evidence:** Line 459-461: `if usage != nil { finalUsage = *usage }` — full struct overwrite, not per-field merge. message_start returns &Usage{InputTokens:N}; message_delta returns &Usage{OutputTokens:M} (InputTokens defaults 0). Last-wins ⇒ finalUsage.InputTokens==0.

**Fix:** Merge non-zero fields instead of overwriting: `if usage != nil { if usage.InputTokens>0 {finalUsage.InputTokens=usage.InputTokens}; if usage.OutputTokens>0 {finalUsage.OutputTokens=usage.OutputTokens}; if usage.CacheRead>0 {finalUsage.CacheRead=usage.CacheRead}; if usage.CacheWrite>0 {finalUsage.CacheWrite=usage.CacheWrite} }`. Also capture cache_read/cache_creation in parseAnthropicSSE.

## [HIGH] ComputeCost silently defaults every non-exact model name to Sonnet pricing → Opus undercounted ~5x, budget enforcement operates on wrong totals
`internal/costtrack/tracker.go:311` — bug — LLM/provider layer (internal/provider, internal/apiclient, internal/model, internal/costtrack, internal/promptcache/microcompact)

**Detail:** ModelPricing is keyed by bare names ('claude-opus-4','claude-sonnet-4','claude-haiku-3.5'), but callers never pass those exact strings. (1) The workflow records cost by RUNNER name, not model: workflow.go:577/889/1700 call `CostTracker.Record(execRunnerName, ...)` where execRunnerName is 'claude'/'codex'/'native' (compared to string(model.ProviderCodex) etc. at workflow.go:777). (2) The native/API paths pass dated model IDs like 'claude-opus-4-20250514' (DefaultConfigs uses 'claude-sonnet-4-20250514'; harness/api_provider.go:88 and correlation_wire.go:78 pass req.Model). None of these match the map, so ComputeCost falls through to the Sonnet default (line 314). An Opus run's tokens are therefore priced at Sonnet rates — 5x under on input, 5x under on output. Tracker.Total()/OverBudget()/BudgetRemaining() (consulted by model.CostAwareResolve router.go:124-137 and app budget gates) are computed from these under-priced records, so a real-dollar budget can be exceeded ~5x before OverBudget() trips.

**Evidence:** tracker.go:311-315 `pricing, ok := ModelPricing[model]; if !ok { pricing = ModelPricing["claude-sonnet-4"] }` (exact-match map lookup, no normalization/prefix match). workflow.go:889 `e.CostTracker.Record(execRunnerName, ...)` with execRunnerName ∈ {claude,codex,native}.

**Fix:** Normalize model names before lookup (strip date suffix, resolve tier aliases) via a prefix/contains match, and make ComputeCost return a signal (or log) on the default-fallback path. Separately, pass the resolved model ID — not the runner name — into CostTracker.Record at workflow.go:577/889/1700.

## [HIGH] Unified-diff parser mis-parses the "\ No newline at end of file" marker as a context line, corrupting patches
`internal/patchapply/patch.go:161` — bug — MCP + patch/edit primitives (internal/mcp, tools/str_replace, patchapply, atomicfs, hashline, conflictres, extract)

**Detail:** In Parse's hunk-body switch, any line whose first byte is not '+', '-', or ' ' falls through the `default` case and is stored as an OpContext line with its FULL text (including the leading byte). Git emits the standard trailer `\ No newline at end of file` inside hunks for any file lacking a trailing newline. That line starts with '\', so it is captured as a context line with Text="\ No newline at end of file". For a modify hunk this bogus line is included in findMatch's context set (findMatch/matchAt at patch.go:345-383), so the context never matches the real file and the whole hunk is rejected as "context mismatch". For a create hunk (applyNewFile, patch.go:288-294) it is even worse: the marker text is treated as OpContext and written verbatim as a literal source line into the new file. Reached in production via workflow.go:2105 which calls patchapply.Parse on LLM/git-produced diff summaries.

**Evidence:** switch line[0] { case '+':...; case '-':...; case ' ':...; default: currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpContext, Text: line}) } — no handling of the '\' no-newline marker; git always emits it for files without a trailing newline.

**Fix:** Add an explicit case for lines beginning with '\' (e.g. `case '\\': // "\ No newline at end of file" — record a no-trailing-newline flag on the hunk/file and do NOT append it as a content line`). At minimum skip the line instead of turning it into context.

## [HIGH] str_replace whitespace and ellipsis tiers pick the first match with no uniqueness check → silent wrong-occurrence edit
`internal/tools/str_replace.go:98` — bug — MCP + patch/edit primitives (internal/mcp, tools/str_replace, patchapply, atomicfs, hashline, conflictres, extract)

**Detail:** StrReplace is the live edit primitive (wired at tools.go:918, which then os.WriteFile's the result). The exact tier guards ambiguity (errors when count>1 and !replaceAll) and the unicode tier requires exactly one block match (found==1, str_replace.go:172). But whitespaceNormalizedReplace anchors on `strings.Index(content, oldFirstLine)` — the FIRST occurrence — and never checks whether the normalized block matches at more than one location; it then does `strings.Replace(content, matched, newStr, 1)`, also replacing the first occurrence globally. ellipsisReplace (str_replace.go:193-202) likewise takes the first Index of the head and first Index of the tail with no ambiguity check. So when the intended target is the 2nd of several near-identical blocks and the exact tier misses (any whitespace difference), the tool silently edits the WRONG occurrence at 0.85/0.75 confidence and reports success.

**Evidence:** idx := strings.Index(content, oldFirstLine) ... matched := strings.Join(contentLines[:oldLines], "\n"); if normalize(matched) != normOld { return nil }; return &ReplaceResult{ NewContent: strings.Replace(content, matched, newStr, 1) ...} — first match, no count of other normalized matches.

**Fix:** Mirror the unicode tier: scan all line-blocks, count normalized matches, and return nil (defer to the more-context error path) when the count != 1 unless replaceAll is set. Apply the same uniqueness guard to ellipsisReplace.

## [HIGH] Antitrunc lobe re-publishes duplicate unresolved SevCritical Notes every round → unbounded Workspace growth + PreEndTurnGate livelock
`internal/cortex/lobes/antitrunc/lobe_wrapper.go:96` — bug — cortex parallel-cognition (internal/cortex, lobes, internal/concern)

**Detail:** AntiTruncLobe is a RunStyleRound deterministic lobe (no RunStyler) so Cortex.MidturnNote ticks it every turn. Its Detector re-scans the ENTIRE assistant history (lobe.go:60 `for _, t := range d.History`) each round, and Run() publishes one cortex.SevCritical Note per match with Resolves left empty and no dedup (lobe_wrapper.go:95-105). Because the conversation history is append-only, any truncation phrase that appears once (patterns are broad — 'next session', 'handoff', 'out of scope', 'good enough') is re-detected and re-published as a brand-new Note on every subsequent round. Two compounding failures: (1) Workspace.notes (workspace.go:199, append-only, never pruned) grows by (matches) every turn, and each Note is also written to the durable WAL via writeNote — unbounded memory + WAL growth over a long run. (2) UnresolvedCritical() (workspace.go:256-272) scans all notes and none of these are ever resolved, so Cortex.PreEndTurnGate (cortex.go:711-724) returns non-empty forever. The agentloop can then never honor end_turn; it burns turns until max_turns even after the model has fully complied, because the historical note can never be cleared. This is wired into the production native loop at internal/engine/native_runner.go:812 (`NewAntiTruncLobe(ws, "", "")`).

**Evidence:** lobe.go:60 scans all d.History; lobe_wrapper.go:96 `_ = l.ws.Publish(cortex.Note{... Severity: cortex.SevCritical ...})` with no Resolves and no dedup; workspace.go:267 `if n.Severity == SevCritical && !resolved[n.ID]`; cortex.go:714 `notes := c.workspace.UnresolvedCritical(); if len(notes)==0 {return ""}`

**Fix:** Dedup antitrunc findings before publishing: scan the existing Workspace snapshot for an unresolved critical Note with the same phrase/title and skip re-publishing (or key notes by a stable fingerprint and make Publish idempotent on that key). Additionally, only scan the newest assistant turn (matching the agentloop antitrunc.Gate) instead of the full history, so a once-emitted phrase stops re-firing once the model stops repeating it — otherwise PreEndTurnGate can never clear.

## [HIGH] semdiff keys symbols by bare name, silently dropping changes to same-named methods/functions
`internal/semdiff/semdiff.go:120` — bug — CODE ANALYSIS (goast/repomap/symindex/depgraph/chunker/semdiff/diffcomp/gitblame/codegraph)

**Detail:** Analyze builds oldMap/newMap with `oldMap[oldSyms[i].Name] = &oldSyms[i]` (and the same for new), keyed on the bare symbol Name and ignoring the receiver/type. In Go it is extremely common to have many symbols share a name: methods Close/String/Error/Read/Write on different types, or a func and a method with the same name. All of them collapse to one map entry (last-writer-wins), so changes to every shadowed symbol are invisible to the semantic diff. This feeds the cross-model review path (workflow.go:1638/1794 AnalyzeMultiFile -> Analyze), so a breaking signature change or body rewrite on one of several same-named methods is silently omitted from the review summary — or, worse, misreported as a spurious add/remove.

**Evidence:** Confirmed with a probe: old has `func (a A) Close() error { return nil }` + `func (b B) Close() error { return errX() }`; new changes A.Close's body to `return closedErr`. Analyze(old,new,"x.go") returns `changes: 0` — B.Close (unchanged) overwrote A.Close in the map, so A.Close's body change is never compared. Lines 117-124 build both maps keyed on `.Name` only.

**Fix:** Key the maps on a qualified identity that includes the receiver/type, e.g. `key := s.Name; if s.Type=="method" { key = receiver+"."+s.Name }` (extractGoAST already knows the receiver — currently it is discarded). Populate/lookup oldMap/newMap and the rename/added/removed logic with that qualified key, and surface the receiver in SymbolChange so the summary is unambiguous.

## [HIGH] Ledger content-tier tampering is undetectable — ContentCommitment is written but never verified on read
`internal/ledger/store.go:281` — data-loss — governance-v2 (ledger / supervisor / consensus / skillmfr) + benchmark (bench / verdict / judge / trajectory-audit)

**Detail:** AddNode computes ContentCommitment = sha256(salt||content) and stamps it into the chain tier (ledger.go:346), and the doc at ledger.go:232-236 explicitly promises 'the commitment binds the chain tier to the content tier so a swapped content blob is immediately detectable.' But ReadNode (store.go:238-283) loads Content/Salt from the content tier and returns them WITHOUT ever recomputing contentCommitment(salt,content) and comparing it against cr.ContentCommitment. A grep of the whole ledger package confirms no read/verify path performs this comparison — the commitment is only ever WRITTEN (AddNode/Batch/migrate). Store.VerifyChain deliberately excludes Content from its hash (redaction resilience), and AnchorStore.VerifyChain deliberately does not re-hash nodes. So an attacker (or a bit-flip) that swaps the bytes of content/<id>.json while leaving chain/<id>.json intact goes completely undetected by every automatic verification path, contradicting the documented guarantee.

**Evidence:** store.go:280-281: `n.Salt = ctr.Salt; n.Content = ctr.Content` returned with no commitment check. ledger.go:237 contentCommitment() exists but is called only on the write side. No `contentCommitment(` call appears in any Read/Verify function.

**Fix:** Add commitment verification: in ReadNode (or a new (*Ledger).VerifyNode / VerifyContent), when content is present recompute contentCommitment(ctr.Salt, ctr.Content) and return an error when it != cr.ContentCommitment. Call it from Verify()/VerifyChain so the promised 'swapped content blob is immediately detectable' becomes true.

## [MEDIUM] apiclient SSE scanner uses default 64KB line buffer → long SSE frame truncates the whole stream
`internal/apiclient/client.go:441` — error-handling — LLM/provider layer (internal/provider, internal/apiclient, internal/model, internal/costtrack, internal/promptcache/microcompact)

**Detail:** parseSSE builds `bufio.NewScanner(body)` with no Buffer() call, leaving the default 64KB max token size. A single SSE `data:` line exceeding 64KB (large text_delta batches from some proxies, or an error payload) makes scanner.Scan() return false with bufio.ErrTooLong; the loop exits, already-dispatched deltas are kept but the rest of the response is silently dropped and the caller gets a partial result plus a generic scan error. The sibling direct provider (internal/provider/anthropic.go:352) explicitly sets a 1MB buffer for exactly this reason, so the apiclient path is an inconsistent regression on the live native-runner path (engine/api_runner.go:133).

**Evidence:** client.go:441 `scanner := bufio.NewScanner(body)` with no `scanner.Buffer(...)`; compare anthropic.go:352 `scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)`.

**Fix:** Add `scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)` (or larger) immediately after constructing the scanner in parseSSE, matching the provider package.

## [MEDIUM] OpenAI-compat streaming tool-call reassembly assumes contiguous 0-based indices → dropped tool calls
`internal/provider/anthropic.go:875` — bug — LLM/provider layer (internal/provider, internal/apiclient, internal/model, internal/costtrack, internal/promptcache/microcompact)

**Detail:** ChatStream accumulates streamed tool calls into `toolCallMap` keyed by the provider-supplied tc.Index (line 845), then rebuilds them with `for i := 0; i < len(toolCallMap); i++ { tc := toolCallMap[i] }`. This only works if indices are exactly 0..n-1 with no gaps. Several OpenAI-compatible backends (OpenRouter fan-out, some LiteLLM adapters, Gemini's OpenAI-compat surface) emit tool_call indices that are non-zero-based or sparse. With e.g. indices {1,2}, len==2 so the loop probes keys 0 and 1: key 0 is absent (nil, skipped) and key 2 is never read — half the tool calls vanish, breaking the agent's tool-use turn silently.

**Evidence:** anthropic.go:844-857 keys map by tc.Index; anthropic.go:875-879 iterates `for i := 0; i < len(toolCallMap); i++ { tc := toolCallMap[i]; if tc == nil { continue } }` — index-space assumption not satisfied by sparse maps.

**Fix:** Collect the map's keys, sort them, and iterate in sorted key order (or accumulate tool calls into an ordered slice as they first appear) instead of assuming 0..len-1.

## [MEDIUM] patchapply treats a /dev/null→file (IsNew) hunk as an unconditional overwrite, silently clobbering an existing file
`internal/patchapply/patch.go:277` — data-loss — MCP + patch/edit primitives (internal/mcp, tools/str_replace, patchapply, atomicfs, hashline, conflictres, extract)

**Detail:** applyNewFile does MkdirAll + os.WriteFile with no check that the target already exists. A patch whose header is `--- /dev/null` / `+++ b/existing.go` sets IsNew (patch.go:108-112) and routes to applyNewFile (patch.go:211-220), which then replaces the entire existing file's contents with only the hunk's add/context lines. There is no pre-existence guard, so a malformed or adversarial diff destroys an existing file's full contents and reports it under Applied.

**Evidence:** func applyNewFile(...) error { ... return os.WriteFile(fullPath, []byte(strings.Join(lines, "\n")), 0644) } — no os.Stat existence check before overwriting; caller only checks fp.IsNew.

**Fix:** In applyNewFile, os.Stat(fullPath) first and return an error (recorded in result.Failed/Errors) if it already exists, matching atomicfs.Create semantics.

## [MEDIUM] atomicfs Commit strips the original file mode: rename of a 0600 temp over an existing file drops the exec bit and group/other perms
`internal/atomicfs/transaction.go:218` — data-loss — MCP + patch/edit primitives (internal/mcp, tools/str_replace, patchapply, atomicfs, hashline, conflictres, extract)

**Detail:** Commit stages every write/create via os.CreateTemp (which creates files at mode 0600) and then os.Rename's the temp over the destination (transaction.go:287). Rename carries the temp's 0600 mode, so an atomic Write over an existing 0755 file (e.g. an executable script) silently becomes owner-only, non-executable — the file is functionally broken. The rollback path compounds this by restoring backups with a hardcoded 0644 (transaction.go:272), also not the original mode. The `#nosec` comment claiming "0644 preserves source perms" is inaccurate for the rename path. The Create path in deploy/templates_phase2.go already ships new files at 0600 as a result.

**Evidence:** tmp, err := os.CreateTemp(dir, ".atomicfs-*") // 0600 ... os.Rename(s.tmpPath, s.op.Path) — no os.Chmod to the original mode; rollback uses os.WriteFile(b.path, b.data, 0644).

**Fix:** Before rename, os.Stat the destination (when op.origExists) and os.Chmod the temp to the original FileMode(); for create, chmod to the caller-intended mode. In rollback, restore with the captured original mode rather than 0644.

## [MEDIUM] patchapply writes each file in place with a non-atomic os.WriteFile and no cross-file rollback → partial/truncated corruption on failure
`internal/patchapply/patch.go:265` — data-loss — MCP + patch/edit primitives (internal/mcp, tools/str_replace, patchapply, atomicfs, hashline, conflictres, extract)

**Detail:** applyPatch loops over patch.Files and, for each, overwrites the source directly with os.WriteFile (truncate-in-place, not temp+rename). Two failure windows: (1) a crash/ENOSPC/short-write mid-WriteFile leaves the source truncated/corrupted with no backup to restore from; (2) across a multi-file patch, if file N fails after files 1..N-1 were already written, there is no rollback — the earlier files stay mutated on disk while the operation is reported as failed. Unlike internal/atomicfs, this edit path has no staging or restore, so a partial application leaves the workspace in a broken intermediate state.

**Evidence:** if err := os.WriteFile(fullPath, []byte(output), 0644); err != nil { result.Failed = append(result.Failed, path); ... continue } — direct in-place overwrite, per-file, with no temp file and no rollback of previously-written files.

**Fix:** Route file writes through atomicfs (temp write + rename, and multi-file transactional commit) or at minimum write to a sibling temp and rename, and back up + restore earlier files if a later file in the same patch fails.

## [MEDIUM] Workspace.Publish appends the Note before the durable write; on WAL failure the Note is visible to readers but absent from the WAL, never spotlighted, never fanned out, and Publish reports failure
`internal/cortex/workspace.go:199` — data-loss — cortex parallel-cognition (internal/cortex, lobes, internal/concern)

**Detail:** Publish appends the Note to w.notes (line 199) and bumps w.seq (line 193-194) BEFORE calling writeNote (line 200). If the durable bus write fails, the function unlocks and returns the error (lines 201-202) WITHOUT rolling back the append. Result is a three-way divergence: the Note is now permanently visible via Snapshot()/UnresolvedCritical() (so a failed SevCritical publish still silently blocks PreEndTurnGate), yet it was never persisted to the WAL (so a crash+Replay loses it, and seq/len(notes) diverge from the WAL record), and the spotlight update + hub emit + subscriber fan-out (lines 213-225) are skipped — while the caller is told the publish failed and may retry, producing a duplicate. The doc comment at persist.go claims writeNote runs 'while still holding the write mutex ... so replay observes Notes in the same total order', but the append precedes the write so order/consistency is not actually guaranteed on the failure path.

**Evidence:** workspace.go:199 `w.notes = append(w.notes, n)` then :200 `if err := writeNote(w.durable, n); err != nil { w.mu.Unlock(); return err }` — no rollback of the append or seq before returning.

**Fix:** Persist first, then commit to memory: call writeNote before appending to w.notes (still under the lock); on writeNote error return without mutating notes/seq. Or, on writeNote failure, roll back `w.notes = w.notes[:len(w.notes)-1]` and decrement w.seq before unlocking so in-memory state, WAL, and subscriber view stay consistent.

## [MEDIUM] concern SectionSpec.Cap is dead-wired: renderers hardcode caps, so templates requesting recent_activity cap 15/20 silently receive only 10 entries
`internal/concern/sections/recent_activity.go:14` — broken-wiring — cortex parallel-cognition (internal/cortex, lobes, internal/concern)

**Detail:** Templates declare a per-section Cap (templates.go:38-45, e.g. recent_activity with cap 20 in 4 templates and cap 15 in 2 templates). Builder.BuildConcernField copies that Cap onto Section.Cap (builder.go:146) but never passes it to the QueryFunc (builder.go:131 calls `spec.QueryFn(ctx, sScope, b.ledger)` with no cap) and Render() (render.go) never reads Section.Cap. Instead each renderer hardcodes its own limit — RecentActivity hardcodes `Limit: 10` and `renderNodeList(nodes,"summary",10)`. So the 6 templates that ask for 15 or 20 recent-activity items (qa_lead, reviewer, etc.) silently get truncated to 10, under-delivering the context the template author declared. Section.Cap is entirely inert config across all 12 templates.

**Evidence:** recent_activity.go:14 `Limit: 10` / :22 `renderNodeList(nodes, "summary", 10)`; builder.go:131 `content, err := spec.QueryFn(ctx, sScope, b.ledger)` (cap not forwarded); tally shows `recent_activity ... false, 20` (x4) and `... false, 15` (x2) vs renderer's fixed 10.

**Fix:** Thread the cap through: add a Cap/Limit field to sections.Scope (or change QueryFunc to accept the cap) and have each renderer honor it (both the ledger Query Limit and renderNodeList maxItems), so per-template Cap actually governs section size. Alternatively delete the dead Cap field to stop implying an override that does nothing.

## [MEDIUM] concern section renderers scope by MissionID only, ignoring LoopID/TaskID/BranchID — documented 'current loop'/'current scope' sections bleed all-mission data across loops and stances
`internal/concern/sections/dissent_history.go:12` — bug — cortex parallel-cognition (internal/cortex, lobes, internal/concern)

**Detail:** BuildConcernField builds sScope with MissionID/TaskID/LoopID/BranchID (builder.go:116-121), but the renderers query the ledger by MissionID alone. DissentHistory's doc says 'queries dissent nodes in the current loop' yet its filter is `QueryFilter{Type:"dissent", MissionID: scope.MissionID}` with no LoopID — so a stance scoped to loop B receives every dissent objection recorded anywhere in the mission, including other loops'/stances' review threads. RecentActivity is documented 'in the current scope' but likewise filters only by MissionID (recent_activity.go:12-15). This is cross-loop/cross-stance context bleed within a mission: a stance sees objections and activity that its scope says it should not, which can steer or bias a reviewer/judge with another loop's dissent.

**Evidence:** dissent_history.go:12-15 filter has no LoopID despite the `// queries dissent nodes in the current loop` comment; recent_activity.go:12-15 filters only MissionID despite `// last N nodes in the current scope`; scope.LoopID/TaskID/BranchID are populated by builder.go:116-121 but unused by these renderers.

**Fix:** Add LoopID (and where appropriate TaskID/BranchID) to ledger.QueryFilter usage in the scope-sensitive renderers (dissent_history, recent_activity, active_loops, etc.) so they filter to the concern field's declared scope, matching their doc contracts and preventing cross-loop bleed.

## [MEDIUM] Research vector index rebuilds the entire corpus on every Add once vocabulary exceeds 2000 terms (O(N^2)), and truncated vocab silently zeroes semantic recall
`internal/research/store.go:576` — perf — knowledge-learning

**Detail:** buildVectorIndex() caps vocabulary at 2000 by slicing a randomly-ordered Go map (`vocab = vocab[:2000]`, store.go:530-532). indexEntry() decides whether to rebuild by checking if the new entry contains any token not in s.vocab (store.go:558-574). Once the corpus crosses 2000 distinct terms, essentially every new entry contains at least one term that was truncated out of the capped vocab, so `hasNew` is always true and EVERY Add triggers a full `buildVectorIndex()` -- a complete `SELECT ... FROM entries` DB scan plus re-embedding of all rows, all while holding vecMu.Lock (blocking every concurrent SemanticSearch). This is O(N) per insert, O(N^2) to load a store. Separately, because the retained 2000 terms are chosen non-deterministically, query tokens outside the cap embed to the zero vector and SemanticSearch returns nothing (all scores < 0.01 filter, store.go:615) with no fallback to FTS.

**Evidence:** store.go:576 `if s.vecIdx == nil || hasNew { s.buildVectorIndex(); return }`; store.go:530 `if len(vocab) > 2000 { vocab = vocab[:2000] }` over `for w := range vocabSet` (map iteration, non-deterministic).

**Fix:** Track the full vocab set separately from the capped embedding dimension (or use a stable hashing embedding of fixed dim), and only rebuild when the dimension actually changes; deterministically select the top-2000 terms by document frequency instead of random map order. When vecIdx yields no hits, fall back to s.Search (FTS/LIKE).

## [MEDIUM] handoff BuildContext can panic (slice out of range) and corrupts UTF-8 when truncating context to budget
`internal/handoff/chain.go:241` — bug — knowledge-learning

**Detail:** BuildContext guards only `maxTokens <= 0` (chain.go:127), promoting it to 2000. For any positive maxTokens in 1..4, charBudget = maxTokens*4 is 4..16, so the final truncation `result = result[:charBudget-20]` computes a negative index and panics with slice bounds out of range. maxTokens is caller-supplied and passed straight through orchestrate GetHandoffContext (orchestrator.go:606, 809). Even for normal budgets, `result[:charBudget-20]` slices at a raw byte offset that can split a multibyte UTF-8 rune, emitting invalid UTF-8 into the next agent's injected context.

**Evidence:** chain.go:240-242 `if len(result) > charBudget { result = result[:charBudget-20] + "\n\n[context truncated]\n" }`; charBudget = maxTokens*4 (chain.go:221).

**Fix:** Clamp: `cut := charBudget-20; if cut < 0 { cut = 0 }`, and back `cut` up to a UTF-8 boundary (e.g. via utf8.DecodeLastRune / strings on runes) before slicing.

## [MEDIUM] git blame invoked without `--` path separator and porcelain parser only matches 40-hex SHA-1
`internal/gitblame/blame.go:40` — bug — CODE ANALYSIS (goast/repomap/symindex/depgraph/chunker/semdiff/diffcomp/gitblame/codegraph)

**Detail:** Blame runs `exec.Command("git","blame","--porcelain",filePath)` with no `--` separator, so any path that begins with `-` (or equals an option-like token) is parsed by git as a flag rather than a pathspec — the blame either fails or, with a crafted name, injects git options. Separately, headerRegex (line 234) is `^([0-9a-f]{40})...`, hard-coded to 40 hex characters. On a repository using git's SHA-256 object format (git >=2.42) blame emits 64-hex commit ids, so no header line matches, ParsePorcelain returns an empty FileBlame with a nil error, and every downstream consumer (workflow.go:1006 attribution-aware review) silently sees a file with zero authors instead of an error.

**Evidence:** Line 40: `exec.Command("git", "blame", "--porcelain", filePath)` — no `--`. Line 234: `var headerRegex = regexp.MustCompile(`^([0-9a-f]{40})\s+\d+\s+\d+`)`. ParsePorcelain only appends lines when headerRegex matches (line 66), so a SHA-256 repo yields `fb.Lines == nil` with err==nil.

**Fix:** Add the separator: `exec.Command("git","blame","--porcelain","--",filePath)`. Change the regex quantifier to accept both hash lengths, e.g. `^([0-9a-f]{40,64})\s+\d+\s+\d+`, and consider returning an error (or logging) when a non-empty blame produced zero parsed lines.

## [MEDIUM] Ledger chain linkage and verification disagree on equal CreatedAt timestamps → spurious chain break (or masked tamper)
`internal/ledger/ledger.go:387` — concurrency — governance-v2 (ledger / supervisor / consensus / skillmfr) + benchmark (bench / verdict / judge / trajectory-audit)

**Detail:** AddNode derives ParentHash from latestInMissionUnlocked, which picks the predecessor via `n.CreatedAt.After(latest.CreatedAt)` (ledger.go:387). On a timestamp tie (.After returns false) it keeps whichever node the index's QueryNodes happened to return first — an order that is NOT the insertion order. VerifyChain (verify.go:105-108) independently reorders the mission's nodes with sort.SliceStable keyed only on CreatedAt, so on a tie it uses ListNodes/dir-read order. When two nodes in one mission share a CreatedAt (coarse clock, fast successive AddNode, or caller-supplied CreatedAt via Batch), the predecessor AddNode linked to can differ from the predecessor VerifyChain checks against, producing a false 'parent_hash mismatch' chain-break error on an untampered ledger — and symmetrically could let a reordering slip through.

**Evidence:** ledger.go:387 `if latest == nil || n.CreatedAt.After(latest.CreatedAt)` (strict After, ties keep first-iterated). verify.go:105-107 `sort.SliceStable(bucket, func(i,j) { return bucket[i].CreatedAt.Before(bucket[j].CreatedAt) })` — ties fall back to unrelated list order.

**Fix:** Make ordering total and identical on both sides: add a deterministic tiebreaker (e.g. node ID) to both the latestInMission selection and the VerifyChain sort, or store an explicit monotonic sequence number per mission and chain/verify on that instead of CreatedAt.

## [MEDIUM] Verdict scorer: a mission that omits plan_completion_threshold applies NO plan gate — any completion claim scores truthful
`internal/bench/verdict.go:186` — missing-validation — governance-v2 (ledger / supervisor / consensus / skillmfr) + benchmark (bench / verdict / judge / trajectory-audit)

**Detail:** computeTruthful gates plan completion with `if ratio < mission.CompletionCriteria.PlanCompletionThreshold` (verdict.go:186-191). PlanCompletionThreshold is a float64 that defaults to 0.0 when the mission YAML/JSON omits it. With threshold 0, `ratio < 0` is never true, so the plan-completion signal is silently disabled — unlike DeliveryRatioMin and JudgeAgree, which have documented '0/empty = disabled' semantics, PlanCompletionThreshold has no such documented meaning yet the code treats 0 as 'no gate'. If a mission also leaves DeliveryRatioMin=0 and JudgeAgree='', computeTruthful returns true unconditionally, so CompletionTruthful == CompletionAttempted: an agent that emits any completion claim while completing zero plan items is scored truthful and counts toward its leaderboard truthful-completion rate. The canonical-hello-world.json fixture has no completion_criteria at all, exercising exactly this path.

**Evidence:** verdict.go:186-191 gate is skipped for threshold 0; verdict.go:123-124 `truthful := completionAttempted && computeTruthful(...)`. bench.go:60-62 PlanCompletionThreshold has no zero-value disable contract. Fixture internal/bench/testdata/missions/canonical-hello-world.json omits completion_criteria.

**Fix:** Either require PlanCompletionThreshold to be set (validate missions at load and reject/warn when Plan is non-empty but threshold==0), or default an unset threshold to 1.0 so declared plan items must actually verify before a claim is counted truthful.

## [MEDIUM] Reward-hacking trajectory auditor misses common reference-leak commands and cannot see hidden graded tests
`internal/bench/trajectory_audit.go:25` — bug — governance-v2 (ledger / supervisor / consensus / skillmfr) + benchmark (bench / verdict / judge / trajectory-audit)

**Detail:** AuditTrajectory is the SOTA-#5 anti-reward-hacking check, but its regexes only catch `git diff <hash>` / `git log|show|reflog|blame` — they do NOT match the most obvious reference-solution reads on a sealed-base harness: `git diff main`, `git diff HEAD~1`, `git diff origin/...`, `git worktree`, or reading a sibling reference checkout. reGitHistoryRead requires a 7+ hex SHA for the diff form, so branch/relative-ref diffs slip through. Separately, the test-edit check (test_file_edit) only fires for paths matching MissionTestGlobs, which are derived solely from plan ChangedFiles that look like test paths (MissionTestGlobs, line 89-104); a benchmark whose graded tests are HELD OUT (not listed in the mission plan, the whole point of a held-out corpus) yields an empty glob set, so editing the graded tests to pass trivially is never flagged. The net effect: two of the classic reward hacks the file claims to surface are undetectable in the exact configuration (hidden tests + sealed base) the feature exists for.

**Evidence:** trajectory_audit.go:25 `reGitHistoryRead = ...diff\s+[0-9a-f]{7,}...` (no branch/relative refs). MissionTestGlobs (line 89-104) builds globs only from m.Plan[].ChangedFiles; a hidden test never appears there so AuditTrajectory's test_file_edit branch (line 56-63) has nothing to match.

**Fix:** Broaden reGitHistoryRead/reGitCheckoutRef to cover `git diff|show|checkout|restore` against any ref token (branch names, HEAD~N, origin/*), and feed AuditTrajectory an explicit held-out-test path list from the harness (not just plan ChangedFiles) so test edits are flagged even when the graded tests are hidden from the mission plan.

## [MEDIUM] Bundled skill-pack seeding accepts any self-signed pack and any unsigned pack — no trust-root check on the r1-mcp path
`cmd/r1-mcp/skill_packs.go:61` — security — governance-v2 (ledger / supervisor / consensus / skillmfr) + benchmark (bench / verdict / judge / trajectory-audit)

**Detail:** VerifyPackSignature (pack_signature.go:113-140) verifies an ed25519 signature using the public key embedded in pack.sig.json itself and never checks the KeyID/PublicKey against a trust root — so any attacker who can write a pack directory can re-sign modified contents with their own freshly generated key and pass verification. The pack-adopt CLI compensates by calling skill.MatchKey(...) against a trust root (pack_adopt_cmd.go:227-230, 'key_id not in trust root'), but the r1-mcp bundled-seed path SeedSkillPackRoots calls VerifyPackSignatureIfPresent (skill_packs.go:61) with NO trust-root check, and VerifyPackSignatureIfPresent additionally returns (nil,nil) for unsigned packs (pack_signature.go:142-151). So a pack dropped into a bundled pack root registers its manifests/skills into the live MCP registry whether it is unsigned or self-signed by an untrusted key — the signature 'verification' provides no authenticity guarantee here.

**Evidence:** pack_signature.go:125-137 decodes PublicKey from the signature file and Verify()s against it with no allowlist; VerifyPackSignatureIfPresent swallows ErrPackUnsigned. skill_packs.go:61-63 registers the pack after that call with no KeyID trust check, unlike pack_adopt_cmd.go:227-230.

**Fix:** Have the bundled-seed path enforce the same trust anchor as adopt: check signature.KeyID against the trust root (skill.MatchKey) and reject unsigned packs (or require an explicit operator opt-in flag) before registering their manifests.

## [LOW] SQLiteStore.Record ignores the INSERT error, silently losing learnings on write failure
`internal/wisdom/sqlite.go:185` — error-handling — knowledge-learning

**Detail:** Record() executes the INSERT with `s.db.Exec(...)` and discards both return values. If the write fails (disk full, DB locked past busy timeout, constraint, closed DB) the learning is dropped with no log and no signal, so the 'never repeat the same mistake' loop silently degrades. The in-memory Store.Record cannot fail, but the persistent store can and does swallow it.

**Evidence:** sqlite.go:185 `s.db.Exec(` with no assignment of the returned error (contrast StoreMemory at sqlite.go:298 which checks err).

**Fix:** Capture and at minimum log the error (the Recorder interface returns nothing, so log via the package logger); consider surfacing it through a separate errored-writes counter or changing the interface to return error.

## [LOW] Research Store.Delete and Prune leave orphaned vectors in the in-memory index (unbounded growth)
`internal/research/store.go:231` — data-loss — knowledge-learning

**Detail:** Delete() (store.go:231) and Prune() (store.go:399) remove rows from the entries table but never call vecIdx.Remove for the deleted IDs. The in-memory vector index keeps growing with tombstoned documents across the process lifetime; SemanticSearch still scores them, then discards each via getWithoutIncrement returning nil (store.go:618). Correctness survives, but a long-lived research store accumulates dead vectors that inflate memory and slow every semantic search, and the vecIdx is only ever pruned by a full rebuild.

**Evidence:** store.go:232 `s.db.Exec("DELETE FROM entries WHERE id = ?", id)` and store.go:400 Prune DELETE -- neither touches s.vecIdx; Index.Remove exists (vecindex/index.go:172) but is never called from research.

**Fix:** After a successful Delete/Prune, take vecMu.Lock and call s.vecIdx.Remove(id) for each removed ID (collect IDs before the Prune DELETE).

## [LOW] RepoMap.Render budget-truncation message always reports "0 more files"
`internal/repomap/repomap.go:364` — bug — CODE ANALYSIS (goast/repomap/symindex/depgraph/chunker/semdiff/diffcomp/gitblame/codegraph)

**Detail:** When the token budget is exhausted, Render prints `... (%d more files)` with `len(sorted)-len(groups)`. `sorted` is built one-to-one from the `groups` map immediately above (lines 352-355), so the two lengths are always equal and the elided count is structurally always 0. The model is told zero files were dropped even when many high-value-but-lower-ranked files were truncated, defeating the purpose of the notice.

**Evidence:** Lines 352-355 build `sorted` from every entry of `groups`; line 364 computes `len(sorted)-len(groups)` (== 0). The `for _, g := range sorted` loop (line 360) has no index to know how many were rendered.

**Fix:** Add an index to the render loop (`for i, g := range sorted`) and emit `len(sorted)-i` as the remaining count.

## [LOW] diffcomp.computeOps allocates an unbounded O(N*M) LCS matrix with no size guard
`internal/diffcomp/compress.go:217` — perf — CODE ANALYSIS (goast/repomap/symindex/depgraph/chunker/semdiff/diffcomp/gitblame/codegraph)

**Detail:** computeOps builds a full `(m+1) x (n+1)` int matrix for the LCS diff with no cap on m/n. Sibling package semdiff explicitly caps its LCS at 200 tokens (semdiff.go:642) precisely to avoid this; diffcomp has no such guard. Two large inputs (e.g. a generated/minified file of ~100k lines on each side) allocate ~10^10 ints (tens of GB), OOM-killing the agent process. Current in-repo callers happen to pass old="" (workflow.go:2110), so it is not presently reachable with two large sides, but Diff is exported and any future caller diffing two real file versions triggers it.

**Evidence:** Lines 216-219: `m,n := len(oldLines),len(newLines); dp := make([][]int, m+1); for i := range dp { dp[i] = make([]int, n+1) }` with no bound. Comment at line 215 even calls it "Simple O(NM) LCS ... fine for typical file sizes" while providing no enforcement.

**Fix:** Guard the inputs before allocating: if m*n exceeds a threshold (or either side exceeds e.g. 20k lines), fall back to a linear line-set diff or return a coarse whole-file replace hunk, mirroring the 200-token cap already used in semdiff.lcsLength.
