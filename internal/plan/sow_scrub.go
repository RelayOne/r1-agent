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

// scrubFileExistsCmd matches "file_exists PATH" at the start of a
// command with optional "&& ..." suffix. If matched, the path is
// captured and the command becomes empty + FileExists field set.
var scrubFileExistsCmd = regexp.MustCompile(`^\s*file_exists\s+([^\s&]+)\s*(?:&&.*)?$`)

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
	scrubGitClonePre  = regexp.MustCompile(`(?:cd\s+\$\(mktemp\s+-d\)\s*&&\s*)?git\s+clone\s+\$\{?REPO_URL\}?[^&;]*(?:&&\s*)?`)
	scrubOrEchoOK     = regexp.MustCompile(`\s*\|\|\s*echo\s+(?:['"][^'"]*['"]|\w+)\s*`)
	scrubOrTrue       = regexp.MustCompile(`\s*\|\|\s*true\s*`)
	scrubNpx          = regexp.MustCompile(`\bnpx\s+`)
	scrubPnpmExec     = regexp.MustCompile(`\bpnpm\s+exec\s+`)
	scrubStderrNull   = regexp.MustCompile(`\s*2>/dev/null\s*`)
	scrubPlaywright   = regexp.MustCompile(`\bplaywright\s+test\b[^;&|]*`)
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
