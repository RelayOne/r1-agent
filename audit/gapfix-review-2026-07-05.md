# Gap-fix review — confirmed issues in the fixes (2026-07-05)

Adversarial review of the 26 gap fixes. 4 of 6 raw confirmed — ALL in the security fixes (destructive-breaker + protected-file), which were still bypassable. This is the second-order review catching incomplete first-order fixes.

## [HIGH/regression] Destructive-breaker rewrite regressed: /bin/rm, \rm, env-prefix, and path-form chmod all bypass the native floor
`internal/tools/bash_breaker.go:48`

The rewrite from regex to field parsing anchors on the command NAME being the literal first token: rmDeletesRootHome returns false unless fields[0]=="rm" (line 48) and chmodRecursiveRoot returns false unless fields[0] is chmod/chown/chgrp (line 107). The OLD regexes matched \brm and \bch(mod|own) anywhere in the segment via a word boundary, so they caught these invocations. stripWrappers only removes a hardcoded wrapper allowlist (sudo/env/timeout/...), NOT absolute paths or leading VAR=val assignments. Because this is the always-on floor for the native bash tool and the policy gate fails OPEN when unwired (see file header comment), defeating it re-opens the exact critical destructive-command gap the fix targeted.

**Scenario:** I compiled the new rmDeletesRootHome against the old regex over a case table. Blocked-by-old / allowed-by-new (REGRESSION): `/bin/rm -rf /`, `/usr/bin/rm -rf /`, `\rm -rf /` (backslash-escaped to skip aliases), `FOO=bar rm -rf /`. Same root cause makes `/bin/chmod -R 777 /` and `FOO=x chmod -R /` bypass chmodRecursiveRoot. Each executes an unrecoverable recursive delete / permission change of the filesystem root on the native tool path.

**Fix:** Match on the command basename, not the raw first field: compute cmd := path.Base(strings.TrimPrefix(fields[0], "\\")) and also skip leading NAME=VALUE assignment tokens (like stripWrappers does) before deciding, so /bin/rm, \rm and `FOO=bar rm` all resolve to command "rm".

## [HIGH/still-bypassable] hooks.go protected-file redirect guard is defeated by any path prefix, including a plain absolute path
`internal/hooks/hooks.go:214`

The new redirect guard regex is (>>?|[0-9]+>)[[:space:]]*(\./)?${PROT}: it only accepts the protected name immediately after the redirect operator, with at most a single optional `./` prefix. Any other directory prefix before the filename evades it. The sibling tee/sed/dd/cp/mv guard uses `[^|;&]*${PROT}` (arbitrary non-separator run) and is robust to prefixes, so the two guards are inconsistent and the redirect one is the weak link.

**Scenario:** Ran the exact regex over inputs: BLOCKED `> CLAUDE.md` and `> ./CLAUDE.md`, but ALLOWED `echo pwned > /home/eric/repos/r1-agent/CLAUDE.md` (plain absolute path), `echo x > $PWD/CLAUDE.md`, `echo x > .git/../CLAUDE.md`, and `echo x > docs/../CLAUDE.md`. All resolve to and overwrite the real root CLAUDE.md (or .env / .claude/settings.json via `>> /abs/.claude/settings.json`), which is precisely what this guard was added to prevent. A model naturally emits full paths, so this is not an obscure evasion.

**Fix:** Give the redirect pattern the same tolerance as the tee pattern: allow an arbitrary non-separator run before PROT, e.g. (>>?|[0-9]+>)[[:space:]]*[^|;&<>[:space:]]*${PROT}, and/or resolve the redirect target path before matching (the live workspace guard-bash-writes.sh already resolves the path, which is why it catches these).

## [MEDIUM/still-bypassable] Native rm/chmod breaker never strips quotes and anchors the target, so `rm -rf "/"` and `rm -rf /*` pass
`internal/tools/bash_breaker.go:38`

reRootHomeTarget is ^(/|~|$HOME|${HOME})(/.*)?$ applied to raw tokens from strings.Fields, and bashBreakerCheck does no quote stripping. A quoted root operand (token literally `"/"`) starts with a quote so it fails the anchor, and a glob operand `/*` is `/` followed by `*` which also fails `(/.*)?`. The parallel hooks.go fix specifically added a quote-stripped CMDN copy for its checks; the Go breaker on the native path did not get the same treatment even though the diff reworked exactly this code.

**Scenario:** Executed the new rmDeletesRootHome/bashBreakerCheck: ALLOWED `rm -rf "/"`, `rm -rf '/'`, `rm -rf /*`, and `rm -rf /home/*`. The shell expands `"/"` to `/` and `/*` to every top-level entry, so each is an unrecoverable root wipe that the always-on native floor lets through.

**Fix:** Strip surrounding single/double quotes from each operand before matching (or strip quotes from the segment like hooks.go's CMDN), and treat a trailing `/*` / `/.` glob on a root/home operand as a root/home target (match `^(/|~|$HOME|${HOME})(/.*)?$` after also allowing a bare `/*`).

## [LOW/incomplete-fix] notebook_cell_run write sink still uses unprotected resolvePath, bypassing the new protected-write deny
`internal/tools/notebook_tools.go:173`

The protected-write deny (resolveWritePath) was wired into handleWrite, handleEdit, and handleEnvCopyOut, but handleNotebookCellRun resolves its target with the plain resolvePath (line 173) and then writes the notebook file in place. A native tool call can therefore still land on a protected path.

**Scenario:** Invoke notebook_cell_run with path=".claude/settings.json" (or "CLAUDE.md"): the handler ReadFile-fails, builds a minimal notebook, appends the cell, and writes notebook JSON to that path, overwriting the protected config with nbformat JSON. Impact is bounded (content is notebook JSON, and it requires jupyter installed and the OS sandbox inactive), but it defeats the same deny the fix intended to be uniform.

**Fix:** Use r.resolveWritePath(args.Path) in handleNotebookCellRun (and audit image_tools.go:60 / any other write sink) so every native write path shares the protected-file deny.
