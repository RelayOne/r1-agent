<!-- STATUS: ready -->
<!-- CREATED: 2026-06-03 -->
<!-- DEPENDS_ON: (none) -->
<!-- BUILD_ORDER: 1 -->

# cmd/r1 Test Isolation — Implementation Spec

## Overview
`go test ./cmd/r1` is non-hermetic and unreliable. Three concrete defects, mapped in
`specs/research/raw/RT-cmd-r1-test-isolation.md`, were observed live this session:
(1) `TestRealSpawnDaemon_LaunchesDetachedProcess` drives the *production* `realSpawnDaemon()`, which
`exec`s the **test binary** with a `serve` arg under `Setsid`; with no `TestMain` to trap the stray
argv, the spawned binary re-runs the entire suite, which re-invokes the same test — a recursive
`Setsid`-detached fan-out that leaked ~120 orphaned `r1.test serve` processes, none reaped;
(2) those detached children inherit the test process cwd (the real repo root) and a non-sandboxed
`HOME`, so skill-pack `git pull --ff-only` (skills_pack_cmd.go:1433) and suite git ops race the
operator's real `.git/index.lock`; (3) `TestStartSessionCtlServer_*` bind a unix socket whose path is
built from `t.TempDir()` (a long path including the 46-char test name), pushing it past the
`sockaddr_un.sun_path` 108-byte Linux limit (112 bytes measured), producing intermittent
`bind: invalid argument`. This spec makes the package hermetic: no leaked daemons, no git against the
real repo, no socket-path overflow — directly unblocking reliable verification (this is why
`.git/index.lock` raced this session). BUILD_ORDER is 1 because reliable `cmd/r1` tests gate the
ability to verify every other spec. Audience: whoever runs `/build` against this spec.

## Stack & Versions
- Go (module `github.com/RelayOne/r1`); package `main` under `cmd/r1`, package `sessionctl` under
  `internal/sessionctl`.
- Standard library only: `testing` (`TestMain`, `t.Cleanup`, `t.Setenv`, `t.TempDir`),
  `os/exec`, `syscall` (`Setsid`, `Kill`, `SIGKILL`, process groups), `os` (`MkdirTemp`,
  `RemoveAll`, `Executable`), `net`.
- CI gate (per `cmd/r1/CLAUDE.md`): `go build ./cmd/r1`, `go test ./...`, `go vet ./...`.
- Unix socket limit: `sockaddr_un.sun_path[108]` on Linux (`<sys/un.h>`), `104` on Darwin/BSD. Target
  margin: `len(path) < 104` so the fix is cross-platform.

## Existing Patterns to Follow
- Canonical waited+filtered re-exec of the test binary:
  `cmd/r1/serve_single_instance_test.go` — `runHelperProcess` (:112) passes
  `-test.run=TestHelperProcess_SecondInstance` (:121) under a context timeout (:118), `cmd.Run()`
  (:131, waited); the helper `TestHelperProcess_SecondInstance` (:157) calls `os.Exit`. Mirror this.
- Canonical reaped-stdio spawn: `cmd/r1/mcp_serve_runtime_test.go` — `cmd.Output()` with bounded
  stdin; `buildR1ForTest` (:258) builds into `t.TempDir()`, `testing.Short()`-guarded at :260.
- Canonical kill-and-wait of a process group: `cmd/r1/pipe_watchdog_test.go` —
  `TestKillChildProcessGroup_KillsSilentChild` (:63) spawns with `Setpgid` (:72), then
  `killChildProcessGroup(cmd, …)` (:97) and waits on `done` (:81, :108).
- Existing duplicate temp-git helpers (to consolidate, see §B):
  `simple_loop_state_test.go` `initGitRepoWithCommit` (:14) + `appendCommit` (:45);
  `antitrunc_cmd_test.go` `initRepo` (:54); `descent_bridge_bootstrap_test.go` `initTempGitRepo`
  (:14); `task_cmd_test.go` — the inline `gitCmd` closure inside its enclosing helper (closure opens
  at :200; `cmd := exec.Command("git", …)` at :202) and the `commitChange` helper (func at :226, with
  its own inner `gitCmd` closure at :233); `skills_pack_cmd_test.go` `runGit(t, dir, …)` (:1018).
  NOTE: `task_cmd_test.go`'s `gitCmd` is a LOCAL closure (defined twice, once per enclosing helper),
  not a package-level function — migrate its callers to `newTempGitRepo`/`commitChange` rather than
  "delegating its body". All init in `t.TempDir()`, set `cmd.Dir`, fixed author/committer env.
- Sibling test that *correctly* sandboxes HOME:
  `TestUpdateSkillPackPullsExternalGitSourceAndInstallsNewDependency` (skills_pack_cmd_test.go:879)
  does `t.Setenv("HOME", home)` (:882) with a bare remote/clone under `t.TempDir()`.
- Existing accessor: `sessionctl.(*Server).SocketPath()` (internal/sessionctl/server.go:128) returns
  the bound socket path — use it for the length assertion; no new accessor needed.
- Production sites this spec touches (read before editing):
  - `realSpawnDaemon()` — `cmd/r1/daemon_http.go:108-136` (`os.Executable()` :109 → test binary;
    `exec.Command(exe, "serve")` :113; `applyDetachAttrs(cmd)` :117; `cmd.Start()` :130).
  - `applyDetachAttrs` — `cmd/r1/daemon_http_unix.go:16` sets `cmd.SysProcAttr.Setsid = true` (:20).
  - `refreshSkillPackSource` — `cmd/r1/skills_pack_cmd.go:1405`; `git -C gitRoot pull --ff-only`
    at :1433; `gitTopLevel` (rev-parse --show-toplevel) at :1443; `pathWithin` guards at :1406/:1419.
  - `startSessionCtlServer` — `cmd/r1/ctl_bootstrap.go:44` reads `R1_CTL_DIR`/`STOKE_CTL_DIR`,
    defaults `/tmp`, passes `SocketDir` to `sessionctl.StartServer`.
  - `sessionctl.StartServer` — `internal/sessionctl/server.go:24`; `socketPath` join at :52-53;
    `net.Listen("unix", path)` at :35.

## Library Preferences
- Test scaffolding: stdlib `testing` only. Do NOT add a test framework (no testify, no gomock).
- Process control: stdlib `os/exec` + `syscall`. Reuse the in-repo `killChildProcessGroup` helper
  (`cmd/r1/pipe_watchdog.go`, exercised by pipe_watchdog_test.go:97) where a group kill is needed.
- Git in tests: shell out to real `git` via `exec.Command` with explicit `cmd.Dir` and sandboxed
  `HOME`. Do NOT add a Go git library.

## Data Models
No persisted data models. New/used in-test artifacts:

### newTempGitRepo result
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| dir | string | absolute path under `t.TempDir()`; has its own `.git`; one initial commit | — |
| head | string | full 40-hex SHA of the initial commit; non-empty | — |

### shortCtlDir result
| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| dir | string | `/tmp`-rooted via `os.MkdirTemp("/tmp","rctl")`; `len(dir)+len("/stoke-<mode>-<12hex>.sock") < 104` | — |

### TestMain serve-argv trap (control flow, not a struct)
| Signal | Source | Action |
|--------|--------|--------|
| stray `serve` (or `mcp serve`, `agent serve`) in `os.Args[1:]` under the test binary | `realSpawnDaemon` re-exec | `os.Exit(0)` before `m.Run()` — never re-enter the suite |

## API Endpoints
None. This spec changes test scaffolding plus one optional production guard. No HTTP/RPC surface
changes except the existing ctl unix socket whose path length this spec bounds.

## Business Logic

### TestMain serve-argv trap (defense in depth against fan-out)
1. Detect: scan `os.Args[1:]` for a daemon subcommand token (`serve`; also `mcp serve`,
   `agent serve` forms) that would be present only on a re-exec of the test binary as a daemon.
2. Execute: if present, `os.Exit(0)` immediately — do NOT call `m.Run()`. This guarantees a spawned
   `<test-bin> serve` can never re-run the suite even if a future call path drives `realSpawnDaemon`.
3. Side effects: none in the trap branch. In the normal branch, call `m.Run()` and `os.Exit(code)`.
4. Return: process exit code (0 on trap; `m.Run()` result otherwise).

### TestRealSpawnDaemon reaping
1. Validate: assert the spawn syscalls succeed (open `/dev/null` + `Start` with detach attrs) —
   preserve the existing assertion that `realSpawnDaemon` returns nil and `os.DevNull != ""`.
2. Execute: spawn via a seam (see §A) that (a) passes `-test.run=^$ -test.timeout=2s` so a re-exec
   runs **zero** tests, and (b) returns the spawned `*os.Process` (or `*exec.Cmd`).
3. Side effects: register `t.Cleanup` that kills the child **process group** (the child is
   `Setsid`-detached: `syscall.Kill(-pid, SIGKILL)` then `proc.Wait()`), so nothing survives the test.
4. Return: test pass with zero residual `r1.test serve` processes.

### Skill-pack git isolation
1. Validate: every test that reaches `refreshSkillPackSource`/`updateSkillPack` first sandboxes
   `HOME` (and any env the resolver reads) to a `t.TempDir()`.
2. Execute: drive `updateSkillPack(tempRepo, …)` with the repo and all pack/source paths inside
   `t.TempDir()`.
3. Side effects: assert no `git pull` targeted a path outside the temp repo — specifically that no
   pulled git dir equals or contains `/home/eric/repos/r1-agent`.
4. Return: pull is skipped (repo-local / no-git / no-upstream) for repo-local packs; real repo never
   touched.

## Error Handling
| Failure | Strategy | User Sees |
|---------|----------|-----------|
| Spawned `<test-bin> serve` would re-run suite | `TestMain` traps the argv and `os.Exit(0)` before `m.Run()` | No fan-out; suite runs once |
| Detached child survives test | `t.Cleanup` kills the process group (`Kill(-pid, SIGKILL)` + `Wait`) | `pgrep r1.test serve` == 0 after suite |
| Skill-pack resolver walks up into real repo | Sandbox `HOME` to `t.TempDir()`; assert resolved git root ⊂ temp repo; (optional) production `gitTopLevel` treats an ancestor toplevel as no-git | Real `.git/index.lock` never created |
| Socket path ≥ 104 bytes | `shortCtlDir(t)` (`/tmp`-rooted); in-test assert `len(srv.SocketPath()) < 104`; (optional) `StartServer` returns a clear error when `len(path) >= 104` | `bind: invalid argument` gone; actionable error if regressed |

## Boundaries — What NOT To Do
- Do NOT weaken or delete any assertion the affected tests currently make. Preserve every existing
  check: `TestRealSpawnDaemon_*` must still assert `realSpawnDaemon` (via the seam) returns nil and
  `os.DevNull != ""`; the ctl tests must still assert socket creation, `resp.OK`,
  `snap.Mode == "chat"`, the raw `"mode":"chat"` substring, and the `/tmp` negative in
  `TestStartSessionCtlServer_CustomDir`.
- Do NOT "fix" the leak by deleting `TestRealSpawnDaemon_LaunchesDetachedProcess` or any process-
  spawning test. Isolate them — capture and reap.
- Do NOT change production `realSpawnDaemon` detach/`Setsid` behavior except the minimal, opt-in seam
  needed to capture+reap the child under test (e.g. an injectable exe/argv variant
  `realSpawnDaemonWith(exe string, args []string) (*os.Process, error)` that the production
  `realSpawnDaemon` delegates to with `os.Executable()` + `["serve"]`). Production must keep spawning
  a detached `<exe> serve` with `/dev/null` stdio.
- Do NOT add `t.Parallel()` to any test that uses `t.Setenv` (the ctl tests forbid it — see
  ctl_bootstrap_test.go:11). Conversely, you MUST REMOVE the EXISTING `t.Parallel()` at
  skills_pack_cmd_test.go:846 before adding `sandboxHome(t)` to that test (item 6) — `t.Setenv` after
  `t.Parallel()` is a runtime panic, not a compile error, so a missed removal slips past `go build`.
- Do NOT route the ctl socket through `t.TempDir()` for the bind tests — that long path is the bug.
- Do NOT skip a test as a substitute for fixing it; `t.Skip` is allowed ONLY as the documented
  fallback in §C when `len(path) >= 104` would otherwise be unavoidable on an exotic `TMPDIR`.
- Do NOT remove the `testing.Short()` guard on `buildR1ForTest` (mcp_serve_runtime_test.go:260).

## Testing
### A — Daemon-leak elimination (`cmd/r1/daemon_http_test.go`, new `cmd/r1/main_test.go`)
- [ ] Happy: `go test -run TestRealSpawnDaemon_LaunchesDetachedProcess -count=5 ./cmd/r1` → pass, and
      after `sleep 2`, `pgrep -fc 'r1\.test serve'` → `0`.
- [ ] Edge: a spawned `<test-bin> serve` invoked manually exits 0 immediately and runs zero tests
      (`TestMain` trap), e.g. `go test -c -o /tmp/r1.test ./cmd/r1 && /tmp/r1.test serve; echo $?` → `0`
      with no suite output.
- [ ] Error: if the seam fails to `Start`, the test reports the error (preserve the `t.Fatalf`).

### B — Git isolation (`cmd/r1` testhelpers + `skills_pack_cmd_test.go`)
- [ ] Happy: `newTempGitRepo(t)` returns `(dir, head)` with `dir` containing `.git`, `head` a
      40-hex SHA; `git -C dir rev-parse HEAD` equals `head`.
- [ ] Happy: `TestUpdateSkillPackSkipsRepoLocalGitPullAndRelinksDependencies` (skills_pack_cmd_test.go:845)
      with `t.Setenv("HOME", t.TempDir())` → pull skipped, status repo-local/no-git.
- [ ] Edge: assert no pulled git dir equals or `strings.Contains` `/home/eric/repos/r1-agent`.
- [ ] Verify untouched: `git -C /home/eric/repos/r1-agent rev-parse HEAD` and
      `git -C /home/eric/repos/r1-agent status --porcelain` identical before/after the suite; and
      `test ! -e /home/eric/repos/r1-agent/.git/index.lock` holds throughout.

### C — Socket path under limit (`cmd/r1/ctl_bootstrap_test.go`, `ctl_daemon_cmd_test.go`)
- [ ] Happy: `go test -run TestStartSessionCtlServer -count=20 ./cmd/r1` → pass, zero
      `bind: invalid argument`.
- [ ] Edge: in each of the three ctl_bootstrap tests assert `len(srv.SocketPath()) < 104`.
- [ ] Error (optional production guard): `StartServer` with a `SocketDir` forcing `len(path) >= 104`
      returns a non-nil error whose message names the limit (not raw `bind: invalid argument`).

### D — Shared helpers (`cmd/r1` testhelpers)
- [ ] `newTempGitRepo(t) (dir, head string)` — used by every git-running cmd/r1 test.
- [ ] `shortCtlDir(t) string` — used by all ctl socket-binding tests.
- [ ] `sandboxHome(t)` — `t.Setenv("HOME", t.TempDir())` (+ `R1_HOME`, `XDG_*` as the resolver reads).
- [ ] spawn+reap daemon helper (or the `realSpawnDaemonWith` seam) — used by `TestRealSpawnDaemon_*`.

## Acceptance Criteria
- WHEN `go test -count=3 ./cmd/r1` (and `setsid go test ./cmd/r1`) completes THE SYSTEM SHALL leave
  `pgrep -fc 'r1\.test serve'` == `0` and `pgrep -fc 'r1\.test'` == `0` after a 2s settle.
- WHEN `go test -run TestRealSpawnDaemon_LaunchesDetachedProcess -count=5 ./cmd/r1` runs THE SYSTEM
  SHALL leave zero leaked `r1.test serve` processes (today it multiplies them).
- WHEN the full suite runs THE SYSTEM SHALL leave `/home/eric/repos/r1-agent` HEAD and
  `git status --porcelain` byte-identical to before, and `/home/eric/repos/r1-agent/.git/index.lock`
  SHALL never exist during or after the run.
- WHEN `go test -run TestStartSessionCtlServer -count=20 ./cmd/r1` runs THE SYSTEM SHALL pass all 20
  iterations with zero `bind: invalid argument`, and each bound `srv.SocketPath()` SHALL be < 104 bytes.
- WHEN `go test -race -count=3 ./cmd/r1` and `go vet ./...` run THE SYSTEM SHALL pass (CI gate).

## Implementation Checklist
<!-- Each item self-contained: exact files, functions/tests, and a VERIFY command. -->

### Group A — Kill the leaked-daemon recursion (highest priority)

1. [ ] **Add a `TestMain` serve-argv trap.** Create `cmd/r1/main_test.go` (package `main`) with
   `func TestMain(m *testing.M)` that scans `os.Args[1:]` for a daemon subcommand token (`serve`, and
   the `mcp serve` / `agent serve` forms) and, if present, `os.Exit(0)` BEFORE calling `m.Run()`;
   otherwise `os.Exit(m.Run())`. This is defense-in-depth: a spawned `<test-bin> serve` exits
   immediately and never re-runs the suite (root cause of the ~120-orphan fan-out per
   RT §1 "recursive test-binary fan-out", daemon_http_test.go:165 → daemon_http.go:113). Ensure no
   conflict with the existing test functions `TestMain_DaemonAlias_WritesHintAndForwards` and
   `TestMain_AgentServeAlias_WritesHintAndForwards` (serve_aliases_test.go:23/:54) — those are test
   funcs, not the entry point. VERIFY:
   `go test -c -o /tmp/r1.test ./cmd/r1 && /tmp/r1.test serve; echo exit=$?` → `exit=0` with no
   test output; `grep -c 'func TestMain(m \*testing.M)' cmd/r1/main_test.go` → `1`.

2. [ ] **Add an injectable spawn seam in production `realSpawnDaemon`.** In
   `cmd/r1/daemon_http.go:108-136` extract the body into
   `realSpawnDaemonWith(exe string, args []string) (*os.Process, error)` that keeps the existing
   `/dev/null` stdio + `applyDetachAttrs(cmd)` (Setsid) behavior and returns `cmd.Process`; have the
   existing `realSpawnDaemon()` call `realSpawnDaemonWith(os.Executable()-result, []string{"serve"})`
   and return only the error (production behavior unchanged). Do NOT change detach/Setsid semantics.
   VERIFY: `go build ./cmd/r1` succeeds; `grep -n 'realSpawnDaemonWith' cmd/r1/daemon_http.go` shows
   the new func and the delegating call.

3. [ ] **Reap the child in `TestRealSpawnDaemon_LaunchesDetachedProcess`** (daemon_http_test.go:165).
   Replace the raw `realSpawnDaemon()` call (:177) with the seam from item 2, passing
   `args = []string{"-test.run=^$", "-test.timeout=2s"}` so a re-exec runs zero tests, and register
   `t.Cleanup(func(){ _ = syscall.Kill(-proc.Pid, syscall.SIGKILL); _, _ = proc.Wait() })` — note
   the negative PID kills the whole `Setsid` group (RT §1 documents Setsid detach + no reap).
   Preserve the existing assertions: spawn returns nil error and `os.DevNull != ""` (daemon_http_test.go:177-184).
   Keep the Windows skip (:166-168). Update the now-inaccurate comment block (:160-176) that claims
   `/bin/true` is the child — the child is the test binary. VERIFY:
   `go test -run TestRealSpawnDaemon_LaunchesDetachedProcess -count=5 ./cmd/r1 && sleep 2 && echo leaked=$(pgrep -fc 'r1\.test serve' || echo 0)` → `leaked=0`.

### Group B — Guarantee git isolation

4. [ ] **Consolidate temp-git helpers into `newTempGitRepo(t) (dir, head string)`** in a new
   `cmd/r1/testhelpers_test.go` (package `main`). It must: `dir := t.TempDir()`; `git -C dir init`;
   set fixed author/committer env (`GIT_AUTHOR_NAME/EMAIL`, `GIT_COMMITTER_NAME/EMAIL`,
   `GIT_AUTHOR_DATE/GIT_COMMITTER_DATE`) and `cmd.Dir = dir` on every git invocation; make one initial
   commit; return `(dir, full-HEAD-SHA)`. Replace the five duplicate helpers' bodies to delegate to it
   (or migrate callers): `simple_loop_state_test.go` `initGitRepoWithCommit`(:14)/`appendCommit`(:45),
   `antitrunc_cmd_test.go` `initRepo`(:54), `descent_bridge_bootstrap_test.go` `initTempGitRepo`(:14),
   `task_cmd_test.go` inline `gitCmd` closure (opens :200; `exec.Command("git",…)` :202) /
   `commitChange`(func :226, inner `gitCmd` closure :233), `skills_pack_cmd_test.go` `runGit`(:1018).
   NOTE: `task_cmd_test.go`'s `gitCmd` is a per-helper LOCAL closure (not a shared func) — migrate its
   callers to `newTempGitRepo`; do NOT try to point a package-level `gitCmd` at the new helper.
   These already set `cmd.Dir` to temp dirs (RT §2 confirms isolation) — consolidation prevents a
   future cwd-relative regression. VERIFY:
   `go test ./cmd/r1 -run 'TestValidateResumeCompat|TestUpdateSkillPack' -count=1` passes; and
   `( H=$(git -C /home/eric/repos/r1-agent rev-parse HEAD); go test ./cmd/r1 >/dev/null 2>&1; [ "$H" = "$(git -C /home/eric/repos/r1-agent rev-parse HEAD)" ] && echo HEAD-UNCHANGED )` → `HEAD-UNCHANGED`.

5. [ ] **Add `sandboxHome(t)`** in `cmd/r1/testhelpers_test.go`: `t.Setenv("HOME", t.TempDir())` plus
   any env the skill-pack resolver reads (`R1_HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME` → temp dirs).
   VERIFY: `grep -n 'func sandboxHome' cmd/r1/testhelpers_test.go` → present.

6. [ ] **Sandbox HOME in the repo-local skill-pack test.** In
   `TestUpdateSkillPackSkipsRepoLocalGitPullAndRelinksDependencies` (skills_pack_cmd_test.go:845):
   FIRST remove the `t.Parallel()` call at line 846 — this is MANDATORY, not optional. `sandboxHome(t)`
   calls `t.Setenv`, and Go's `t.Setenv` PANICS at runtime ("testing: t.Setenv called after
   t.Parallel; cannot set environment variables in parallel tests") in any test that has called
   `t.Parallel()`. (The sibling external-pull test at :879 is NOT parallel, which is exactly why its
   `t.Setenv("HOME", home)` at :882 is legal.) THEN call `sandboxHome(t)` (matching that sibling's
   `t.Setenv("HOME", home)` at :882) and assert no pulled git dir equals or `strings.Contains`
   `/home/eric/repos/r1-agent`. The test already asserts `len(result.PulledGitDirs) != 0` → fail
   (:859-860) and `PullStatus == skillPackPullStatusSkippedRepoLocal` (:863-864) — KEEP both; the new
   real-repo-path assertion strengthens them (RT §2 upward-walk via `gitTopLevel`
   rev-parse --show-toplevel at skills_pack_cmd.go:1443 → `git pull --ff-only` at :1433).
   VERIFY: `go test -run TestUpdateSkillPackSkipsRepoLocalGitPullAndRelinksDependencies -count=3 ./cmd/r1`
   passes (no panic) and `test ! -e /home/eric/repos/r1-agent/.git/index.lock`.

7. [ ] **(Optional, recommended) Harden production `gitTopLevel`/`refreshSkillPackSource`.** In
   `cmd/r1/skills_pack_cmd.go`: if `git rev-parse --show-toplevel` (gitTopLevel :1443) returns a dir
   that is an ANCESTOR of the requested `dir` (i.e. `dir` has no own `.git`), have
   `refreshSkillPackSource` (:1405) treat it as `skillPackPullStatusSkippedNoGit` rather than pulling
   the enclosing repo — so a pack dir without its own `.git` can never drive `git -C <realRoot> pull`
   (:1433). VERIFY: `go test ./cmd/r1 -run TestUpdateSkillPack -count=1` passes; `go vet ./...` clean.

### Group C — Fix the unix-socket length flake

8. [ ] **Add `shortCtlDir(t) string`** in `cmd/r1/testhelpers_test.go`:
   `dir, err := os.MkdirTemp("/tmp", "rctl")`, fail on err, `t.Cleanup(func(){ os.RemoveAll(dir) })`,
   return `dir`. It must guarantee `len(dir)+len("/stoke-<mode>-<12hex>.sock") < 104` (basename
   `stoke-<mode>-<12hex>.sock` ≈ 26-30 chars; an `/tmp/rcltXXXXXXXXX` root keeps the total well under
   104). RT §3 root-causes the flake to `t.TempDir()` baking the 46-char test name into the path
   (measured 112 > 108). VERIFY: `grep -n 'func shortCtlDir' cmd/r1/testhelpers_test.go` → present.

9. [ ] **Use `shortCtlDir(t)` + assert length in all three ctl_bootstrap tests.** In
   `cmd/r1/ctl_bootstrap_test.go` replace `dir := t.TempDir()` with `dir := shortCtlDir(t)` in
   `TestStartSessionCtlServer_Listens` (:27), `TestStartSessionCtlServer_DefaultStatus_ModeMatches`
   (:60), and `TestStartSessionCtlServer_CustomDir` (:104), keeping the `t.Setenv("STOKE_CTL_DIR", dir)`
   line. After each `srv` is bound, add `if len(srv.SocketPath()) >= 104 { t.Fatalf("socket path %d>=104: %s", len(srv.SocketPath()), srv.SocketPath()) }`
   (accessor `sessionctl.(*Server).SocketPath()` exists at internal/sessionctl/server.go:128).
   Preserve every existing assertion (socket creation, `resp.OK`, `snap.Mode=="chat"`, the raw
   `"mode":"chat"` substring, and the `/tmp` negative at :117-120). VERIFY:
   `go test -run TestStartSessionCtlServer -count=20 ./cmd/r1 2>&1 | grep -c 'bind: invalid argument'`
   → `0`; suite passes.

10. [ ] **Audit BOTH unix-socket-binding ctl_daemon tests for the same exposure.** Two tests in
    `cmd/r1/ctl_daemon_cmd_test.go` bind a unix socket whose path is built from `t.TempDir()` and so
    share the `sun_path` length exposure (RT §4(c) class):
    (a) `TestCtl_UnixSocketPreferred` (func at :306): `dir := t.TempDir()` (:310),
        `sockPath := filepath.Join(dir, "test.sock")` (:311), `net.Listen("unix", sockPath)` (:312).
    (b) `TestCtl_UnixSocketEndToEnd` (func at :361): `dir := t.TempDir()` (:362),
        `sockPath := filepath.Join(dir, "r1.sock")` (:363), `net.Listen("unix", sockPath)` (:379),
        real `http.Server{Handler: mux}` (:384).
    For EACH, replace `t.TempDir()` with `shortCtlDir(t)` and add
    `if len(sockPath) >= 104 { t.Fatalf("socket path %d>=104: %s", len(sockPath), sockPath) }`
    before the `net.Listen`. Preserve every existing assertion in both tests.
    (NOTE: the prior spec draft cited a single test at :356 with the listener at :384 — that conflated
    these two distinct tests; the real func headers are :306 and :361, and :384 belongs to
    `TestCtl_UnixSocketEndToEnd`, not `TestCtl_UnixSocketPreferred`.)
    VERIFY: `go test -run 'TestCtl_UnixSocketPreferred|TestCtl_UnixSocketEndToEnd' -count=20 ./cmd/r1`
    passes with zero `bind: invalid argument`.

11. [ ] **(Optional) Fail-fast guard in `sessionctl.StartServer`.** In
    `internal/sessionctl/server.go:31-35`, after building `path := socketPath(...)` and before
    `net.Listen("unix", path)` (:35), if `len(path) >= 104` return a clear error naming the limit
    (e.g. `fmt.Errorf("sessionctl: socket path %d bytes exceeds 104-byte sun_path limit: %s", len(path), path)`)
    instead of surfacing the raw `bind: invalid argument`. VERIFY: `go test ./internal/sessionctl/...`
    passes; `go build ./...` clean.

### Group D — Verification gates (wire into completion evidence)

12. [ ] **No leaked processes.** Run
    `BEFORE=$(pgrep -fc 'r1\.test serve' || echo 0); go test -count=3 ./cmd/r1; sleep 2; AFTER=$(pgrep -fc 'r1\.test serve' || echo 0); echo "BEFORE=$BEFORE AFTER=$AFTER"`
    and assert `BEFORE == 0` and `AFTER == 0`. Stronger:
    `setsid go test ./cmd/r1; sleep 2; pgrep -f 'r1\.test' | wc -l` → `0`. VERIFY: both checks print 0.

13. [ ] **Real repo untouched.** Run
    `H=$(git -C /home/eric/repos/r1-agent rev-parse HEAD); S=$(git -C /home/eric/repos/r1-agent status --porcelain); go test ./cmd/r1 >/dev/null 2>&1; test ! -e /home/eric/repos/r1-agent/.git/index.lock && [ "$H" = "$(git -C /home/eric/repos/r1-agent rev-parse HEAD)" ] && [ "$S" = "$(git -C /home/eric/repos/r1-agent status --porcelain)" ] && echo REPO-UNTOUCHED`.
    VERIFY: prints `REPO-UNTOUCHED`.

14. [ ] **Socket flake gone (10x+).** Run
    `go test -run TestStartSessionCtlServer -count=20 ./cmd/r1 2>&1 | grep -c 'bind: invalid argument'`
    → `0`. VERIFY: prints `0`; the run also passes overall (`go test -run TestStartSessionCtlServer -count=10 ./cmd/r1` exits 0).

15. [ ] **CI gate green.** Run `go build ./cmd/r1`, `go test -race -count=3 ./cmd/r1`, and
    `go vet ./...` (the gate per `cmd/r1/CLAUDE.md`). VERIFY: all three exit 0.
