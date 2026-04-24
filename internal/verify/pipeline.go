package verify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ericmacdougall/stoke/internal/env"
	"github.com/ericmacdougall/stoke/internal/failure"
)

// Outcome is the result of one verification step.
type Outcome struct {
	Name    string
	Skipped bool
	Success bool
	Output  string
}

// Pipeline runs build, test, lint, and scope checks in a worktree.
type Pipeline struct {
	buildCmd string
	testCmd  string
	lintCmd  string

	// Optional execution environment. When set, commands run via env.Exec
	// instead of os/exec on the host.
	environ env.Environment
	handle  *env.Handle
}

// NewPipeline creates a verification pipeline.
func NewPipeline(buildCmd, testCmd, lintCmd string) *Pipeline {
	return &Pipeline{buildCmd: buildCmd, testCmd: testCmd, lintCmd: lintCmd}
}

// WithEnvironment returns a copy of the pipeline configured to run commands
// in the given execution environment instead of directly on the host.
func (p *Pipeline) WithEnvironment(e env.Environment, h *env.Handle) *Pipeline {
	return &Pipeline{
		buildCmd: p.buildCmd,
		testCmd:  p.testCmd,
		lintCmd:  p.lintCmd,
		environ:  e,
		handle:   h,
	}
}

// Commands returns the configured build, test, and lint commands.
func (p *Pipeline) Commands() (build, test, lint string) {
	return p.buildCmd, p.testCmd, p.lintCmd
}

// Run executes all verification steps. Returns outcomes and an error if any step failed.
// When an execution environment is configured via WithEnvironment, commands run
// inside that environment. Otherwise they run directly on the host via os/exec.
func (p *Pipeline) Run(ctx context.Context, dir string) ([]Outcome, error) {
	outcomes := make([]Outcome, 0, 3)
	var hadFailure bool
	for _, item := range []struct {
		name string
		cmd  string
	}{{"build", p.buildCmd}, {"test", p.testCmd}, {"lint", p.lintCmd}} {
		if strings.TrimSpace(item.cmd) == "" {
			outcomes = append(outcomes, Outcome{Name: item.name, Skipped: true, Success: true, Output: "no command configured"})
			continue
		}

		var outcome Outcome
		if p.environ != nil && p.handle != nil {
			outcome = p.runInEnv(ctx, item.name, item.cmd)
		} else {
			outcome = p.runLocal(ctx, item.name, item.cmd, dir)
		}
		outcomes = append(outcomes, outcome)
		if !outcome.Success {
			hadFailure = true
		}
	}
	if hadFailure {
		return outcomes, fmt.Errorf("verification failed")
	}
	return outcomes, nil
}

// runLocal executes a command directly on the host.
func (p *Pipeline) runLocal(ctx context.Context, name, cmdStr, dir string) Outcome {
	cmd := exec.CommandContext(ctx, "bash", "-lc", cmdStr) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return Outcome{Name: name, Success: err == nil, Output: string(out)}
}

// runInEnv executes a command inside the configured execution environment.
func (p *Pipeline) runInEnv(ctx context.Context, name, cmdStr string) Outcome {
	result, err := p.environ.Exec(ctx, p.handle, []string{"bash", "-lc", cmdStr}, env.ExecOpts{})
	if err != nil {
		return Outcome{Name: name, Success: false, Output: fmt.Sprintf("env exec error: %v", err)}
	}
	return Outcome{Name: name, Success: result.Success(), Output: result.CombinedOutput()}
}

// AnalyzeOutcomes converts verification outcomes into a failure analysis.
// Returns nil if all steps passed. diffSummary is used for policy violation
// scanning against the actual code diff rather than tool output.
func AnalyzeOutcomes(outcomes []Outcome, diffSummary ...string) *failure.Analysis {
	var buildOut, testOut, lintOut string
	allPassed := true
	for _, o := range outcomes {
		if o.Skipped {
			continue
		}
		if !o.Success {
			allPassed = false
		}
		switch o.Name {
		case "build":
			if !o.Success {
				buildOut = o.Output
			}
		case "test":
			if !o.Success {
				testOut = o.Output
			}
		case "lint":
			if !o.Success {
				lintOut = o.Output
			}
		}
	}
	if allPassed {
		return nil
	}
	a := failure.Analyze(buildOut, testOut, lintOut, diffSummary...)
	return &a
}

// HasCommands returns true if at least one verification command is configured.
func (p *Pipeline) HasCommands() bool {
	return strings.TrimSpace(p.buildCmd) != "" ||
		strings.TrimSpace(p.testCmd) != "" ||
		strings.TrimSpace(p.lintCmd) != ""
}

// CheckProtectedFiles returns any modified files that match protected patterns.
// Protected patterns support trailing / for directories and leading * for wildcards.
func CheckProtectedFiles(modifiedFiles []string, protectedPatterns []string) []string {
	var violations []string
	for _, file := range modifiedFiles {
		for _, pattern := range protectedPatterns {
			if matchProtected(file, pattern) {
				violations = append(violations, file)
				break
			}
		}
	}
	return violations
}

func matchProtected(file, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}

	// Directory pattern: ".claude/" matches any file under .claude/
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(file, pattern) || strings.HasPrefix(file, strings.TrimSuffix(pattern, "/")+"/")
	}

	// Wildcard prefix: ".env*" matches .env, .env.local, .env.production
	if strings.HasPrefix(pattern, ".") && strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(file, prefix)
	}

	// Exact match
	return file == pattern
}

// CheckScope returns files that were modified but are NOT in the allowed scope.
// Scope semantics (must match worktree.ScopeCheck):
//   - Exact file paths match exactly: "src/auth/middleware.ts"
//   - Directory grants MUST end with "/": "src/auth/" allows any file under src/auth/
//   - A file entry like "src/auth/middleware.ts" does NOT grant access to other files in src/auth/
func CheckScope(modifiedFiles []string, allowedFiles []string) []string {
	if len(allowedFiles) == 0 {
		return nil // no scope restriction
	}
	exactFiles := make(map[string]bool)
	var dirPrefixes []string
	for _, f := range allowedFiles {
		if strings.HasSuffix(f, "/") {
			dirPrefixes = append(dirPrefixes, f)
		} else {
			exactFiles[f] = true
		}
	}
	var violations []string
	for _, f := range modifiedFiles {
		if exactFiles[f] {
			continue
		}
		inDir := false
		for _, prefix := range dirPrefixes {
			if strings.HasPrefix(f, prefix) {
				inDir = true
				break
			}
		}
		if !inDir {
			violations = append(violations, f)
		}
	}
	return violations
}
