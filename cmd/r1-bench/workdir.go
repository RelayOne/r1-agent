// workdir.go — mission workspace preparation for the RunOne pipeline.
package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// prepareWorkDir makes workDir a git repository with a sealed baseline
// commit so that (a) end-of-run diff capture works — agents.GitDiff
// needs a repo to diff against, and without one every real run scored
// as an empty diff — and (b) the graded tree carries no upstream
// history for the agent to mine (the reward-hacking auditor's
// `git log` vector, SOTA gap #5).
//
// A directory already inside a git work tree is left untouched: a
// caller-supplied --workdir pointing at a real checkout is the
// caller's to manage.
func prepareWorkDir(workDir string) error {
	check := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	check.Dir = workDir
	if check.Run() == nil {
		return nil
	}
	// Identity is set per-command (not --global) so the bench never
	// touches the operator's git config.
	ident := []string{"-c", "user.name=r1-bench", "-c", "user.email=bench@r1-bench.invalid"}
	steps := [][]string{
		{"init", "-q"},
		append(append([]string{}, ident...), "add", "-A"),
		append(append([]string{}, ident...), "commit", "-q", "--allow-empty", "-m", "r1-bench baseline"),
	}
	for _, args := range steps {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("prepareWorkDir: git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
