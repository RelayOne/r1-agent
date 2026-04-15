package plan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/depcheck"
)

// depcheckOnceMu / depcheckOnce dedup the multi-language registry
// validation per projectRoot so we pay the HEAD cost once, not per AC.
var (
	depcheckOnceMu sync.Mutex
	depcheckOnce   = map[string]bool{}
	depcheckClient = depcheck.DefaultClient()
)

// runDepCheck validates every declared dep in every recognized manifest
// under projectRoot against the matching public registry, emitting loud
// warnings when a package name doesn't exist. Rate-limited to once per
// projectRoot per process. The check has a short deadline so a slow
// registry can't delay the run.
func runDepCheck(ctx context.Context, projectRoot string) {
	depcheckOnceMu.Lock()
	if depcheckOnce[projectRoot] {
		depcheckOnceMu.Unlock()
		return
	}
	depcheckOnce[projectRoot] = true
	depcheckOnceMu.Unlock()

	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	findings, err := depcheckClient.Validate(deadline, projectRoot)
	if err != nil {
		// Walk error (missing root, permission). Log and move on; the
		// real install will surface real problems.
		fmt.Printf("  ⚠ depcheck: walk error under %s: %v — continuing\n", projectRoot, err)
		return
	}
	if len(findings) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("  ════════════════════════════════════════════════════════════════")
	fmt.Printf("  ❌ depcheck: %d declared dependenc%s cannot be resolved against its registry\n", len(findings), pluralY(len(findings)))
	fmt.Println("     These look like LLM-hallucinated package names. The install step will")
	fmt.Println("     fail with a 404 and every downstream AC will misdiagnose the symptom.")
	fmt.Println("     Fix by removing the entry or replacing it with the real package name.")
	fmt.Println()
	for _, f := range findings {
		// projectRoot-relative path for readability.
		rel := f.PackageJSON
		if r, err := filepath.Rel(projectRoot, f.PackageJSON); err == nil {
			rel = r
		}
		fmt.Printf("     - %s: %s in [%s] (%s)\n", rel, f.Name, f.Section, f.Reason)
	}
	fmt.Println("  ════════════════════════════════════════════════════════════════")
	fmt.Println()
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// AcceptanceResult is the outcome of checking one acceptance criterion.
type AcceptanceResult struct {
	CriterionID string
	Description string
	Passed      bool
	Output      string // command output or diagnostic message

	// JudgeRuled is true when the criterion initially failed its
	// mechanical check but was overridden to pass by the semantic
	// LLM judge. Recorded so the operator knows which passes were
	// literal vs semantic.
	JudgeRuled bool

	// JudgeReasoning is the semantic judge's explanation when it
	// overrode a mechanical failure. Empty when no judge ran.
	JudgeReasoning string
}

// groundTruthCommandTokens lists substrings that, when present in an
// AC's command, mark it as a ground-truth execution whose exit code
// is authoritative. These commands actually build / install / type-
// check / test the code; a failure means the code does not work in
// the literal sense. No amount of semantic reasoning should be able
// to flip a failing `pnpm build` into a pass — that's how run 3
// shipped hallucinated dependencies as "acceptable" until the cascade
// caught it.
//
// Conservative list: only explicit builder / package manager / test
// runner binaries. Shell scaffolding like `cd` or `mkdir` is not here
// because those are typically wrappers around a real command further
// down the pipeline; the pipeline as a whole is judged by its final
// tool's exit.
var groundTruthCommandTokens = []string{
	// Package managers / installers
	"pnpm install", "npm install", "npm ci", "yarn install", "yarn ",
	"pip install", "poetry install", "cargo fetch", "go mod download",
	// Build / bundlers
	"pnpm build", "npm run build", "yarn build",
	"turbo run build", "turbo build", "next build", "vite build",
	"expo build", "eas build", "cargo build", "go build",
	"tsc ", "tsc\n", "tsc\t", "tsc$",
	// Type-check / lint
	"pnpm typecheck", "tsc --noEmit", "cargo check",
	"pnpm lint", "eslint ", "biome check",
	// Test runners
	"vitest", "jest", "mocha", "playwright", "cypress",
	"cargo test", "go test", "pytest", "unittest",
	"npm test", "pnpm test", "yarn test",
	// Generic run that commonly actually executes
	"node ", "python ", "deno ",
}

// isGroundTruthACCommand returns true when the AC's command is a
// build / install / type-check / test / run invocation whose exit
// code should be treated as authoritative. Case-insensitive
// substring match on a conservative allow-list.
func isGroundTruthACCommand(cmd string) bool {
	if cmd == "" {
		return false
	}
	lc := strings.ToLower(cmd)
	for _, tok := range groundTruthCommandTokens {
		if strings.Contains(lc, tok) {
			return true
		}
	}
	return false
}

// SemanticEvaluator is called when a mechanical AC check fails. The
// implementation typically delegates to JudgeAC from this package.
// Returning (true, ...) overrides the mechanical failure to a pass.
// Returning (false, ...) or (_, err) leaves the mechanical verdict.
type SemanticEvaluator func(ctx context.Context, ac AcceptanceCriterion, failureOutput string) (overridePass bool, reasoning string, err error)

// installedOnce tracks workspace roots where we've already run the
// package-manager install, so we don't redo it on every AC invocation
// within a single SOW run. Keyed on absolute projectRoot.
var (
	installedOnceMu sync.Mutex
	installedOnce   = map[string]bool{}
)

// CheckAcceptanceCriteria evaluates all criteria for a session against the
// project directory. Returns results for each criterion and an overall pass/fail.
//
// Workspace prep: if projectRoot contains a package.json / pnpm-workspace.yaml
// and node_modules is missing, run the appropriate install command once per
// SOW run before evaluating criteria. This prevents the ubiquitous "tsc: not
// found" / "node_modules missing" failures that happen when an AC command
// expects workspace dependencies to already be installed by a prior task.
//
// PATH augmentation: AC commands are run with node_modules/.bin prepended to
// PATH so locally-installed binaries (tsc, vitest, next, eslint, etc.)
// resolve directly without needing `pnpm exec` or `npx` wrappers.
func CheckAcceptanceCriteria(ctx context.Context, projectRoot string, criteria []AcceptanceCriterion) ([]AcceptanceResult, bool) {
	return CheckAcceptanceCriteriaWithJudge(ctx, projectRoot, criteria, nil)
}

// CheckAcceptanceCriteriaWithJudge is the full-featured acceptance
// runner. When judge is non-nil, any criterion that fails its
// mechanical check gets consulted with the semantic judge before
// being marked failed. If the judge says the code actually
// implements the requirement (pattern mismatch, not real gap), the
// mechanical failure is overridden to a pass and the result is
// annotated with JudgeRuled + JudgeReasoning so the operator sees
// which passes are mechanical vs semantic.
func CheckAcceptanceCriteriaWithJudge(ctx context.Context, projectRoot string, criteria []AcceptanceCriterion, judge SemanticEvaluator) ([]AcceptanceResult, bool) {
	ensureWorkspaceInstalled(ctx, projectRoot)

	var results []AcceptanceResult
	allPassed := true

	for _, ac := range criteria {
		result := checkOneCriterion(ctx, projectRoot, ac)
		// If the mechanical check failed AND we have a semantic judge
		// AND the AC's command is not a ground-truth command, ask the
		// judge whether the code actually implements the requirement.
		//
		// Ground-truth commands (build / install / typecheck / test /
		// run) cannot be overridden by any amount of semantic reasoning:
		// a failing `pnpm build` is not a pattern mismatch, it's real
		// compile failure. Run 3 burned through ACs with hallucinated
		// deps still broken because the judge kept approving "code is
		// structurally sound" while `turbo` wasn't even on PATH. That
		// hole is closed here: the judge annotates but never overrides
		// ground truth.
		//
		// Grep / pattern / file-existence ACs remain overridable: a
		// worker can produce correct code that grep happens to miss.
		if !result.Passed && judge != nil {
			// Malformed-AC failures are non-overridable for the same
			// reason ground-truth command failures are: there's no
			// pattern-mismatch story; the AC itself is broken and
			// must be repaired by refine.
			if strings.HasPrefix(result.Output, "MALFORMED-AC:") {
				_, reasoning, err := judge(ctx, ac, result.Output)
				if err == nil && reasoning != "" {
					result.JudgeReasoning = reasoning
					result.Output = fmt.Sprintf("%s\n\nJudge observation:\n%s", result.Output, reasoning)
				}
				results = append(results, result)
				if !result.Passed {
					allPassed = false
				}
				continue
			}
			if isGroundTruthACCommand(ac.Command) {
				// Judge can still speak — we record its verdict for the
				// operator, but pass stays false regardless.
				_, reasoning, err := judge(ctx, ac, result.Output)
				if err == nil && reasoning != "" {
					result.JudgeReasoning = reasoning
					result.Output = fmt.Sprintf("GROUND-TRUTH COMMAND FAILED (judge cannot override):\n%s\n\nJudge observation:\n%s", result.Output, reasoning)
				}
			} else {
				overridePass, reasoning, err := judge(ctx, ac, result.Output)
				if err == nil && overridePass {
					result.Passed = true
					result.JudgeRuled = true
					result.JudgeReasoning = reasoning
					result.Output = fmt.Sprintf("MECHANICAL CHECK FAILED but semantic judge approved:\n%s\n\nOriginal mechanical output:\n%s", reasoning, result.Output)
				} else if err == nil && !overridePass && reasoning != "" {
					result.JudgeReasoning = reasoning
					result.Output = fmt.Sprintf("MECHANICAL CHECK FAILED and semantic judge agrees:\n%s\n\nOriginal mechanical output:\n%s", reasoning, result.Output)
				}
			}
		}
		results = append(results, result)
		if !result.Passed {
			allPassed = false
		}
	}

	return results, allPassed
}

// ensureWorkspaceInstalled runs a one-shot `pnpm install` (or `npm install`
// fallback) at projectRoot if the workspace looks like a Node project and
// node_modules is missing. Guarded by installedOnce so repeated AC passes
// don't keep reinstalling. Silent on success; errors are ignored — the AC
// commands themselves will surface any real issue with their own output.
//
// Before attempting install, we run a fast registry-resolution check
// against every declared dep in the repo (npm / PyPI / crates / Go).
// When the check flags packages that don't exist (the classic LLM
// hallucination: e.g. "@nativewind/style" invented when the real
// package is just "nativewind"), we emit a loud warning. That warning
// shows up in the AC failure context and helps the downstream
// reasoning loop correctly classify the root cause as code_bug
// rather than ac_bug. Transport errors are silent — a dead registry
// must not block a real build.
func ensureWorkspaceInstalled(ctx context.Context, projectRoot string) {
	installedOnceMu.Lock()
	if installedOnce[projectRoot] {
		installedOnceMu.Unlock()
		return
	}
	installedOnce[projectRoot] = true
	installedOnceMu.Unlock()

	// Only touch Node workspaces.
	if _, err := os.Stat(filepath.Join(projectRoot, "package.json")); err != nil {
		return
	}
	// Pre-install registry validation. Runs even when node_modules
	// exists on disk — a pre-existing node_modules doesn't tell us
	// whether a NEWLY added dep resolves.
	runDepCheck(ctx, projectRoot)
	// If node_modules already exists, trust whatever is there.
	if _, err := os.Stat(filepath.Join(projectRoot, "node_modules")); err == nil {
		return
	}

	// Prefer pnpm when the workspace looks pnpm-shaped; fall back to npm.
	// We pick the first manager that's on PATH; we intentionally do NOT
	// error out if neither is present — the AC commands will surface
	// that problem themselves with a clearer message.
	tryInstall := func(bin string, args ...string) bool {
		if _, err := exec.LookPath(bin); err != nil {
			return false
		}
		// 3-minute sub-deadline so a stuck install (waiting for
		// network, postinstall hang, stdin prompt) can't block the
		// entire SOW run. The SOW-level ctx has no timeout when
		// --timeout=0 (default), so without this we'd hang forever
		// — which killed run18 and run20 in exactly this spot.
		installCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(installCtx, bin, args...)
		cmd.Dir = projectRoot
		// Silence output: anything useful shows up in the AC failure
		// anyway. We just want to get node_modules on disk.
		return cmd.Run() == nil
	}
	_, pnpmWorkspace := os.Stat(filepath.Join(projectRoot, "pnpm-workspace.yaml"))
	if pnpmWorkspace == nil {
		if tryInstall("pnpm", "install", "--silent") {
			return
		}
	}
	if tryInstall("pnpm", "install", "--silent") {
		return
	}
	if tryInstall("npm", "install", "--silent") {
		return
	}
	// Nothing worked. Fall through — the AC will emit its own error.
}

// acceptanceCommandEnv returns the environment used when executing an
// acceptance command. It copies the current environment and prepends
// <projectRoot>/node_modules/.bin to PATH so locally-installed workspace
// binaries (tsc, vitest, next, eslint) resolve directly, matching the
// convention every Node workspace tool assumes.
func acceptanceCommandEnv(projectRoot string) []string {
	base := os.Environ()
	localBin := filepath.Join(projectRoot, "node_modules", ".bin")
	out := make([]string, 0, len(base))
	sawPath := false
	for _, kv := range base {
		if strings.HasPrefix(kv, "PATH=") {
			sawPath = true
			out = append(out, "PATH="+localBin+string(os.PathListSeparator)+strings.TrimPrefix(kv, "PATH="))
			continue
		}
		out = append(out, kv)
	}
	if !sawPath {
		out = append(out, "PATH="+localBin)
	}
	return out
}

func checkOneCriterion(ctx context.Context, projectRoot string, ac AcceptanceCriterion) AcceptanceResult {
	result := AcceptanceResult{
		CriterionID: ac.ID,
		Description: ac.Description,
	}

	// Command check: run a shell command and check exit code. The
	// command runs in projectRoot with node_modules/.bin prepended to
	// PATH so locally-installed workspace binaries (tsc, vitest, etc.)
	// resolve without needing pnpm exec / npx wrappers.
	//
	// Per-AC timeout: 5 minutes. An AC command that doesn't terminate
	// in 5 minutes is either a dev server (should never be an AC) or
	// a hung process. The SOW-level ctx has no timeout by default, so
	// without a sub-deadline a hung AC blocks the entire SOW.
	if ac.Command != "" {
		cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cmdCtx, "bash", "-lc", ac.Command)
		cmd.Dir = projectRoot
		cmd.Env = acceptanceCommandEnv(projectRoot)
		out, err := cmd.CombinedOutput()
		result.Output = string(out)
		result.Passed = err == nil
		if !result.Passed {
			result.Output = fmt.Sprintf("command failed: %v\n%s", err, result.Output)
		}
		return result
	}

	// File existence check
	if ac.FileExists != "" {
		path := ac.FileExists
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectRoot, path)
		}
		if _, err := os.Stat(path); err == nil {
			result.Passed = true
			result.Output = fmt.Sprintf("file exists: %s", ac.FileExists)
		} else {
			result.Passed = false
			result.Output = fmt.Sprintf("file not found: %s", ac.FileExists)
		}
		return result
	}

	// Content match check.
	if ac.ContentMatch != nil {
		// Empty File is the tolerated bare-string parse shape
		// (UnmarshalJSON leaves a non-nil zero-value struct).
		// Critique skips them as malformed; the OLD executor would
		// silently fail on ReadFile(projectRoot). Make the failure
		// explicit so the operator sees a real signal — auto-pass
		// would mask incomplete work, and silent-fail looks like a
		// flaky test.
		if strings.TrimSpace(ac.ContentMatch.File) == "" {
			result.Passed = false
			// Tag this output with the GROUND-TRUTH prefix so the
			// judge-override branch in CheckAcceptanceCriteriaWithJudge
			// classifies it as non-overridable. Without that, a
			// semantic judge could rule "feature looks present in code"
			// and override the malformed-AC failure to PASS, exactly
			// the auto-pass regression codex flagged.
			result.Output = "MALFORMED-AC: content_match has no file (refine path can repair this)"
			return result
		}
		path := ac.ContentMatch.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectRoot, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			result.Passed = false
			result.Output = fmt.Sprintf("cannot read %s: %v", ac.ContentMatch.File, err)
			return result
		}
		if strings.Contains(string(data), ac.ContentMatch.Pattern) {
			result.Passed = true
			result.Output = fmt.Sprintf("pattern %q found in %s", ac.ContentMatch.Pattern, ac.ContentMatch.File)
		} else {
			result.Passed = false
			result.Output = fmt.Sprintf("pattern %q not found in %s", ac.ContentMatch.Pattern, ac.ContentMatch.File)
		}
		return result
	}

	// No verifiable check configured — pass by default (description-only criterion)
	result.Passed = true
	result.Output = "no automated check configured (manual verification)"
	return result
}

// FormatAcceptanceResults returns a human-readable summary of acceptance check results.
func FormatAcceptanceResults(results []AcceptanceResult) string {
	var b strings.Builder
	passed := 0
	for _, r := range results {
		status := "FAIL"
		if r.Passed {
			status = "PASS"
			passed++
		}
		fmt.Fprintf(&b, "  [%s] %s: %s\n", status, r.CriterionID, r.Description)
		if !r.Passed && r.Output != "" {
			// Indent output lines
			for _, line := range strings.Split(strings.TrimSpace(r.Output), "\n") {
				fmt.Fprintf(&b, "         %s\n", line)
			}
		}
	}
	fmt.Fprintf(&b, "  %d/%d criteria passed\n", passed, len(results))
	return b.String()
}
