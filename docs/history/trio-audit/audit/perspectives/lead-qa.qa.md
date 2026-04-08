# Lead QA Audit — Test Coverage & Quality

**Date:** 2026-04-01
**Scope:** ember/devbox, flare, stoke
**Method:** Full read of all test files + key source files

---

## Summary

| Repo | Test files | Key gaps | Fake/weak | Missing integration |
|------|-----------|----------|-----------|-------------------|
| ember/devbox | 3 (billing, identity, terminal) | HTTP routes, middleware, rate-limit, webhook verification | Low | CRITICAL: no route-level tests |
| flare | 3 (manager_test, reconciler_test, integration_test) | firecracker Manager methods, networking, store, reconcileOne | Low | HIGH: integration tests all t.Skip'd |
| stoke | 29 test files | workflow live-run path, context compaction ordering, hooks guard logic | Low | HIGH: dry-run only for workflow |

Overall quality is good: assertions are specific, no `_ = result` fake patterns, no always-true checks. The main exposure is **breadth** (untested critical paths) rather than assertion weakness.

---

## Findings

### ember/devbox

- [ ] **CRITICAL** [ember/devbox/src/routes/billing.ts, src/routes/auth.ts, src/middleware.ts] **Zero HTTP route tests** — The entire Hono route layer (billing checkout, Stripe webhook handler, auth callbacks, machine operations, rate limiting, CSRF) has no test coverage at all. The three test files only exercise raw SQL constraints. The billing webhook handler alone is ~400 lines of business logic that handles subscription creation/cancellation, machine provisioning signals, and payment recovery. — fix: Add Hono `app.request()` integration tests that mock Stripe and the DB, covering at minimum: webhook `customer.subscription.created`, `customer.subscription.deleted`, `invoice.payment_failed`, `invoice.paid`; the checkout intent idempotency path; and the `POST /v1/terminal/auth` exchange-code path. — effort: large

- [ ] **CRITICAL** [ember/devbox/src/middleware.ts] **requireAuth / requireAdmin untested** — The session-cookie auth, banned-user check (403), and admin gate are exercised zero times in tests. A regression here would silently expose all authenticated routes. — fix: Add route-level tests with mock session cookies for: unauthenticated (401), valid session, expired session (blank cookie), banned user (403), admin endpoint accessed as non-admin. — effort: medium

- [ ] **CRITICAL** [ember/devbox/src/v1-auth.ts] **requireApiKeyV1 untested** — The API key hash validation and user-status check used by all `/v1/*` endpoints has no test. The SHA-256 hash join, the `user_status !== "active"` branch, and the async `last_used_at` update are all untested. — fix: SQL-level test: insert an api_key row with a known hash, call the validation query directly, assert the 401/403 branches. — effort: small

- [ ] **HIGH** [ember/devbox/src/routes/billing.ts:~150] **Stripe webhook signature verification untested** — The webhook handler likely calls `stripe.webhooks.constructEvent()` with a secret. There is no test for signature validation rejection (tampered payload) or what happens when `STRIPE_WEBHOOK_SECRET` is missing. — fix: Test with a forged payload that should produce 400, and with a valid HMAC-signed payload that should succeed. — effort: small

- [ ] **HIGH** [ember/devbox/src/__tests__/billing.test.ts:131-148] **Reconciler logic embedded in test body** — The `"needs_stop machine is cleared when slot is reactivated"` test writes the reconciliation logic inline in the test (`if (s.status === "active") { await testSql... }`). This tests the test's own code, not application code. The actual reconciler that runs this logic is not tested at all. — fix: Extract the reconciliation logic to an application function and test that function, or mark this test as a schema-validation-only test and add a separate test for the actual reconciler function. — effort: small

- [ ] **MEDIUM** [ember/devbox/src/__tests__/billing.test.ts] **No test for slot status transition constraints** — Tests verify cancellation and reactivation via raw SQL updates, but there is no test that a slot cannot transition from "cancelled" to an invalid state, or that the `cancelled_at` timestamp is always set when status becomes "cancelled". — fix: Add constraint tests: verify `cancelled_at IS NOT NULL` when status is "cancelled", verify that updating status to an unknown value is rejected by a check constraint if one exists. — effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/rate-limit.ts] **Rate limiter untested** — The in-memory and Postgres rate limiting backends have zero tests. The window-sliding logic, the `getClientIp` header priority chain (fly-client-ip > x-forwarded-for > x-real-ip), and the 429 response path are all untested. — fix: Unit test `memCheck` directly: fill window, verify block; verify `remaining` counter; verify window slides after time advances. — effort: small

- [ ] **MEDIUM** [ember/devbox/src/__tests__/identity.test.ts] **No test for email uniqueness constraint** — Users table almost certainly has a unique index on `email`, but the identity tests never attempt to insert a duplicate email. A migration removing that constraint would not be caught. — fix: Add: `createTestUser({email:'dup@t.com'}); expect(createTestUser({email:'dup@t.com'})).rejects.toThrow()` — effort: trivial

---

### flare

- [ ] **CRITICAL** [flare/cmd/control-plane/integration_test.go:112,188,239,265] **All integration tests are permanently t.Skip'd** — `TestVMLifecycleCreateStartStopDestroy`, `TestTwoVMsRouteCorrectly`, `TestDaemonRestartRecovery`, and `TestDestroyFailedCreate` all call `t.Skip()` unconditionally. The file has a `//go:build integration` tag, but even with that tag the four substantive tests skip immediately. Only `TestReconcilerMarksMachinesLost` (which executes raw SQL directly, not via the actual reconciler code) actually runs. The four critical invariants documented in the file header ("state survives restart", "placement survives daemon restart", "two VMs route correctly", "create/start/stop/destroy return truthful responses") have no passing test at any level. — fix: At minimum implement `TestDestroyFailedCreate` (requires only a running Postgres, no Firecracker binary) by starting a real control-plane HTTP server in-process using `httptest.NewServer`. Implement `TestDaemonRestartRecovery` using subprocess management with `os/exec` and a mock or stub placement daemon. — effort: large

- [ ] **CRITICAL** [flare/internal/firecracker/manager_test.go] **Manager has only one test (GenerateMAC)** — The `Manager` struct has `Create`, `Start`, `Stop`, `Destroy`, `RecoverFromDisk`, `List`, `Get`, `State` methods. Only `GenerateMAC` (a pure utility function) is tested. The path-traversal guard in `Create` (image_ref validation), the idempotency check in `Start`, the PID-zero early-return in `Stop`, and the `persistVM`/`RecoverFromDisk` round-trip are all untested. — fix: Add unit tests that do not require Firecracker binary: (1) `Create` with invalid `ImageRef` containing `../` should return error; (2) `Create` with valid opts should write `vm.json` to a temp dir; (3) `RecoverFromDisk` should repopulate `vms` map from a pre-written `vm.json`. These require only filesystem access. — effort: medium

- [ ] **HIGH** [flare/internal/reconcile/reconciler_test.go] **reconcileOne has zero unit tests** — `computeAction` has thorough table-driven tests, but `reconcileOne` (the function that actually calls `PlacementClient` and `Store`) is never tested with a mock. The backoff logic, the "host not ready" branch, the "observe then release" path, and the `actionCleanupDestroyed` dead-host bypass are untested. — fix: Define a `mockPlacement` and `mockStore` implementing the interfaces, then write table-driven tests for `reconcileOne` covering: start succeeds immediately (returns running), start fails (backoff called), stop with no host (observed updated directly), cleanup with dead host (skips placement call). — effort: medium

- [ ] **HIGH** [flare/internal/reconcile/reconciler_test.go:297] **TestReconcilerMarksMachinesLost executes raw SQL, not reconciler code** — This test bypasses the actual `Store.MarkDeadHosts` and `Store.MarkMachinesLostOnDeadHosts` methods and reproduces their logic directly in the test body. If those methods are changed, this test will not catch the regression. — fix: Use the real `store.Store` against the test DB (the integration test already has a DB connection), call `cfg.Store.MarkDeadHosts()` and `cfg.Store.MarkMachinesLostOnDeadHosts()` directly, assert the results match expected. — effort: small

- [ ] **HIGH** [flare/internal/networking/tap.go] **Networking layer has zero tests** — `Manager.AllocateTAP`, `Manager.ReleaseTAP`, `Manager.SetupBridge`, and `GenerateMAC` (in manager.go) are completely untested apart from the MAC address utility. The IP allocation (`nextIP`, `freeIPs` recycle) logic is non-trivial and has no test. — fix: Unit-test the Manager's IP allocation logic without actually creating TAP devices: verify that sequential allocations get sequential IPs, that releasing an IP puts it back in `freeIPs`, that exhaustion (>253 IPs) returns an error. Use dependency injection or extract the allocation logic to a pure function. — effort: medium

- [ ] **MEDIUM** [flare/internal/reconcile/reconciler_test.go] **Missing: desired=destroyed with no host** — `computeAction` is tested for `destroyed/running`, `destroyed/stopped`, `destroyed/destroyed` — all with a valid `HostID`. The case where `HostID == ""` and `DesiredState == StateDestroyed` is not covered. The code in `reconcileOne` handles this (skips placement call), but `computeAction` itself returns `actionCleanupDestroyed` regardless of host presence. — fix: Add table entry: `{name: "destroyed/no-host -> cleanup", machine: store.Machine{HostID: "", DesiredState: api.StateDestroyed, ObservedState: api.StateStopped}, expected: actionCleanupDestroyed}` and verify the integration path skips placement. — effort: trivial

- [ ] **MEDIUM** [flare/internal/reconcile/reconciler_test.go] **Missing: desired=stopped with lost/unknown observed state** — `computeAction` has no test for `desired=stopped, observed=lost` or `desired=stopped, observed=unknown`. These fall through to the default `actionNone`, which may or may not be correct. — fix: Add table entries to confirm the intended behavior for these state combinations. — effort: trivial

---

### stoke

- [ ] **CRITICAL** [stoke/internal/workflow/workflow.go] **No test for the live (non-dry-run) workflow path** — `workflow.Engine.Run` has two branches: dry-run (returns `StepResult` with prepared commands, never executes) and live (actually calls engine runners, handles retries, merges). The live path — pool acquire, process spawn, stream parse, verify, cross-model review, merge — is never tested even with mocks. The retry loop (`for attempt := 1; attempt <= maxAttempts; attempt++`) is not exercised. A regression in the retry prompt, evidence recording, or merge-on-success path would not be caught. — fix: Add a test using stub runners (`CommandRunner` interface) that return pre-canned `RunResult` values, exercising: first attempt fails build, second attempt succeeds; cross-model review rejects on first pass; scope violation blocks merge. These do not require real Claude/Codex processes. — effort: large

- [ ] **HIGH** [stoke/internal/context/context_test.go] **Compaction tests do not assert specific eviction outcomes** — `TestCompactGentle` asserts only that *some* compaction happened (level != "none") but does not verify which blocks were evicted. `TestCompactAggressive` asserts "keep" is in assembled output but does not verify that the junk block is absent. A regression that compacts the wrong tier would not be caught. — fix: After `Compact()`, call `m.Assemble()` and assert that low-priority `TierSession` blocks are absent (not in output) while high-priority `TierActive` blocks are present. — effort: trivial

- [ ] **HIGH** [stoke/internal/hooks/hooks_test.go] **Hook script content is not verified** — `TestInstallCreatesHookFiles` confirms that `pre-tool-use.sh` and `post-tool-use.sh` exist and are executable, but does not check their content. The guard logic (protected file rejection, git mutation block, destructive command detection) is entirely untested. — fix: Add tests that execute the installed hook scripts with representative inputs and verify exit codes: (1) write to `.claude/settings.json` → should exit non-zero; (2) write to `src/auth.ts` → should exit zero; (3) `Bash(rm -rf /)` → should exit non-zero. — effort: medium

- [ ] **HIGH** [stoke/internal/stream/parser_test.go] **No timeout/idle scenario tests** — The parser has three configurable timeouts (`StreamIdleTimeout`, `PostResultTimeout`, `GlobalTimeout`) but the tests only exercise normal EOF and broken-JSON cases. A stream that hangs without producing events (idle timeout) or one that produces a result but then hangs (post-result timeout) is never tested. — fix: Add a test that feeds events via a pipe and then stalls; assert that the channel closes within the configured idle timeout window. Use short timeout values (10-50ms) to keep the test fast. — effort: small

- [ ] **HIGH** [stoke/internal/workflow/workflow_test.go:49] **TestDryRunPhaseEngineRouting uses TaskTypeDevOps** — The test asserts `result.Steps[1].Engine == "codex"` for DevOps tasks but this relies on `model.Routes` routing. If the routing table is changed, the test fails but the workflow logic itself is not tested. More importantly, there is no test for what happens when the resolved engine's pool is unavailable (fallback chain). — fix: Add a test with a `stubManager` that returns pool-unavailable, verify the workflow falls back through the provider chain. — effort: small

- [ ] **MEDIUM** [stoke/integration_test.go:216] **TestSchedulerWithDeps does not verify C runs after A and B** — The test verifies `A` precedes `B` but does not assert that `C` (independent) can run in parallel with `A`, nor that `C` ran at all. The `order` slice ordering for `C` is unchecked. — fix: Assert `len(order) == 3` and verify `indexOf(order, "B") > indexOf(order, "A")` (already done), then also add `if indexOf(order, "C") < 0 { t.Error("C never ran") }`. — effort: trivial

- [ ] **MEDIUM** [stoke/internal/scheduler/scheduler_test.go:57] **TestFileConflictSequential is flaky under load** — The test measures `maxConcurrent` using `atomic.CompareAndSwap` to track peak concurrency and asserts `maxConcurrent <= 1`. The 10ms sleep is long enough that on a heavily loaded CI host, both goroutines could overlap in the brief window between `atomic.AddInt32(&current, 1)` and the sleep, making the assertion non-deterministic. — fix: Add explicit mutex locking around the concurrent section and record start/end timestamps for each task, then assert the time intervals do not overlap. — effort: small

- [ ] **MEDIUM** [stoke/internal/failure/analyzer_test.go] **No test for Rust/Clippy failure classification** — The analyzer claims support for "10 classes, TS/Go/Python/Rust/Clippy parsers" (per CLAUDE.md). Tests cover TS build failure, Go build failure, Jest test failure, Go test failure, policy violations, timeout, and the incomplete case. Rust (`error[E0308]: mismatched types`) and Clippy (`warning: clippy::...`) classification paths are not exercised. — fix: Add `TestClassifyBuildFailureRust` with a `rustc` error pattern and `TestClassifyClippyViolation` with a clippy output pattern. — effort: trivial

- [ ] **MEDIUM** [stoke/internal/session/store_test.go + sqlstore_test.go] **No test for concurrent SaveAttempt** — Both stores are used from parallel scheduler goroutines. Neither test exercises concurrent writes. SQLite in WAL mode serializes writes, but the JSON file store uses file rewrites that could race. — fix: Add a concurrent test: 10 goroutines each call `SaveAttempt` for different task IDs simultaneously; assert all 10 attempts are retrievable with no data loss. — effort: small

- [ ] **MEDIUM** [stoke/internal/worktree/manager_test.go] **TestPrepareAndCleanupWorktree does not test Merge** — The manager test only tests `Prepare` and `Cleanup`. The `Merge` method (which uses `git merge-tree --write-tree` for conflict validation and `mergeMu` for serialization) is not tested in the unit test. It is tested indirectly via the integration test, but only for the happy path. — fix: Add a test for: merge with a conflict (two worktrees both modify the same file) — should return an error, not silently corrupt main. — effort: small

- [ ] **LOW** [stoke/internal/app/app_test.go] **TestDoctor only checks output is non-empty** — `Doctor("nonexistent-claude", "nonexistent-codex")` is called and the test only asserts `result != ""`. A Doctor function that returns `"ok"` for missing binaries would pass. — fix: Assert that the result contains a specific indication that the binaries were not found, e.g. `strings.Contains(result, "not found")`. — effort: trivial

- [ ] **LOW** [stoke/internal/scan/scan_test.go] **No test for eval() detection in JS** — The scan test verifies `node_modules` is skipped (the test file uses `eval` in the node_modules fixture) but there is no test that `eval()` in a non-ignored source file produces a finding with the expected rule name. — fix: Add `os.WriteFile(dir, "src.js", []byte("eval('danger')"), 0644)` and assert `f.Rule == "no-eval"` is present in findings. — effort: trivial

---

## Test Infrastructure Observations

**ember/devbox/src/__tests__/setup.ts**
- `SET session_replication_role = replica` in `afterEach` disables FK checks for truncation. This is correct but means constraint violations during cleanup are silently swallowed. If a test leaves the DB in a bad state, the next test may see unexpected rows.
- `createTestMachine` always uses `fly_machine_id = 'fm_test'` (hardcoded). If a unique constraint exists on `fly_machine_id`, multi-machine tests will fail spuriously. — Recommend generating a unique value per call.

**flare/cmd/control-plane/integration_test.go**
- `TestReconcilerMarksMachinesLost` requires `DATABASE_URL` to be set and modifies real database tables. There is no isolation (no transaction rollback, cleanup is best-effort). If the test crashes mid-run, `host-dead` and `m-on-dead-host` rows leak into the test database. — Recommend wrapping in a transaction that is always rolled back.

**stoke/integration_test.go**
- `setupGitRepo` creates a temporary git repo per test. This is correct and well-isolated.
- `TestParallelWorktreesMerge` merges sequentially (h1 then h2). It does not test the `mergeMu` concurrent path that the production code protects. Two goroutines racing to merge is not exercised.
