package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/RelayOne/r1/internal/executor"
	"github.com/RelayOne/r1/internal/router"
)

// taskCmd is the thin `stoke task "..."` entry point added by
// Track B Task 19. It classifies natural-language input via the
// router and, when a registered executor exists, hands off to it.
//
// For this MVP commit TaskCode routes back to `stoke ship` (see the
// CodeExecutor default-fallback error), and the remaining types are
// registered with scaffolding executors that return a not-wired
// sentinel. Exit codes:
//
//	0 — success (classification-only mode, or future direct dispatch)
//	1 — usage error (empty input, bad flag)
//	2 — classified + dispatched but executor not wired yet
func taskCmd(args []string) {
	code := runTaskCmd(args, os.Stdout, os.Stderr, buildDefaultTaskRouter())
	os.Exit(code)
}

// buildDefaultTaskRouter assembles the production router with all
// current executors registered. Tests construct their own router
// and call runTaskCmd directly.
func buildDefaultTaskRouter() *router.Router {
	r := router.New()
	// TaskCode routes through the CodeExecutor fallback — prints
	// the operator-friendly "use `stoke ship`" message.
	repoRoot, _ := os.Getwd()
	r.Register(executor.TaskCode, executor.NewCodeExecutor(repoRoot))
	r.Register(executor.TaskResearch, &executor.ResearchExecutor{})
	r.Register(executor.TaskBrowser, &executor.BrowserExecutor{})
	r.Register(executor.TaskDeploy, &executor.DeployExecutor{})
	r.Register(executor.TaskDelegate, &executor.DelegateExecutor{})
	return r
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
		fmt.Fprintf(stderr, "usage: stoke task [--classify-only] [--type=<type>] \"<request>\"\n\n")
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
			fmt.Fprintf(stderr, "stoke task: unknown --type value %q\n", *typeOverride)
			return 1
		}
	} else {
		tt = r.Classify(input)
		if tt == executor.TaskUnknown {
			// The router returns TaskUnknown only for empty input, and
			// we already guarded that case above. Treat this as a
			// defensive "cannot classify" and surface exit 1.
			fmt.Fprintf(stderr, "stoke task: could not classify input\n")
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
			fmt.Fprintf(stderr, "stoke task: classified as %s but no executor is registered\n", dispatchedType.String())
			return 2
		}
		fmt.Fprintf(stderr, "stoke task: %v\n", err)
		return 1
	}

	// MVP: print the classification + executor binding and exit 2
	// with a pointer at the real entry points. Track B follow-up
	// commits replace this with an actual Execute call + descent.
	fmt.Fprintf(stdout, "task classified as %s; routing to %T — direct dispatch not yet wired.\n", dispatchedType.String(), e)
	switch dispatchedType {
	case executor.TaskCode:
		fmt.Fprintf(stdout, "hint: run `stoke ship --task \"%s\"` to execute the code pipeline today.\n", input)
	case executor.TaskChat:
		fmt.Fprintf(stdout, "hint: run `stoke chat` to start a conversation.\n")
	case executor.TaskDeploy:
		fmt.Fprintf(stdout, "hint: run `stoke deploy --provider fly --app <name>` to run the fly.io deploy pipeline.\n")
	case executor.TaskUnknown, executor.TaskResearch, executor.TaskBrowser, executor.TaskDelegate:
		fmt.Fprintf(stdout, "hint: this executor is scaffolding; real pipeline lands in a follow-up Track B task.\n")
	default:
		fmt.Fprintf(stdout, "hint: this executor is scaffolding; real pipeline lands in a follow-up Track B task.\n")
	}
	return 2
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
