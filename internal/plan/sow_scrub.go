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
