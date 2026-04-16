// Package main — sow_ac_target.go
//
// Extracts the concrete file path(s) an acceptance criterion (AC)
// actually tests, so the repair prompt can tell the worker:
//
//	EDIT TARGET: packages/design-tokens/package.json
//
// instead of leaving it to the model to infer. Run 40 hit this:
// the semantic judge correctly identified "missing runtime deps in
// package.json", but the repair worker kept editing `.env.example`,
// `.gitignore`, `.prettierrc`, and `README.md` — anything BUT the
// `dependencies:{}` block the AC `grep`s. The judge's wrong_layer
// verdict fired every round but repair attempts never broke through.
//
// Scope: best-effort pattern matching on AC commands. The extraction
// is non-authoritative — a worker can still edit other files if a
// genuine fix requires it — but the target hint biases the first edit
// toward the file the AC literally inspects.
package main

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// extractACTargets returns the file path(s) an AC command is most
// likely testing. Returns an empty slice when the command shape isn't
// recognized (caller should fall back to the generic prompt).
//
// Recognized shapes, in order:
//
//  1. FileExists / ContentMatch structured fields — authoritative
//  2. grep / egrep / rg against named file arguments
//  3. test -f / test -e / [ -f ... ] / [ -e ... ]
//  4. pnpm --filter <scope/pkg> <script>   →  <scope/pkg>/package.json
//  5. cat <file> | ...     →  <file>
//  6. jq '...' <file>      →  <file>
//
// Shapes deliberately NOT interpreted:
//   - `pnpm install` / `pnpm build` at root (too broad)
//   - `go test ./...` / `cargo test` (no specific file)
//   - any command with redirection or complex pipelines we can't
//     parse confidently — better to emit nothing than hint at the
//     wrong file
func extractACTargets(criterion plan.AcceptanceCriterion) []string {
	seen := map[string]struct{}{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		path = strings.Trim(path, "'\"")
		if path == "" || strings.HasPrefix(path, "-") {
			return
		}
		// Reject obvious non-path tokens.
		if path == "&&" || path == "||" || path == ";" {
			return
		}
		seen[path] = struct{}{}
	}

	if criterion.FileExists != "" {
		add(criterion.FileExists)
	}
	if criterion.ContentMatch != nil && criterion.ContentMatch.File != "" {
		add(criterion.ContentMatch.File)
	}

	if criterion.Command != "" {
		for _, chunk := range splitCommandChunks(criterion.Command) {
			for _, p := range extractPathsFromChunk(chunk) {
				add(p)
			}
		}
	}

	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// splitCommandChunks splits a shell command line on boolean and pipe
// separators so we can extract paths from each sub-command
// independently. Doesn't handle quoting edge cases — a filename
// containing `&&` would break this — but AC commands the scheduler
// emits don't have those in practice.
func splitCommandChunks(cmd string) []string {
	// Replace all separators with a single token, then split.
	for _, sep := range []string{"&&", "||", " | ", ";"} {
		cmd = strings.ReplaceAll(cmd, sep, " \x00 ")
	}
	parts := strings.Split(cmd, "\x00")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// grepCmdRe matches grep/egrep/fgrep/rg invocations. We grab the tail
// tokens after the pattern and treat them as file args.
//
// testCmdRe / bracketFileRe deliberately accept ONLY file-focused
// flags (-f, -e, -r for readable). Directory checks (-d) are NOT
// an edit target because the AC is verifying a directory EXISTS,
// not its contents — pointing the repair worker at e.g.
// `node_modules` as an "EDIT TARGET" is actively misleading.
var (
	grepHeadRe    = regexp.MustCompile(`(?i)^\s*(?:e|f)?grep\b|^\s*rg\b`)
	testCmdRe     = regexp.MustCompile(`(?i)\btest\s+-[efr]\s+(\S+)`)
	bracketFileRe = regexp.MustCompile(`\[\s+-[efr]\s+(\S+)\s+\]`)
	catHeadRe     = regexp.MustCompile(`(?i)^\s*(?:cat|head|tail|wc|file)\s+(\S+)`)
	jqHeadRe      = regexp.MustCompile(`(?i)^\s*jq\s+(?:-[a-zA-Z]+\s+)*'[^']*'\s+(\S+)`)

	// pnpmFilterRe uses FindAllStringSubmatch to pick up
	// every --filter occurrence when a command stacks
	// multiple (e.g. `pnpm -F @scope/a -F @scope/b build`).
	// A single filter only catches the first package, biasing
	// repair toward the wrong package when the ACs fail on
	// the second.
	pnpmFilterRe = regexp.MustCompile(`pnpm\s+(?:--filter|-F)\s+([^\s]+)`)
)

// extractPathsFromChunk inspects a single sub-command and returns any
// file paths it names. Kept simple: string-prefix dispatch so we never
// over-interpret a complex pipeline.
func extractPathsFromChunk(chunk string) []string {
	chunk = strings.TrimSpace(chunk)
	if chunk == "" {
		return nil
	}

	// test -f FILE  /  test -e FILE  /  [ -f FILE ]
	if m := testCmdRe.FindStringSubmatch(chunk); len(m) > 1 {
		return []string{m[1]}
	}
	if m := bracketFileRe.FindStringSubmatch(chunk); len(m) > 1 {
		return []string{m[1]}
	}

	// grep "pattern" file1 file2 ...
	// Everything after the pattern (first quoted or unquoted arg that
	// looks like a regex) and excluding flags is a file candidate.
	if grepHeadRe.MatchString(chunk) {
		return extractGrepFiles(chunk)
	}

	// pnpm --filter <SELECTOR> is a PACKAGE SELECTOR, not a
	// repo path — it can be a bare name ("ui"), a scoped
	// name ("@scope/app"), or a glob. Without the workspace
	// manifest we can't resolve it to an on-disk path, and
	// emitting "@scope/app/package.json" as an EDIT TARGET
	// steers repair at a path that doesn't exist. Better to
	// emit nothing and let the worker consult the workspace
	// manifest itself.
	//
	// We still walk every match (FindAllStringSubmatch, not
	// FindStringSubmatch) so a future refactor that gets
	// access to the workspace map can translate the list in
	// one pass rather than only seeing the first filter.
	_ = pnpmFilterRe.FindAllStringSubmatch(chunk, -1)

	// cat FILE, head FILE, etc.
	if m := catHeadRe.FindStringSubmatch(chunk); len(m) > 1 {
		return []string{m[1]}
	}

	// jq 'expr' FILE
	if m := jqHeadRe.FindStringSubmatch(chunk); len(m) > 1 {
		return []string{m[1]}
	}

	return nil
}

// extractGrepFiles walks a grep invocation and returns the trailing
// file arguments. Skips flags and the pattern arg.
func extractGrepFiles(chunk string) []string {
	fields := shellSplitBasic(chunk)
	if len(fields) < 2 {
		return nil
	}
	// Drop `grep` head.
	fields = fields[1:]
	// Skip flags. First non-flag token is the pattern; remaining
	// non-flag tokens are file paths.
	seenPattern := false
	var out []string
	for _, f := range fields {
		if strings.HasPrefix(f, "-") && f != "-" && !strings.HasPrefix(f, "--=") {
			// Flags with attached values (-e pattern / --regexp=X) are
			// a pain; the common case in AC commands is -q / -E / -r
			// which don't take a separate arg. Skip and keep scanning.
			continue
		}
		if !seenPattern {
			seenPattern = true
			continue
		}
		// Trim quote chars. Paths with spaces in AC commands are rare.
		out = append(out, strings.Trim(f, "'\""))
	}
	return out
}

// shellSplitBasic is a minimal shell-word splitter: handles single-
// and double-quoted strings, treats whitespace as a separator.
// Sufficient for the AC commands the scheduler emits.
func shellSplitBasic(s string) []string {
	var out []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
		case r == '"':
			inDouble = true
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// acTargetBlurb renders an "EDIT TARGET" hint for inclusion in repair
// prompts. Returns empty string when no usable target was extractable
// so the caller can skip the line entirely rather than printing
// "EDIT TARGET: (none)" which would be noise.
func acTargetBlurb(criterion plan.AcceptanceCriterion) string {
	targets := extractACTargets(criterion)
	if len(targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("EDIT TARGET")
	if len(targets) > 1 {
		b.WriteString("S")
	}
	b.WriteString(" (file(s) the AC inspects — edit HERE, not adjacent config):\n")
	for _, t := range targets {
		b.WriteString("  - ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	return b.String()
}

// findCriterionByID looks up an AcceptanceCriterion in a session by
// ID. Returns zero-value + false when not found so callers can
// gracefully fall back to the un-enriched failure format (e.g. when a
// repair round operates on a synthetic AC the session doesn't list).
func findCriterionByID(session plan.Session, id string) (plan.AcceptanceCriterion, bool) {
	for _, c := range session.AcceptanceCriteria {
		if c.ID == id {
			return c, true
		}
	}
	return plan.AcceptanceCriterion{}, false
}
