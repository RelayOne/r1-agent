package plan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// AcceptanceResult is the outcome of checking one acceptance criterion.
type AcceptanceResult struct {
	CriterionID string
	Description string
	Passed      bool
	Output      string // command output or diagnostic message
}

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
	ensureWorkspaceInstalled(ctx, projectRoot)

	var results []AcceptanceResult
	allPassed := true

	for _, ac := range criteria {
		result := checkOneCriterion(ctx, projectRoot, ac)
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
		cmd := exec.CommandContext(ctx, bin, args...)
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
	if ac.Command != "" {
		cmd := exec.CommandContext(ctx, "bash", "-lc", ac.Command)
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

	// Content match check
	if ac.ContentMatch != nil {
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
