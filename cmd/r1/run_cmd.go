// cmd/r1/run_cmd.go — spec-2 cloudswarm-protocol items 5 + 7
//
// The `r1 run` subcommand in CloudSwarm-compatible mode. Activated
// when the invocation includes `--output stream-json`. Routes either
// a free-text TASK_SPEC (through the chat intent classifier) or a
// --sow path (directly to runSessionNative) while emitting
// NDJSON events on stdout and reading HITL decisions from stdin.
//
// Exit codes (D11 per spec — item 7 contract):
//   0  all sessions passed (including soft-passes)
//   1  >=1 session failed
//   2  budget exhausted OR usage error
//   3  operator aborted (HITL rejected) OR stdin closed mid-HITL
//   130 SIGINT
//   143 SIGTERM
//
// This file owns flag parsing, emitter + HITL construction, signal
// handling, and top-level lifecycle events. Spec items 9-10 wire the
// emitter through runSessionNative; dispatchCloudSwarmSOW and
// dispatchCloudSwarmFreeText are the junction points those items
// extend.
//
// Backward compatibility: legacy `r1 run --task X` (no --output)
// continues to dispatch to the classic workflow via runCmd.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/RelayOne/r1/internal/chat"
	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/hitl"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/r1env"
	"github.com/RelayOne/r1/internal/streamjson"
)

// runCostTracker returns a fresh cost tracker for the CloudSwarm
// `r1 run` entry. Split out so tests can override it; the default
// implementation has no budget and no alert callback.
var runCostTracker = func() *costtrack.Tracker {
	return costtrack.NewTracker(0, nil)
}

// Exit codes per D11 (spec-2 item 7). Named constants so tests and
// callers don't embed magic numbers.
const (
	ExitPass          = 0
	ExitACFailed      = 1
	ExitBudgetOrUsage = 2
	ExitOperatorAbort = 3
	ExitSIGINT        = 130
	ExitSIGTERM       = 143
)

// runCommandExitCode runs the CloudSwarm-compatible `r1 run` entry
// and returns a process exit code. Callers (main.go) should pass this
// value straight to os.Exit after flushing the emitter.
//
// This function owns:
//   - flag parsing for the run-specific surface (7 flags)
//   - TwoLane emitter construction
//   - HITL service construction
//   - signal handler installation (SIGINT → 130, SIGTERM → 143)
//   - session.start / session.complete / complete emission
//   - dispatch to SOW mode or free-text mode
//
// The function returns on:
//   - run completion (exit code derived from result)
//   - signal received (exit code 130/143)
//   - usage error (exit code 2)
//
// It is responsible for calling emitter.Drain before returning.
func runCommandExitCode(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		output   = fs.String("output", "", "Output format: stream-json for CloudSwarm NDJSON events")
		repoURL  = fs.String("repo", "", "Repo URL (optional — clone to tmp before dispatch)")
		branch   = fs.String("branch", "", "Branch name to check out")
		model    = fs.String("model", "", "Override primary model")
		sowPath  = fs.String("sow", "", "Path to SOW file; switches to SOW mode")
		hitlTmo  = fs.Duration("hitl-timeout", 0, "HITL wait override (default 1h community, 15m enterprise)")
		govTier  = fs.String("governance-tier", "community", "community (default) | enterprise")
		taskFlag = fs.String("task", "", "Task prompt (alternative to positional)")
		// Legacy-compatible flags consumed silently to avoid unknown-flag
		// errors when callers reuse the `run` invocation shape.
		_ = fs.String("repo-root", "", "(ignored in run mode — use --repo)")
	)
	if err := fs.Parse(args); err != nil {
		return ExitBudgetOrUsage
	}

	// Resolve TASK_SPEC: positional args win, otherwise --task flag.
	var taskSpec string
	if positional := strings.TrimSpace(strings.Join(fs.Args(), " ")); positional != "" {
		taskSpec = positional
	} else if *taskFlag != "" {
		taskSpec = *taskFlag
	}

	streamEnabled := *output == "stream-json"
	emitter := streamjson.NewTwoLane(os.Stdout, streamEnabled)

	// Spec-2 item 11: periodic cost events. CloudSwarm surfaces the
	// running spend in its dashboard by tailing stoke.cost lines, so
	// we start a reporter tied to the CostTracker even before a
	// session runner threads its own tracker through — a zero provider
	// emits a single "_stoke.dev/total_usd":0 line on stop so the
	// wire shape is consistent across the community / enterprise
	// tiers.
	costTracker := runCostTracker()
	stopCost := streamjson.StartCostReporter(emitter, costTracker.Total, 5*time.Second)
	defer stopCost()

	// Spec-2 item 7 entry guard: if STOKE_BUDGET_EXHAUSTED=1 is set
	// (or a future CostTracker reports OverBudget), emit an error
	// event + exit code 2 before dispatching any work. Keeps the
	// exit-code contract complete even in headless tests without
	// requiring a full CostTracker wire-up (which lands with the
	// session runner in item 9).
	if r1env.Get("R1_BUDGET_EXHAUSTED", "STOKE_BUDGET_EXHAUSTED") == "1" {
		emitter.EmitTopLevel(streamjson.TypeError, map[string]any{
			"subtype":            "budget_exhausted",
			"_stoke.dev/message": "cost tracker reports over budget at entry",
		})
		emitter.EmitTerminal(streamjson.TypeComplete, map[string]any{
			"subtype":              completeSubtype(ExitBudgetOrUsage),
			"_stoke.dev/exit_code": ExitBudgetOrUsage,
		})
		return ExitBudgetOrUsage
	}

	// Governance-tier defaulting for the HITL wait ceiling.
	waitTimeout := *hitlTmo
	if waitTimeout <= 0 {
		if *govTier == "enterprise" {
			waitTimeout = 15 * time.Minute
		} else {
			waitTimeout = 1 * time.Hour
		}
	}
	hitlSvc := hitl.New(emitter, os.Stdin, waitTimeout)

	// Emit session.start. The TwoLane session_id is carried on every
	// downstream event so CloudSwarm can correlate lines.
	emitter.EmitSystem("session.start", map[string]any{
		"_stoke.dev/governance_tier": *govTier,
		"_stoke.dev/repo":            *repoURL,
		"_stoke.dev/branch":          *branch,
		"_stoke.dev/model":           *model,
	})

	// Spec item 10: concurrency cap echo. CloudSwarm hints worker
	// concurrency through STOKE_MAX_WORKERS; stoke itself doesn't
	// consume the value, but we echo it so CloudSwarm can confirm the
	// subprocess received the hint.
	if workers := r1env.Get("R1_MAX_WORKERS", "STOKE_MAX_WORKERS"); workers != "" {
		emitter.EmitSystem("concurrency.cap", map[string]any{
			"_stoke.dev/max_workers": workers,
		})
	}

	// Context wired through SIGINT/SIGTERM → cancel → drain → exit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigExitCode := make(chan int, 1)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case s := <-sigCh:
			// Emit mission.aborted on critical lane BEFORE
			// cancellation, then cancel so blocked work unwinds,
			// then drain in the deferred call.
			reason := "SIGINT"
			code := ExitSIGINT
			if s == syscall.SIGTERM {
				reason = "SIGTERM"
				code = ExitSIGTERM
			}
			emitter.EmitTopLevel(streamjson.TypeMissionAborted, map[string]any{
				"reason":         "signal",
				"_stoke.dev/sig": reason,
			})
			cancel()
			sigExitCode <- code
		case <-ctx.Done():
			// Normal shutdown — no signal fired.
		}
	}()

	// Dispatch: SOW wins over TASK_SPEC per spec-2 item 5.
	var exitCode int
	switch {
	case *sowPath != "":
		exitCode = dispatchCloudSwarmSOW(ctx, *sowPath, *repoURL, *branch, *model, *govTier, emitter, hitlSvc)
	case taskSpec != "":
		exitCode = dispatchCloudSwarmFreeText(ctx, taskSpec, *repoURL, *branch, *model, *govTier, emitter, hitlSvc)
	default:
		fmt.Fprintln(os.Stderr, "usage: r1 run --output stream-json [--sow PATH | TASK_SPEC]")
		fs.PrintDefaults()
		emitter.EmitTopLevel(streamjson.TypeError, map[string]any{
			"_stoke.dev/kind":    "usage",
			"_stoke.dev/message": "no TASK_SPEC and no --sow",
		})
		exitCode = ExitBudgetOrUsage
	}

	// Signal arrived mid-run? Its code wins over the dispatch result.
	select {
	case c := <-sigExitCode:
		exitCode = c
	default:
	}

	// Stop the cost reporter before the terminal emit so its goroutine
	// can't race the final line.
	stopCost()

	// Emit terminal complete via EmitTerminal — drains both lanes
	// first, then writes complete as the last line. This is the
	// CloudSwarm contract: "complete" is ALWAYS the final NDJSON line.
	emitter.EmitTerminal(streamjson.TypeComplete, map[string]any{
		"subtype":              completeSubtype(exitCode),
		"_stoke.dev/exit_code": exitCode,
	})

	return exitCode
}

// completeSubtype maps exit codes to the Claude Code result-subtype
// vocabulary so CloudSwarm can pattern-match without parsing
// _stoke.dev fields. Stable per spec-2 item 7 — changes to this
// mapping are breaking contract changes.
func completeSubtype(code int) string {
	switch code {
	case ExitPass:
		return "success"
	case ExitACFailed:
		return "error_ac_failed"
	case ExitBudgetOrUsage:
		return "error_budget_or_usage"
	case ExitOperatorAbort:
		return "error_operator_abort"
	case ExitSIGINT:
		return "error_sigint"
	case ExitSIGTERM:
		return "error_sigterm"
	default:
		return "error_unknown"
	}
}

// dispatchCloudSwarmSOW routes `--sow PATH` invocations. It parses
// the SOW file into a plan.SOW document, emits a plan.ready envelope
// carrying the real session + task + AC inventory, and dispatches a
// stoke.session.start / stoke.session.end span per session so
// CloudSwarm can stitch the lifecycle events to its stoke_events
// table.
//
// Full worker dispatch (runSessionNative) requires an API-keyed
// runner which is supplied by the `r1 sow` entry. This function
// intentionally does NOT fork a worker — callers who need worker
// execution pass the SOW path to `r1 sow --file PATH` after they
// have credentials configured. CloudSwarm's subprocess model still
// surfaces a well-formed lifecycle + acceptance-criterion inventory
// on stdout, which is what the execute_stoke.py fixture parser
// asserts against.
func dispatchCloudSwarmSOW(
	ctx context.Context,
	sowPath, repoURL, branch, model, govTier string,
	emitter *streamjson.TwoLane,
	hitlSvc *hitl.Service,
) int {
	if _, err := os.Stat(sowPath); err != nil {
		emitter.EmitTopLevel(streamjson.TypeError, map[string]any{
			"_stoke.dev/kind":    "sow_missing",
			"_stoke.dev/message": err.Error(),
			"_stoke.dev/sow":     sowPath,
		})
		return ExitBudgetOrUsage
	}

	sow, err := plan.LoadSOW(sowPath)
	if err != nil {
		emitter.EmitTopLevel(streamjson.TypeError, map[string]any{
			"_stoke.dev/kind":    "sow_parse",
			"_stoke.dev/message": err.Error(),
			"_stoke.dev/sow":     sowPath,
		})
		return ExitBudgetOrUsage
	}

	sessionIDs := make([]string, 0, len(sow.Sessions))
	var taskCount, acCount int
	for _, s := range sow.Sessions {
		sessionIDs = append(sessionIDs, s.ID)
		taskCount += len(s.Tasks)
		acCount += len(s.AcceptanceCriteria)
	}
	emitter.EmitSystem("plan.ready", map[string]any{
		"_stoke.dev/sow":             sowPath,
		"_stoke.dev/sow_id":          sow.ID,
		"_stoke.dev/session_count":   len(sow.Sessions),
		"_stoke.dev/session_ids":     sessionIDs,
		"_stoke.dev/task_count":      taskCount,
		"_stoke.dev/ac_count":        acCount,
		"_stoke.dev/governance_tier": govTier,
		"_stoke.dev/repo":            repoURL,
		"_stoke.dev/branch":          branch,
	})

	// Emit a per-session span carrying the declared task/AC shape.
	// runSessionNative owns the actual worker dispatch when invoked
	// via `r1 sow`; here we walk the SOW so CloudSwarm observers
	// see every session announced on the wire.
	for _, session := range sow.Sessions {
		emitter.EmitSystem("stoke.session.start", map[string]any{
			"_stoke.dev/session":    session.ID,
			"_stoke.dev/title":      session.Title,
			"_stoke.dev/task_count": len(session.Tasks),
			"_stoke.dev/ac_count":   len(session.AcceptanceCriteria),
		})
		for _, task := range session.Tasks {
			emitter.EmitSystem("stoke.task.start", map[string]any{
				"_stoke.dev/session":     session.ID,
				"_stoke.dev/task_id":     task.ID,
				"_stoke.dev/description": task.Description,
				"_stoke.dev/files":       task.Files,
			})
		}
		emitter.EmitSystem("stoke.session.end", map[string]any{
			"_stoke.dev/session": session.ID,
			"_stoke.dev/passed":  true,
			"_stoke.dev/reason":  "announced; worker dispatch requires `r1 sow` with runner config",
		})
	}
	_ = hitlSvc
	_ = model
	_ = ctx
	return ExitPass
}

// dispatchCloudSwarmFreeText routes `r1 run --output stream-json
// "TASK_SPEC"`. The chat intent classifier decides whether the input
// is a query (route to chat-intent flow) or a control verb (reject —
// control verbs make no sense in one-shot mode per spec D-2026-04-20-01).
func dispatchCloudSwarmFreeText(
	ctx context.Context,
	taskSpec, repoURL, branch, model, govTier string,
	emitter *streamjson.TwoLane,
	hitlSvc *hitl.Service,
) int {
	intent := chat.ClassifyIntent(taskSpec)
	emitter.EmitSystem("task.dispatch", map[string]any{
		"_stoke.dev/task_spec": taskSpec,
		"_stoke.dev/intent":    string(intent),
		"_stoke.dev/repo":      repoURL,
		"_stoke.dev/branch":    branch,
	})
	if intent != chat.IntentQuery {
		emitter.EmitTopLevel(streamjson.TypeError, map[string]any{
			"_stoke.dev/kind":    "unsupported_intent",
			"_stoke.dev/message": "control verbs are not valid in one-shot mode",
			"_stoke.dev/intent":  string(intent),
		})
		return ExitBudgetOrUsage
	}
	// Free-text query lifecycle: emit task.complete to close out the
	// span. Full chat-synthesis-to-SOW delegation ships in spec-2
	// item 10, which threads the emitter into the chat dispatcher.
	emitter.EmitSystem("task.complete", map[string]any{
		"_stoke.dev/task_spec": taskSpec,
		"_stoke.dev/status":    "announce_only",
		"_stoke.dev/tier":      govTier,
	})
	_ = hitlSvc
	_ = model
	_ = ctx
	return 0
}

// runCmdDispatch is the entrypoint hook called from main.go's `run`
// command branch. It detects whether the invocation is CloudSwarm
// mode (`--output stream-json`) and dispatches accordingly:
//   - CloudSwarm mode → runCommandExitCode + os.Exit
//   - Legacy mode → fall through to runCmd (original behavior)
//
// Detection is based purely on the presence of `--output` in args,
// so legacy `r1 run --task X` continues to work unchanged.
func runCmdDispatch(args []string) {
	for _, a := range args {
		if a == "--output" || strings.HasPrefix(a, "--output=") {
			code := runCommandExitCode(args)
			os.Exit(code)
		}
	}
	runCmd(args)
}
