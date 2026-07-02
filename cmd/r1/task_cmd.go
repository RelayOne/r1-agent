package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/RelayOne/r1/internal/deploy"
	"github.com/RelayOne/r1/internal/executor"
	"github.com/RelayOne/r1/internal/research"
	"github.com/RelayOne/r1/internal/router"
)

// taskCmd is the thin `r1 task "..."` entry point added by
// Track B Task 19. It classifies natural-language input via the
// router and, when a registered executor exists, hands off to it.
//
// TaskCode dispatches end-to-end through the SOW/ship pipeline via
// the CodeExecutor.ExecuteHook wired in buildDefaultTaskRouter.
// Research / browser / deploy / delegate route through the real
// executors landed in Tasks 20-22 + work-stoke TASK 2 (audit A051).
//
// Exit codes:
//
//	0 — success (classification-only mode, or a successful dispatch)
//	1 — usage error (empty input, bad flag) or executor-returned error
//	2 — classified but no executor registered (chat), or the executor
//	    reported ExecutorNotWiredError (e.g. interactive browser
//	    without the stoke_rod backend)
func taskCmd(args []string) {
	code := runTaskCmd(args, os.Stdout, os.Stderr, buildDefaultTaskRouter())
	os.Exit(code)
}

// buildDefaultTaskRouter assembles the production router with all
// current executors registered — the same constructors agent-serve
// uses (buildExecutorRegistry in agent_serve_cmd.go), so `r1 task`
// and the HTTP surface dispatch identical pipelines. Tests construct
// their own router and call runTaskCmd directly.
func buildDefaultTaskRouter() *router.Router {
	r := router.New()
	// LINT-ALLOW chdir-cli-entry: r1 task subcommand; cwd captured once and embedded in the CodeExecutor as repoRoot, never re-read at dispatch time.
	repoRoot, _ := os.Getwd()
	codeExec := executor.NewCodeExecutor(repoRoot)
	codeExec.ExecuteHook = defaultCodeExecuteHook(repoRoot)
	r.Register(executor.TaskCode, codeExec)
	// Real HTTP fetcher — same wiring as `r1 research` (research_cmd.go).
	// A zero-value ResearchExecutor has a nil Fetcher and fails every
	// Execute with "no Fetcher configured" (audit A051).
	r.Register(executor.TaskResearch, executor.NewResearchExecutor(research.NewHTTPFetcher()))
	r.Register(executor.TaskBrowser, executor.NewBrowserExecutor())
	// ProviderFly is the deploy default (deploy.Deploy applies the same
	// fallback for ProviderUnknown); constructing with it makes Execute
	// surface actionable adapter errors (missing app name) instead of
	// "unknown provider unknown" from the registry lookup.
	r.Register(executor.TaskDeploy, executor.NewDeployExecutor(deploy.DeployConfig{Provider: deploy.ProviderFly}))
	// DelegateExecutor has no production backend at the CLI yet
	// (Hirer / Delegator / Submitter / Delivery are operator config);
	// the zero value surfaces targeted "configure X" errors from
	// Execute rather than a scaffolding banner.
	r.Register(executor.TaskDelegate, &executor.DelegateExecutor{})
	return r
}

// defaultCodeExecuteHook is the production wiring between the
// `r1 task` Executor interface and the existing SOW/ship pipeline
// (cmd/r1/main.go shipCmd → runBuild → runSessionNative). The hook:
//
//  1. captures HEAD as the base commit so we can compute a unified
//     diff after the pipeline returns,
//  2. invokes shipCmd with --task <description> --repo <repoRoot>,
//     which drives plan generation + the convergence loop,
//  3. captures `git diff <base>..HEAD` as the deliverable diff.
//
// The hook intentionally calls shipCmd rather than reaching into
// app.Orchestrator.Run directly: shipCmd is the single canonical
// "run a code task end-to-end" entry point, and any improvement to
// the ship pipeline (auth pools, signatures, ctl socket, signal
// handling) is automatically inherited by `r1 task`.
//
// shipCmd uses fatal() / os.Exit on hard errors. That matches the
// `r1 task` CLI contract — `r1 task` is itself a CLI process, so a
// pipeline-level fatal exits the same process the way an `r1 ship`
// invocation would. Tests inject their own ExecuteHook directly on
// the CodeExecutor and never exercise this default closure.
func defaultCodeExecuteHook(repoRoot string) func(ctx context.Context, p executor.Plan, effort executor.EffortLevel) (executor.Deliverable, error) {
	return func(ctx context.Context, p executor.Plan, _ executor.EffortLevel) (executor.Deliverable, error) {
		desc := strings.TrimSpace(p.Task.Description)
		if desc == "" {
			desc = strings.TrimSpace(p.Query)
		}
		if desc == "" {
			return nil, errors.New("CodeExecutor: empty task description")
		}

		base := gitHeadSHA(repoRoot)

		// Hand off to the canonical ship pipeline. shipCmd parses its
		// own flag set, spins up the convergence loop, and (on hard
		// failure) calls fatal() which os.Exit(1)s the process — same
		// contract as `r1 ship`.
		shipCmd([]string{"--task", desc, "--repo", repoRoot})

		diff := gitDiffSince(ctx, repoRoot, base)
		return executor.CodeDeliverable{
			RepoRoot: repoRoot,
			Diff:     diff,
		}, nil
	}
}

// gitDiffSince returns the unified diff from base..HEAD in repoRoot.
// Empty base falls back to `git diff HEAD` (working tree vs HEAD)
// so a freshly-initialized repo with no commits still reports the
// agent's working-tree changes. Best-effort: returns "" on any git
// error so we never poison the deliverable with a stderr fragment.
func gitDiffSince(ctx context.Context, repoRoot, base string) string {
	var cmd *exec.Cmd
	if base == "" {
		cmd = exec.CommandContext(ctx, "git", "diff", "HEAD")
	} else {
		cmd = exec.CommandContext(ctx, "git", "diff", base+"..HEAD")
	}
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// runTaskCmd is the testable core of taskCmd. It takes explicit
// writers and a router so unit tests can observe output and inject
// deterministic routing.
func runTaskCmd(args []string, stdout, stderr io.Writer, r *router.Router) int {
	fs := flag.NewFlagSet("task", flag.ContinueOnError)
	fs.SetOutput(stderr)
	classifyOnly := fs.Bool("classify-only", false, "Print the classified task type and exit without dispatching.")
	typeOverride := fs.String("type", "", "Force a TaskType instead of using the classifier (code|research|browser|deploy|delegate|chat).")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: r1 task [--classify-only] [--type=<type>] \"<request>\"\n\n")
		fmt.Fprintf(stderr, "Classifies a natural-language request and routes it through the Executor interface.\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}

	input := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if input == "" {
		fs.Usage()
		return 1
	}

	// Classification — either forced via --type or computed via router.
	var tt executor.TaskType
	if *typeOverride != "" {
		tt = parseTaskType(*typeOverride)
		if tt == executor.TaskUnknown {
			fmt.Fprintf(stderr, "r1 task: unknown --type value %q\n", *typeOverride)
			return 1
		}
	} else {
		tt = r.Classify(input)
		if tt == executor.TaskUnknown {
			// The router returns TaskUnknown only for empty input, and
			// we already guarded that case above. Treat this as a
			// defensive "cannot classify" and surface exit 1.
			fmt.Fprintf(stderr, "r1 task: could not classify input\n")
			return 1
		}
	}

	if *classifyOnly {
		fmt.Fprintf(stdout, "task type: %s\n", tt.String())
		return 0
	}

	// Dispatch. When --type was passed we route through the forced
	// type by swapping in a one-shot classifier that returns the
	// operator's choice. Otherwise we use the default classifier.
	if *typeOverride != "" {
		forced := tt
		r.SetClassifier(func(_ string) executor.TaskType { return forced })
		defer r.SetClassifier(nil)
	}
	e, dispatchedType, err := r.Dispatch(input)
	if err != nil {
		if errors.Is(err, router.ErrNoExecutor) {
			fmt.Fprintf(stderr, "r1 task: classified as %s but no executor is registered\n", dispatchedType.String())
			if dispatchedType == executor.TaskChat {
				fmt.Fprintf(stderr, "hint: run `r1 chat` to start a conversation.\n")
			}
			return 2
		}
		fmt.Fprintf(stderr, "r1 task: %v\n", err)
		return 1
	}

	// Every registered executor dispatches for real (audit A051):
	// TaskCode routes through the SOW/ship pipeline via the
	// CodeExecutor.ExecuteHook; research / browser / deploy /
	// delegate route through the pipelines landed in Tasks 20-22 +
	// work-stoke TASK 2. Exit 2 is reserved for executors reporting
	// ExecutorNotWiredError (a genuinely unwired sub-path, e.g.
	// interactive browser without the stoke_rod backend).
	label := ""
	if dispatchedType == executor.TaskCode {
		label = " (SOW/ship pipeline)"
	}
	fmt.Fprintf(stdout, "task classified as %s; routing to %T%s.\n", dispatchedType.String(), e, label)
	plan := executor.Plan{
		Task: executor.Task{
			Description: input,
			TaskType:    dispatchedType,
		},
		Query: input,
	}
	deliverable, execErr := e.Execute(context.Background(), plan, executor.EffortStandard)
	if execErr != nil {
		var notWired *executor.ExecutorNotWiredError
		if errors.As(execErr, &notWired) {
			fmt.Fprintf(stdout, "hint: %v\n", notWired)
			return 2
		}
		fmt.Fprintf(stderr, "r1 task: %v\n", execErr)
		if dispatchedType == executor.TaskDeploy {
			fmt.Fprintf(stderr, "hint: `r1 deploy --provider fly --app <name>` runs the deploy pipeline with explicit config.\n")
		}
		return 1
	}
	if deliverable != nil {
		fmt.Fprintf(stdout, "deliverable: %s\n", deliverable.Summary())
	}
	return 0
}

// parseTaskType maps a user-supplied --type flag value to the
// TaskType enum. Returns TaskUnknown for unrecognized input.
func parseTaskType(s string) executor.TaskType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "code":
		return executor.TaskCode
	case "research":
		return executor.TaskResearch
	case "browser":
		return executor.TaskBrowser
	case "deploy":
		return executor.TaskDeploy
	case "delegate":
		return executor.TaskDelegate
	case "chat":
		return executor.TaskChat
	default:
		return executor.TaskUnknown
	}
}
