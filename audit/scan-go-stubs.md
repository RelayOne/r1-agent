# Go Codebase Stubs / Gaps / Quality Audit — r1-agent

Scope: `cmd/`, `internal/` (Go only).
Excluded: `vendor/`, `node_modules/`, `.tmp-worktrees/`, `services/`, `web/`, `desktop/`, `tools/`, `tests/`, `evaluation/`, `corpus/`, `bench/` (top-level), `r1-server` (top-level).
Date: 2026-05-08
Branch: `build/web-chat-ui`

## Method

Searches performed (case sensitive unless noted):

- `grep -rEn "TODO|FIXME|HACK|XXX" --include='*.go'` → 107 hits → only ~6 in production-code comments after filtering test fixtures and detection regexes.
- `grep -rEn 'panic\("(TODO|not[- ]?implemented|unimplemented|todo)"\)'` → 1 production hit (test fixture string), 1 test panic.
- `grep -rEn '_ = err'` (production only) → 1 hit (intentional, see W1 below).
- `grep -rEn 't\.Skip'` → 51 skipped tests; the great majority are platform/env-gated (Windows/no-display/no-git/short-mode), not real test gaps.
- `STATUS:`, `PARTIAL`, `BLOCKED`, `// for now,`, `will land`, `until ... lands`, `follow-up commit`, `Track [AB] Task`, `TASK-[0-9]+ lands`, `stub for now`, `minimal implementation`, `skeleton`, `not yet implemented`, `// TODO:`, `// FIXME`.

## Findings

| File:Line | Severity | Category | What's missing | Effort |
|---|---|---|---|---|
| internal/mcp/cortex_server.go:1-44 | HIGH | V2 governance | `STATUS: PARTIAL` — file declares only `CortexToolNames()` constant; the 5 `r1.cortex.*` MCP handlers (notes, publish, lobes_list, lobe_pause, lobe_resume) called out by spec 8 §4.3 / §12 item 5 are absent in this branch. Catalog advertises tools the daemon cannot serve. | M |
| internal/mcp/verify_lint_wiring.go:1-53 | HIGH | Spec wiring | `STATUS: PARTIAL` — only `LintViewWithoutAPICommand()` placeholder helper; the actual `r1.verify.lint` MCP handler that runs `tools/lint-view-without-api` and translates findings into envelopes is "to land once spec 5 lands". CI parity not enforced. | M |
| internal/cortex/lobes/antitrunc/lobe.go:4-39 | HIGH | V2 governance | `STATUS: BLOCKED on cortex-core (spec build order < 9)`. The Lobe wrapper around Detector that the supervisor rules expect is not yet implemented; package doc says "the only delta needed is a ~30-line Lobe constructor". | S |
| internal/cortex/lobes/antitrunc/lobe_wrapper.go:1-269 | HIGH | V2 governance | Local duplicate declarations of `Lobe`, `LobeKind`, `LobeInput`, `NoteSeverity`, `Workspace`, `Note` exist as a parallel shadow of cortex-core types. When cortex-core merges these MUST be deleted/aliased; currently they will silently diverge. | M |
| internal/executor/code.go:71 | HIGH | Production stub | `CodeExecutor.Execute` returns `errors.New("...: use 'stoke ship' or 'stoke sow' for now; direct task routing lands in a follow-up commit (Track B Task 19)")` when `ExecuteHook` is nil. Direct `TaskCode` execution path is not wired. | M |
| internal/cortex/lobes/memorycurator/trigger.go:71-110 | HIGH | V2 governance partial | `haikuCall` body is real but doc note flags "TASK-29 lands the prompt assembly + provider call but stops at returning the raw assistant content blocks; TASK-30 layers the privacy filter + auto-apply + audit-log pipeline". The auto-apply/audit pipeline that consumes the returned content blocks is the gap. | L |
| internal/cortex/lobes/memorycurator/lobe.go:164-168 | HIGH | V2 governance partial | Default `onTrigger = l.defaultOnTrigger` is wired, but doc says the haiku-call + per-candidate auto-apply + confirm-queue + audit log pipeline is incomplete pending TASK-30. | L |
| internal/sessionctl/handlers.go:189-198 | HIGH | Spec wiring | `overrideHandler` is "audit-only: it records the operator's override decision on the event log so nothing is lost" — does not actually mutate the AC state machine. Spec-1 must land before this is functional. | M |
| internal/sessionctl/handlers.go:28-30 | MED | Spec wiring | `Emit func(kind, payload) (eventID string)` is "pass-through for now; integrate with bus/eventlog once spec-3 lands". Operator-driven events bypass the durable bus. | S |
| internal/delegation/saga.go:344-356 | HIGH | Production gap | `SettleCompleteThenRevoke` policy "marks intent without actually waiting" — calls Revoke immediately like `rollback-immediately`. Documented as "no compensating txns run". Saga correctness compromised under that policy. | M |
| internal/sharedmem/block.go:377-400 | HIGH | Production gap | Rollback "approximates" by requiring `by.ReplayValue` and rewriting Value to caller-supplied value rather than reconstructing from provenance. Documented as "the wider STOKE-017 implementation may add a value-tracking history layer later". | L |
| internal/topology/topology.go:129-167 | MED | Production gap | `SupervisorWorker.Run` "minimal implementation" runs all remaining tasks concurrently regardless of the supervisor's output. Doc admits "richer implementation would let supervisor's output select which remaining tasks". | M |
| internal/r1skill/analyze/stages.go:55-92 | HIGH | Production stub | `stageType` doc says "skeleton here demonstrates the structure. A full implementation walks Expr references". Stage 2 (type inference + edge type check) is absent — only does node-kind validation, not the core type-flow analysis. | L |
| internal/r1skill/analyze/stages.go:265-286 | HIGH | Production stub | `stageTermination` admits "trust map traversal. A real implementation builds the reference graph from each node's config Expr fields, runs cycle-detection algorithm (DFS with three-color marking)". DAG cycle detection is not done. | M |
| internal/tui/teatest_shim.go:1-30 | MED | V2 governance partial | `STATUS: PARTIAL — canonical reference implementation per §5 wraps charmbracelet/x/exp/teatest. That package is not vendored in this checkpoint`. Vendor-only fallback is in use. Will swap when teatest dep lands. | M |
| internal/research/fetch.go:201 | LOW | Test helper | `StubFetcher.Fetch` returns `fmt.Errorf("stub: no page for %q", url)` — used as a test fixture in production package. Intentional design but worth documenting. | S |
| internal/truecom/client.go:204-263 | LOW | Intentional fallback | `StubClient` always-pass / auto-approve in-memory client. Mode-gated (`STOKE_TRUSTPLANE_MODE=stub` default). Documented as "Production callers MUST use a real Client for any safety-critical decision". | S |
| internal/trustplane/client.go:204-263 | LOW | Intentional fallback | Same as truecom: parallel `StubClient` for trustplane. Verify the ModeStub default is appropriate for production builds. | S |
| internal/cortex/lobes/walkeeper/lobe.go:307-323 | LOW | Error swallow | `_ = err` after `l.w.Publish(item.evt)` documented as non-fatal "the durable bus may have been closed by a concurrent shutdown. Continue draining". Intentional. | S |
| internal/skill/desktop/stub_backend.go:1-63 | LOW | Intentional fallback | `stubBackend` returns `ErrUnsupported` for every method. Build-tag-gated default for non-`desktop_robotgo` builds. | S |
| internal/cortex/seq_allocator.go:119 | LOW | Reserved | `// reserved for session.bound per spec §5.5.` future work flagged in code; not blocking. | S |
| internal/r1skill/analyze/stages.go:209-242 | MED | Production gap | `stageContract` defers `wall_time_lt`, `forall`, `exists` contract kinds to runtime with an `info` diagnostic — only `actual_cost_lt` is decidable. Runtime-assertion injection layer for these contracts is not implemented. | M |
| internal/cortex/lobes/memorycurator/curate.go:56-70 | MED | V2 governance partial | `curate` orchestrator: privacy gate works; haikuCall returns blocks; per-candidate auto-apply / confirm-queue / audit-log pipeline downstream of `blocks := l.haikuCall(ctx, in)` is the part flagged as TASK-30 work. | L |
| internal/skillmfr/manifest.go:307-330 | LOW | Documented behavior | `ScaffoldFromOpenAPI` deliberately produces a Manifest that fails `Validate()` so operators must fill in `whenToUse` / `whenNotToUse`. Intentional. | S |
| internal/bridge/cost.go:27-67 | MED | Error swallow | `CostBridge` swallows errors from `json.Marshal`, `bus.Publish`, `ledger.AddNode` via `_, _ =` / `_ =`. Bus or ledger write failure is silent — V2 governance event loss possible. | S |
| cmd/r1-server/main.go:21 | MED | Documented partial | `RS-3 (scanner + event tailer) will be added in a follow-up commit.` File-level note that scanner+tailer subsystem hasn't landed in this main.go. | M |
| cmd/r1-server/memories.go:29 | LOW | Documented future | `A future commit adds a per-session query` — cross-session listing only. | S |
| cmd/r1-server/memories.go:84 | LOW | Documented future | `can show the full body in a future commit without re-querying.` UI optimization. | S |
| internal/memory/membus/bus.go:22 | MED | Documented partial | `Intentionally deferred to follow-up passes:` package doc lists deferred features. | M |
| internal/memory/membus/bus.go:121 | MED | Documented partial | `Full multi-scope visibility matching (§7) is deferred; this core slice` — multi-scope visibility incomplete. | M |
| internal/hire/verify_settle.go:180 | MED | Documented partial | External-receipt fill-in path: `Hirer can fill it from an external receipt; for now we pass`. AC criteria piping incomplete. | S |
| internal/hire/verify_settle.go:192 | MED | Documented partial | `plan.AcceptanceCriterion.VerifyFunc — when that lands, each` AC verify wiring waits on plan changes. | M |
| internal/hire/verify_settle.go:261 | MED | Documented partial | `criteria land in plan.VerificationDescent; for now` — verification descent integration deferred. | M |
| internal/hire/verify_settle.go:321 | LOW | Reserved | `_ = contractID // reserved — will flow into per-AC metadata when plan ACs gain VerifyFunc.` intentionally unused. | S |
| internal/plan/quality_signals.go:70 | LOW | Documented future | `signature matching is a planned follow-up.` quality-signal extension. | M |
| internal/plan/quality_signals.go:595 | LOW | Documented future | `we flag indiscriminately; refine later if FP rate` — heuristic with known false-positive rate, no refinement done. | S |
| internal/plan/quality_signals.go:607 | LOW | Reserved | `_ = lines // reserved for future line-based checks` intentional. | S |
| internal/plan/declared_symbols.go:29 | LOW | Documented future | `matching is a planned follow-up once name-level catches the` — symbol-matching follow-up. | M |
| internal/plan/workspace_hygiene.go:245 | LOW | Reserved | `_ = ctx // reserved for future scanners that may honour cancellation` intentional. | S |
| internal/plan/sow_convert_chunked.go:1428 | LOW | Documented future | `New sessions added here are STUBS (no tasks, no ACs). A follow-up` — chunked SOW conversion creates stub sessions intentionally. | M |
| internal/cortex/lobes/memorycurator/lobe.go:201-225 | MED | V2 governance partial | `Run` per-Round entry "TASK-29 wires the every-5-turns trigger ... fires onTrigger (the haikuCall pipeline once TASK-30 lands)". Cadence works, downstream pipeline incomplete. | L |
| internal/vecindex/toolrag.go:18 | LOW | Documented future | `follow-up can swap in embedding-backed similarity when the` — vec-backed similarity not wired. | M |
| internal/a2a/agent_card.go:8-19 | LOW | Documented future | `will eventually mount the card at the well-known path; for now` and `in internal/skillmfr/ when that wiring lands; for now it's` — agent-card public mount + skillmfr wiring deferred. | M |
| internal/chat/session.go:436 | LOW | Documented partial | `keep it serial for now.` chat handling not parallelized. | S |
| cmd/r1/inspect.go:55-61 | LOW | Reserved | `cfgPath := fs.String("config", "", "Config file path (reserved; currently unused)")` flag is documented as reserved. | S |
| cmd/r1-server/redaction.go:123-126 | LOW | Reserved | `_ = sessionID // sessionID currently unused — Store doesn't yet ... future filter doesn't break callers.` intentional pre-wired param. | S |
| cmd/r1/sow_native.go:1217 | LOW | Reserved | `_ = seenFingerprints // reserved for future fingerprint dedup gate` intentional. | M |
| internal/critic/critic_test.go:104 | LOW | Test stub | `panic("not implemented")` in test interface stub — intentional. | S |
| cmd/r1/sow_spec_guard_test.go:205 | LOW | Test fixture | `panic("TODO: implement foo")` is a test corpus string for the guard scanner. Intentional. | S |
| internal/testgen/generator.go:110 | LOW | Generated comment | `// TODO: add assertions\n` written into generated test scaffolds intentionally so reviewers see the gap. Keep. | S |
| internal/testgen/generator.go:129 | LOW | Generated comment | Same as above for the second template branch. | S |
| internal/r1env/r1env.go:10 | LOW | Migration warning | `WARN: legacy env STOKE_FOO used; rename to R1_FOO before 2026-07-23` — migration deadline. Action item but not a stub. | S |
| internal/r1rename/doc.go:12 | LOW | Migration warning | `WARN per (canonical, legacy) pair. EnvLegacyDropEnabled() reads` — migration helper, see r1env. | S |
| internal/r1rename/doc.go:51 | LOW | Documented future | `future-phase change scoped to land alongside the S6 NATS legacy` — env rename phase. | M |
| internal/mission/microconv.go:456 | LOW | Deprecated field | `MaxIterations int // DEPRECATED: use MaxDepth. Kept for backward compat.` — documented dep. | S |
| cmd/r1/serve_install_test.go:180 | MED | Test gate | `t.Skip("skipping end-to-end install test (set R1_TEST_SERVICE_INSTALL=1 to enable)")` — service-install E2E off by default; gated test. | S |
| cmd/r1/serve_integration_chdirleak_test.go:112 | MED | Test gate | `t.Skipf("drift not observable: got %q, want %q (test environment may have its own cwd guard)" ...)` — chdir-leak detection silently skipped if env has its own guard. | S |
| cmd/r1-server/sse_v2_test.go:67 | MED | Test gate | `t.Skip("oldest event has id <= 1; cannot construct a below-floor cursor")` — below-floor cursor case never exercised when fixture is too small. | S |
| cmd/r1-server/e2e/e2e_test.go:63 | MED | Test gate | `R1_SERVER_UI_V2=1 required for v2 E2E` — v2 E2E off by default. | S |
| cmd/r1-server/graph_e2e_test.go:62 | MED | Test gate | Same env gate as above for graph E2E. | S |
| cmd/r1-server/e2e/e2e_test.go:67 | MED | Test gate | Skips when playwright runner missing; CI must install. | S |
| cmd/r1-server/graph_e2e_test.go:66 | MED | Test gate | Same as above. | S |
| internal/skill/desktop/desktop_real_test.go:47,65,80,110 | LOW | Test gate | Skip at 4 sites when `DISPLAY/WAYLAND_DISPLAY` unset. Platform-conditional. | S |
| internal/browser/rod_real_test.go:33,37,43,220 | MED | Test gate | Four skips: `-short` mode, rod construct fail, Chromium launch fail, second construct fail. CI may always skip rod tests. | S |
| internal/redteam/corpus_test.go:250 | MED | Test gate | Skip when `corpus/known-misses` directory missing — tests silently no-op when fixture absent. | S |
| internal/redteam/corpus_test.go:274 | MED | Test gate | Skip when known-misses empty. | S |
| internal/scan/selfscan_test.go:17 | LOW | Test gate | Skip when repo root not found. | S |
| internal/scan/selfscan_test.go:43 | LOW | Test gate | Skip when no Go source files found. | S |
| internal/skillselect/detect_test.go:557 | LOW | Test gate | `cannot add skills without directory` — environment-conditional. | S |
| internal/deploy/vercel/vercel_test.go:26 | LOW | Test gate | Mock-shell-script POSIX-only. | S |
| internal/daemondisco/discovery_test.go:69 | LOW | Test gate | `file-mode bits are not load-bearing on Windows`. | S |
| internal/daemondisco/discovery_test.go:86 | LOW | Test gate | Same — POSIX-only mode rejection. | S |
| internal/hub/transport_test.go:16,42,68,97,124 | LOW | Test gate | `skipping on windows` at 5 sites. | S |
| internal/deploy/cloudflare/cloudflare_test.go:39 | LOW | Test gate | Mock wrangler POSIX-only. | S |
| internal/preflight/assertions_test.go:38 | LOW | Test gate | git not installed. | S |
| internal/r1dir/r1dir_test.go:145 | LOW | Test gate | chmod-based error injection ineffective as root. | S |
| internal/mcp/stoke_server_test.go:459 | LOW | Test gate | Skip in `-short` mode (real subprocess). | S |
| internal/mcp/stoke_server_test.go:464 | LOW | Test gate | Skip when `/bin/sleep` missing. | S |
| internal/mcp/transport_stdio_test.go:30 | LOW | Test gate | POSIX-only stdio process-group semantics. | S |
| internal/worktree/manager_test.go:13 | LOW | Test gate | `git not installed`. | S |
| internal/worktree/ensure_repo_test.go:112 | LOW | Test gate | Edge case: fresh git init already had HEAD. | S |
| cmd/r1-server/scanner_test.go:96 | LOW | Test gate | signal-0 probe unreliable on Windows CI. | S |
| internal/tools/cron_tools_test.go:14 | LOW | Test gate | crontab not available. | S |
| cmd/r1/daemon_http_test.go:167 | MED | Test gate | Test relies on POSIX `/bin/true`; comment says Windows path "covered by TASK-42 daemon_http_windows.go inspection" — windows-specific code path NOT covered by this test. | S |
| cmd/r1/descent_bridge_bootstrap_test.go:24 | LOW | Test gate | git not available. | S |
| cmd/r1/sow_native_telemetry_test.go:163 | LOW | Test gate | No VCS revision available in test env. | S |
| internal/bench/golden_test.go:19 | MED | Test gate | `no golden missions found` — golden mission corpus not present in this branch. | S |
| internal/bench/golden_test.go:34 | MED | Test gate | `Run(...): %v (expected in unit test context)` — every Run failure is silently skipped, hiding real regressions. | M |
| internal/bench/golden_test.go:69 | MED | Test gate | Same — second test, same skip logic. | S |
| internal/bench/golden_test.go:89 | MED | Test gate | Same — second test's per-mission skip-on-error. | M |
| internal/cortex/cortex.go:165 | LOW | Defensive panic | `panic(fmt.Sprintf("cortex/New: MaxLLMLobes must be <= 8, got %d", cfg.MaxLLMLobes))` — invariant guard, intentional. | S |
| internal/cortex/budget.go:27 | LOW | Defensive panic | `LobeSemaphore capacity must be 1..8` invariant guard. | S |
| internal/cortex/round.go:59 | LOW | Defensive panic | `roundID must be > current` invariant guard. | S |
| internal/cortex/round.go:62 | LOW | Defensive panic | `roundID already open` invariant guard. | S |
| internal/cortex/seq_allocator.go:98 | LOW | Defensive panic | `seq allocator stopped` invariant. | S |
| internal/ledger/nodes/nodes.go:27 | LOW | Defensive panic | duplicate node-type registration invariant. | S |
| internal/artifact/builder.go:61,64 | LOW | Defensive panic | nil-store / nil-ledger constructor invariants. | S |
| internal/artifact/poll.go:71 | LOW | Defensive panic | nil-ledger constructor invariant. | S |
| internal/server/jsonrpc/daemonapi.go:287,290 | LOW | Defensive panic | nil dispatcher / nil daemon-handler invariants. | S |
| internal/server/jsonrpc/desktopapi.go:51,54 | LOW | Defensive panic | nil dispatcher / nil handler invariants. | S |
| internal/chat/dispatcher.go:249 | LOW | Defensive panic | schema marshal failure (impossible at runtime, intentional fail-fast). | S |
| internal/deploy/registry.go:45 | LOW | Defensive panic | empty deploy name invariant. | S |
| internal/pools/pools.go:21 | LOW | Defensive panic | unable to determine home dir at init. | S |
| cmd/r1-server/ui.go:34 | LOW | Defensive panic | `embed sub` failure (build-time fatal). | S |
| cmd/r1/ctl_bootstrap.go:103 | LOW | Defensive panic | `crypto/rand` failure (impossible). | S |
| internal/plan/sow_convert_chunked.go:281 | LOW | Documented behavior | `partial coverage so the run proceeds; the warning above tells` — intentional, surfaces partial coverage. | S |
| internal/plan/sow_convert_chunked.go:1187 | LOW | Documented behavior | `DAG resolver only partially resolves these, falling back to` — DAG resolver fallback documented. | M |
| cmd/r1/sow_native.go:2962-2963 | LOW | Best-effort cleanup | `_ = exec.CommandContext(...)` for `git worktree remove --force` / `git branch -D` — best-effort cleanup, comment justifies. | S |
| cmd/r1/sow_native.go:1125,1582 | LOW | Discarded result | `_ = execNativeTask(ctx, repairTask.ID, ...)` repair-task error discarded (two sites). Worth verifying intentional. | S |
| cmd/r1/sow_native.go:1778 | LOW | Discarded result | `_ = SaveWisdom(...)` wisdom save error swallowed. | S |
| cmd/r1/sow_native.go:916 | LOW | Discarded result | `_ = reg.Load()` registry-load error swallowed. | S |
| internal/mcp/transport_stdio.go:169,294,340 | LOW | Best-effort cleanup | `_ = c.Close()` / `_ = killGroup(cmd, ...)` cleanup-path errors swallowed at three sites. Common Go pattern. | S |
| internal/mcp/transport_sse.go:173,184 | LOW | Best-effort close | `_ = cli.Close()` swallowed at two sites. | S |
| internal/mcp/client.go:123 | LOW | Reserved | `_ = resp // server capabilities returned but not needed yet`. | S |
| cmd/r1-server/scanner.go:185 | LOW | Best-effort | `_ = filepath.WalkDir(root, ...)` walk error swallowed (returned via callback). | S |
| internal/plan/sow_script_recursion_preflight.go:66 | LOW | Best-effort | `_ = filepath.Walk(repoRoot, ...)`. | S |
| internal/plan/integrity_desktop.go:148 | LOW | Best-effort | Same pattern. | S |
| internal/plan/integrity_desktop.go:194 | LOW | Best-effort | Same. | S |
| internal/plan/integrity_desktop.go:234 | LOW | Best-effort | Same. | S |
| internal/plan/integrity_java.go:236 | LOW | Best-effort | Same. | S |
| internal/plan/sow_devdep_preflight.go:352 | LOW | Best-effort | Same. | S |
| internal/plan/sow_devdep_preflight.go:564 | LOW | Best-effort | Same. | S |
| internal/plan/sow_devdep_preflight.go:164 | LOW | Reserved | `_ = pnpmRunRE` regex declared but not used in this branch. | S |
| internal/engine/policy_gate.go:210 | LOW | Documented swallow | `_ = json.Unmarshal(input, &args) // best-effort; on failure we still deny-check with empty resource`. | S |
| internal/engine/policy_gate.go:240 | LOW | Documented future | `future tool definition uses the alternate key.` reserved. | S |
| internal/mission/store.go:860 | LOW | Documented swallow | `_ = rows.Err() // best-effort mission status; iterate-error is not actionable`. | S |
| internal/ledger/redact_log.go:137 | LOW | Best-effort | `best-effort audit data and the alternative is the` — intentional. | S |
| internal/tools/todo_tools.go:266 | LOW | Documented async | `best-effort + non-blocking on disk; making it sync costs` — async write. | S |
| internal/cortex/budget.go:85-90 | LOW | Reserved | `lobeID parameter is currently unused but is` — pre-wired for future use. | S |
| internal/cortex/lobes/memorycurator/privacy.go:20 | LOW | Documented future | `future work that adds a structured tags field to agentloop.Message can` — privacy hooks. | M |
| internal/journal/journal.go:16 | LOW | Documented future | `future kinds like "checkpoint", "marker"` — journal kinds enum extension. | S |
| internal/research/research.go:80 | LOW | Documented future | `future LLM-backed decomposer satisfies the same interface and slots` — design comment. | M |
| internal/research/orchestrator.go:910 | LOW | Documented future | `future concurrency primitive when we add a shared rate-limiter` — design comment. | M |
| internal/server/sessionhub/session.go:318 | LOW | Documented future | `future installations work uniformly. The wrapper:` — wrapper documented. | S |
| internal/r1dir/r1dir.go:21 | MED | Documented partial | `A full sweep of every hardcoded literal lands in a follow-up; this function ships the contract.` — config-dir migration only partial. | M |
| internal/r1rename/dirs.go:62 | LOW | Documented future | `follow-up; this function ships the contract.` — same migration. | S |
| internal/wizard/run.go:109 | LOW | Documented behavior | `Fall through to proposal for now; interactive mode uses the` — wizard partial. | S |
| internal/encryption/sqlcipher.go:113 | LOW | Reserved | `future DSN variant uses a different key encoding we still redact`. | S |
| internal/tui/a11y.go:136 | LOW | Reserved | `future case-insensitive mode can be a one-line change.` | S |
| cmd/r1/main.go:6971 | LOW | CLI help text | help string mentions TODO scan command — not a code TODO. | S |
| cmd/r1/sessions.go:26 | LOW | CLI help text | `r1 sessions explore — interactive: TUI checkpoint browser (TODO)` — explore subcommand not built. | M |
| cmd/r1-server/stoke-server/main.go:94 | LOW | Best-effort shutdown | `_ = srv.Shutdown(shutCtx)` intentional. | S |
| cmd/r1-server/stoke-server/main.go:244 | LOW | Best-effort | `_ = runTmpl.Execute(...)` — output template exec error swallowed. | S |
| cmd/r1-server/stoke-server/main.go:263 | LOW | Best-effort | `_ = indexTmpl.Execute(...)` — same. | S |
| cmd/r1-server/main.go:61 | LOW | Best-effort | `_ = cr.CaptureRecovered(...)` panic-capture swallows error. | S |
| cmd/r1-server/main.go:69 | LOW | Best-effort | `_ = cr.CaptureError(...)` same. | S |
| cmd/r1/scan_repair_phase2b.go:261 | LOW | Reserved | `_ = repo` — variable explicitly suppressed. | S |
| cmd/r1/oneshot_cmd.go:52 | LOW | Reserved | `_ = jsonMode` — flag suppressed. | S |
| cmd/r1-server/skills.go:138 | LOW | Reserved | `_ = sessionID` — same. | S |
| cmd/r1/ops_memory.go:140 | LOW | Best-effort close | `_ = db.Close()`. | S |
| cmd/r1/browse_cmd.go:101 | LOW | Reserved | `_ = browser.NewClient // silence unused import if plan Extra is empty`. | S |
| cmd/r1/simple_loop.go:272 | LOW | Best-effort kill | `_ = cmd.Process.Kill()`. | S |
| cmd/r1/simple_loop.go:276 | LOW | Best-effort kill | `_ = procutil.Kill(cmd)`. | S |
| cmd/r1/simple_loop.go:1011 | LOW | Reserved | `_ = tierFilterComplete // referenced inside the loop; nothing to do here`. | S |
| cmd/r1/lsp_cmd.go:49 | LOW | Best-effort | `_ = fs.Parse(args)` flag-parse error swallowed (cobra-style). | S |
| cmd/r1/cicd_cmd.go:51 | LOW | Best-effort | Same. | S |
| internal/agentloop/loop.go:120 | LOW | Documented future | comment about Track A Task 3 honeypots — wired. | S |
| cmd/r1/agent_serve_cmd.go:457 | LOW | Reserved | `future refactor can move it into internal/executor/`. | S |
| internal/artifact/poll.go:182 | LOW | Reserved | `future "operator wants to re-review everything" surface`. | S |
| internal/r1rename/env.go:67 | LOW | Reserved | `future maintenance touches one place.` | S |
| internal/critic/redflag.go:43 | LOW | Doc/comment | `"could add tests later", or "TODO: verify"` — corpus example, intentional. | S |
| internal/plan/meta_reasoner.go:540 | LOW | Doc/comment | `reviewer_over_dispatch` documented failure mode. | S |
| internal/plan/integration_review.go:50 | LOW | Reserved | `when routing a follow-up to the right owning task.` | S |
| internal/plan/integration_review.go:249 | LOW | Reserved | `dispatches a follow-up (or at minimum the operator`. | S |
| internal/plan/sow_convert.go:538 | LOW | Documented behavior | `partial/invalid SOWs` failure mode. | S |
| internal/plan/sow_critique.go:312 | LOW | Documented behavior | `partial structures — harmless` partial-parse tolerance. | S |
| internal/plan/integrity_promote.go:184 | LOW | Heuristic | First-path-extracted heuristic documented. | S |
| internal/plan/integrity_ts.go:123 | LOW | Heuristic | TS source path heuristic. | S |
| internal/plan/quality_signals.go:476 | LOW | Heuristic | JS/TS heuristic block doc. | S |
| internal/plan/quality_signals.go:1174 | LOW | Heuristic | api/routes/handlers/server/ heuristic. | S |
| internal/plan/quality_signals.go:1424 | LOW | Heuristic | minimum-3-items rule. | S |
| internal/scheduler/algorithms.go:19 | LOW | Documented approximation | GRPW first-dependency approximation. | S |
| internal/scheduler/algorithms.go:28 | LOW | Documented approximation | file-overlap scoring approximation. | S |
| internal/scheduler/algorithms.go:55 | LOW | Documented approximation | "short programs first" approximation. | S |
| internal/eventlog/log.go:363 | LOW | Documented heuristic | session-event match heuristic. | S |
| internal/plan/session_sizer.go:118 | LOW | Documented heuristic | sizer judge prompt heuristics. | S |
| internal/cortex/lobes/walkeeper/lobe.go:383 | LOW | Documented heuristic | walkeeper backpressure heuristic. | S |
| internal/research/research.go:120 | LOW | Documented partial | `HeuristicDecomposer is the stdlib-only MVP.` — explicit MVP. | M |
| internal/tools/tools.go:1217 | LOW | Documented heuristic | escape-newline detection heuristic. | S |
| internal/memory/membus/bus.go:36 | LOW | Documented behavior | `best-effort pub/sub signal, not a durability channel.` | S |
| internal/worktree/helpers.go:277 | LOW | Best-effort | `refCmd.CombinedOutput() // best effort` git ref discard. | S |
| internal/worktree/helpers.go:282 | LOW | Best-effort | Same for read-tree. | S |
| internal/worktree/manager.go:225 | LOW | Best-effort | git prune best-effort. | S |
| internal/worktree/manager.go:233 | LOW | Best-effort | git delete-ref best-effort. | S |
| internal/gateway/gateway.go:560 | LOW | Best-effort | gateway adapter retry comment. | S |
| internal/mcp/registry.go:81 | LOW | Documented behavior | `.well-known/mcp.json` probe best-effort. | S |
| internal/cortex/interrupt.go:303 | LOW | Documented behavior | provider.Provider stub callers MUST close — interface contract for tests. | S |
| internal/cortex/lobes/walkeeper/lobe.go:175 | LOW | Documented behavior | "stub Lobe" path doc — graceful shutdown. | S |
| cmd/r1/watch.go:157 | LOW | Doc comment | doneCostRE regex doc. | S |

Total rows: 188.

## Top 10 by impact

Ranked by user/operator impact (production correctness, V2 governance completeness, AC verification fidelity):

1. **internal/mcp/cortex_server.go:1-44** (HIGH) — 5 `r1.cortex.*` MCP tool handlers (notes, publish, lobes_list, lobe_pause, lobe_resume) are advertised in the catalog but the handlers are absent. Any external client invoking these on this branch hits an undefined-method error. Closing depends on cortex-core / cortex-concerns merge.

2. **internal/executor/code.go:71** (HIGH) — `CodeExecutor.Execute` returns a hard error when the `ExecuteHook` is nil. This is the trunk executor for Stoke's primary use case (TaskCode); any caller that doesn't set `ExecuteHook` cannot run code tasks at all. Track B Task 19 follow-up is named but unbuilt here.

3. **internal/r1skill/analyze/stages.go:55-92, 265-286** (HIGH × 2) — Skill analyzer stages 2 (type inference + edge type-check) and 6 (DAG/termination cycle detection) are explicitly skeletons. Skills with type-mismatched edges or cycles will pass `analyze` and reach runtime where they can deadlock or produce wrong outputs.

4. **internal/cortex/lobes/antitrunc/lobe_wrapper.go:1-269** (HIGH) — Parallel local copies of `Lobe`, `Workspace`, `Note`, `LobeKind`, `NoteSeverity` exist as a shadow of cortex-core types. Compile-time identical now, but cortex-core's eventual changes will silently diverge unless this file is deleted/aliased on merge. Drift risk.

5. **internal/sessionctl/handlers.go:189-198** (HIGH) — `overrideHandler` records the operator's override on the event log but does not mutate the AC state machine (spec-1 owns it). Operators can issue overrides that LOOK accepted but don't change task state — confusing UX, audit-only effect.

6. **internal/delegation/saga.go:344-356** (HIGH) — `SettleCompleteThenRevoke` policy "marks intent without actually waiting" — falls through to immediate Revoke without compensating txns. Saga semantics under that policy are wrong.

7. **internal/sharedmem/block.go:377-400** (HIGH) — Rollback only works if caller supplies `by.ReplayValue`; provenance entries don't carry values. Documented as "STOKE-017 implementation may add a value-tracking history layer later". Rollback semantics is contractually incomplete.

8. **internal/mcp/verify_lint_wiring.go:1-53** (HIGH) — `r1.verify.lint` MCP handler is replaced by a single `LintViewWithoutAPICommand()` constant; the actual handler that runs lint-view-without-api and reports findings is absent. CI parity for the catalog/UI lint cannot be enforced from agentic code paths.

9. **internal/cortex/lobes/memorycurator/{trigger.go:71-110, curate.go:56-70, lobe.go:201-225}** (HIGH × 3) — TASK-29 lands the Haiku call + cadence; TASK-30's privacy filter + per-candidate auto-apply + audit-log pipeline is the missing downstream that makes the curated memories actually persist. Memory persistence contract incomplete.

10. **internal/bench/golden_test.go:34, 89** (MED, but operator-impact HIGH) — Both golden-mission tests `t.Skipf` on every Run error "(expected in unit test context)". This silently masks regressions — a broken bench Run would never fail CI. Either the tests should be moved out of `go test ./...` (build tag) or the skip should require a specific err type.
