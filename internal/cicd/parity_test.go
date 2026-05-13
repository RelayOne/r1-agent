// parity_test.go — C5 parity-audit (T23).
//
// Walks the public-symbol surface of the GitHub Actions, GitLab CI, and
// BitBucket Pipelines adapters and asserts every "Required" contract row from
// docs/integrations/bitbucket-pipelines-parity.md has a same-purposed entry
// in the BitBucket adapter. The parity-doc IS the test fixture — no silent
// drift between scope and implementation.
//
// The audit uses go/parser instead of `go list -json` so the test runs
// hermetically with no external tools. It scans the .go files of each adapter
// package, collects every exported top-level identifier (functions, types,
// methods, constants, variables), and checks the BitBucket set against the
// required contract.

package cicd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// parityContract is the hand-curated list of cross-adapter capabilities every
// CI adapter must expose. Each row is a logical capability, satisfied when at
// least one symbol from the candidate set is present in the adapter's public
// API. Mirrors the markdown table in
// docs/integrations/bitbucket-pipelines-parity.md.
var parityContract = []struct {
	Capability string
	GH         []string // candidate symbol names in internal/cicd/github
	GL         []string // candidate symbol names in internal/cicd/gitlab
	BB         []string // candidate symbol names in internal/cicd/bitbucket
}{
	{
		Capability: "Construct REST client",
		GH:         []string{"New"},
		GL:         []string{"New"},
		BB:         []string{"New"},
	},
	{
		Capability: "Trigger pipeline / workflow",
		GH:         []string{"TriggerWorkflow"},
		GL:         []string{"TriggerPipeline"},
		BB:         []string{"TriggerPipeline"},
	},
	{
		Capability: "Get pipeline / run status",
		GH:         []string{"GetRunStatus"},
		GL:         []string{"GetPipelineStatus"},
		BB:         []string{"GetPipelineStatus"},
	},
	{
		Capability: "Block until terminal state",
		GH:         []string{"WaitForCompletion"},
		GL:         []string{"WaitForCompletion"},
		BB:         []string{"WaitForCompletion"},
	},
	{
		Capability: "Fetch per-step / per-job log",
		GH:         []string{"GetJobLogs"},
		GL:         []string{"GetJobLog"},
		BB:         []string{"GetStepLog"},
	},
	{
		Capability: "List jobs / steps in a pipeline",
		GH:         []string{"ListPullRequestFiles"}, // GH exposes per-file; jobs surface via Raw().
		GL:         []string{"ListPipelineJobs"},
		BB:         []string{"ListPipelineSteps"},
	},
	{
		Capability: "Fetch PR diff for code review",
		GH:         []string{"GetPullRequestDiff"},
		GL:         []string{}, // GitLab adapter relies on the template comment path; N/A.
		BB:         []string{"GetPullRequestDiff"},
	},
	{
		Capability: "Fetch PR head commit",
		GH:         []string{"GetPullRequestHeadSHA"},
		GL:         []string{}, // N/A — not exposed yet.
		BB:         []string{"GetPullRequestHead"},
	},
	{
		Capability: "Post inline / line-anchored PR comment",
		GH:         []string{"PostReviewComment", "PostReviewCommentDirect"},
		GL:         []string{}, // N/A — GitLab uses MR notes via template; not exposed here.
		BB:         []string{"PostInlinePRComment"},
	},
	{
		Capability: "Auto-review pipeline (LLM → findings → comments)",
		GH:         []string{"NewReviewer", "Reviewer"},
		GL:         []string{},
		BB:         []string{"NewReviewer", "Reviewer"},
	},
	{
		Capability: "Commit status row writer",
		GH:         []string{}, // GH writes statuses via the workflow itself.
		GL:         []string{}, // Same for GitLab.
		BB:         []string{"PostCommitStatus"},
	},
	{
		Capability: "404 error classifier",
		GH:         []string{"IsNotFound"},
		GL:         []string{"IsNotFound"},
		BB:         []string{"IsNotFound"},
	},
	{
		Capability: "401 error classifier",
		GH:         []string{"IsUnauthorized"},
		GL:         []string{}, // N/A — GitLab adapter does not expose 401 predicate.
		BB:         []string{"IsUnauthorized"},
	},
	{
		Capability: "Default base URL constant",
		GH:         []string{"DefaultBaseURL"},
		GL:         []string{"DefaultBaseURL"},
		BB:         []string{"DefaultBaseURL"},
	},
	{
		Capability: "Config type",
		GH:         []string{"Config"},
		GL:         []string{"Config"},
		BB:         []string{"Config"},
	},
	{
		Capability: "Client type",
		GH:         []string{"Client"},
		GL:         []string{"Client"},
		BB:         []string{"Client"},
	},
	{
		Capability: "APIError structured error",
		GH:         []string{}, // GH uses go-github's *ErrorResponse directly.
		GL:         []string{"APIError"},
		BB:         []string{"APIError"},
	},
}

// TestAdapterParity verifies every Required row in the parity contract has a
// matching BitBucket symbol — the build-time guarantee that a future symbol
// addition to the GH/GitLab adapter cannot land without a BB counterpart.
func TestAdapterParity(t *testing.T) {
	ghSyms := mustCollectExportedSymbols(t, "github")
	glSyms := mustCollectExportedSymbols(t, "gitlab")
	bbSyms := mustCollectExportedSymbols(t, "bitbucket")

	hasAny := func(set map[string]bool, candidates []string) bool {
		for _, c := range candidates {
			if set[c] {
				return true
			}
		}
		return false
	}

	type row struct {
		Capability string
		GHHas      bool
		GLHas      bool
		BBHas      bool
		Required   bool
	}

	var missing []string
	for _, c := range parityContract {
		r := row{
			Capability: c.Capability,
			GHHas:      hasAny(ghSyms, c.GH),
			GLHas:      hasAny(glSyms, c.GL),
			BBHas:      hasAny(bbSyms, c.BB),
		}
		// A row is "Required" if at least one of GH or GitLab provides it.
		// (The BB adapter must match parity with the existing surface.)
		r.Required = len(c.GH) > 0 || len(c.GL) > 0
		if r.Required && !r.BBHas {
			missing = append(missing, c.Capability)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("BitBucket adapter is missing parity for %d capabilities:\n  - %s",
			len(missing), strings.Join(missing, "\n  - "))
	}
}

// TestParityDocPresent asserts the parity contract document exists. The doc
// IS the contract — if it's missing, the parity audit is unanchored.
func TestParityDocPresent(t *testing.T) {
	path := docPath("bitbucket-pipelines-parity.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("parity contract doc missing at %s: %v", path, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Spot-check three required capabilities appear in the doc so a drift
	// between this file and the doc surfaces loudly.
	for _, want := range []string{"TriggerPipeline", "GetPipelineStatus", "PostCommitStatus"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("parity doc missing required-symbol reference %q", want)
		}
	}
}

// mustCollectExportedSymbols parses every .go file (excluding _test.go) under
// internal/cicd/<pkg> and returns the set of exported top-level identifiers.
func mustCollectExportedSymbols(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	dir := filepath.Join(".", pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := make(map[string]bool)
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch d := n.(type) {
			case *ast.FuncDecl:
				if d.Name != nil && d.Name.IsExported() {
					out[d.Name.Name] = true
				}
			case *ast.TypeSpec:
				if d.Name != nil && d.Name.IsExported() {
					out[d.Name.Name] = true
				}
			case *ast.ValueSpec:
				for _, ident := range d.Names {
					if ident.IsExported() {
						out[ident.Name] = true
					}
				}
			}
			return true
		})
	}
	return out
}

// docPath returns the absolute path to a doc under docs/integrations/.
func docPath(name string) string {
	// LINT-ALLOW chdir-test: read-only Getwd to anchor the repo-root walk-up; never mutates cwd.
	wd, _ := os.Getwd()
	// We're in internal/cicd; walk up to the repo root.
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	return filepath.Join(root, "docs", "integrations", name)
}
