// verify_lint_wiring.go — STATUS: PARTIAL — see plans/HANDOFF.md.
//
// Spec 8 §12 item 39 calls for wiring the lint-view-without-api scanner
// into the r1.verify.lint MCP tool so the same FAILs/WARNs surface in
// the agentic-driven verify path AND in CI:
//
//   > Wire the lint into r1.verify.lint so the MCP tool reports the
//   > same failures the CI does.
//
// The r1.verify.lint handler lives with the daemon (cmd/r1d / internal/
// r1d, ships with spec 5 r1d-server). The handler must invoke
// `tools/lint-view-without-api` (either by spawning the binary or by
// calling its run() function in-process) and translate the resulting
// Findings into the Slack-style envelope from envelope.go.
//
// The recommended invocation (when spec 5 lands) is:
//
//   import lint "github.com/RelayOne/r1/tools/lint-view-without-api"
//   findings := lint.RunInProcess(repoRoot, catalogPath, allowlistPath)
//   if hasFails(findings) {
//       return ErrEnvelope("r1.verify.lint", "validation",
//           "lint-view-without-api FAIL findings",
//           "r1.verify.build", "r1.verify.test")
//   }
//   return OKEnvelope("r1.verify.lint", findings)
//
// The pre-spec-5 surface declared here is a single helper that returns
// the canonical command line — the daemon shell can shell out to it
// once item 5 merges, and the next pass refactors to in-process.
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
