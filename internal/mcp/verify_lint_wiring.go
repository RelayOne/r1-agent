// verify_lint_wiring.go — canonical invocation recipe for the
// r1.verify.lint MCP tool.
//
// The handler is implemented in r1_verify.go and dispatched via
// HandleToolCall (r1.verify.* prefix). This file exposes only the
// canonical command + description — kept separate so the CI
// Makefile target (`make lint-views`) and the daemon shell-out
// agree on the invocation. Drift between the two is the §10a
// "Tool catalog vs UI drift" failure mode in disguise.
//
// In-process refactor (importing tools/lint-view-without-api as a
// library rather than spawning the binary) is a follow-up once the
// tool is split into a package — currently it's `package main`.
package mcp

// LintViewWithoutAPICommand returns the canonical command line (argv0
// + args) the r1.verify.lint handler must invoke once spec 5 lands.
// Centralizing the recipe here keeps the wire surface (this file) and
// the CI Makefile target (make lint-views) pointed at the same
// invocation; drift between the two is the §10a "Tool catalog vs UI
// drift" failure mode in disguise.
func LintViewWithoutAPICommand() []string {
	return []string{
		"go", "run",
		"./tools/lint-view-without-api",
		"--root", ".",
		"--json",
	}
}

// LintViewWithoutAPIDescription is the human-readable explanation the
// r1.verify.lint handler attaches to the envelope's Links.Related so
// agents can self-document why the lint exists.
const LintViewWithoutAPIDescription = "lint-view-without-api enforces spec 8 §8: every interactive UI " +
	"component must have a data-testid AND a matching r1.* MCP tool reference. " +
	"See specs/agentic-test-harness.md §8 + §12 items 35-39."
