# Spec Review — cmd/r1 Test Isolation

Spec under review: `/home/eric/repos/r1-agent/specs/cmd-r1-test-isolation.md`
Source map (ground truth): `/home/eric/repos/r1-agent/specs/research/raw/RT-cmd-r1-test-isolation.md`
Reviewer model: opus, effort: high. Date: 2026-06-03.

## Result: READY (after repair) — 10/10 PASS

All citations were re-verified against the working tree. Three critical issues were found and
fixed inline. Two were STALE/conflated citations (would have sent a builder to the wrong line),
and one was a latent **runtime panic** the original draft would have introduced. Scope was NOT
weakened — every fix preserves or strengthens what the affected tests assert.

---

## Critical finding 1 (FIXED) — `t.Setenv` after `t.Parallel()` runtime panic

**The blocker the spec missed.** Implementation item 6 instructs adding `sandboxHome(t)` (which calls
`t.Setenv`) to `TestUpdateSkillPackSkipsRepoLocalGitPullAndRelinksDependencies`
(`cmd/r1/skills_pack_cmd_test.go:845`). That test calls **`t.Parallel()` at line 846**
(verified: `awk` shows `846: \tt.Parallel()`). Go's `t.Setenv` PANICS at runtime if the test (or a
parent) has called `t.Parallel()`: *"testing: t.Setenv called after t.Parallel; cannot set
environment variables in parallel tests"*. This is a **runtime** panic, not a compile error — it
slips past `go build` and `go vet` and only blows up when the test runs, which is exactly the
"reliable verification" this spec is supposed to deliver. The sibling external-pull test at :879 is
NOT parallel, which is precisely why its `t.Setenv("HOME", home)` at :882 (the pattern item 6 tells
the builder to mirror) is legal.

- Fix applied: item 6 now makes removal of `t.Parallel()` at line 846 a MANDATORY first step (with
  the panic message quoted and the rationale), before `sandboxHome(t)` is added. The Boundaries
  section was also amended: it previously only said "do NOT ADD `t.Parallel()` to tests using
  `t.Setenv`"; it now also says you MUST REMOVE the existing `t.Parallel()` at
  skills_pack_cmd_test.go:846, and flags that the failure is a runtime panic invisible to `go build`.
- Does-not-weaken check: PASS. The two existing assertions (`len(result.PulledGitDirs) != 0` → fail
  at :859-860, `PullStatus == skillPackPullStatusSkippedRepoLocal` at :863-864) are explicitly kept;
  the new real-repo-path assertion strengthens them.

## Critical finding 2 (FIXED) — STALE/conflated ctl_daemon citation (item 10)

Original item 10 cited `TestCtl_UnixSocketPreferred` at `ctl_daemon_cmd_test.go:356` with the
listener / `http.Server` at `:384`. Verified against the tree, this conflates TWO distinct tests and
both line numbers are wrong:
- `TestCtl_UnixSocketPreferred` — func header at **:306**; `dir := t.TempDir()` (:310),
  `sockPath := filepath.Join(dir, "test.sock")` (:311), `net.Listen("unix", sockPath)` (:312). No
  `http.Server` here.
- `TestCtl_UnixSocketEndToEnd` — func header at **:361**; `dir := t.TempDir()` (:362),
  `sockPath := filepath.Join(dir, "r1.sock")` (:363), `net.Listen("unix", sockPath)` (:379),
  `http.Server{Handler: mux}` (:384). The `:384` the spec attributed to `_Preferred` actually belongs
  to `_EndToEnd`.

Both tests build their socket path from `t.TempDir()` and therefore BOTH carry the `sun_path` length
exposure (RT §4(c)). The original item 10 would have sent a builder to a nonexistent line and missed
the second exposed test entirely.

- Fix applied: item 10 rewritten to name BOTH tests with verified func headers (:306, :361) and exact
  socket-construction lines, instructs routing each through `shortCtlDir(t)` plus a
  `len(sockPath) >= 104` fatal guard, preserves existing assertions, and records the conflation as a
  correction note. VERIFY command broadened to run both tests `-count=20`.
- Does-not-weaken check: PASS — "preserve every existing assertion in both tests" is explicit.

## Critical finding 3 (FIXED) — STALE/mischaracterized task_cmd_test.go citation (item 4)

Spec (and the RT source map) cited `task_cmd_test.go` `gitCmd`(:202) / `commitChange`(:233) as if
both were package-level reusable helpers to "consolidate". Verified:
- There is **no package-level `gitCmd` function**. `gitCmd` is a **local closure** (`gitCmd := func(...)`)
  defined INSIDE its enclosing helper; the closure opens at **:200**, and `:202` is the
  `cmd := exec.Command("git", args...)` line within it. A second, separate `gitCmd` closure is
  re-declared inside `commitChange` at **:233**.
- `commitChange` is a real func, but its header is at **:226**, not `:233` (the `:233` the spec gave
  is the inner closure, not the func).

Telling a builder to "replace the helper's body to delegate" would fail — there is no shared symbol
to repoint; the closures must be removed and their callers migrated to `newTempGitRepo`.

- Fix applied: both the "Existing Patterns to Follow" header reference and Implementation item 4 now
  state `gitCmd` is a per-helper LOCAL closure (opens :200; `exec.Command` :202), give `commitChange`
  the correct func line (:226), and direct the builder to MIGRATE CALLERS rather than repoint a
  (nonexistent) package-level helper.
- Does-not-weaken check: PASS — consolidation target and isolation guarantee unchanged.

---

## Per-failure-mode verdict

### 1. Self-contained checklist items — PASS
Each of items 1–15 names its exact target file, function/test, and a VERIFY command. After the item-6
repair, the one cross-item runtime hazard (`t.Parallel()` removal) is stated in-item rather than left
to the builder to infer. The `shortCtlDir`/`sandboxHome`/`newTempGitRepo` helpers (items 4/5/8) are
defined before their consumers (items 6/9/10) — see ordering (mode 7).

### 2. Library ambiguity — PASS
Every item names stdlib packages explicitly (`testing`, `os/exec`, `syscall`, `os`, `net`). The spec
forbids testify/gomock/Go-git libraries and reuses the in-repo `killChildProcessGroup`. No "use the X
library" hand-waving.

### 3. Pattern references — PASS (all verified real)
Citations checked and resolved against the tree:
- `realSpawnDaemon` daemon_http.go:108 ✓ (`os.Executable()` :109, `exec.Command(exe,"serve")` :113,
  `applyDetachAttrs(cmd)` :117, `cmd.Start()` :130).
- `applyDetachAttrs` daemon_http_unix.go:16, `Setsid = true` :20 ✓.
- `TestRealSpawnDaemon_LaunchesDetachedProcess` daemon_http_test.go:165, raw `realSpawnDaemon()` call
  :177, Windows skip :166-168, stale `/bin/true` comment block :160-176 ✓ (comment IS inaccurate as
  the spec says — the child is the test binary, not `/bin/true`).
- `sessionctl.StartServer` server.go:24, `socketPath(...)` join :31, `socketPath` func :52-53,
  `net.Listen("unix", path)` :35, `SocketPath()` accessor :128-129 ✓.
- `startSessionCtlServer` ctl_bootstrap.go:44, `newSessionID` :45/:97, `r1env.Get(...)` :71, `/tmp`
  default :73, `StartServer` opts :76; `newSessionID` returns `<mode>-<12hex>` (6 random bytes) ✓.
- `refreshSkillPackSource` skills_pack_cmd.go:1405, `pathWithin` guards :1406/:1419,
  `git pull --ff-only` :1433, `gitTopLevel` :1443, status consts
  `skillPackPullStatusSkippedNoGit`/`...RepoLocal` :1151/:1153 ✓.
- Existing temp-git helpers: `initGitRepoWithCommit` simple_loop_state_test.go:14, `appendCommit`
  :45; `initRepo` antitrunc_cmd_test.go:54; `initTempGitRepo` descent_bridge_bootstrap_test.go:14;
  `runGit` skills_pack_cmd_test.go:1018 — all ✓ and all set `cmd.Dir` to a temp dir.
- Pattern exemplars: `runHelperProcess` serve_single_instance_test.go:112, `-test.run=...` :121,
  ctx timeout :118, `cmd.Run()` :131, `TestHelperProcess_SecondInstance` :157 ✓; `buildR1ForTest`
  mcp_serve_runtime_test.go:258 with `testing.Short()` guard :260 ✓; `TestKillChildProcessGroup_*`
  pipe_watchdog_test.go:63, `Setpgid` :72, `killChildProcessGroup` :97 ✓.
- Sibling sandbox test `TestUpdateSkillPackPullsExternalGitSourceAndInstallsNewDependency`
  skills_pack_cmd_test.go:879, `t.Setenv("HOME", home)` :882 ✓.
- `serve_aliases_test.go` `TestMain_DaemonAlias_*` :23 / `TestMain_AgentServeAlias_*` :54 ✓ (test
  funcs, NOT the entry point — see TestMain finding below).

STALE citations found and fixed: task_cmd_test.go `gitCmd`(:202→closure@:200)/`commitChange`(:233→func@:226)
[finding 3]; ctl_daemon_cmd_test.go `:356`/`:384` single-test → split into :306 + :361 [finding 2].

### 4. Missing error responses — PASS (N/A endpoints)
No HTTP/RPC surface added. The Error Handling table enumerates the four real failure modes
(fan-out re-run, surviving detached child, resolver upward-walk, socket ≥104 bytes) each with a
strategy and an observable user-visible signal.

### 5. Vague acceptance criteria — PASS
Criteria use concrete values: `pgrep -fc 'r1\.test serve' == 0`, `len(path) < 104`, `-count=20`,
`-count=3`, `-test.timeout=2s`, byte-identical HEAD + `git status --porcelain`, `.git/index.lock`
absent. No "appropriate"/"proper"/"as needed".

### 6. Missing boundaries — PASS (strengthened)
The "Boundaries — What NOT To Do" section is concrete (no assertion deletion, no test deletion, no
Setsid behavior change beyond the opt-in seam, no `t.Parallel()` with `t.Setenv`, no `t.TempDir()`
for the bind tests, `t.Skip` only as the documented §C fallback). Amended in this review to also
mandate REMOVING the existing `t.Parallel()` at skills_pack_cmd_test.go:846 (finding 1).

### 7. Dependency ordering — PASS
Helpers (item 4 `newTempGitRepo`, item 5 `sandboxHome`, item 8 `shortCtlDir`) precede their
consumers (item 6, item 9, item 10). Item 2 (production seam) precedes item 3 (test using the seam).
Group A → B → C → D ordering is acyclic; no item depends on a later one.

### 8. Verification specificity — PASS
Every item carries a runnable VERIFY command checking a specific observable (process count, grep
hit-count == 0, file presence, build/vet exit code). Group D items 12–15 are dedicated
fresh-evidence gates (no-leak, repo-untouched, socket-flake-gone-10x+, CI-green) — concrete and
re-runnable, satisfying the requested hermeticity proof.

### 9. Bundled items — PASS
Items are single-responsibility. Optional hardening (items 7, 11) are explicitly marked optional and
separated from the mandatory fixes, so a builder can ship the core isolation without coupling to the
production guards.

### 10. Does-not-weaken-test-assertions safety — PASS (explicitly stated)
The spec's first Boundary is "Do NOT weaken or delete any assertion" and enumerates the exact checks
to preserve per test (`realSpawnDaemon` returns nil + `os.DevNull != ""`; ctl `resp.OK`,
`snap.Mode == "chat"`, raw `"mode":"chat"` substring, `/tmp` negative; PulledGitDirs/PullStatus).
Each of the three repairs in this review was made specifically to keep that property — isolate and
reap, never delete or skip. Confirmed: the fix surface isolates (TestMain trap + process-group reap +
short socket dir + HOME sandbox), it does not delete or `t.Skip` any test (`t.Skip` survives only as
the documented exotic-`TMPDIR` fallback in §C).

---

## TestMain-existence finding (the requested critical check)

**No `func TestMain(m *testing.M)` exists in `cmd/r1`.** Verified:
`grep -rn 'func TestMain' cmd/r1/` returns ONLY two **test functions** —
`TestMain_DaemonAlias_WritesHintAndForwards` (serve_aliases_test.go:23) and
`TestMain_AgentServeAlias_WritesHintAndForwards` (serve_aliases_test.go:54). Neither is the
`testing.M` entry point; both are ordinary `func TestXxx(t *testing.T)` tests whose names merely
begin with `TestMain_`.

Therefore the spec's instruction (item 1) to CREATE a new `func TestMain(m *testing.M)` in
`cmd/r1/main_test.go` is CORRECT and will compile — there is no duplicate entry point to collide
with. The spec already calls out the name-collision non-issue ("Ensure no conflict with the existing
test functions ... those are test funcs, not the entry point"), which matches the verified reality.
No amendment needed for this check; the AMEND-vs-DUPLICATE risk does NOT apply here. (Verification
this matters: a second `func TestMain(m *testing.M)` in the same package would be a compile error —
that scenario does not exist, so the CREATE instruction stands.)

---

## Citations checked (summary)

Real and resolving: daemon_http.go:108/109/113/117/130; daemon_http_unix.go:16/20;
daemon_http_test.go:165/177; server.go:24/31/35/52-53/128; ctl_bootstrap.go:44/45/71/73/76/97;
skills_pack_cmd.go:1151/1153/1405/1406/1419/1433/1443; skills_pack_cmd_test.go:845/846/859/863/879/882/1018;
simple_loop_state_test.go:14/45; antitrunc_cmd_test.go:54; descent_bridge_bootstrap_test.go:14;
serve_single_instance_test.go:112/118/121/131/157; mcp_serve_runtime_test.go:258/260;
pipe_watchdog_test.go:63/72/97; serve_aliases_test.go:23/54; ctl_bootstrap_test.go:11/26/27/59/60/103/104/117-120.

Stale → fixed: task_cmd_test.go gitCmd(:202 closure@:200)/commitChange(:233→:226);
ctl_daemon_cmd_test.go single-test@:356/:384 → two tests @:306 and @:361.

---

## Criticals: found 3, fixed 3
1. `t.Setenv` after `t.Parallel()` runtime panic (item 6 + Boundaries) — FIXED.
2. Conflated/stale ctl_daemon_cmd_test.go citation, second exposed test missed (item 10) — FIXED.
3. task_cmd_test.go `gitCmd` mischaracterized as shared helper + wrong `commitChange` line
   (Existing Patterns + item 4) — FIXED.

No scope was weakened. Hermeticity verification (zero leaked `r1.test serve` procs, real `.git`
HEAD + porcelain byte-identical with no `index.lock`, socket bind test at `-count=20`) is concrete
and present in Acceptance Criteria + Group D gates.

## Final verdict: READY
The spec is ready for `/build` after the three inline repairs in this review. The TestMain CREATE
instruction is correct (no existing entry point to amend). All remaining citations resolve to real
code at the cited (or corrected) lines.
