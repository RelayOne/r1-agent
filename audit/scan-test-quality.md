# Test Quality Audit — r1-agent

Scope: Go tests under `cmd/`, `internal/`, `bench/`, repo-root `integration_test.go`, and `services/`; JS/TS under `web/` and `desktop/`. Excludes `vendor/`, `node_modules/`, `.tmp-worktrees/`.

Counts at scan time:
- Go test files: **1474** (excluding `.tmp-worktrees`)
- JS/TS test files: **41**
- Total Go `func Test...`: **6561**
- Skipped Go tests (`t.Skip` / `t.Skipf` lines): **46**
- Skipped JS/TS tests: **0** (no `.skip` / `.todo` / `xit` / `xtest`)
- `time.Sleep` occurrences in Go tests: **269**
- `expect(...).toBeTruthy()` / `toBeDefined()` weak assertions in JS: **79**
- Go files in cortex/lanes/ledger packages with no test pair: see findings below; **0 critical missing**, **5 indirect** (covered by neighbor file or integration tests).

---

## Findings

| File:Line | Severity | Category | Issue | Recommended action |
|---|---|---|---|---|
| internal/agentloop/integration_antitrunc_test.go:3 | HIGH | Skipped/disabled (BLOCKED comment) | "cortex-driven mission test from spec §item 25 is BLOCKED on cortex-core merging" — substitute test exercises a thinner mock surface, not the real cortex path. | Re-enable as cortex-core is now in tree (`internal/cortex` exists). Wire a real `cortex.Workspace` driven mission test. |
| internal/antitrunc/soak_test.go:4 | MED | Skipped/disabled (BLOCKED comment) | 8h soak BLOCKED by build-session budget; replaced by 1k iter fuzz. Fine, but the substitute does not exercise the long-tail false-positive distribution. | Add a nightly cron job that invokes the real soak with `-timeout 8h` outside CI gate. |
| cmd/r1-server/graph_e2e_test.go:62 | HIGH | Skipped (env gate) | Skips entirely if `R1_SERVER_UI_V2=1` not set — primary v2 graph E2E never runs in default CI. | Either flip default to enable v2 graph E2E in CI, or document the env gate as required in CI workflow. |
| cmd/r1-server/graph_e2e_test.go:66 | MED | Skipped (binary missing) | Skips if playwright runner binary missing. Silent skip on a v2 graph contract test. | Ensure CI installs Playwright; fail loud if missing rather than skipping. |
| cmd/r1-server/e2e/e2e_test.go:63 | HIGH | Skipped (env gate) | E2E test skipped without `R1_SERVER_UI_V2=1`; ships the v2 path untested in default CI. | Same as above — make env-on default in CI. |
| cmd/r1-server/e2e/e2e_test.go:67 | MED | Skipped (binary missing) | Playwright runner skip. | Fail loud / install in CI. |
| cmd/r1-server/sse_v2_test.go:67 | LOW | Skipped (data shape) | Skips when oldest event id ≤ 1; cursor floor cannot be constructed. | Seed the WAL with > 1 event in the fixture so the path is always exercised. |
| cmd/r1/serve_install_test.go:180 | HIGH | Skipped (opt-in env) | Service install end-to-end test only runs if `R1_TEST_SERVICE_INSTALL=1`. CI does not set this — install path is effectively untested. | Set this env in a dedicated CI lane; OR use a temp-dir mock systemd target. |
| cmd/r1/sow_native_telemetry_test.go:163 | MED | Skipped (CI shape) | Skips if no VCS revision in test env. Some CI configs hit this. | Inject a synthetic revision via build-tags or stub `runtime/debug.ReadBuildInfo`. |
| cmd/r1/serve_integration_chdirleak_test.go:112 | MED | Skipped (env-dependent) | Skips when "test environment may have its own cwd guard" — a test for a CHdir-leak fix that silently no-ops where the bug is hardest to trigger. | Construct the cwd-leak fixture inside a fresh process-group via t.Run+exec to remove the env dependency. |
| cmd/r1/daemon_http_test.go:167 | LOW | Skipped (POSIX-only) | Relies on `/bin/true`. Documented Windows path is "covered by inspection of daemon_http_windows.go". Not a real test. | Add a Windows-side fixture using `cmd /c exit 0` (or skip on platform but exercise structure). |
| cmd/r1/descent_bridge_bootstrap_test.go:24 | LOW | Skipped (git missing) | Skips if `git` not in PATH; expected for non-git CI. | OK — but document in README that git is required for full test pass. |
| cmd/r1-server/scanner_test.go:96 | LOW | Skipped (Windows) | signal-0 probe unreliable on Windows CI. | OK — Windows-specific. |
| internal/mcp/transport_stdio_test.go:30 | LOW | Skipped (POSIX) | stdio process-group semantics POSIX-only. | OK — platform constraint. |
| internal/mcp/transport_stdio_test.go:197 | HIGH | Race-y (`time.Sleep(60s)`) | A `time.Sleep(60 * time.Second)` inside a goroutine in a test — slow + deadlock-prone if test cancels early. | Use ctx with timeout; replace fixed sleep with `<-ctx.Done()` poll. |
| internal/mcp/stoke_server_test.go:459 | MED | Skipped (-short) | Real subprocess test skipped in `-short`; no non-short CI step. | Add a non-`-short` CI step or remove the gate. |
| internal/mcp/stoke_server_test.go:464 | LOW | Skipped (binary) | Skips without `/bin/sleep`. | OK on POSIX. |
| internal/mcp/stoke_server_test.go:303 | MED | Race-y (sleep "for monotonic clock") | `time.Sleep(2ms) // ensure monotonic clock separates them` — relies on wall-clock granularity. | Inject a clock interface (`func() time.Time`); avoid relying on real time separation. |
| internal/r1dir/r1dir_test.go:145 | LOW | Skipped (root) | Skips when running as root because chmod injection ineffective. | OK — but add a non-root assertion that the function's failure path returns a structured error. |
| internal/worktree/manager_test.go:13 | LOW | Skipped (git missing) | Skips if `git` missing. | OK. |
| internal/worktree/ensure_repo_test.go:112 | LOW | Skipped ("unexpected: fresh git init had HEAD") | Test's invariant doesn't hold on some systems and silently skips. | Investigate; an unexpected env state should be a HARD-FAIL or fixed assumption. |
| internal/scan/selfscan_test.go:17,43 | MED | Skipped (no repo root / no Go files) | Selfscan tests skip if repo root or Go files not found. Skips hide scanner regressions in CI sandboxes. | Use a controlled fixture tree (`testdata/`) so the test never depends on cwd discovery. |
| internal/skill/desktop/desktop_real_test.go:47,65,80,110 | MED | Skipped (no display server, batch) | All four desktop_real tests skip without `DISPLAY` / `WAYLAND_DISPLAY`. CI almost never has these — desktop skill is functionally untested. | Run under Xvfb in CI. |
| internal/redteam/corpus_test.go:250,274 | HIGH | Skipped (missing/empty corpus, batch) | Red-team regression suite silently no-ops if `corpus/known-misses/` is absent OR empty. Two skip points hide scanner regressions. | Vendor a minimal corpus into `testdata/`; make data presence a hard contract. |
| internal/tools/cron_tools_test.go:14 | LOW | Skipped (crontab missing) | Reasonable platform skip. | OK. |
| internal/daemondisco/discovery_test.go:69 | LOW | Skipped (Windows) | File-mode bits don't apply on Windows. | OK. |
| internal/daemondisco/discovery_test.go:86 | LOW | Skipped (Windows) | Same. | OK. |
| internal/bench/golden_test.go:19,34,69,89 | MED | Skipped (no missions / transient, batch) | Bench-regression suite skips entirely when missions directory empty (lines 19, 69) and silently buries per-mission failures as `t.Skipf("expected in unit test context")` (lines 34, 89). Cannot detect bench regressions. | Vendor at least one canonical mission; distinguish expected-from-runtime errors via typed error and t.Fatal on unexpected. |
| internal/skillselect/detect_test.go:557 | LOW | Skipped (data shape) | "cannot add skills without directory". | OK if covered elsewhere. |
| internal/preflight/assertions_test.go:38 | LOW | Skipped (git missing) | OK. | — |
| internal/deploy/cloudflare/cloudflare_test.go:39 | LOW | Skipped (Windows) | Mock wrangler shell script is POSIX-only. | Document Windows coverage gap. |
| internal/deploy/vercel/vercel_test.go:26 | LOW | Skipped (Windows) | Same. | Same. |
| internal/browser/rod_real_test.go:33 | MED | Skipped (-short) | Chromium launch skipped in -short. Real browser path not in default CI. | Add CI lane that invokes without -short. |
| internal/browser/rod_real_test.go:43 | LOW | Skipped (CI sandbox) | Reasonable. | OK. |
| internal/hub/transport_test.go:16,42,68,97,124 | LOW | Skipped (Windows) | Five tests gated to non-Windows. | OK as platform constraint. |
| (batch — TODO inside test fixture strings, no action) | LOW | False-positive (fixture content) | "TODO" / "FIXME" inside Go fixture strings used by scanner tests are intentional and exercise the no-todo rule. Sites: critic/critic_test.go:165, convergence/validator_test.go:371,388,1318. | None — fixture content is the test. |
| internal/tui/lanes/lanes_focus_test.go:81 | MED | Weak assertion (panic-only) | "Just assert the view doesn't panic / produce empty" — only checks substring "main"; renders narrow focus mode without verifying any of the contract (focus highlight, lane content, scrollbar). | Add concrete assertions: focus border around `m.focusID`, expected lane payload visible, footer rendered. |
| integration_test.go:1104 | HIGH | Race-y (`time.Sleep`) | `time.Sleep(50ms)` after `bus.Emit` to let observer hooks run before assertion. Three call sites in `TestDashboardStateBridge` (1104, 1119, 1133). Hub uses async observers — flaky on slow CI / under -race. | Replace with a blocking wait: register a synchronous channel-based subscriber that signals `done` and `<-done` instead of sleeping. |
| integration_test.go:1119 | HIGH | Race-y (`time.Sleep`) | See above. | Same. |
| integration_test.go:1133 | HIGH | Race-y (`time.Sleep`) | See above. | Same. |
| internal/cortex/lane_lifecycle_test.go:250 | MED | Race-y (no-op verify via sleep) | After `EmitDelta` on terminal lane, sleeps 50ms then checks event count didn't grow. A subscriber that fires after the sleep would silently pass. | Use `time.AfterFunc(timeout, signal)` + drain the bus and assert `len(events) == before` once a barrier event lands. |
| internal/cortex/lane_lifecycle_test.go:365 | MED | Race-y (no-op verify via sleep) | Same pattern: re-Kill, sleep 50ms, check event count unchanged. | Same — barrier-event approach. |
| internal/cortex/lane_lifecycle_test.go:452 | LOW | Race-y (`time.Sleep` between transitions) | 2ms sleep between transitions in a goroutine to "ensure ordering". | Use channel handshake instead of time-based ordering. |
| internal/cortex/cortex_test.go:771 | MED | Race-y (post-drain sleep) | `time.Sleep(50ms)` after `waitForBudget` returns — a "sanity" recheck that depends on no late event mutating state during the sleep. | Replace with a quiescent-bus barrier: emit a sentinel and wait for it before asserting steady-state. |
| internal/cortex/cortex_test.go:600 | LOW | Race-y (poll loop sleep) | 5ms sleep inside `waitForBudget` polling. Acceptable since outer loop has deadline. | OK. |
| internal/cortex/prewarm_test.go:311,345 | MED | Race-y (tick-count via wall-clock) | Two tests assert a 50ms-tick pump fires ≥3 times in 500ms. -race overhead can break this on slow CI. | Use `chan struct{}` per fire and count via channel reads; no sleep. |
| internal/cortex/lobes/all_integration_test.go:454,462,769 | MED | Race-y (settle sleeps, batch) | Three "let async settle" sleeps (300ms / 100ms / 300ms) before reading counters/snapshots. Counters can drift if a late tick fires. | Stop cortex first (drains) and/or use sync.WaitGroup signaled from handlers. |
| internal/cortex/lobes/walkeeper/lobe_test.go:350 | MED | Race-y (`150ms multiple ticks`) | "150 ms multiple tick intervals" — unreliable when tick=20ms and CI is slow. | Replace with explicit fire-count channel from the lobe under test. |
| internal/cortex/interrupt_test.go:84 | MED | Race-y (sleep then interrupt) | Sleeps 20ms before sending interrupt, "deterministic-enough proxy without a fake clock". | Add fake clock or synchronous handshake (event-on-first-block-received). |
| internal/cortex/interrupt_test.go:152,160 | MED | Race-y (goroutine-leak via timing) | `runtime.GC; sleep 20ms; before := NumGoroutine` then re-snapshot after sleep. Inherently flaky for leak detection. | Use `goleak` (Uber) or test-scoped goroutine accounting. |
| internal/harness/harness_test.go:412 | HIGH | Race-y (PauseStance pre-ack timing) | `time.Sleep(50ms)` then asserts PauseStance has NOT returned. If the goroutine schedule races, false-pass. | Use a `<-time.After(50ms)` race against the channel — but combined with a "should still be blocked" channel timeout assertion, this is the right shape. The improvement is to assert via a synchronous "checkpoint signaled" channel before sleeping. |
| internal/harness/harness_test.go:574 | LOW | Race-y (resume settle) | 50ms sleep before second pause cycle. | Use checkpoint signal. |
| internal/harness/harness_test.go:152,198,480,553 | LOW | Race-y (goroutine + sleep) | Worker goroutine pattern: `for { checkpointFn(ctx); time.Sleep(5ms) }`. Acceptable for stress, but introduces tight CPU spin under -race. | OK as-is; document. |
| internal/sessionctl/operator_bridge_test.go:138 | MED | Race-y (sleep grace window) | "Small grace window to let the goroutine attempt its idempotent Resolve" — 50ms hard sleep. | Use a sync.WaitGroup or done channel from the goroutine. |
| internal/sessionctl/takeover_test.go:207 | LOW | Race-y (poll loop sleep) | 10ms inside a polling loop — acceptable. | OK. |
| internal/sessionctl/router_test.go:148 | MED | Race-y (`time.Sleep(75ms)`) | Sleep before assertion in a router test. | Replace with channel barrier. |
| internal/sessionctl/router_test.go:162 | LOW | Race-y (small sleep) | 2ms sleep "for ordering". | OK or use channel handshake. |
| internal/sessionctl/router_test.go:196 | LOW | Race-y (small sleep) | 1ms sleep. | OK. |
| internal/cortex/lobes/clarifyq/trigger_test.go:181 | MED | Race-y (settle sleep) | 50ms post-emit settle. | Use sync barrier. |
| internal/cortex/lobes/planupdate/confirm_test.go:156,186 | MED | Race-y (settle sleeps) | 100ms x2 post-emit. | Use sync barrier. |
| internal/cortex/lobes/rulecheck/lobe_test.go:45 | MED | Race-y (settle sleep) | 200ms post-publish. | Use sync barrier. |
| internal/supervisor/core_test.go:166,227,287,331,395 | MED | Race-y (settle sleeps, batch) | Five `time.Sleep(50–150ms)` post-rule-emission settle calls in supervisor core_test. | Replace with a rule-completion signal/barrier helper. |
| internal/supervisor/hooks_test.go:126,227 | MED | Race-y (settle sleeps) | 100ms / 150ms post-hook. | Same — barrier-based. |
| internal/supervisor/rules/* (consensus/dissent_requires_address_test.go:129, partner_timeout_test.go:144,208, draft_requires_review_test.go:131, convergence_detected_test.go:156, iteration_threshold_test.go:141; sdm/duplicate_work_detected_test.go:143, dependency_crossed_test.go:168, schedule_risk_critical_path_test.go:120, collision_file_modification_test.go:149, drift_cross_branch_test.go:128; drift/budget_threshold_test.go:107,157, judge_scheduled_test.go:171, intent_alignment_check_test.go:77, more_test.go:34; hierarchy/escalation_forwards_upward_test.go:148, completion_requires_parent_agreement_test.go:96,162, user_escalation_test.go:152,217; research/timeout_test.go:130, report_unblocks_requester_test.go:118, request_dispatches_researchers_test.go:82,140; skill/import_consensus_test.go:116, extraction_trigger_test.go:130, contradicts_outcome_test.go:117, load_audit_test.go:78, application_requires_review_test.go:106; snapshot/formatter_requires_consent_test.go:170, modification_requires_cto_test.go:184; trust/fix_requires_second_opinion_test.go:97, completion_requires_second_opinion_test.go:142, problem_requires_second_opinion_test.go:132; antitrunc/scope_underdelivery_test.go:136, truncation_phrase_detected_test.go:106, subagent_summary_truncation_test.go:142; cross_team/modification_requires_cto_test.go:147) | MED | Race-y (poll-loop sleep, batch) | 30+ supervisor rule tests use `time.Sleep(5ms)` inside event-collection polling loops. Acceptable as bounded poll, but if outer deadline isn't enforced consistently → flake. | Audit each file for outer deadline; OR introduce a shared `waitForRuleEvents` helper with bounded-poll + hard-fail timeout. |
| internal/bench/r1d_serve_bench_test.go:184 | MED | Race-y (settle sleep) | 50ms post-server-start. | Use readiness probe (poll `/health`). |
| internal/notify/notify_test.go:43 | LOW | Race-y (designed) | `time.Sleep(10s) // exceed 5s timeout` — intentional in a timeout test. | OK by design. |
| internal/streamjson/twolane_test.go:23,58 | MED | Race-y (settle sleeps) | 50ms / 100ms then assertion in streaming-lane test. | Use sync.WaitGroup over emitted records. |
| internal/streamjson/cost_test.go:27,84 | MED | Race-y (settle sleeps) | 75ms / 50ms. | Use barrier. |
| internal/hub/hub_test.go:136,261,399 | MED | Race-y (settle sleeps, 3 sites) | 50/60/50ms post-emit. | Use sync barrier. |
| internal/hub/builtin/builtin_test.go:176,208 | MED | Race-y (settle sleeps) | Two 50ms sleeps. | Same. |
| internal/hub/adapters_test.go:88,106,201,250 | MED | Race-y (settle sleeps, batch) | Four 50ms sleeps in adapter tests. | Same. |
| internal/mcp/registry_test.go:455,487 | MED | Race-y (settle sleeps) | Two 50ms sleeps. | Use sync barrier. |
| internal/mcp/transport_http_test.go:580 | MED | Race-y (settle sleep) | 50ms. | Use barrier. |
| internal/gateway/gateway_test.go:364 | MED | Race-y (settle sleep) | 50ms. | Same. |
| internal/skillmfr/manufacturer_test.go:338 | MED | Race-y (settle sleep) | 50ms. | Same. |
| internal/studioclient/http_test.go:272 | MED | Race-y (settle sleep) | 200ms. | Use poll-with-deadline. |
| cmd/r1/run_cmd_hitl_test.go:40 | MED | Race-y (settle sleep) | 50ms post-spawn. | Use readiness signal. |
| cmd/r1/daemon_http_test.go:117 | MED | Race-y (settle sleep) | 150ms inside loop. | Use poll. |
| cmd/r1/pipe_watchdog_test.go:23,45 | MED | Race-y (settle sleep) | 120ms each. | Replace with barrier. |
| internal/model/cacherouter_test.go:43 | LOW | Race-y (settle sleep) | 150ms post-emit in cache-router test. | Use barrier. |
| internal/model/cacherouter_test.go:116 | LOW | Race-y (settle sleep) | 100ms. | Same. |
| internal/filewatcher/watcher_test.go:47,180,203 | LOW | Race-y (settle sleep) | 10ms after fs change. fs notify is OS-dependent — 10ms may be too short on slow inotify. | Use `fsnotify.Event` channel directly with bounded deadline. |
| cmd/r1-server/sse_test.go:213 | MED | Race-y (settle sleep) | 100ms post-emit before SSE-read. | Use SSE-event channel barrier. |
| cmd/r1-server/retention_sweep_test.go:75 | MED | Race-y (settle sleep) | 80ms post-write before sweep check. | Drive sweep synchronously. |
| cmd/r1-server/scanner_test.go:85,108,200,273 | MED | Race-y (loop sleeps, 4 sites) | Four sleeps in scanner test polling. | Tighten poll loop with deadline. |
| internal/daemon/executor_test.go:337,494 | MED | Race-y (settle sleeps) | 100ms / 25ms. | Use barrier. |
| internal/session/signature_test.go:191 | MED | Race-y (sleep > timeout) | `time.Sleep(RegisterTimeout + 200ms)` to force expiry — wall-clock dependence. | Inject timeout via test seam. |
| internal/tui/teatest_shim_test.go:152,179,317 | MED | Race-y (settle sleeps) | 50ms / 10ms / 50ms. | Use teatest `.WaitFor(...)` matchers. |
| internal/tui/lanes/lanes_producer_test.go:76,127 | MED | Race-y (settle sleeps) | `PRODUCER_TICK_MS+150ms` then 20ms. | Use tick channel directly. |
| internal/tui/cost_dashboard_test.go:243,246 | MED | Race-y (settle sleeps) | 30ms each. | Use refresh signal. |
| internal/telemetry/collector_test.go:35 | MED | Race-y (settle sleep) | 5ms post-emit. | Use barrier. |
| internal/mcp/lanes_server_pin_test.go:153,168 | MED | Race-y (settle sleeps) | 20ms / 50ms. | Use barrier. |
| internal/convergence/rules_research_test.go:177,196 | MED | Race-y (`time.Sleep(1s)` + param) | One-second hard sleep + parameterized sleep. Slow and flaky. | Inject a clock. |
| internal/cortex/lobes/clarifyq/resolve_test.go:122 | MED | Race-y (settle sleep) | 50ms. | Use barrier. |
| internal/cortex/lobes/rulecheck/integration_test.go:181,194 | LOW | Race-y (settle sleeps) | 50ms / 25ms post-emit. | Use barrier. |
| (batch — small bounded poll-loop sleeps, not flaky in practice) | LOW | Race-y (poll-loop sleep, batch) | ~30 sites with 1–10ms sleeps inside `for { ...; sleep(d) }` polling loops that have an outer deadline. Examples: cortex/lobe_test.go:175,217,460; cortex/round_test.go:124,159; cortex/lobes/walkeeper/lobe_test.go:41,95,147,331,398,428; cortex/lobes/memorycurator/curate_test.go:121; cortex/workspace_test.go:651; mcp/lanes_server_kill_test.go:278; mcp/events_test.go:102; mcp/transport_http_test.go:276; mcp/transport_sse_test.go:142,223; mcp/stoke_server_test.go:534; cmd/r1/shell_helpers_test.go:202; cmd/r1/scan_repair_test.go:171; cmd/r1/sow_native_streamjson_cs4_test.go:131,133; lsp/client/client_test.go:268; research/orchestrator_test.go:368; agentserve/pool_test.go:356; agentserve/async_handlers_test.go:272; subscriptions/drain_test.go:36,73; subscriptions/manager_test.go:160; tui/cost_dashboard_test.go:72; tui/progress_test.go:360; streamjson/lane_test.go:101; metrics/metrics_test.go:90; cortex/budget_test.go:65; cortex/lanes_streaming bench. | OK as-is; consider extracting a shared `pollUntil(deadline, predicate)` helper. |
| (batch — by-design synthetic delays) | LOW | Race-y (designed) | Sleeps that simulate latency / cancellation timing: agentserve/pool_test.go:224; scheduler/scheduler_test.go:46,75; daemon/executor_test.go:55,135,277; mcp/transport_http_test.go:151; plan/sow_convert_chunked_test.go:67. | OK by design. |
| (batch — E2E poll loops) | LOW | Race-y (E2E poll) | cmd/r1-server/e2e/e2e_test.go:151; cmd/r1-server/graph_e2e_test.go:172. 100ms each in playwright wait loops. | OK in E2E. |
| internal/modelsource/modelsource_test.go:15,22 | MED | Env-mutating w/o t.Setenv | `os.Setenv(k, v)` without `t.Setenv` — leaks env between parallel tests. | Switch to `t.Setenv`. |
| internal/plan/declared_symbols_treesitter_test.go:134-144 | MED | Env-mutating w/o t.Setenv | Three `os.Setenv` calls + `defer os.Setenv(old)`. Not parallel-safe. | Use `t.Setenv`. |
| internal/plan/declared_symbols_harness_test.go:72 | MED | Env-mutating w/o t.Setenv | `os.Setenv("STOKE_H27_TREESITTER", "1")` without restore guarantee. | Use `t.Setenv`. |
| internal/websearch/websearch_test.go:66,100,126 | MED | Env-mutating w/o t.Setenv | Three `os.Setenv`+`defer os.Setenv(prev)` patterns; t.Cleanup not used. | Switch to `t.Setenv`. |
| internal/provider/pool_test.go:335 | MED | Env-mutating w/o t.Setenv | `setEnv(v) { os.Setenv(envKey, v) }` shared mutator — parallel-hostile. | Use `t.Setenv` per call site. |
| internal/server/sessionhub/sessionhub_test.go:22 | LOW | Env-mutating w/o t.Setenv | `os.Setenv(R1_HOME, prev)` — restored explicitly. | Use `t.Setenv`. |
| internal/chat/session_test.go:599,635 | MED | Env-mutating w/o t.Setenv | `defer os.Setenv("ANTHROPIC_API_KEY", old)` in a critical-path test. Parallel-hostile. | Use `t.Setenv`. |
| internal/secrets/secrets_test.go:18,22 | LOW | Env-mutating helper | Helper sets env via os.Setenv. | Refactor helper to take `*testing.T` and use `t.Setenv`. |
| internal/daemonlock/lock_test.go:80 | LOW | Hardcoded port in fixture | `port:9090` baked into fixture JSON. Test does not bind, so OK. | OK as fixture data. |
| cmd/r1/ctl_daemon_cmd_test.go:339,347 | LOW | Hardcoded port (mock URL) | `127.0.0.1:9091` used as expected URL prefix in a mock-server test that doesn't bind. | OK; uses default port for assertion only. |
| cmd/r1/serve_cmd_test.go:30,43,157 | LOW | Hardcoded port | Tests parse `--addr 127.0.0.1:9091`. Doesn't bind. | OK. |
| cmd/r1/serve_install_test.go:25 | LOW | Hardcoded port (parse) | classifyServeAction parse test. | OK. |
| desktop/src/r1d-3.test.ts:149 | MED | Weak assertion (`toBeTruthy`) | `expect(TIER_COLORS[tier]).toBeTruthy()` — the next test (line 155) tightens to regex; this assertion is redundant + weak. | Drop the weak one or replace with `expect(TIER_COLORS[tier]).toMatch(/^#[0-9a-f]{6}$/i)` directly. |
| web/src/components/chat/MessageBubble.test.tsx:108 | MED | Weak assertion (`toBeTruthy`) | `expect(cost).toBeTruthy()` after cost computation — should assert the actual numeric/string. | Replace with `expect(cost.textContent).toContain("$")` or actual cost value. |
| web/src/components/chat/ChatPane.test.tsx:31,32,55,77,97 | MED | Weak assertion (`toBeTruthy`) batch | Five tests assert mounted DOM elements are merely truthy. Doesn't catch wrong content / role / aria. | Replace with content / role / aria-label assertions. |
| web/src/components/chat/PlanCard.test.tsx:25,59,65,66 | MED | Weak assertion batch | Same — `getByTestId(...).toBeTruthy()`. | Assert checked-state, item count, ordering. |
| web/src/components/chat/ToolCard.test.tsx:33,42,80,94,95 | MED | Weak assertion batch | Same — element merely present. | Assert tool name visible, expanded state correct, output formatting. |
| web/src/components/chat/DiffCard.test.tsx:68,76,77 | MED | Weak assertion batch | Same. | Assert hunk content + add/remove counts. |
| web/src/components/chat/StopButton.test.tsx:14,15 | LOW | Weak assertion | Element-truthy. The aria-label query already implies presence. | Drop one of the two; one is redundant. |
| web/src/components/chat/MessageBubble.test.tsx:56,80,91,92 | MED | Weak assertion batch | Same `toBeTruthy()`. | Strengthen to content checks. |
| web/src/components/chat/MessageLog.test.tsx:65,146 | MED | Weak assertion | Empty-state element truthy. | Assert empty-state copy ("No messages yet" or similar). |
| web/src/components/chat/ReasoningCard.test.tsx:42,72,73,94,98 | MED | Weak assertion batch | Same. | Assert reasoning text + collapsed/expanded state. |
| web/src/components/chat/Composer.test.tsx:34,38 | LOW | Weak assertion | Element truthy. | Assert input is enabled / has placeholder. |
| web/src/components/session/NewSessionDialog.test.tsx:40-43 | MED | Weak assertion batch | Four toBeTruthy on form elements. | Assert form fields' default values, validation messages. |
| web/src/components/lanes/LaneTile.test.tsx:51,73 | MED | Weak assertion batch | Lane tiles truthy. | Assert lane name, status badge, ghost-missing label content. |
| web/src/lib/store/daemonStore.test.tsx:323 | LOW | Weak assertion (`toBeDefined`) | Sets up `beforeSessions` then asserts defined. The subsequent `toBe(beforeSessions)` reference-equality is the real assertion. | Drop the redundant `toBeDefined()`. |
| desktop/tests/e2e/daemon-discovery.spec.ts:38,53,79 | MED | Weak assertion (`toBeTruthy`) | `expect(app).toBeTruthy()` after `launchDesktopApp`. Real test exists in subsequent banner-state asserts. | Drop the redundant `toBeTruthy()` line. |
| desktop/tests/e2e/popout-lane.spec.ts:53 | MED | Weak assertion | Same `expect(app).toBeTruthy()`. | Drop. |
| desktop/tests/e2e/lanes-streaming.spec.ts | LOW | Mocked-but-should-be-real | Driven by tauri-driver helpers — fixture seems substantive but depends on `desktop-fixtures` shim that may not actually bind a real WebView. | Confirm helpers spawn a real Tauri instance in CI; otherwise the E2E coverage is illusory. |
| web/src/hooks/useWorkdir.test.tsx:92,93 | LOW | Race-y (microtask flush) | `await new Promise((r) => setTimeout(r, 0))` x2 to flush microtasks. Acceptable React idiom. | OK; consider `await act(async () => { ... })` for clarity. |
| internal/ledger/anchor.go:142,208,227,277,303,337,367,530 | HIGH | Missing tests | Eight exported functions on `AnchorStore` (Append, LastAnchor, ReadChain, VerifyChain) + `LeafDigestForNode`, `ComputeAnchor`. No `anchor_test.go`. Anchor chain integrity is load-bearing for ledger trust. | Add `anchor_test.go` with: append-then-readchain, ComputeAnchor determinism, VerifyChain detects a tampered link. |
| internal/ledger/edge_matrix.go | LOW | Missing tests | No exported funcs (data-only). | OK if data correctness is checked at usage sites. |
| internal/ledger/index.go:20,35,66,72,80,88,100,109,157,181 | MED | Missing tests | `Index` SQLite schema + node/edge insert + EdgesFrom/EdgesTo. Indirectly tested via ledger_test.go (drift judge_scheduled invokes). No direct schema test. | Add `index_test.go` with: CreateTables idempotency, InsertNode then QueryNodes round-trip, Drop+recreate. |
| internal/ledger/store.go:68,107,163,211,227,248 | MED | Missing tests | `Store` (filesystem node store) WriteNode/ReadNode round-trip not directly tested. Covered indirectly via ledger.New. | Add `store_test.go`: WriteNode→ReadNode equality, ListNodes ordering, error on missing node. |
| internal/bus/wal.go:30,90,97,122,134,165,186,208,249 | MED | Missing tests | WAL OpenWAL / Append / FindByID / ReadFrom / AppendDelayed / ReadDelayed / AppendDelayedCancel / Close. Covered indirectly by bus_test (some). FindByID and AppendDelayedCancel not exercised. | Add `wal_test.go`: replay-after-restart, FindByID hits & misses, delayed cancel removes from ReadDelayed. |
| internal/bridge/verify.go:21,31 | MED | Mocked-but-should-be-real | bridge_test.go runs VerifyBridge with `buildCmd="true"` only — never exercises a failing build. Verify-failure path untested. | Add a verify-bridge test where `buildCmd="false"` and assert verify.Outcome marked failed + a ledger node written. |
| internal/bridge/wisdom.go:21,30,64,69 | LOW | Missing partial coverage | NewWisdomBridge + Record covered. ForPrompt + FindByPattern less directly tested. | Add coverage for FindByPattern with a known hash. |
| internal/supervisor/rule.go:47 | MED | Missing direct test | `ValidatePayload` is the central rule-payload validator. Tested indirectly via every rule's _test.go. No direct test of error shape on schema mismatch. | Add `rule_test.go`: ValidatePayload returns wrapped error containing field path on missing field. |
| internal/supervisor/schemas.go:24,44,65,81 | MED | Missing direct test | Four schema-builder funcs (WorkerPaused / SpawnRequested / EscalationForwarded / ConsensusLoopState). No `schemas_test.go`. | Add direct schema-validation tests with valid + invalid payloads. |
| internal/harness/stances/{cto,dev,judge,po,reviewer,sdm_stance,stakeholder,vp_eng,qa_lead,lead_designer,lead_engineer}.go | LOW | Missing direct tests (batch) | 11 stance-template files; only `stances_test.go` covers shared registry (5 refs). Per-stance prompt + tool-set content untested. | Add a table-driven test asserting each registered stance has non-empty Prompt + DefaultTools and the role string matches the filename. |
| (batch — covered, no action) | LOW | Covered | cortex/cortex.go (16 tests), cortex/lane.go (Clone, IsTerminal), cortex/budget.go, cortex/router.go, harness/session.go (CheckpointCheck via pause/resume), harness/spawn.go (helpers only), bridge/audit.go, bridge/cost.go, bridge/wisdom.go (basic), bridge/{cost,audit}.go all covered indirectly or via _test pair. Unexported helpers in cortex/lobes/{memorycurator,planupdate} exercised by neighbour tests. | OK. |

---

## Top 10 by impact

These are the items most likely to catch real regressions if strengthened.

1. **`internal/ledger/anchor.go` (8 exported funcs untested)** — anchor chain hash integrity is the trust root for the v2 ledger. A regression in `ComputeAnchor` or `VerifyChain` is silent until cross-anchor verification fails. Add direct tests covering: deterministic ComputeAnchor, append→readchain round-trip, tamper detection in VerifyChain.

2. **`integration_test.go:1104,1119,1133` (`TestDashboardStateBridge` × 3 sleeps)** — load-bearing dashboard state bridge tested with `time.Sleep(50ms)` after async hub.Emit. This will flake on slow CI and `-race`, hiding real races in observer registration. Replace sleeps with synchronous channel-signal subscriber barriers.

3. **`cmd/r1-server/{graph_e2e_test.go:62, e2e/e2e_test.go:63}` (env-gated v2 E2E)** — both v2 graph + v2 server E2E suites silently skip without `R1_SERVER_UI_V2=1`. Default CI almost certainly does not set this. Force-enable in CI; otherwise the v2 server ships with zero E2E coverage in the default lane.

4. **`internal/redteam/corpus_test.go:250,274` (corpus-data skip)** — red-team regression suite skips entirely when `corpus/known-misses/` is missing or empty. A drifted scanner that misses real attacks will pass CI silently. Vendor the corpus into `testdata/`.

5. **`cmd/r1/serve_install_test.go:180`** — service-install end-to-end gated on `R1_TEST_SERVICE_INSTALL=1`. Service install is a fragile platform-specific path; skipping by default means a regression hits production users first.

6. **Supervisor rule tests' shared `time.Sleep(5ms)` poll pattern (~30 files)** — every rule test file uses `for { sleep(5ms) }` to wait for emitted events. Without consistent outer deadlines, the suite is one slow CI run away from cascading flakes. Refactor to a shared `waitForRuleEvents(t, bus, types, deadline)` helper.

7. **`internal/cortex/lane_lifecycle_test.go:250,365` (no-op verification via sleep)** — terminal-lane no-op tests rely on "sleep 50ms then count events didn't grow." If a future bug emits the event 60ms later, the test still passes. Replace with barrier-event approach: emit a sentinel after the action, wait for it, then assert.

8. **Web chat component tests (`ToolCard`, `ChatPane`, `MessageBubble`, `PlanCard`, `DiffCard`, `MessageLog`, `ReasoningCard`, `LaneTile`, `NewSessionDialog`) — 79 `.toBeTruthy()` weak assertions** — these tests pass even if the rendered content is empty/wrong, only checking that an element with the test-id exists. A whole class of "blank card", "missing data", "wrong field formatted" regressions slips through. Strengthen each to assert content, role, and aria-label.

9. **`internal/cortex/interrupt_test.go:152,160` (goroutine-leak detection via timing)** — leak detection by `runtime.NumGoroutine` before/after with sleeps. Inherently flaky; will either false-positive (CI noise) or false-negative (sleep too short). Adopt `goleak.VerifyTestMain` or `goleak.VerifyNone(t)`.

10. **`internal/agentloop/integration_antitrunc_test.go:3` (BLOCKED on cortex-core)** — the comment says the real integration test is BLOCKED waiting for cortex-core. Cortex-core is now in tree (`internal/cortex/...`). The substitute test exercises only the agentloop+gate pair, not the cortex→stance→agentloop end-to-end path described in spec §item 25. Wire the real test now that the dependency landed; this is the file that actually proves the layered defense works in production conditions.
