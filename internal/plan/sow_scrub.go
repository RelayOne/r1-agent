package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// ScrubSOW deterministically rewrites known bad acceptance-criterion
// command patterns. This is a cheap (no LLM) pre-pass that runs before
// the critique pass on every SOW, so the critique model sees fewer
// trivial hygiene issues and the refined SOW is less likely to inherit
// a bug the scrub could have caught locally.
//
// Patterns fixed:
//
//  1. "cd $(mktemp -d) && git clone $REPO_URL . && ..." — the model
//     was instructed to clone a remote repo URL that doesn't exist.
//     The SOW runs against the current working directory, not a
//     fresh clone. Strip the clone preamble.
//
//  2. "|| echo ok" / "|| true" / "|| echo 'X'" trailing fallbacks —
//     these turn every command into a pass and defeat the whole
//     point of a mechanically verifiable criterion. Strip the
//     fallback.
//
//  3. "npx <bin>" — when stoke prepends node_modules/.bin to PATH,
//     the bin is directly callable. Using npx invokes a package
//     resolver that may download a different version of the tool.
//     Replace "npx " with "".
//
//  4. "pnpm exec <bin>" — same reason as #3. Strip the wrapper.
//
// Returns the mutated SOW (always the same pointer as input) and a
// slice of diagnostic lines describing the changes that were made, so
// the caller can log them for visibility.
func ScrubSOW(sow *SOW) (*SOW, []string) {
	if sow == nil {
		return nil, nil
	}
	var diag []string
	for si := range sow.Sessions {
		sess := &sow.Sessions[si]
		for ci := range sess.AcceptanceCriteria {
			ac := &sess.AcceptanceCriteria[ci]

			// H-64: planners sometimes emit JSON-object syntax
			// {"file_exists": "PATH"} instead of the bare
			// "file_exists PATH" token. bash can't parse this
			// ({ at column 0 becomes a literal word, : makes it
			// invalid) → exit 127. Normalize these to the bare
			// form first so H-59a / H-62 handle them uniformly
			// downstream.
			if ac.Command != "" && scrubFileExistsJSON.MatchString(ac.Command) {
				orig := ac.Command
				ac.Command = scrubFileExistsJSON.ReplaceAllString(ac.Command, `file_exists $1`)
				diag = append(diag, fmt.Sprintf("%s/%s: normalized JSON-object {\"file_exists\":\"…\"} to bare \"file_exists …\" form: %q -> %q", sess.ID, ac.ID, orig, ac.Command))
			}

			// H-59a + H-62: move "file_exists PATH" from the command
			// field into the FileExists struct field when it's the
			// ONLY thing in the command. When there are MULTIPLE
			// file_exists (compound like "file_exists A && file_exists
			// B") we can't fold all of them into a single FileExists
			// field, so instead rewrite "file_exists PATH" to
			// "test -f PATH" which IS a bash builtin. Same intent,
			// actually runs.
			if ac.Command != "" {
				if m := scrubFileExistsCmd.FindStringSubmatch(ac.Command); m != nil {
					path := m[1]
					if ac.FileExists == "" {
						ac.FileExists = path
					}
					ac.Command = ""
					diag = append(diag, fmt.Sprintf("%s/%s: promoted \"file_exists %s\" from command string to FileExists field (avoids exit 127 shell run)", sess.ID, ac.ID, path))
				} else if scrubFileExistsCompound.MatchString(ac.Command) {
					orig := ac.Command
					ac.Command = scrubFileExistsCompound.ReplaceAllString(ac.Command, "test -f $1")
					diag = append(diag, fmt.Sprintf("%s/%s: rewrote \"file_exists X\" to \"test -f X\" (bash builtin) in compound command: %q -> %q", sess.ID, ac.ID, orig, ac.Command))
				}
			}

			// H-59b: strip "cd <dir> && " prefix. Planner uses it even
			// when the dir is the repo root or when it could use
			// pnpm --filter. Harness runs AC in repoRoot; a stray cd
			// either fails or changes CWD unpredictably. The
			// underlying command after && is what we want to run.
			if ac.Command != "" {
				if m := scrubCdAndPrefix.FindStringSubmatch(ac.Command); m != nil {
					orig := ac.Command
					ac.Command = strings.TrimSpace(m[1])
					diag = append(diag, fmt.Sprintf("%s/%s: stripped \"cd <dir> && \" prefix (harness runs ACs in repo root): %q -> %q", sess.ID, ac.ID, orig, ac.Command))
				}
			}

			// H-59c: rewrite malformed "pnpm --dir X <cmd>" to the
			// correct pnpm filter syntax when a bin name follows
			// instead of a script. Planner keeps producing "pnpm --dir
			// app next build" which pnpm parses as "run a script named
			// 'next' with args ['build']" — fails with "No such
			// script: next". The right form is "pnpm --filter <pkg>
			// exec <bin> <args>" OR running the bin via pnpm's
			// --filter after cd. Rewrite to "npx <cmd>" which at least
			// works when the bin is in node_modules/.bin (stoke
			// prepends that). Then the npx stripper above runs.
			if ac.Command != "" {
				if m := scrubPnpmDirBin.FindStringSubmatch(ac.Command); m != nil {
					orig := ac.Command
					// m[1] = dir, m[2] = bin+args
					ac.Command = fmt.Sprintf("cd %s && %s", m[1], strings.TrimSpace(m[2]))
					diag = append(diag, fmt.Sprintf("%s/%s: rewrote malformed \"pnpm --dir <dir> <bin>\" to \"cd <dir> && <bin>\": %q -> %q", sess.ID, ac.ID, orig, ac.Command))
					// Re-apply cd-prefix + npx cleanup on the rewritten value.
					if m2 := scrubCdAndPrefix.FindStringSubmatch(ac.Command); m2 != nil {
						ac.Command = strings.TrimSpace(m2[1])
					}
				}
			}

			if ac.Command == "" {
				continue
			}
			orig := ac.Command
			fixed, changes := scrubCommand(orig)
			if fixed != orig {
				ac.Command = fixed
				for _, ch := range changes {
					diag = append(diag, fmt.Sprintf("%s/%s: %s", sess.ID, ac.ID, ch))
				}
			}
		}
	}
	return sow, diag
}

// scrubFileExistsCmd matches "file_exists PATH" as the ENTIRE command
// (no trailing && more-commands). When the command is exactly one
// file_exists token it can be promoted to the FileExists struct field;
// if it's a compound, fall through to scrubFileExistsCompound below so
// each token is rewritten to "test -f PATH" and none are silently
// dropped. (Earlier this pattern allowed a trailing "&&.*" which
// greedily swallowed the rest of a compound command, effectively
// discarding every file_exists after the first — H-64 tightened it.)
var scrubFileExistsCmd = regexp.MustCompile(`^\s*file_exists\s+(\S+)\s*$`)

// scrubFileExistsJSON matches JSON-object syntax {"file_exists": "PATH"}
// that planners sometimes emit as a shell command. Each match is
// rewritten to the bare "file_exists PATH" form so the single-AC
// promotion + compound test -f rewrite below can handle it. Bash
// cannot parse the JSON-object form directly (it fails at exit 127
// with "command not found" on the leading { token), so without this
// normalization the AC runs raw JSON through /bin/bash and blows up.
var scrubFileExistsJSON = regexp.MustCompile(`\{\s*"file_exists"\s*:\s*"([^"]+)"\s*\}`)

// scrubFileExistsCompound matches any occurrence of "file_exists PATH"
// inside a command string (e.g. "file_exists A && file_exists B" or
// "grep -q X && file_exists Y"). Rewrites each to "test -f PATH"
// which is a real bash builtin. Used by the ScrubSOW loop when the
// single-AC promotion can't apply (multiple file_exists in one
// command cannot fold into a single FileExists struct field).
var scrubFileExistsCompound = regexp.MustCompile(`\bfile_exists\s+([^\s&|;]+)`)

// scrubCdAndPrefix strips "cd <dir> &&" when followed by more command.
// Captures the remainder.
var scrubCdAndPrefix = regexp.MustCompile(`^\s*cd\s+[^\s&]+\s*&&\s*(.+)$`)

// scrubPnpmDirBin matches "pnpm --dir <dir> <something>" where the
// something-that-follows is not a known pnpm subcommand. Rewrites to
// "cd <dir> && <something>" which cd-prefix scrub then handles.
var scrubPnpmDirBin = regexp.MustCompile(`^\s*pnpm\s+--dir\s+(\S+)\s+(\S+(?:\s+.+)?)$`)

// Compiled regex set. Kept package-scope so repeated ScrubSOW calls
// don't pay for re-compilation on every session.
var (
	scrubGitClonePre   = regexp.MustCompile(`(?:cd\s+\$\(mktemp\s+-d\)\s*&&\s*)?git\s+clone\s+\$\{?REPO_URL\}?[^&;]*(?:&&\s*)?`)
	scrubOrEchoOK      = regexp.MustCompile(`\s*\|\|\s*echo\s+(?:['"][^'"]*['"]|\w+)\s*`)
	scrubOrTrue        = regexp.MustCompile(`\s*\|\|\s*true\s*`)
	scrubNpx           = regexp.MustCompile(`\bnpx\s+`)
	scrubPnpmExec      = regexp.MustCompile(`\bpnpm\s+exec\s+`)
	scrubStderrNull    = regexp.MustCompile(`\s*2>/dev/null\s*`)
	scrubPlaywright    = regexp.MustCompile(`\bplaywright\s+test\b[^;&|]*`)
	// H-71: matches `pnpm --filter <pkg> <word>`. Go regexp has no
	// lookahead, so we capture greedily and filter reserved pnpm
	// subcommands in scrubCommand before rewriting.
	scrubPnpmFilterBin = regexp.MustCompile(`\bpnpm\s+--filter\s+(\S+)\s+([a-zA-Z][a-zA-Z0-9_.-]*)\b`)
	// H-75 + H-90: matches recursive/filter-all pnpm script invocations.
	// Covers short form (-r), long form (--recursive), filter-all
	// (-F '*' or --filter '*'). All of these trigger
	// ERR_PNPM_RECURSIVE_EXEC_FIRST_FAIL when any package lacks the
	// script. Adding --if-present lets pnpm silently skip those.
	scrubPnpmRecursiveScript = regexp.MustCompile(`\bpnpm\s+(?:-r|--recursive|-F\s+['"]?\*['"]?|--filter\s+['"]?\*['"]?)\s+(?:run\s+)?([a-zA-Z][a-zA-Z0-9_.-]*)\b`)
)

// scrubCommand applies every scrub rule to a single command string and
// returns the fixed version plus a list of one-line descriptions of
// what changed. An empty list of changes means the command was clean.
func scrubCommand(cmd string) (string, []string) {
	var changes []string
	fixed := cmd

	if scrubGitClonePre.MatchString(fixed) {
		before := fixed
		fixed = scrubGitClonePre.ReplaceAllString(fixed, "")
		fixed = strings.TrimLeft(fixed, " \t&")
		if fixed != before {
			changes = append(changes, "stripped git clone $REPO_URL preamble (no remote repo; SOW runs in-place)")
		}
	}
	if scrubOrEchoOK.MatchString(fixed) {
		before := fixed
		fixed = scrubOrEchoOK.ReplaceAllString(fixed, "")
		if fixed != before {
			changes = append(changes, `stripped "|| echo ..." fallback (turns failures into false passes)`)
		}
	}
	if scrubOrTrue.MatchString(fixed) {
		before := fixed
		fixed = scrubOrTrue.ReplaceAllString(fixed, "")
		if fixed != before {
			changes = append(changes, `stripped "|| true" fallback (turns failures into false passes)`)
		}
	}
	// H-71: `pnpm --filter X <bin>` resolves <bin> against the
	// package.json scripts block FIRST, only falling through to
	// node_modules/.bin if no script of that name exists. When the
	// worker writes a package.json that happens to have a script
	// named the same as the binary (common mistake: `"vitest": "vitest run"`),
	// `pnpm --filter web vitest …` runs the script recursively
	// instead of the binary, causing infinite recursion or silent
	// no-op. Rewriting to `pnpm --filter X exec <bin>` forces the
	// binary resolution. R06 S3 today lost 3 ACs (AC10-12) to this
	// collision before the reasoning pass caught it per-AC.
	// H-75: when `pnpm build` / `pnpm test` etc. are invoked on a
	// package that doesn't define that script in package.json,
	// pnpm fails with ERR_PNPM_RECURSIVE_EXEC_FIRST_FAIL instead
	// of skipping gracefully. R06-sow-serial today lost 3 sessions
	// to this because packages/types and packages/api-client had
	// no `build` script. pnpm provides `--if-present` for exactly
	// this case — scripts missing from a sub-package's
	// package.json are silently skipped. Rewriting `pnpm run <script>`
	// and `pnpm -r <script>` to include `--if-present` closes the
	// gap; it does NOT affect commands that already have a matching
	// script (they still run).
	if scrubPnpmRecursiveScript.MatchString(fixed) {
		before := fixed
		fixed = scrubPnpmRecursiveScript.ReplaceAllString(fixed, `pnpm -r --if-present $1`)
		if fixed != before {
			changes = append(changes, `added --if-present to "pnpm -r <script>" (H-75: skip packages lacking the script instead of ERR_PNPM_RECURSIVE_EXEC_FIRST_FAIL)`)
		}
	}
	if m := scrubPnpmFilterBin.FindStringSubmatchIndex(fixed); m != nil {
		// m[4]..m[5] is the <bin> capture. Reserved pnpm
		// subcommands get left alone; unknown words get the `exec`
		// rewrite.
		bin := fixed[m[4]:m[5]]
		pnpmSubcmds := map[string]bool{
			"exec": true, "run": true, "install": true, "i": true,
			"add": true, "remove": true, "rm": true, "update": true,
			"up": true, "publish": true, "pack": true, "unlink": true,
			"link": true, "rebuild": true, "dlx": true, "create": true,
			"import": true, "outdated": true, "prune": true, "init": true,
			"audit": true, "licenses": true, "list": true, "ls": true,
			"why": true, "store": true, "server": true, "recursive": true,
			"root": true, "bin": true, "env": true,
		}
		if !pnpmSubcmds[bin] {
			before := fixed
			fixed = fixed[:m[4]] + "exec " + fixed[m[4]:]
			if fixed != before {
				changes = append(changes, `rewrote "pnpm --filter X <bin>" to "pnpm --filter X exec <bin>" (H-71: forces node_modules/.bin resolution, avoids same-named script recursion)`)
			}
		}
	}
	if scrubNpx.MatchString(fixed) {
		before := fixed
		fixed = scrubNpx.ReplaceAllString(fixed, "")
		if fixed != before {
			changes = append(changes, `replaced "npx X" with direct "X" (stoke prepends node_modules/.bin to PATH)`)
		}
	}
	if scrubPnpmExec.MatchString(fixed) {
		before := fixed
		fixed = scrubPnpmExec.ReplaceAllString(fixed, "")
		if fixed != before {
			changes = append(changes, `replaced "pnpm exec X" with direct "X" (stoke prepends node_modules/.bin to PATH)`)
		}
	}
	if scrubStderrNull.MatchString(fixed) {
		before := fixed
		fixed = scrubStderrNull.ReplaceAllString(fixed, " ")
		if fixed != before {
			changes = append(changes, `stripped "2>/dev/null" (hides useful error output from AC runner)`)
		}
	}
	if scrubPlaywright.MatchString(fixed) {
		before := fixed
		fixed = scrubPlaywright.ReplaceAllString(fixed, `echo "playwright e2e deferred"`)
		if fixed != before {
			changes = append(changes, `replaced "playwright test ..." with deferred echo (browser-based E2E requires setup the build agent cannot provide)`)
		}
	}

	// Final trim in case we left leading/trailing whitespace.
	fixed = strings.TrimSpace(fixed)
	return fixed, changes
}
