# RT-cmd-r1-test-isolation — `cmd/r1` test-isolation bug map (audit finding I1)

Read-only investigation. All paths absolute. Line numbers verified against the working tree on the
`fix/desktop-tauri-types-and-vite-override` branch.

Live symptoms observed this session while running `go test ./cmd/r1`:

1. ~120 orphaned `r1.test serve` daemon processes leaked.
2. Real `git` commands (init/commit/pull, messages `c1`/`divergent`) ran against the ACTUAL repo
   root, racing and recreating `.git/index.lock`, blocking commits.
3. `TestStartSessionCtlServer_DefaultStatus_ModeMatches` flaky with `bind: invalid argument`
   (unix socket path > `sun_path` limit).

Root causes, exact sites, and a comprehensive fix surface follow.

---

## 1. LEAKED SERVE DAEMONS

### Primary leak — `TestRealSpawnDaemon_LaunchesDetachedProcess`

- **Test:** `TestRealSpawnDaemon_LaunchesDetachedProcess`
- **File:** `/home/eric/repos/r1-agent/cmd/r1/daemon_http_test.go:165` (calls `realSpawnDaemon()` at
  line **177**).
- **How it starts the process:** It calls the *real* production `realSpawnDaemon()` with no mock.
  - `realSpawnDaemon()` is defined at `/home/eric/repos/r1-agent/cmd/r1/daemon_http.go:108`.
  - Line **109**: `exe, err := os.Executable()` — under `go test` this resolves to the compiled
    **test binary** (`.../cmd/r1.test` or a `go test` temp binary), **not** a real `r1`.
  - Line **113**: `cmd := exec.Command(exe, "serve")` — spawns `<test-binary> serve`.
  - Line **117**: `applyDetachAttrs(cmd)` → `/home/eric/repos/r1-agent/cmd/r1/daemon_http_unix.go:20`
    sets `cmd.SysProcAttr.Setsid = true`. The child is detached into its **own session + process
    group**, so it survives the parent test process and is NOT in the test's group.
  - Line **130**: `cmd.Start()` — fire-and-forget. The function returns immediately (line **135**).
- **Where it kills/reaps it:** **NOWHERE.** `realSpawnDaemon()` never retains a reference to
  `cmd.Process`, never calls `cmd.Wait()`, and the test has no `t.Cleanup`, no `cmd.Process.Kill()`,
  no `Process.Release`. The only "cleanup" is `_ = devnull.Close()` (daemon_http.go:134), which
  closes the parent's FD, not the child.

### Why ~120 leaked — recursive test-binary fan-out

- There is **no `func TestMain(m *testing.M)`** in package `main` under `cmd/r1`. Verified: the only
  `TestMain*` symbols are the *test functions* `TestMain_DaemonAlias_WritesHintAndForwards` and
  `TestMain_AgentServeAlias_WritesHintAndForwards` at
  `/home/eric/repos/r1-agent/cmd/r1/serve_aliases_test.go:23` and `:54`. There is no special test
  main that intercepts a `serve` argv and exits.
- Therefore `<test-binary> serve` runs under Go's **default test main**. The positional `serve`
  token is not a recognized `-test.*` flag; the default main does not filter on it, so the spawned
  binary **re-runs the entire `cmd/r1` test suite** (no `-test.run` filter is passed by
  `realSpawnDaemon`; contrast daemon_http_test.go:113 — only `"serve"`).
- That recursive run **itself executes `TestRealSpawnDaemon_LaunchesDetachedProcess`**, which calls
  `realSpawnDaemon()` again → spawns yet another detached `<test-binary> serve`. This is an
  exponential/fan-out spawn. Each generation is `Setsid`-detached and never reaped → the ~120
  orphaned `r1.test serve` processes.
- The comment at daemon_http_test.go:170-176 asserts the child "exits immediately because /bin/true
  treats serve as an unknown argument" — but the child is the **test binary**, not `/bin/true`. The
  comment's stated mechanism does not hold; the binary does not exit fast, it re-runs the suite.
- Secondary aggravation: each detached child inherits the test process **cwd** (the real repo root
  under `go test ./cmd/r1`), so the recursive suite runs touch real-tree paths (feeds symptom 2).

### Other process-spawning serve tests (NOT leaking — for completeness)

- `/home/eric/repos/r1-agent/cmd/r1/mcp_serve_runtime_test.go` — 4 tests
  (`TestMCPServeRuntime_ThreeMessageExchange` :16, `_AuthGate` :95, `_DeterministicLobes` :158,
  `_NoCortex` :201) spawn `r1 mcp serve` via `exec.Command(r1Bin, "mcp", "serve", ...)` (lines 22,
  103, 162, 205). `r1Bin` is built by `buildR1ForTest` (:258) into `t.TempDir()`. Each pipes a
  **bounded** stdin buffer; the MCP stdio server reads EOF and exits. `cmd.Output()` waits on the
  child → reaped. NOT a leak, but: (a) `buildR1ForTest` shells out to `go build` (slow, but
  `testing.Short()`-guarded at :260), (b) `build.Dir = wd` where `wd = os.Getwd()` is the real
  package dir (read-only, acceptable). If any of these stdio servers ever block on input instead of
  EOF, they would leak — keep the bounded-stdin + `cmd.Output()` (waited) invariant.
- `/home/eric/repos/r1-agent/cmd/r1/serve_single_instance_test.go` —
  `TestSingleInstance` (:56) re-execs the test binary via `runHelperProcess` (:112) with
  `-test.run=TestHelperProcess_SecondInstance` (:121) under a context timeout (:118) and
  `cmd.Run()` (:131, waited). The helper `TestHelperProcess_SecondInstance` (:157) exits via
  `os.Exit`. Properly filtered + waited → NOT a leak. This is the **correct re-exec pattern** the
  leaking test should imitate.
- `/home/eric/repos/r1-agent/cmd/r1/pipe_watchdog_test.go:71` —
  `TestKillChildProcessGroup_KillsSilentChild` spawns `bash -c "echo hello; sleep 60"` with
  `Setpgid` (:72), then `killChildProcessGroup(cmd, ...)` (:97) and waits on `done` (:81, :108).
  This test exists to verify the kill path; child is reliably killed. NOT a leak.
- `/home/eric/repos/r1-agent/cmd/r1/daemon_http_test.go:99` (`TestDaemonHTTP_AutoSpawn`) and the
  `httptest.NewServer` users below — in-process `httptest` servers with `defer srv.Close()` /
  `t.Cleanup(srv.Close)`. NOT a leak.

---

## 2. GIT-IN-REPO-ROOT

### Finding: NO explicit test `exec.Command("git", …)` call lacks isolation

Every direct `exec.Command("git", …)` in `cmd/r1/*_test.go` sets `cmd.Dir` to a `t.TempDir()` (or
passes `git -C <tempdir>`). Audited exhaustively:

- `/home/eric/repos/r1-agent/cmd/r1/simple_loop_state_test.go` — helpers `initGitRepoWithCommit`
  (:14, `cmd.Dir = dir` at :18, :33), `appendCommit` (:45, `cmd.Dir = dir` at :52, :63), the inline
  `run` in `TestValidateResumeCompat_HeadDivergedRefuses` (:353, `cmd.Dir = repo` at :354), and
  `git -C repo add` (:475). Commit messages `c1` (:309, :394, :416, :440, :457, :469, :493),
  `divergent` (:369), `c2` (:310, :470) all land in **isolated `t.TempDir()` repos**. These are the
  `c1`/`divergent` strings observed live, but the *test code itself* is isolated.
- `/home/eric/repos/r1-agent/cmd/r1/task_cmd_test.go` — `gitCmd` (:202, `cmd.Dir = dir` :203),
  `commitChange` (:233, `cmd.Dir = dir` :234), messages `init` (:221), all temp-dir scoped.
- `/home/eric/repos/r1-agent/cmd/r1/antitrunc_cmd_test.go` — `initRepo` (:54, `c.Dir = dir` :59,
  `cmd.Dir = dir` :75), temp-dir scoped.
- `/home/eric/repos/r1-agent/cmd/r1/descent_bridge_bootstrap_test.go` — `initTempGitRepo` (:14,
  `cmd.Dir = dir` :18, :52), `git init` (:26), temp-dir scoped.
- `/home/eric/repos/r1-agent/cmd/r1/skills_pack_cmd_test.go` — `runGit(t, dir, …)` helper (:1018,
  `cmd.Dir = dir` :1022) always takes an explicit dir; callers pass temp dirs (e.g.
  `TestUpdateSkillPackPullsExternalGitSourceAndInstallsNewDependency` :879 uses bare remote/clone
  under `t.TempDir()` and `t.Setenv("HOME", home)` :882).

### Real cause: production code resolves git cwd via process cwd / walks up to the real repo

The repo-root `.git/index.lock` contention does **not** come from explicit test git calls — it comes
from production code reached by tests that resolves a git root relative to the **process cwd** (which
is the real repo root under `go test ./cmd/r1`) or walks **up** the directory tree into the real
repo. The two concrete vectors:

- **Recursive serve fan-out (section 1):** the leaked `<test-binary> serve` children inherit the
  test process cwd (real repo root) and re-run the entire suite there. Any suite code that runs git
  against `.` or that creates `.git/index.lock` then races the operator's real commits. This is the
  dominant source of the observed `.git/index.lock` thrash, and it is fixed by fixing section 1.
- **Skill-pack `git pull --ff-only` upward walk:** production
  `/home/eric/repos/r1-agent/cmd/r1/skills_pack_cmd.go`:
  - `gitTopLevel(dir)` at :1443 runs `git -C dir rev-parse --show-toplevel` (:1444). If `dir` is a
    pack directory that is **not itself a git repo**, `--show-toplevel` walks **up** and can resolve
    to the **enclosing real repo** (`/home/eric/repos/r1-agent`).
  - `refreshSkillPackSource` (:1405) guards this with `pathWithin(repoRoot, gitRoot)` (:1419) and
    `pathWithin(repoRoot, sourcePath)` (:1406), skipping repo-local pulls
    (`skillPackPullStatusSkippedRepoLocal`). But the guard's `repoRoot` is the **caller-supplied**
    repo (the test's `t.TempDir()`), NOT the real enclosing repo. If a test invokes
    `updateSkillPack(tempRepo, …)` while a pack/source path under `$HOME` or cwd resolves up into the
    *real* `r1-agent` checkout (e.g. when `HOME` is not sandboxed), `gitTopLevel` returns the real
    repo root, `pathWithin(tempRepo, realRoot)` is **false**, and line :1433
    `git -C <realRoot> pull --ff-only` runs against the **real repository**, creating
    `.git/index.lock` and racing operator commits.
  - The exposed test `TestUpdateSkillPackSkipsRepoLocalGitPullAndRelinksDependencies`
    (`skills_pack_cmd_test.go:845`) calls `updateSkillPack(repo, "app-pack")` with `repo` a
    `t.TempDir()` but does **NOT** sandbox `HOME` (contrast `:882` in the sibling external-pull test
    which does `t.Setenv("HOME", home)`). It relies on the pack being repo-local; if path resolution
    walks outside the temp repo, the `--ff-only` pull can target the real tree.

**Fix for section 2:** (a) eliminate the recursive serve fan-out (section 1), which removes the bulk
of the contention; (b) for the skill-pack tests, sandbox `HOME` (and any env the resolver reads) to
a `t.TempDir()` in every `updateSkillPack`/`installSkillPack` test, and assert the resolved git root
is within the temp repo; (c) optionally harden production `gitTopLevel`/`refreshSkillPackSource` to
refuse a `gitRoot` that is an ancestor of the requested `dir` (i.e. treat an upward-resolved
toplevel as "no own git repo" → `skillPackPullStatusSkippedNoGit`).

---

## 3. UNIX SOCKET PATH

- **Test:** `TestStartSessionCtlServer_DefaultStatus_ModeMatches`
  (`/home/eric/repos/r1-agent/cmd/r1/ctl_bootstrap_test.go:59`). Sibling tests in the same file share
  the flaw: `TestStartSessionCtlServer_Listens` (:26) and `TestStartSessionCtlServer_CustomDir`
  (:103).
- **Where the path is built:**
  - Test sets `t.Setenv("STOKE_CTL_DIR", dir)` with `dir := t.TempDir()`
    (ctl_bootstrap_test.go:60-61).
  - Production `startSessionCtlServer` (`/home/eric/repos/r1-agent/cmd/r1/ctl_bootstrap.go:44`) reads
    `ctlDir := r1env.Get("R1_CTL_DIR", "STOKE_CTL_DIR")` (:71), defaulting to `/tmp` (:73), and
    passes it as `SocketDir` to `sessionctl.StartServer` (:76-80).
  - `sessionctl.StartServer` (`/home/eric/repos/r1-agent/internal/sessionctl/server.go:24`) builds
    the path via `socketPath(opts.SocketDir, opts.SessionID)` (:31), where
    `socketPath` = `filepath.Join(dir, "stoke-"+sessionID+".sock")` (:52-53), then calls
    `net.Listen("unix", path)` (:35).
  - `sessionID` = `newSessionID(mode)` = `"<mode>-<12 hex>"` (ctl_bootstrap.go:45, :97-99). The
    socket basename `stoke-<mode>-<12hex>.sock` is ~26-30 chars.
- **Why it fails:** the unix `sockaddr_un.sun_path` field is capped at **108 bytes on Linux**
  (104 on macOS/BSD); `net.Listen("unix", path)` returns `bind: invalid argument` when
  `len(path) >= 108`. A representative `t.TempDir()` socket path (test name is 46 chars; current
  `TMPDIR` is `/tmp/claude-1000`) measures **112 bytes** —
  `/tmp/go-build…/TestStartSessionCtlServer_DefaultStatus_ModeMatches…/001/stoke-chat-0123456789ab.sock`
  > 108. The long test-function name baked into `t.TempDir()` is what pushes it over the limit; the
  failure is intermittent because exact temp path length varies with `TMPDIR`, build-dir hash, and
  the random hex in the session ID.
- **Fix:** do not route the ctl socket through `t.TempDir()` for these tests. Use a SHORT directory
  under `/tmp` (e.g. `dir, _ := os.MkdirTemp("/tmp", "rctl")` + `t.Cleanup(os.RemoveAll)`), or assert
  `len(socketPath) < 104` and `t.Skip` otherwise. Best: a shared helper `shortCtlDir(t)` that
  guarantees `len(dir)+len("/stoke-<mode>-<12hex>.sock") < 104`. The 104-not-108 margin covers
  macOS/BSD CI. Cite: `sockaddr_un.sun_path[108]` (Linux `<sys/un.h>`), 104 on Darwin.

---

## 4. SCOPE OF BLAST — every `cmd/r1` test that spawns / runs git / binds sockets

### (a) Spawns processes (subprocess or detached child)

| Test | File:line | Spawns | Reaped? |
|------|-----------|--------|---------|
| `TestRealSpawnDaemon_LaunchesDetachedProcess` | daemon_http_test.go:165 | `<test-bin> serve` (Setsid, via `realSpawnDaemon`) | **NO — LEAK** |
| `TestMCPServeRuntime_ThreeMessageExchange` | mcp_serve_runtime_test.go:16 | `r1Bin mcp serve` | yes (`cmd.Output`) |
| `TestMCPServeRuntime_AuthGate` | mcp_serve_runtime_test.go:95 | `r1Bin mcp serve` | yes |
| `TestMCPServeRuntime_DeterministicLobes` | mcp_serve_runtime_test.go:158 | `r1Bin mcp serve` | yes |
| `TestMCPServeRuntime_NoCortex` | mcp_serve_runtime_test.go:201 | `r1Bin mcp serve` | yes |
| (`buildR1ForTest`) | mcp_serve_runtime_test.go:258 | `go build` (`build.Dir = os.Getwd()`) | yes (waited) |
| `TestSingleInstance` | serve_single_instance_test.go:56 | re-exec `<test-bin> -test.run=TestHelperProcess_SecondInstance` | yes (`cmd.Run`) |
| `TestKillChildProcessGroup_KillsSilentChild` | pipe_watchdog_test.go:63 | `bash -c "…; sleep 60"` | yes (killed + waited) |
| `TestMultiSession_RaceFree` | serve_integration_test.go:48 | `bash -c 'echo $PWD'` ×8 (`cmd.Dir=SessionRoot`) | yes (`CombinedOutput`) |
| desktop-rpc runner | desktop_rpc_cmd_test.go:37 (`runOneRequest`) | in-process, single request | n/a |

### (b) Run git (all set `cmd.Dir`/`-C` to a temp dir — see §2)

- simple_loop_state_test.go (helpers :14, :45, :353, :475)
- task_cmd_test.go (helpers :202, :233)
- antitrunc_cmd_test.go (helper :54)
- descent_bridge_bootstrap_test.go (helper :14)
- skills_pack_cmd_test.go (helper `runGit` :1018; tests :845, :879 drive production
  `git pull --ff-only` via `updateSkillPack` — see §2 upward-walk risk)

### (c) Bind sockets

- ctl_bootstrap_test.go: `TestStartSessionCtlServer_Listens` (:26),
  `TestStartSessionCtlServer_DefaultStatus_ModeMatches` (:59),
  `TestStartSessionCtlServer_CustomDir` (:103) — all bind via `startSessionCtlServer` →
  `net.Listen("unix", …)`. All three share the `sun_path` length risk (§3).
- ctl_daemon_cmd_test.go:356 (`TestCtl_UnixSocketPreferred`) binds a REAL `http.Server` on a unix
  socket (`srv := &http.Server{Handler: mux}` :384) — verify its socket path is also short / under a
  temp dir (same `sun_path` exposure class; not flagged live but in-scope to audit).
- httptest-based (TCP loopback, no `sun_path` risk, `Close`d): serve_smoke_test.go:28,
  serve_cmd_test.go:243, verify_cmd_test.go:28, skills_pack_server_test.go (:36/:94/:140),
  skills_pack_server_v2_test.go (:26/:166), ctl_daemon_cmd_test.go:54, daemon_http_test.go:99.

### Shared helpers that should be fixed once (see §5)

- `realSpawnDaemon` (production, daemon_http.go:108) — the leak's actual source; the *test* must not
  drive it unfiltered.
- `startSessionCtlServer` / `sessionctl.StartServer` socket-path construction (§3) — fix the *test*
  socket dir; optionally add a guard.
- skill-pack `gitTopLevel`/`refreshSkillPackSource` upward-walk (§2) — fix tests' `HOME` sandboxing;
  optionally harden production.

---

## 5. TEST HELPER PATTERN — what exists, what to add

### Already exist (reuse these patterns)

- **Isolated temp git repo:** multiple ad-hoc helpers already do this correctly but are duplicated:
  - `initGitRepoWithCommit` (simple_loop_state_test.go:14) + `appendCommit` (:45)
  - `initRepo` (antitrunc_cmd_test.go:54)
  - `initTempGitRepo` (descent_bridge_bootstrap_test.go:14)
  - `gitCmd`/`commitChange` (task_cmd_test.go:202/:233)
  - `runGit(t, dir, …)` (skills_pack_cmd_test.go:1018)
  These are functionally identical (init in `t.TempDir()`, set `cmd.Dir`, fixed author/committer
  env). They should be **consolidated into one package-level `newTempGitRepo(t) (dir, headSHA)`** so
  every git test is provably isolated and future tests can't reintroduce a cwd-relative call.
- **Reaped subprocess re-exec:** `runHelperProcess` + `TestHelperProcess_SecondInstance`
  (serve_single_instance_test.go:112/:157) is the canonical waited+filtered pattern. `cmd.Output()`
  with bounded stdin in mcp_serve_runtime_test.go is the canonical reaped-stdio pattern.

### Missing — the spec should ADD

- **`startTestServer(t)` / reaped-daemon helper:** there is **no** helper that spawns the serve
  daemon and registers a `t.Cleanup` kill. The leaking test bypasses any such guard by calling the
  raw production `realSpawnDaemon()`. The spec should make `TestRealSpawnDaemon_*` NOT call
  `realSpawnDaemon()` directly; instead introduce a seam so the spawned argv carries
  `-test.run=^$ -test.timeout=...` (run zero tests) and is captured + killed via `t.Cleanup`, OR
  inject the executable/argv into `realSpawnDaemon` so the test can point it at `/bin/true` and wait
  on it. See §6 for the verification gate.
- **`shortCtlDir(t) string`:** a helper returning a `/tmp`-rooted short dir (with cleanup) that
  guarantees the resulting `stoke-<mode>-<12hex>.sock` path stays under 104 bytes, used by all three
  ctl_bootstrap tests.
- **`sandboxHome(t)`:** a helper that `t.Setenv("HOME", t.TempDir())` (and `R1_HOME`, `XDG_*` as
  needed) for every skill-pack test, so path resolution can never walk into the real checkout.

---

## 6. VERIFY APPROACH

How to prove the fix, with fresh evidence (no "should"):

1. **Zero leaked daemons.** Before/after process count must be identical:
   - `BEFORE=$(pgrep -fc 'r1\.test serve' || echo 0)` then `go test -count=3 ./cmd/r1` then
     `sleep 2; AFTER=$(pgrep -fc 'r1\.test serve' || echo 0)`; assert `AFTER == BEFORE == 0`.
   - Stronger: run the suite in its own process group and assert no descendant processes survive:
     `setsid go test ./cmd/r1`; after exit, `pgrep -f 'r1\.test' | wc -l` must be 0.
   - Targeted: `go test -run TestRealSpawnDaemon_LaunchesDetachedProcess -count=5 ./cmd/r1` must
     leave `pgrep -fc 'r1\.test serve' == 0` (today this multiplies processes).
2. **Never touches the real `.git`.** Snapshot the real index/HEAD and assert no mutation + no lock:
   - `git -C /home/eric/repos/r1-agent rev-parse HEAD` and `git status --porcelain` before/after the
     test run must be identical, and `test ! -e /home/eric/repos/r1-agent/.git/index.lock` must hold
     during and after.
   - Add a CI guard: run `go test ./cmd/r1` from a cwd that is a **scratch copy / outside the real
     repo** so any cwd-relative git op provably cannot reach the operator tree; OR wrap the suite to
     fail if `.git/index.lock` appears under the real root while tests run.
3. **Socket path under limit.** `go test -run TestStartSessionCtlServer -count=20 ./cmd/r1` must pass
   with zero `bind: invalid argument`. Assert in-test that `len(srv.SocketPath()) < 104`.
4. **Suite green + race-clean.** `go test -race ./cmd/r1` and `go vet ./...` (the CI gate per
   `cmd/r1/CLAUDE.md`) must pass after the fix.

---

## Spec recommendations (concrete fix checklist items)

**A. Kill the leaked-daemon recursion (highest priority).**

- [ ] In `TestRealSpawnDaemon_LaunchesDetachedProcess`
      (`/home/eric/repos/r1-agent/cmd/r1/daemon_http_test.go:165-185`), stop calling the raw
      `realSpawnDaemon()`. Either (i) refactor `realSpawnDaemon` (daemon_http.go:108) to accept an
      injectable executable + argv (e.g. `realSpawnDaemonWith(exe string, args []string)`), and in
      the test point it at `/bin/true` (or `<test-bin> -test.run=^DOES_NOT_EXIST$ -test.timeout=2s`)
      and `t.Cleanup` a `Process.Kill()`; or (ii) capture the spawned `*os.Process` and register
      `t.Cleanup(func(){ _ = proc.Kill(); _, _ = proc.Wait() })`.
- [ ] Pass an explicit `-test.run=^$` (run zero tests) filter to any re-exec of the test binary so a
      spawned child can never re-run the suite (mirror serve_single_instance_test.go:121).
- [ ] Add a package `TestMain` (`func TestMain(m *testing.M)`) under `cmd/r1` that, if `os.Args`
      contains a stray `serve`/`mcp` daemon argv under the test binary, exits 0 immediately —
      defense in depth so a future raw `realSpawnDaemon` call cannot fan out the suite.
- [ ] Production hardening (optional but recommended): in `realSpawnDaemon`
      (daemon_http.go:108-136) retain `cmd.Process` and expose it, or guard with a build-tag/env so
      the test path is non-detaching; document that `os.Executable()` under test is the test binary.

**B. Guarantee git isolation.**

- [ ] Consolidate the five duplicate temp-git helpers (simple_loop_state_test.go:14/:45,
      antitrunc_cmd_test.go:54, descent_bridge_bootstrap_test.go:14, task_cmd_test.go:202/:233,
      skills_pack_cmd_test.go:1018) into one `newTempGitRepo(t) (dir, head string)` that always sets
      `cmd.Dir` and fixed author/committer env.
- [ ] In `TestUpdateSkillPackSkipsRepoLocalGitPullAndRelinksDependencies`
      (`skills_pack_cmd_test.go:845`) add `t.Setenv("HOME", t.TempDir())` (match the sibling at :882)
      and assert no `git pull` targeted a path outside the temp repo (e.g. assert
      `result.PulledGitDirs` is empty AND none equals/contains `/home/eric/repos/r1-agent`).
- [ ] Harden production `gitTopLevel`/`refreshSkillPackSource`
      (`skills_pack_cmd.go:1443`, `:1405`): if `git rev-parse --show-toplevel` returns a directory
      that is an **ancestor** of the requested `dir` (i.e. `dir` has no own `.git`), treat it as
      `skillPackPullStatusSkippedNoGit` rather than pulling the enclosing repo.

**C. Fix the unix-socket length flake.**

- [ ] Add `shortCtlDir(t) string` (`/tmp`-rooted, `os.MkdirTemp("/tmp","rctl")` +
      `t.Cleanup(os.RemoveAll)`) and use it for `STOKE_CTL_DIR` in all three ctl_bootstrap tests
      (ctl_bootstrap_test.go:28, :61, :105) instead of `t.TempDir()`.
- [ ] In `sessionctl.StartServer` (`internal/sessionctl/server.go:31-35`) return a clear error when
      `len(path) >= 104` (so callers/tests fail fast with an actionable message instead of the raw
      `bind: invalid argument`); add an in-test assertion `len(srv.SocketPath()) < 104`.
- [ ] Audit `TestCtl_UnixSocketPreferred` (`ctl_daemon_cmd_test.go:356`, listener at :384) for the
      same `sun_path` exposure and apply `shortCtlDir` if its socket is under `t.TempDir()`.

**D. Verification gates (wire into the fix's evidence).**

- [ ] Add a post-suite assertion (script or `TestMain` teardown) that `pgrep -fc 'r1\.test serve'`
      is 0 and `/home/eric/repos/r1-agent/.git/index.lock` does not exist.
- [ ] Run `go test -race -count=3 ./cmd/r1`, `go test -run TestStartSessionCtlServer -count=20
      ./cmd/r1`, and `go vet ./...` as the completion evidence (CI gate per `cmd/r1/CLAUDE.md`).
