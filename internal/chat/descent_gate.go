package chat

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ericmacdougall/stoke/internal/operator"
)

// DescentGate detects dirtied source files after a chat turn and runs a
// trimmed verification descent.
//
// Spec: specs/chat-descent-control.md §1.1 (trigger logic). This file
// implements CDC-1: the gate struct + ShouldFire detector. The Run
// method that invokes plan.DescentEngine with trimmed config is added
// by CDC-3 alongside the descent config trimmer.
type DescentGate struct {
	// Repo is the session cwd, expected to be a git repo root. If not
	// a git repo, StartCommit will be "" and ShouldFire returns false.
	Repo string
	// StartCommit is the SHA captured at chat session open. An empty
	// value disables the gate entirely (no-git environments are
	// legitimate — per spec §1.1, we skip the gate with a one-line
	// warning rather than failing).
	StartCommit string
	// OnLog is called to stream each descent log line into the chat
	// reply. Consumed by the Run method added in CDC-3. May be nil.
	OnLog func(string)

	// RepairFunc is called once after an AC fails. Given the AC and
	// failure output, it should attempt a remediation (e.g. a short
	// agentloop turn). Return nil if remediation succeeded; non-nil
	// error if it couldn't even attempt. The AC is re-run after.
	RepairFunc func(ctx context.Context, ac AcceptanceCriterion, stderr string) error

	// Ask presents a 3-option prompt to the operator. Return value is
	// one of: "retry", "accept-as-is", "edit-prompt", or "". Empty
	// string is treated as "retry" (safest default).
	Ask func(ctx context.Context, prompt string) string

	// acsBuilder is visible for testing. Tests override it to inject
	// synthetic ACs without needing a real repo + toolchain. nil means
	// Run delegates to the default BuildACsForTouched.
	acsBuilder func(repo string, changed []string) []AcceptanceCriterion
}

// ChatVerdict is the gate's summary of what descent found.
type ChatVerdict struct {
	Outcomes   []ACOutcome
	SoftPass   bool  // true iff operator selected "accept-as-is"
	EditPrompt bool  // true iff operator selected "edit-prompt"
	FatalErr   error
}

// ACOutcome is one AC's result.
type ACOutcome struct {
	AC          AcceptanceCriterion
	Passed      bool
	Stdout      string
	Stderr      string
	RepairTried bool
	Err         error
}

// Run executes the trimmed descent for the touched files. See spec
// §1.4–1.6: build ACs, run sequentially, repair-once on failure, then
// ask the operator with retry/accept-as-is/edit-prompt.
func (g *DescentGate) Run(ctx context.Context, changed []string) (ChatVerdict, error) {
	builder := g.acsBuilder
	if builder == nil {
		builder = BuildACsForTouched
	}
	acs := builder(g.Repo, changed)
	if len(acs) == 0 {
		return ChatVerdict{}, nil
	}
	verdict := ChatVerdict{Outcomes: make([]ACOutcome, 0, len(acs))}

	for _, ac := range acs {
		out := runAC(ctx, g.Repo, ac)
		if out.Passed {
			g.log("  ✓ " + ac.ID + " passed")
			verdict.Outcomes = append(verdict.Outcomes, out)
			continue
		}
		g.log("  ✗ " + ac.ID + " failed: " + truncate(out.Stderr, 200))
		// Retry once via RepairFunc if available.
		if g.RepairFunc != nil {
			g.log("  → Retrying once...")
			if err := g.RepairFunc(ctx, ac, out.Stderr); err == nil {
				out2 := runAC(ctx, g.Repo, ac)
				out2.RepairTried = true
				if out2.Passed {
					g.log("  ✓ " + ac.ID + " passed after repair")
					verdict.Outcomes = append(verdict.Outcomes, out2)
					continue
				}
				out = out2 // keep the post-repair failure as the outcome
			}
		}
		// Repair exhausted; ask operator.
		if g.Ask != nil {
			choice := g.Ask(ctx, "AC "+ac.ID+" still failing after 1 repair. What now?\n  [retry] [accept-as-is] [edit-prompt]")
			switch choice {
			case "accept-as-is":
				verdict.SoftPass = true
				verdict.Outcomes = append(verdict.Outcomes, out)
				return verdict, nil
			case "edit-prompt":
				verdict.EditPrompt = true
				verdict.Outcomes = append(verdict.Outcomes, out)
				return verdict, nil
			default: // "retry" or empty
				out3 := runAC(ctx, g.Repo, ac)
				verdict.Outcomes = append(verdict.Outcomes, out3)
				if !out3.Passed {
					verdict.FatalErr = fmt.Errorf("AC %s failed after retry", ac.ID)
					return verdict, verdict.FatalErr
				}
			}
		} else {
			verdict.Outcomes = append(verdict.Outcomes, out)
			verdict.FatalErr = fmt.Errorf("AC %s failed (no operator available)", ac.ID)
			return verdict, verdict.FatalErr
		}
	}
	return verdict, nil
}

// runAC shells out to `sh -c` with the AC command. cwd is the repo
// root so relative-path commands (e.g. `go build ./...`) resolve
// correctly. Stdout and stderr are captured separately for display.
func runAC(ctx context.Context, repo string, ac AcceptanceCriterion) ACOutcome {
	cmd := exec.CommandContext(ctx, "sh", "-c", ac.Command)
	cmd.Dir = repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return ACOutcome{
		AC:     ac,
		Passed: err == nil,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Err:    err,
	}
}

// log is a nil-safe OnLog forwarder used by Run's progress output.
func (g *DescentGate) log(line string) {
	if g != nil && g.OnLog != nil {
		g.OnLog(line)
	}
}

// truncate shortens s to at most n runes plus an ellipsis. Used to
// cap long stderr payloads in streamed log lines.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ShouldFire returns (fire, changedFiles, err). fire is true iff the
// turn dirtied any source file under Repo since StartCommit. The filter
// honors the source/config allowlist and the docs/internal-state skip
// list documented in §1.1 of specs/chat-descent-control.md.
//
// Detection uses `git status --porcelain` in Repo. This catches both
// tracked edits and untracked new files in a single pass, matching the
// spec's step 2 union. Rename lines ("R  old -> new") contribute the
// destination path only.
func (g *DescentGate) ShouldFire(ctx context.Context) (bool, []string, error) {
	if g == nil || g.StartCommit == "" {
		return false, nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = g.Repo
	out, err := cmd.Output()
	if err != nil {
		return false, nil, err
	}
	var touched []string
	for _, line := range strings.Split(string(out), "\n") {
		// Porcelain v1 lines are "XY path" — XY is a 2-char status
		// code, column 3 is a space, remainder is the path. Anything
		// shorter than 4 chars cannot carry a path.
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		// Rename: "R  old -> new" or "R  \"old\" -> \"new\"". Take
		// the destination (post-rename) path since that is what the
		// turn actually dirtied.
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		// Strip surrounding quotes that porcelain adds for paths
		// containing spaces or special chars.
		path = strings.Trim(path, "\"")
		if path == "" {
			continue
		}
		touched = append(touched, path)
	}
	filtered := FilterSourceFiles(touched)
	return len(filtered) > 0, filtered, nil
}

// FilterSourceFiles returns only entries that (a) match the source or
// config allowlist and (b) are not in the skip set. Exported for
// testability.
func FilterSourceFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if isSkipped(p) {
			continue
		}
		if isSource(p) || isConfig(p) {
			out = append(out, p)
		}
	}
	return out
}

// isSkipped returns true for paths the gate should never fire on:
// documentation, license, plans/specs/docs trees, and internal state
// caches (.claude/, .stoke/). See §1.1 spec explicit-skip list.
func isSkipped(p string) bool {
	base := filepath.Base(p)
	lp := strings.ToLower(p)
	if strings.HasSuffix(lp, ".md") || strings.HasSuffix(lp, ".txt") {
		return true
	}
	if strings.HasPrefix(base, "LICENSE") || base == ".gitignore" {
		return true
	}
	// Normalize to forward slashes for cross-platform segment matching
	// since git status emits forward slashes even on Windows.
	segs := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	for _, s := range segs {
		switch s {
		case ".claude", ".stoke", "docs", "specs", "plans":
			return true
		}
	}
	return false
}

// sourceExtensions is the set of file extensions the gate treats as
// source code for descent-trigger purposes. Keep in sync with §1.1
// spec source allowlist.
var sourceExtensions = map[string]struct{}{
	".go":    {},
	".ts":    {},
	".tsx":   {},
	".js":    {},
	".jsx":   {},
	".py":    {},
	".rs":    {},
	".java":  {},
	".kt":    {},
	".rb":    {},
	".php":   {},
	".cs":    {},
	".swift": {},
	".c":     {},
	".cc":    {},
	".cpp":   {},
	".h":     {},
	".hpp":   {},
	".m":     {},
	".mm":    {},
}

// isSource returns true when the path ends in a source-code extension
// that should trigger a mini-descent.
func isSource(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	_, ok := sourceExtensions[ext]
	return ok
}

// configBasenames is the set of dependency-manifest files that should
// trigger a descent even without accompanying code changes — editing
// one of these implies a rebuild and downstream verification is
// warranted. Keep in sync with §1.1 spec config allowlist.
var configBasenames = map[string]struct{}{
	"package.json":     {},
	"pnpm-lock.yaml":   {},
	"go.mod":           {},
	"go.sum":           {},
	"Cargo.toml":       {},
	"Cargo.lock":       {},
	"requirements.txt": {},
	"pyproject.toml":   {},
	"uv.lock":          {},
	"poetry.lock":      {},
}

// isConfig returns true for dependency-manifest basenames that imply
// a dependency rebuild.
func isConfig(p string) bool {
	_, ok := configBasenames[filepath.Base(p)]
	return ok
}

// AskFromOperator returns an Ask function for DescentGate backed by
// an operator.Operator. The three soft-pass options are passed
// verbatim — callers should match the labels "retry", "accept-as-is",
// "edit-prompt" in Run.
func AskFromOperator(op operator.Operator) func(ctx context.Context, prompt string) string {
	return func(ctx context.Context, prompt string) string {
		choice, _ := op.Ask(ctx, prompt, []operator.Option{
			{Label: "retry", Hint: "try again (~$0.20, ~1 min)"},
			{Label: "accept-as-is", Hint: "keep file as modified, mark turn complete"},
			{Label: "edit-prompt", Hint: "abandon turn, restate the request"},
		})
		return choice
	}
}

// CaptureStartCommit runs `git rev-parse HEAD` in repo. Returns the SHA
// on success, or "" if the directory is not a git repo or git is not
// installed. A "" result is the documented signal for DescentGate to
// skip the gate entirely — no-git environments are valid chat targets.
func CaptureStartCommit(ctx context.Context, repo string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
