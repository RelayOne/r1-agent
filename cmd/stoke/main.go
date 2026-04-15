package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ericmacdougall/stoke/internal/app"
	"github.com/ericmacdougall/stoke/internal/boulder"
	"github.com/ericmacdougall/stoke/internal/consent"
	"github.com/ericmacdougall/stoke/internal/logging"
	"github.com/ericmacdougall/stoke/internal/metrics"
	"github.com/ericmacdougall/stoke/internal/audit"
	"github.com/ericmacdougall/stoke/internal/config"
	stokeCtx "github.com/ericmacdougall/stoke/internal/context"
	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/env"
	"github.com/ericmacdougall/stoke/internal/env/docker"
	"github.com/ericmacdougall/stoke/internal/env/ember"
	"github.com/ericmacdougall/stoke/internal/env/fly"
	envssh "github.com/ericmacdougall/stoke/internal/env/ssh"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/flowtrack"
	"github.com/ericmacdougall/stoke/internal/hooks"
	"github.com/ericmacdougall/stoke/internal/hub"
	hubbuiltin "github.com/ericmacdougall/stoke/internal/hub/builtin"
	"github.com/ericmacdougall/stoke/internal/interview"
	litellmPkg "github.com/ericmacdougall/stoke/internal/litellm"
	"github.com/ericmacdougall/stoke/internal/ledger"
	stokeMCP "github.com/ericmacdougall/stoke/internal/mcp"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/modelsource"
	"github.com/ericmacdougall/stoke/internal/smoketest"
	"github.com/ericmacdougall/stoke/internal/websearch"
	"github.com/ericmacdougall/stoke/internal/notify"
	"github.com/ericmacdougall/stoke/internal/orchestrate"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/pools"
	"github.com/ericmacdougall/stoke/internal/progress"
	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/remote"
	"github.com/ericmacdougall/stoke/internal/repl"
	"github.com/ericmacdougall/stoke/internal/report"
	scanpkg "github.com/ericmacdougall/stoke/internal/scan"
	"github.com/ericmacdougall/stoke/internal/replay"
	"github.com/ericmacdougall/stoke/internal/repomap"
	"github.com/ericmacdougall/stoke/internal/scheduler"
	"github.com/ericmacdougall/stoke/internal/specexec"
	"github.com/ericmacdougall/stoke/internal/server"
	"github.com/ericmacdougall/stoke/internal/session"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/skill"
	"github.com/ericmacdougall/stoke/internal/wizard"
	"github.com/ericmacdougall/stoke/internal/stream"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/testselect"
	"github.com/ericmacdougall/stoke/internal/tui"
	"github.com/ericmacdougall/stoke/internal/verify"
	"github.com/ericmacdougall/stoke/internal/wisdom"
	"github.com/ericmacdougall/stoke/internal/stancesign"
	"github.com/ericmacdougall/stoke/internal/worktree"
)

// version is set at build time via ldflags.
var version = "dev"

// BuildConfig holds all parameters for a build run.
// Used by both buildCmd (CLI) and shipCmd (programmatic).
type BuildConfig struct {
	RepoRoot        string
	PlanPath        string // if empty, auto-detect
	PolicyPath      string
	Workers         int
	AuthMode        string
	ClaudeBinary    string
	CodexBinary     string
	ClaudeConfigDir string
	CodexHome       string
	ClaudePoolDirs  []string
	CodexPoolDirs   []string
	BuildCommand    string
	TestCommand     string
	LintCommand     string
	ROIFilter       string // high, medium, low, skip
	UseSQLite       bool
	SpecExec        bool // enable speculative parallel execution
	Timeout         time.Duration
	EnvBackend      string // execution environment: inproc, docker, fly, ember
	EnvImage        string // base image for container/VM environments
	EnvSize         string // machine size for cloud environments
	RunnerMode      string // runner backend: claude, codex, native, hybrid
	NativeAPIKey    string // API key for native runner (required when RunnerMode=native)
	NativeModel     string // model for native runner
	NativeBaseURL   string // base URL for native runner (e.g. LiteLLM proxy)
	SchedulerAlgo   string // task priority algorithm: grpw (default) | plas | continuum
}

// runBuild executes a build plan and returns the result.
// This is the core build logic, called by both buildCmd and shipCmd.
// Returns the build report and any fatal error.
func runBuild(cfg BuildConfig) (*report.BuildReport, error) {
	absRepo := cfg.RepoRoot

	// Register session with Ember dashboard for remote progress monitoring.
	// buildSuccess is captured by the deferred closure below and set to the
	// actual build outcome before the function returns.
	var buildSuccess bool
	reporter := remote.New()
	if reporter != nil {
		if url, err := reporter.RegisterSession(cfg.PlanPath); err == nil && url != "" {
			fmt.Printf("  dashboard: %s\n", url)
		}
		defer func() {
			summary := "build finished"
			if !buildSuccess {
				summary = "build failed"
			}
			_ = reporter.Complete(buildSuccess, summary)
		}()
	}

	// Build pool configurations
	var poolConfigs []subscriptions.Pool
	for i, dir := range cfg.ClaudePoolDirs {
		poolConfigs = append(poolConfigs, subscriptions.Pool{
			ID:        fmt.Sprintf("claude-%d", i+1),
			Provider:  subscriptions.ProviderClaude,
			ConfigDir: dir,
		})
	}
	for i, dir := range cfg.CodexPoolDirs {
		poolConfigs = append(poolConfigs, subscriptions.Pool{
			ID:        fmt.Sprintf("codex-%d", i+1),
			Provider:  subscriptions.ProviderCodex,
			ConfigDir: dir,
		})
	}
	// Build pool manager: explicit → discovered → nil (app.New creates defaults)
	var pools *subscriptions.Manager
	if len(poolConfigs) > 0 {
		pools = subscriptions.NewManager(poolConfigs)
	} else if discovered := autoDiscoverPools(); discovered != nil {
		pools = discovered
	}
	// If pools is nil, app.New will create default single Claude + Codex pool

	// Load plan
	var p *plan.Plan
	var err error
	if cfg.PlanPath != "" {
		p, err = plan.LoadFile(cfg.PlanPath)
	} else {
		p, err = plan.Load(absRepo)
	}
	if err != nil {
		return nil, fmt.Errorf("load plan: %w", err)
	}

	// Route tasks by type
	for i := range p.Tasks {
		if p.Tasks[i].Type == "" {
			p.Tasks[i].Type = string(model.InferTaskType(p.Tasks[i].Description))
		}
	}

	// ROI filter
	var roiClass plan.ROIClass
	switch cfg.ROIFilter {
	case "high":
		roiClass = plan.ROIHigh
	case "medium":
		roiClass = plan.ROIMedium
	case "low":
		roiClass = plan.ROILow
	case "skip":
		roiClass = plan.ROISkip
	default:
		roiClass = plan.ROIMedium
	}
	kept, _ := plan.FilterByROI(p.Tasks, roiClass)
	p.Tasks = kept

	// Session store — auto-upgrade to SQLite for parallel builds (JSON is not concurrency-safe)
	var store session.SessionStore
	if cfg.UseSQLite || cfg.Workers > 1 {
		sqlStore, err := session.NewSQLStore(absRepo)
		if err != nil {
			return nil, fmt.Errorf("sqlite store: %w", err)
		}
		store = sqlStore
	} else {
		store = session.New(absRepo)
	}

	// TUI runner
	ui := tui.NewRunner()

	// Context manager
	ctxMgr := stokeCtx.NewManager(stokeCtx.DefaultBudget())

	checkResume(store, p)
	store.SaveState(&session.State{
		PlanID:    p.ID,
		Tasks:     p.Tasks,
		StartedAt: time.Now(),
	})

	// No wall-clock timeout by default: the supervisor (boulder) is authoritative
	// for detecting stuck workers. cfg.Timeout > 0 still applies as a hard safety
	// ceiling for users who explicitly opt in.
	var ctx context.Context
	var cancel context.CancelFunc
	if cfg.Timeout > 0 {
		sigCtx, sigCancel := signalContext(context.Background())
		defer sigCancel()
		ctx, cancel = context.WithTimeout(sigCtx, cfg.Timeout)
	} else {
		ctx, cancel = signalContext(context.Background())
	}
	defer cancel()

	// Create harness-owned plan state
	taskIDs := make([]string, len(p.Tasks))
	for i, t := range p.Tasks {
		taskIDs[i] = t.ID
	}
	planState := taskstate.NewPlanState(taskIDs)

	sched := scheduler.New(cfg.Workers)
	sched.PriorityName = cfg.SchedulerAlgo
	startTime := time.Now()

	// Create ONE shared worktree manager for the entire build session.
	// The merge mutex MUST be shared across all parallel tasks to prevent
	// concurrent ref mutations that corrupt the repository.
	sharedWorktrees := worktree.NewManager(absRepo)
	// A1: attach the harness stance signing identity. Commits the
	// worktree manager produces (merges to main + conflict-resolution
	// commits) will carry the "stoke" stance's signing-key overlay.
	// Failure to resolve the key is a warn-and-continue — unsigned
	// commits still work; the Signer == nil branch inside Manager is
	// the fallback.
	if stanceID, sErr := stancesign.IdentityFor("", "stoke"); sErr == nil {
		sharedWorktrees.Signer = stanceID
	} else {
		fmt.Printf("  ⚠ stancesign: could not resolve identity (%v) — commits will be unsigned\n", sErr)
	}
	wisdomStore := wisdom.NewStore()

	// Metrics registry: shared across all tasks in this build session.
	metricsReg := metrics.NewRegistry()

	// Progress estimator for ETA tracking.
	progressTasks := make([]progress.Task, len(p.Tasks))
	for i, t := range p.Tasks {
		progressTasks[i] = progress.Task{
			ID: t.ID, Name: t.Description,
			Dependencies: t.Dependencies, Weight: 1.0,
		}
	}
	estimator := progress.New(progressTasks)

	// Boulder idle detection: shared across all parallel tasks.
	boulderEnforcer := boulder.New(filepath.Join(absRepo, ".stoke", "boulder"), boulder.DefaultConfig())

	// Unified event bus: shared across all tasks in this build session.
	eventBus := hub.New()

	// Flow tracking: infer development phase from action sequences.
	flowTracker := flowtrack.NewTracker(flowtrack.Config{})
	eventBus.Register(hub.FlowTrackObserver(flowTracker))

	// Consent gate: enforce human approval for dangerous operations.
	// In headless mode, deny risky ops (no interactive approval handler).
	consentWorkflow := consent.NewWorkflow(nil)
	eventBus.Register(hub.ConsentGate(consentWorkflow))

	// Cost tracking: shared across all tasks in this build session.
	tracker := costtrack.NewTracker(0, func(alert costtrack.Alert) {
		ui.Event("_system", stream.Event{Type: "system", DeltaText: alert.Message})
	})
	// B2: attach the amplification-budget tracker. Load baselines
	// from bench/baselines/token-baselines-2026-Q2.json when present.
	// Missing file or parse failure leaves tracker.amp == nil and
	// enforcement disabled — same as before B2 landed. Task class is
	// inferred from the work at the workflow layer; here we default
	// to "feature_add" as the most common sow shape. Unknown classes
	// get a zero-value (disabled) budget.
	if baselines, bErr := costtrack.LoadBaselines("bench/baselines/token-baselines-2026-Q2.json"); bErr == nil {
		budget := costtrack.BudgetForClass(baselines, "feature_add")
		amp := costtrack.NewAmplificationTracker(budget)
		amp.OnTransition = func(prev, curr costtrack.AmplificationStatus, mult float64) {
			if line := costtrack.FormatStatus(budget.TaskClass, curr, mult); line != "" {
				fmt.Println(line)
			}
		}
		tracker.AttachAmplification(amp)
	}

	// Dependency-aware test selection: build import graph once, reuse per task.
	testGraph, testGraphErr := testselect.BuildGraph(absRepo)
	if testGraphErr != nil {
		testGraph = nil // non-fatal: fall back to running all tests
	}

	// Ranked codebase map for agent context injection.
	repoMap, repoMapErr := repomap.Build(absRepo)
	if repoMapErr != nil {
		repoMap = nil // non-fatal: agents navigate without map
	}

	// Provision execution environment if configured.
	var buildEnv env.Environment
	var buildEnvHandle *env.Handle
	var buildLedger *ledger.Ledger
	if cfg.EnvBackend != "" && cfg.EnvBackend != "inproc" {
		var provErr error
		buildEnv, buildEnvHandle, provErr = provisionEnv(ctx, cfg, absRepo)
		if provErr != nil {
			return nil, fmt.Errorf("provision environment: %w", provErr)
		}

		// Open ledger for env audit trail if available.
		envLedgerDir := filepath.Join(absRepo, ".stoke", "ledger")
		if fileExists(envLedgerDir) {
			if lg, err := ledger.New(envLedgerDir); err == nil {
				buildLedger = lg
				env.RecordProvision(ctx, lg, buildEnvHandle, env.Spec{
					Backend:   env.Backend(cfg.EnvBackend),
					BaseImage: cfg.EnvImage,
					Size:      cfg.EnvSize,
				})
			}
		}

		defer func() {
			if buildEnv != nil && buildEnvHandle != nil {
				if buildLedger != nil {
					cost, _ := buildEnv.Cost(context.Background(), buildEnvHandle)
					env.RecordTeardown(context.Background(), buildLedger, buildEnvHandle, cost)
					buildLedger.Close()
				}
				buildEnv.Teardown(context.Background(), buildEnvHandle)
			}
		}()
		fmt.Printf("  env:     %s (%s)\n", cfg.EnvBackend, buildEnvHandle.ID)
	}

	execFn := func(ctx context.Context, task plan.Task) scheduler.TaskResult {
		metricsReg.Counter("tasks.attempted").Inc()
		estimator.Start(task.ID)
		if task.Status == plan.StatusDone {
			metricsReg.Counter("tasks.skipped").Inc()
			estimator.Skip(task.ID)
			return scheduler.TaskResult{TaskID: task.ID, Success: true}
		}

		ui.TaskStart(task.ID, task.Description, "pool-1")
		taskStart := time.Now()
		ts := planState.Get(task.ID)

		appCfg := app.RunConfig{
			RepoRoot:         absRepo,
			PolicyPath:       cfg.PolicyPath,
			Task:             task.Description,
			TaskType:         task.Type,
			TaskVerification: task.Verification,
			AllowedFiles:     task.Files,
			DryRun:           false,
			PlanOnly:         task.PlanOnly,
			AuthMode:         app.AuthMode(cfg.AuthMode),
			ClaudeBinary:     cfg.ClaudeBinary,
			CodexBinary:      cfg.CodexBinary,
			ClaudeConfigDir:  cfg.ClaudeConfigDir,
			CodexHome:        cfg.CodexHome,
			Pools:            pools,
			Worktrees:        sharedWorktrees,
			State:            ts,
			Wisdom:           wisdomStore,
			BuildCommand:     cfg.BuildCommand,
			TestCommand:      cfg.TestCommand,
			LintCommand:      cfg.LintCommand,
			Boulder:          boulderEnforcer,
			CostTracker:      tracker,
			TestGraph:        testGraph,
			RepoMap:          repoMap,
			EventBus:         eventBus,
			Environ:          buildEnv,
			EnvHandle:        buildEnvHandle,
			RunnerMode:       cfg.RunnerMode,
			NativeAPIKey:     cfg.NativeAPIKey,
			NativeModel:      cfg.NativeModel,
			NativeBaseURL:    cfg.NativeBaseURL,
			Recorder:         replay.NewRecorder(task.ID+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10), task.ID),
			OnEvent: func(ev stream.Event) {
				ui.Event(task.ID, ev)
				if ev.Type == "assistant" {
					ctxMgr.Add(stokeCtx.ContextBlock{
						Label: "tool_output", Content: ev.DeltaText,
						Tier: stokeCtx.TierActive, Priority: 2,
					})
				}
				rState := stokeCtx.ReminderState{ContextUtil: ctxMgr.Utilization()}
				if ev.Type == "assistant" {
					for _, tu := range ev.ToolUses {
						if tu.Name == "Write" || tu.Name == "Edit" {
							if fp, ok := tu.Input["file_path"].(string); ok && strings.Contains(fp, "test") {
								rState.WritingTestFile = true
							}
						}
					}
				}
				for _, reminder := range stokeCtx.CheckReminders(stokeCtx.DefaultReminders(), rState) {
					fmt.Printf("  \u26a0 %s\n", reminder)
				}
			},
		}

		orchestrator, err := app.New(appCfg)
		if err != nil {
			metricsReg.Counter("tasks.failed").Inc()
			estimator.Fail(task.ID)
			ui.TaskComplete(task.ID, false, 0, 0, 1)
			markTask(p, task.ID, plan.StatusFailed)
			store.SaveState(&session.State{PlanID: p.ID, Tasks: p.Tasks, StartedAt: time.Now()})
			return scheduler.TaskResult{TaskID: task.ID, Error: err}
		}

		result, err := orchestrator.Run(ctx)
		elapsed := time.Since(taskStart).Seconds()

		// Determine attempt number from prior history
		priorAttempts, _ := store.LoadAttempts(task.ID)
		attemptNum := len(priorAttempts) + 1

		if err != nil {
			ui.TaskComplete(task.ID, false, elapsed, result.TotalCostUSD, 1)
			attempt := session.Attempt{
				TaskID:   task.ID,
				Number:   attemptNum,
				Success:  false,
				Error:    err.Error(),
				CostUSD:  result.TotalCostUSD,
				Duration: time.Duration(elapsed * float64(time.Second)),
			}
			if analysis := verify.AnalyzeOutcomes(result.Verification); analysis != nil {
				attempt.FailClass = string(analysis.Class)
				attempt.FailSummary = analysis.Summary
				attempt.RootCause = analysis.RootCause
			}
			store.SaveAttempt(attempt)
			metricsReg.Counter("tasks.failed").Inc()
			estimator.Fail(task.ID)
			if ts != nil {
				fmt.Println(ts.ClaimedVsVerified())
			}
			markTask(p, task.ID, plan.StatusFailed)
			store.SaveState(&session.State{PlanID: p.ID, Tasks: p.Tasks, StartedAt: time.Now()})
			tp, tf, dl := extractVerifyMetrics(result.Verification, result.FilesChanged)
			return scheduler.TaskResult{TaskID: task.ID, Error: err, CostUSD: result.TotalCostUSD, TestsPassed: tp, TestsFailed: tf, DiffLines: dl}
		}

		ui.TaskComplete(task.ID, true, elapsed, result.TotalCostUSD, attemptNum)
		if ts != nil {
			fmt.Println(ts.ClaimedVsVerified())
		}
		store.SaveAttempt(session.Attempt{
			TaskID:   task.ID,
			Number:   attemptNum,
			Success:  true,
			CostUSD:  result.TotalCostUSD,
			Duration: time.Duration(elapsed * float64(time.Second)),
		})
		metricsReg.Counter("tasks.succeeded").Inc()
		estimator.Complete(task.ID)
		markTask(p, task.ID, plan.StatusDone)
		store.SaveState(&session.State{PlanID: p.ID, Tasks: p.Tasks, StartedAt: time.Now()})
		tp, tf, dl := extractVerifyMetrics(result.Verification, result.FilesChanged)
		return scheduler.TaskResult{TaskID: task.ID, Success: true, CostUSD: result.TotalCostUSD, TestsPassed: tp, TestsFailed: tf, DiffLines: dl}
	}

	// Optionally wrap with speculative parallel execution
	if cfg.SpecExec {
		execFn = scheduler.WithSpecExec(execFn, scheduler.SpecExecConfig{
			Approaches:  specexec.CommonApproaches(),
			MaxParallel: 3,
			Timeout:     5 * time.Minute,
		})
	}

	results, err := sched.Run(ctx, p, execFn)

	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}

	// Generate report
	buildReport := &report.BuildReport{
		Version:     version,
		PlanID:      p.ID,
		StartedAt:   startTime,
		CompletedAt: time.Now(),
		TasksTotal:  len(p.Tasks),
	}
	for _, r := range results {
		tr := report.TaskReport{ID: r.TaskID, CostUSD: r.CostUSD}
		if r.Success {
			tr.Status = "done"
			buildReport.TasksDone++
		} else {
			tr.Status = "failed"
			buildReport.TasksFailed++
			if r.Error != nil {
				tr.Error = r.Error.Error()
			}
		}
		buildReport.TotalCost += r.CostUSD
		buildReport.Tasks = append(buildReport.Tasks, tr)
	}
	buildReport.Success = buildReport.TasksFailed == 0
	buildReport.DurationSec = time.Since(startTime).Seconds()
	buildSuccess = buildReport.Success // propagate to deferred reporter.Complete()

	buildReport.Save(absRepo)
	buildReport.SaveLatest(absRepo)
	store.ClearState()

	// Show summary with progress ETA data.
	ui.Summary(len(p.Tasks))
	fmt.Printf("  Report: .stoke/reports/latest.json\n")
	if tracker.RequestCount() > 0 {
		fmt.Printf("  Cost: %s\n", tracker.Summary())
	}
	fmt.Printf("  Progress: %s\n", estimator.Summary())

	// Fire webhook notification on build completion (if configured).
	if webhookURL := os.Getenv("STOKE_WEBHOOK_URL"); webhookURL != "" {
		notifier := notify.NewWebhookNotifier(webhookURL, nil, nil)
		eventType := "build_complete"
		if !buildReport.Success {
			eventType = "build_failed"
		}
		_ = notifier.Notify(notify.NotifyEvent{
			Type:      eventType,
			Message:   fmt.Sprintf("Build %s: %d/%d tasks succeeded", p.ID, buildReport.TasksDone, buildReport.TasksTotal),
			Timestamp: time.Now(),
			Details: map[string]string{
				"plan_id":  p.ID,
				"cost":     fmt.Sprintf("$%.4f", buildReport.TotalCost),
				"duration": fmt.Sprintf("%.1fs", buildReport.DurationSec),
			},
		})
	}
	summary := planState.Summary()
	fmt.Printf("\n  Plan state (harness-verified):\n")
	for _, phase := range []taskstate.Phase{taskstate.Committed, taskstate.Failed, taskstate.Blocked, taskstate.UserSkipped, taskstate.Pending} {
		if count, ok := summary[phase]; ok && count > 0 {
			fmt.Printf("    %s: %d\n", phase, count)
		}
	}

	return buildReport, nil
}

// signalContext returns a context that is cancelled on SIGINT or SIGTERM.
// The returned cancel function should be deferred by the caller.
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "\nstoke: received signal, shutting down gracefully...\n")
			cancel()
			// Second signal: hard exit.
			<-sigCh
			fmt.Fprintf(os.Stderr, "stoke: forced exit\n")
			os.Exit(1)
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
	}()
	return ctx, cancel
}

func main() {
	// Initialize structured logging from STOKE_LOG_LEVEL env (default: "info").
	logLevel := os.Getenv("STOKE_LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logging.Init(logLevel, os.Stderr)

	if len(os.Args) < 2 {
		// No args: launch the line REPL (classic). Users who want the
		// full-screen Bubble Tea shell can run `stoke tui` instead. We keep
		// line mode as the default because it composes better with pipes,
		// CI/CD logs, and non-tty contexts.
		launchREPL()
		return
	}

	switch os.Args[1] {
	case "tui", "--tui", "shell":
		launchShell(os.Args[2:])
	case "run":
		runCmd(os.Args[2:])
	case "build":
		buildCmd(os.Args[2:])
	case "plan":
		planCmd(os.Args[2:])
	case "scan":
		scanCmd(os.Args[2:])
	case "audit":
		auditCmd(os.Args[2:])
	case "inspect":
		inspectCmd(os.Args[2:])
	case "watch":
		watchCmd(os.Args[2:])
	case "status":
		statusCmd(os.Args[2:])
	case "pool":
		poolCmd(os.Args[2:])
	case "print-default-policy":
		fmt.Print(app.DefaultPolicyYAML())
	case "doctor":
		doctorCmd(os.Args[2:])
	case "yolo":
		yoloCmd(os.Args[2:])
	case "scope":
		scopeCmd(os.Args[2:])
	case "repair":
		repairCmd(os.Args[2:])
	case "ship":
		shipCmd(os.Args[2:])
	case "add-claude":
		addClaudeCmd(os.Args[2:])
	case "add-codex":
		addCodexCmd(os.Args[2:])
	case "pools":
		poolsCmd(os.Args[2:])
	case "remove-pool":
		removePoolCmd(os.Args[2:])
	case "sow":
		sowCmd(os.Args[2:])
	case "mission":
		missionCmd(os.Args[2:])
	case "serve":
		serveCmd(os.Args[2:])
	case "mcp-serve":
		mcpServeCmd(os.Args[2:])
	case "mcp-serve-stoke":
		mcpServeStokeCmd(os.Args[2:])
	case "init", "wizard":
		initCmd(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// --- init/wizard: project configuration wizard ---

func initCmd(args []string) {
	projectDir, _ := os.Getwd()
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		projectDir = args[0]
	}

	autoMode := false
	for _, a := range args {
		if a == "--auto" || a == "-a" {
			autoMode = true
		}
	}

	// If reinitializing, verify existing ledger integrity first.
	ledgerDir := filepath.Join(projectDir, ".stoke", "ledger")
	if fileExists(ledgerDir) {
		lg, err := ledger.New(ledgerDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ledger open error: %v\n", err)
			os.Exit(1)
		}
		if err := lg.Verify(context.Background()); err != nil {
			lg.Close()
			fmt.Fprintf(os.Stderr, "ledger integrity check failed: %v\n", err)
			os.Exit(1)
		}
		lg.Close()
		fmt.Println("  Ledger integrity: OK (reinitializing)")
	}

	w := wizard.New(projectDir)

	var err error
	if autoMode {
		err = w.RunAutoDetect()
		if err == nil {
			fmt.Printf("  stoke.policy.yaml generated (auto-detect mode)\n")
		}
	} else {
		err = w.Run()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "wizard error: %v\n", err)
		os.Exit(1)
	}
}

// --- mcp-serve: start MCP codebase tool server ---

func mcpServeCmd(args []string) {
	fs := flag.NewFlagSet("mcp-serve", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root to index")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	srv, err := stokeMCP.BuildCodebaseServer(absRepo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building codebase server: %v\n", err)
		os.Exit(1)
	}

	if err := srv.ServeStdio(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// --- mcp-serve-stoke: start MCP server that exposes Stoke as a tool to Claude Code ---
//
// This is the inverse of mcp-serve: instead of giving Claude Code access to a
// project's codebase, this gives Claude Code the ability to drive Stoke itself.
// Claude Code can call stoke_build_from_sow to kick off a multi-session build,
// then poll stoke_get_mission_status until completion.
//
// Wire it into Claude Code with:
//   { "mcpServers": { "stoke": { "command": "stoke", "args": ["mcp-serve-stoke"] } } }
func mcpServeStokeCmd(args []string) {
	fs := flag.NewFlagSet("mcp-serve-stoke", flag.ExitOnError)
	stokeBin := fs.String("stoke-bin", "", "Path to stoke binary used for spawned builds (default: argv[0])")
	fs.Parse(args)

	bin := *stokeBin
	if bin == "" {
		// Resolve our own executable path so subprocess builds can find us.
		if exe, err := os.Executable(); err == nil {
			bin = exe
		}
	}
	srv := stokeMCP.NewStokeServer(bin)
	if err := srv.ServeStdio(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// --- run: single task through PLAN -> EXECUTE -> VERIFY ---

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	policy := fs.String("policy", "", "Path to stoke.policy.yaml")
	task := fs.String("task", "", "Task prompt")
	taskType := fs.String("task-type", "", "Task type override")
	wtName := fs.String("worktree-name", "", "Explicit worktree name")
	dryRun := fs.Bool("dry-run", false, "Print commands without executing")
	authMode := fs.String("mode", "mode1", "Auth mode: mode1 or mode2")
	claudeBin := fs.String("claude-bin", "claude", "Claude Code binary")
	codexBin := fs.String("codex-bin", "codex", "Codex CLI binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	codexHome := fs.String("codex-home", "", "CODEX_HOME")
	buildC := fs.String("build-cmd", "", "Build command")
	testC := fs.String("test-cmd", "", "Test command")
	lintC := fs.String("lint-cmd", "", "Lint command")
	runnerMode := fs.String("runner", "claude", "Runner backend: claude, codex, native, hybrid")
	nativeAPIKey := fs.String("native-api-key", "", "Anthropic API key for native runner")
	nativeModel := fs.String("native-model", "claude-sonnet-4-6", "Model for native runner")
	nativeBaseURL := fs.String("native-base-url", "", "Base URL for native runner (e.g. http://localhost:8000 for LiteLLM)")
	// No wall-clock timeout: supervisor (boulder) monitors liveness and restarts
	// genuinely stuck workers. Use Ctrl-C to abort.
	fs.Parse(args)

	if strings.TrimSpace(*task) == "" {
		fmt.Fprintln(os.Stderr, "--task is required")
		fs.Usage()
		os.Exit(2)
	}

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	ensureGitRepoOrFatal(absRepo)

	// Auto-detect commands
	detected := config.DetectCommands(absRepo)
	if *buildC == "" {
		*buildC = detected.Build
	}
	if *testC == "" {
		*testC = detected.Test
	}
	if *lintC == "" {
		*lintC = detected.Lint
	}

	// Auto-discover LiteLLM for native runner.
	if *runnerMode == "native" && *nativeBaseURL == "" {
		if d := litellmPkg.Discover(); d != nil {
			*nativeBaseURL = d.BaseURL
			if *nativeAPIKey == "" && d.APIKey != "" {
				*nativeAPIKey = d.APIKey
			}
			fmt.Printf("  litellm: auto-discovered %s (%d models)\n", d.BaseURL, len(d.Models))
		}
	}

	// Create TUI runner for live progress
	ui := tui.NewRunner()

	// Create harness-owned task state (anti-deception: model cannot mark status)
	ts := taskstate.NewTaskState("run-task")

	// Build shared resources for the single-task run.
	runTracker := costtrack.NewTracker(0, nil)
	runRepoMap, _ := repomap.Build(absRepo)
	runTestGraph, _ := testselect.BuildGraph(absRepo)
	// Boulder supervisor: now authoritative for stuck-agent detection. Always
	// enabled so the task is monitored for liveness instead of timed out.
	runBoulder := boulder.New(filepath.Join(absRepo, ".stoke", "boulder"), boulder.DefaultConfig())

	cfg := app.RunConfig{
		RepoRoot:        absRepo,
		PolicyPath:      *policy,
		Task:            *task,
		TaskType:        *taskType,
		WorktreeName:    *wtName,
		DryRun:          *dryRun,
		AuthMode:        app.AuthMode(*authMode),
		ClaudeBinary:    *claudeBin,
		CodexBinary:     *codexBin,
		ClaudeConfigDir: *claudeConfigDir,
		CodexHome:       *codexHome,
		Boulder:         runBoulder,
		State:           ts,
		BuildCommand:    *buildC,
		TestCommand:     *testC,
		LintCommand:     *lintC,
		CostTracker:     runTracker,
		RepoMap:         runRepoMap,
		TestGraph:       runTestGraph,
		RunnerMode:      *runnerMode,
		NativeAPIKey:    *nativeAPIKey,
		NativeModel:     *nativeModel,
		NativeBaseURL:   *nativeBaseURL,
		EventBus:        newEventBus(),
		Recorder:        replay.NewRecorder("run-"+strconv.FormatInt(time.Now().UnixMilli(), 10), "run-task"),
		OnEvent: func(ev stream.Event) {
			ui.Event("task", ev)
		},
	}

	orchestrator, err := app.New(cfg)
	if err != nil {
		fatal("stoke init: %v", err)
	}

	// No wall-clock timeout: the supervisor (boulder) monitors liveness and
	// restarts genuinely stuck workers. Ctrl-C still aborts via signalContext.
	ctx, cancel := signalContext(context.Background())
	defer cancel()

	ui.TaskStart("task", *task, "default")
	startTime := time.Now()

	result, err := orchestrator.Run(ctx)
	elapsed := time.Since(startTime).Seconds()

	if err != nil {
		ui.TaskComplete("task", false, elapsed, result.TotalCostUSD, 1)
		fmt.Println(ts.ClaimedVsVerified())
		fatal("stoke run: %v", err)
	}

	ui.TaskComplete("task", true, elapsed, result.TotalCostUSD, 1)
	fmt.Println(ts.ClaimedVsVerified())
	fmt.Print(result.Render())
}

// --- build: multi-task plan with parallel agents ---

func buildCmd(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	planFile := fs.String("plan", "", "Plan file (default: auto-detect)")
	policy := fs.String("policy", "", "Path to stoke.policy.yaml")
	dryRun := fs.Bool("dry-run", false, "Show plan without executing")
	workers := fs.Int("workers", 4, "Max parallel agents")
	schedulerAlgo := fs.String("scheduler", "grpw", "Task priority algorithm: grpw (default) | plas (Autellix program-level attained service approximation) | continuum (file-scope KV-cache affinity approximation). Unknown values fall back to grpw.")
	authMode := fs.String("mode", "mode1", "Auth mode")
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "Single CLAUDE_CONFIG_DIR")
	codexHome := fs.String("codex-home", "", "Single CODEX_HOME")
	claudePoolsFlag := fs.String("claude-pools", "", "Comma-separated Claude pool dirs")
	codexPoolsFlag := fs.String("codex-pools", "", "Comma-separated Codex pool dirs")
	buildC := fs.String("build-cmd", "", "Build command")
	testC := fs.String("test-cmd", "", "Test command")
	lintC := fs.String("lint-cmd", "", "Lint command")
	roiFilter := fs.String("roi", "medium", "ROI threshold: high, medium, low, skip (default: medium)")
	useSQLite := fs.Bool("sqlite", false, "Use SQLite session store instead of JSON")
	interactive := fs.Bool("interactive", false, "Launch interactive Bubble Tea TUI")
	specExec := fs.Bool("specexec", false, "Enable speculative parallel execution (tries multiple strategies per task)")
	envBackend := fs.String("env", "", "Execution environment: inproc, docker, fly, ember (default: from config or inproc)")
	envImage := fs.String("env-image", "", "Base image for container/VM environments")
	envSize := fs.String("env-size", "", "Machine size for cloud environments (e.g. performance-4x)")
	// Hard timeout is disabled by default; supervisor handles stuck workers.
	// Setting a non-zero value re-enables it as a safety ceiling.
	timeout := fs.Duration("timeout", 0, "Hard wall-clock timeout (0 = supervisor-driven, recommended)")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	ensureGitRepoOrFatal(absRepo)

	// Build pool configurations from flags
	var claudePoolDirs, codexPoolDirs []string
	if *claudePoolsFlag != "" {
		claudePoolDirs = splitPools(*claudePoolsFlag)
	} else if *claudeConfigDir != "" {
		claudePoolDirs = []string{*claudeConfigDir}
	}
	if *codexPoolsFlag != "" {
		codexPoolDirs = splitPools(*codexPoolsFlag)
	} else if *codexHome != "" {
		codexPoolDirs = []string{*codexHome}
	}

	// Build subscription pool configs
	var poolConfigs []subscriptions.Pool
	for i, dir := range claudePoolDirs {
		poolConfigs = append(poolConfigs, subscriptions.Pool{
			ID:        fmt.Sprintf("claude-%d", i+1),
			Provider:  subscriptions.ProviderClaude,
			ConfigDir: dir,
		})
	}
	for i, dir := range codexPoolDirs {
		poolConfigs = append(poolConfigs, subscriptions.Pool{
			ID:        fmt.Sprintf("codex-%d", i+1),
			Provider:  subscriptions.ProviderCodex,
			ConfigDir: dir,
		})
	}
	// Build pool manager: explicit flags → auto-discovered → nil (let app.New create defaults)
	var pools *subscriptions.Manager
	if len(poolConfigs) > 0 {
		pools = subscriptions.NewManager(poolConfigs)
		fmt.Printf("  pools:   %d Claude + %d Codex\n", len(claudePoolDirs), len(codexPoolDirs))
	} else if discovered := autoDiscoverPools(); discovered != nil {
		pools = discovered
		snap := discovered.Snapshot()
		claudeCount, codexCount := 0, 0
		for _, p := range snap {
			if p.Provider == subscriptions.ProviderClaude {
				claudeCount++
			}
			if p.Provider == subscriptions.ProviderCodex {
				codexCount++
			}
		}
		fmt.Printf("  pools:   %d Claude + %d Codex (auto-discovered from ~/.stoke/pools/)\n", claudeCount, codexCount)
	}
	// If pools is nil here, app.New will create default single Claude + Codex pool

	// Auto-detect commands
	detected := config.DetectCommands(absRepo)
	if *buildC == "" {
		*buildC = detected.Build
	}
	if *testC == "" {
		*testC = detected.Test
	}
	if *lintC == "" {
		*lintC = detected.Lint
	}

	// Load plan
	var p *plan.Plan
	if *planFile != "" {
		p, err = plan.LoadFile(*planFile)
	} else {
		p, err = plan.Load(absRepo)
	}
	if err != nil {
		fatal("load plan: %v", err)
	}

	// Validate plan structure
	if planErrs := p.Validate(); len(planErrs) > 0 {
		for _, e := range planErrs {
			fmt.Fprintf(os.Stderr, "  plan warning: %s\n", e)
		}
	}

	// Validate commands
	for _, w := range config.ValidateCommands(*buildC, *testC, *lintC) {
		fmt.Fprintf(os.Stderr, "  %s\n", w)
	}

	// Route tasks by type
	for i := range p.Tasks {
		if p.Tasks[i].Type == "" {
			p.Tasks[i].Type = string(model.InferTaskType(p.Tasks[i].Description))
		}
	}

	// ROI filter: remove low-value tasks before execution
	var roiClass plan.ROIClass
	switch *roiFilter {
	case "high":
		roiClass = plan.ROIHigh
	case "medium":
		roiClass = plan.ROIMedium
	case "low":
		roiClass = plan.ROILow
	case "skip":
		roiClass = plan.ROISkip
	default:
		roiClass = plan.ROIMedium
	}
	kept, filtered := plan.FilterByROI(p.Tasks, roiClass)
	if len(filtered) > 0 {
		fmt.Printf("  ROI filter removed %d task(s):\n", len(filtered))
		for _, f := range filtered {
			fmt.Printf("    - %s (%s: %s)\n", f.Task.ID, f.ROI.Class, f.ROI.Reason)
		}
		p.Tasks = kept
		fmt.Println()
	}

	fmt.Printf("⚡ STOKE build %s\n", version)
	fmt.Printf("  plan:    %s (%d tasks)\n", p.ID, len(p.Tasks))
	fmt.Printf("  workers: %d\n", *workers)
	fmt.Printf("  build:   %s\n", orNone(*buildC))
	fmt.Printf("  test:    %s\n", orNone(*testC))
	fmt.Printf("  lint:    %s\n\n", orNone(*lintC))

	if *dryRun {
		fmt.Println("DRY RUN:")
		for _, t := range p.Tasks {
			deps := ""
			if len(t.Dependencies) > 0 {
				deps = " (after " + strings.Join(t.Dependencies, ", ") + ")"
			}
			fmt.Printf("  %s [%s]: %s%s\n", t.ID, t.Type, trunc(t.Description, 55), deps)
			if len(t.Files) > 0 {
				fmt.Printf("    files: %s\n", strings.Join(t.Files, ", "))
			}
		}
		return
	}

	if *interactive {
		// Session store (for interactive mode)
		var store session.SessionStore
		if *useSQLite {
			sqlStore, err := session.NewSQLStore(absRepo)
			if err != nil {
				fatal("sqlite store: %v", err)
			}
			store = sqlStore
		} else {
			store = session.New(absRepo)
		}

		// Launch interactive Bubble Tea TUI
		model := tui.NewInteractiveModel(p.ID, len(p.Tasks))
		program := tea.NewProgram(model)

		// Create harness-owned plan state for interactive mode too
		interactiveTaskIDs := make([]string, len(p.Tasks))
		for i, t := range p.Tasks {
			interactiveTaskIDs[i] = t.ID
		}
		interactivePlanState := taskstate.NewPlanState(interactiveTaskIDs)

		go func() {
			checkResume(store, p)
			store.SaveState(&session.State{PlanID: p.ID, Tasks: p.Tasks, StartedAt: time.Now()})

			sigCtx, sigCancel := signalContext(context.Background())
			defer sigCancel()
			ctx, cancel := context.WithTimeout(sigCtx, *timeout)
			defer cancel()

			sched := scheduler.New(*workers)
			sched.PriorityName = *schedulerAlgo
			interactiveWorktrees := worktree.NewManager(absRepo)
			if stanceID, sErr := stancesign.IdentityFor("", "stoke"); sErr == nil {
				interactiveWorktrees.Signer = stanceID
			}
			wisdomStore := wisdom.NewStore()

			// Shared resources for interactive mode (same as headless).
			tuiTracker := costtrack.NewTracker(0, nil)
			tuiTestGraph, _ := testselect.BuildGraph(absRepo)
			tuiRepoMap, _ := repomap.Build(absRepo)
			tuiBoulder := boulder.New(filepath.Join(absRepo, ".stoke", "boulder"), boulder.DefaultConfig())
			tuiOpts := &buildRunConfigOpts{
				Boulder:     tuiBoulder,
				CostTracker: tuiTracker,
				TestGraph:   tuiTestGraph,
				RepoMap:     tuiRepoMap,
			}

			tuiExecFn := func(ctx context.Context, task plan.Task) scheduler.TaskResult {
				if task.Status == plan.StatusDone {
					return scheduler.TaskResult{TaskID: task.ID, Success: true}
				}
				tui.SendTaskStart(program, task.ID, task.Description, "pool-1")
				taskStart := time.Now()
				cfg := buildRunConfig(absRepo, *policy, task, *authMode, *claudeBin, *codexBin, *claudeConfigDir, *codexHome, *buildC, *testC, *lintC, pools, interactiveWorktrees, interactivePlanState.Get(task.ID), wisdomStore, func(ev stream.Event) {
					tui.SendTaskEvent(program, task.ID, ev)
				}, tuiOpts)
				orchestrator, err := app.New(cfg)
				if err != nil {
					ts := interactivePlanState.Get(task.ID)
					tui.SendTaskComplete(program, task.ID, false, 0, 0, 1, err.Error(), ts.ClaimedVsVerified())
					return scheduler.TaskResult{TaskID: task.ID, Error: err}
				}
				result, err := orchestrator.Run(ctx)
				elapsed := time.Since(taskStart).Seconds()
				ts := interactivePlanState.Get(task.ID)
				priorAttempts, _ := store.LoadAttempts(task.ID)
				attemptNum := len(priorAttempts) + 1
				if err != nil {
					tui.SendTaskComplete(program, task.ID, false, result.TotalCostUSD, elapsed, attemptNum, err.Error(), ts.ClaimedVsVerified())
					store.SaveAttempt(session.Attempt{TaskID: task.ID, Number: attemptNum, Success: false, Error: err.Error(), CostUSD: result.TotalCostUSD})
					markTask(p, task.ID, plan.StatusFailed)
					store.SaveState(&session.State{PlanID: p.ID, Tasks: p.Tasks, StartedAt: time.Now()})
					return scheduler.TaskResult{TaskID: task.ID, Error: err, CostUSD: result.TotalCostUSD}
				}
				tui.SendTaskComplete(program, task.ID, true, result.TotalCostUSD, elapsed, attemptNum, "", ts.ClaimedVsVerified())
				store.SaveAttempt(session.Attempt{TaskID: task.ID, Number: attemptNum, Success: true, CostUSD: result.TotalCostUSD})
				markTask(p, task.ID, plan.StatusDone)
				store.SaveState(&session.State{PlanID: p.ID, Tasks: p.Tasks, StartedAt: time.Now()})
				return scheduler.TaskResult{TaskID: task.ID, Success: true, CostUSD: result.TotalCostUSD}
			}
			if *specExec {
				tuiExecFn = scheduler.WithSpecExec(tuiExecFn, scheduler.SpecExecConfig{
					Approaches:  specexec.CommonApproaches(),
					MaxParallel: 3,
					Timeout:     5 * time.Minute,
				})
			}
			sched.Run(ctx, p, tuiExecFn)
			// Update pool utilization in TUI
			tui.SendPoolUpdate(program, []tui.PoolInfo{
				{ID: "aggregate", Label: "all pools", Utilization: 0},
			})
			tui.SendDone(program)
		}()

		if _, err := program.Run(); err != nil {
			fatal("tui: %v", err)
		}
		store.ClearState()
		return
	}

	// --- Headless mode (default) ---
	// Use the extracted runBuild function which returns a proper result
	buildCfg := BuildConfig{
		RepoRoot:        absRepo,
		PlanPath:        *planFile,
		PolicyPath:      *policy,
		Workers:         *workers,
		AuthMode:        *authMode,
		ClaudeBinary:    *claudeBin,
		CodexBinary:     *codexBin,
		ClaudeConfigDir: *claudeConfigDir,
		CodexHome:       *codexHome,
		ClaudePoolDirs:  claudePoolDirs,
		CodexPoolDirs:   codexPoolDirs,
		BuildCommand:    *buildC,
		TestCommand:     *testC,
		LintCommand:     *lintC,
		ROIFilter:       *roiFilter,
		UseSQLite:       *useSQLite,
		SpecExec:        *specExec,
		Timeout:         *timeout,
		EnvBackend:      *envBackend,
		EnvImage:        *envImage,
		EnvSize:         *envSize,
		SchedulerAlgo:   *schedulerAlgo,
	}

	buildReport, err := runBuild(buildCfg)
	if err != nil {
		fatal("build: %v", err)
	}

	// Exit with error if any tasks failed (important for ship integration)
	if !buildReport.Success {
		fmt.Printf("\n  Build completed with %d failed task(s)\n", buildReport.TasksFailed)
		os.Exit(1)
	}
}

// --- sow: execute a Statement of Work ---

func sowCmd(args []string) {
	fs := flag.NewFlagSet("sow", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	sowFile := fs.String("file", "", "SOW file (default: stoke-sow.json)")
	policy := fs.String("policy", "", "Path to stoke.policy.yaml")
	dryRun := fs.Bool("dry-run", false, "Show SOW summary without executing")
	validate := fs.Bool("validate", false, "Validate SOW and exit")
	workers := fs.Int("workers", 4, "Max parallel agents per session")
	authMode := fs.String("mode", "mode1", "Auth mode")
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	codexHome := fs.String("codex-home", "", "CODEX_HOME")
	buildC := fs.String("build-cmd", "", "Build command (auto-detected)")
	testC := fs.String("test-cmd", "", "Test command (auto-detected)")
	lintC := fs.String("lint-cmd", "", "Lint command (auto-detected)")
	runnerMode := fs.String("runner", "claude", "Runner backend: claude, codex, native, hybrid")
	nativeAPIKey := fs.String("native-api-key", "", "API key for native runner")
	nativeModel := fs.String("native-model", "claude-sonnet-4-6", "Model for native runner")
	nativeBaseURL := fs.String("native-base-url", "", "Base URL for native runner (e.g. http://localhost:8000 for LiteLLM)")
	// Reviewer/judge model can be different from the worker model. Research
	// finding: the verifier is the bottleneck — convergence depends on
	// critic > generator capability. When the same model judges its own
	// kind of output, you get confirmation bias (~54% accuracy on self-
	// review per CRITIC paper). Set --reasoning-model to a stronger model
	// (e.g. claude-opus, gpt-5) to break out of that ceiling. Empty =
	// fall back to --native-model (current behavior, backward compatible).
	reasoningModel := fs.String("reasoning-model", "", "Override the model used for judges/reviewers/decomposers (defaults to --native-model). Recommended: a stronger model than the worker — research shows convergence depends on critic > generator.")
	reasoningBaseURL := fs.String("reasoning-base-url", "", "Override the base URL for the reasoning provider (defaults to --native-base-url).")
	reasoningAPIKey := fs.String("reasoning-api-key", "", "Override the API key for the reasoning provider (defaults to --native-api-key).")
	// Model-source flags: the BUILDER/REVIEWER x MODEL/SOURCE/URL/API_KEY
	// matrix. Each flag takes precedence over its matching env var, which
	// in turn takes precedence over the legacy --native-*/--reasoning-*
	// flags above. Sources: litellm (default, routes through local
	// LiteLLM gateway), openrouter (openrouter.ai), direct (vendor
	// endpoint inferred from the model alias). Models: sonnet / opus /
	// haiku / gemini / flash / codex / gpt-5 / litellm, or any exact
	// vendor model ID. See internal/modelsource for the full resolution
	// table. When GEMINI_KEY is set and no reviewer flags/env are
	// specified, the reviewer role auto-routes to gemini direct so the
	// operator gets a second-perspective reviewer by setting one env var.
	builderModelFlag := fs.String("builder-model", "", "Builder (worker) model alias or exact ID (sonnet|opus|gemini|codex|litellm|<vendor-id>). Overrides BUILDER_MODEL env.")
	builderSourceFlag := fs.String("builder-source", "", "Builder source (litellm|openrouter|direct). Overrides BUILDER_SOURCE env.")
	builderURLFlag := fs.String("builder-url", "", "Builder endpoint URL (source=direct). Overrides BUILDER_URL env.")
	builderAPIKeyFlag := fs.String("builder-api-key", "", "Builder API key. Overrides BUILDER_API_KEY env.")
	reviewerModelFlag := fs.String("reviewer-model", "", "Reviewer (judge) model alias or exact ID. Overrides REVIEWER_MODEL env.")
	reviewerSourceFlag := fs.String("reviewer-source", "", "Reviewer source (litellm|openrouter|direct). Overrides REVIEWER_SOURCE env.")
	reviewerURLFlag := fs.String("reviewer-url", "", "Reviewer endpoint URL (source=direct). Overrides REVIEWER_URL env.")
	reviewerAPIKeyFlag := fs.String("reviewer-api-key", "", "Reviewer API key. Overrides REVIEWER_API_KEY env.")
	roiFilter := fs.String("roi", "medium", "ROI threshold: high, medium, low, skip")
	specExec := fs.Bool("specexec", false, "Enable speculative parallel execution")
	// SOW builds are long-running (hours-to-days for large SOWs). Hard timeout
	// is disabled by default; supervisor handles liveness. Set --timeout to a
	// non-zero duration to re-enable a safety ceiling.
	timeout := fs.Duration("timeout", 0, "Hard wall-clock timeout (0 = supervisor-driven, recommended)")
	resume := fs.Bool("resume", false, "Resume from prior .stoke/sow-state.json: skip completed sessions")
	fresh := fs.Bool("fresh", false, "Clear cached SOW state before running: session markers, scheduler state, cached SOW conversions/refinements, env-var classifier cache. Preserves wisdom, reports, and code. Incompatible with --resume (use one or the other).")
	// Tri-state: "" = auto (on for multi-session, off for single-
	// session), "true" / "false" = explicit override. A multi-session
	// SOW like PERSYS (13 sessions) should try all sessions even if
	// one fails, otherwise the user hits "S1 worked but S2-S13 never
	// ran" after a transient failure. Single-session runs halt on
	// failure because there's nothing else to do.
	continueOnFailureFlag := fs.String("continue-on-failure", "", "Keep running subsequent sessions after a session fails (true/false/auto). Default: auto — true if SOW has >1 session, false otherwise.")
	maxRetries := fs.Int("session-retries", 2, "Retry attempts per session (tasks + acceptance) before giving up")
	parallelSessions := fs.Int("parallel", 0, "Run up to N sessions concurrently via the DAG-driven scheduler (0 = legacy sequential; 2-4 recommended). Sessions are parallelized when their declared Inputs/Outputs or file scopes prove independence; declaration order is the safe fallback.")
	smokeEnabled := fs.Bool("smoke", true, "Run an environment-aware smoke check after each session's ACs pass. Failing smoke flips the session to failed; static-only (e.g. iOS target on Linux) is reported but does not block. Disable only for debugging; this is the #1 anti-fake gate.")
	forceFeasibility := fs.Bool("force", false, "Skip the feasibility gate. Operator explicitly acknowledges that the SOW may reference external services without documentation and that stoke will NOT synthesize mocks. Use only when you know something the gate doesn't.")
	docsDir := fs.String("docs-dir", "", "Directory of additional API documentation (markdown files) the feasibility gate can consult. Each file is appended to the SOW-view the gate evaluates; useful when vendor docs are stored alongside the repo but not pasted into the SOW.")
	maxRepairAttempts := fs.Int("repair-attempts", 3, "Per-session self-repair attempts (run acceptance, feed failures back, retry)")
	costBudget := fs.Float64("cost-budget", 0, "Total cost budget in USD for the SOW run (0 = unlimited)")
	autoCritique := fs.Bool("sow-critique", true, "When a prose SOW is converted, run a critique+refine pass before execution")
	repomapBudget := fs.Int("repomap-tokens", 3000, "Max characters of repo map to inject into task prompts (0 = disable)")
	enableWisdom := fs.Bool("wisdom", true, "Capture per-session learnings (patterns/decisions/gotchas) and inject them into later sessions")
	enableCrossReview := fs.Bool("cross-review", true, "After each successful session, run a cross-model code review over the git diff before accepting the session")
	reviewModel := fs.String("review-model", "", "Model name used for cross-model review (default: same as --native-model)")
	strictScope := fs.Bool("strict-scope", false, "Fail sessions that touched files outside the declared session.Outputs / task.Files set")
	verboseStream := fs.Bool("verbose-stream", false, "Print raw model streaming output (DeltaText) to stdout. Default off — structural events (tool names, completions, warnings) still print. Useful for debugging, noisy in normal runs.")
	parallelTasks := fs.Int("parallel-tasks", 1, "Concurrent tasks within a session when their file sets are disjoint (1 = sequential)")
	compactThreshold := fs.Int("compact-threshold", 100000, "Progressive context compaction kicks in when a task's estimated input tokens exceed this (0 = disabled)")
	dumpPrompts := fs.Bool("dump-task-prompts", false, "Write every task's system+user prompts to .stoke/prompt-dump/ and exit, without calling the LLM. Used to verify spec extraction before spending on a real run.")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	ensureGitRepoOrFatal(absRepo)

	// --fresh: clear cached SOW state BEFORE anything loads. Deletes
	// session completion markers, scheduler state, cached prose→JSON
	// conversions and refinements, and the env-var classifier cache
	// so the next run starts from scratch. Preserves wisdom/, reports/,
	// meta-reports/, and everything outside .stoke/ (code, git, etc.).
	//
	// Incompatible with --resume — if both are passed, --fresh wins
	// (since there's nothing left to resume from after the clear) and
	// a warning is printed.
	if *fresh {
		if *resume {
			fmt.Fprintln(os.Stderr, "  ⚠ --fresh overrides --resume: clearing cached state, nothing to resume from")
		}
		stokeDir := filepath.Join(absRepo, ".stoke")
		targets := []string{
			filepath.Join(stokeDir, "sow-state-markers"),
			filepath.Join(stokeDir, "sow-state.json"),
			filepath.Join(stokeDir, "sow-from-prose.json"),
			filepath.Join(stokeDir, "sow-refined.json"),
			filepath.Join(stokeDir, "env-var-classification.json"),
		}
		var cleared []string
		for _, t := range targets {
			if _, statErr := os.Stat(t); statErr == nil {
				if rmErr := os.RemoveAll(t); rmErr != nil {
					fmt.Fprintf(os.Stderr, "  ⚠ --fresh: could not remove %s: %v\n", t, rmErr)
					continue
				}
				cleared = append(cleared, filepath.Base(t))
			}
		}
		if len(cleared) > 0 {
			fmt.Printf("  🧹 --fresh: cleared %d cached item(s): %s\n", len(cleared), strings.Join(cleared, ", "))
		} else {
			fmt.Println("  🧹 --fresh: no cached state to clear (already clean)")
		}
	}

	// Load the universal context (coding-standards + known-gotchas)
	// ONCE per sowCmd run. These markdown blobs — embedded defaults
	// plus any $HOME/.stoke or <repoRoot>/.stoke overrides — are
	// injected into every agent's system prompt downstream: coding
	// workers, briefing lead, integration reviewer, task judge,
	// fix-DAG planner, reasoning loops. See internal/skill/universal.go.
	universalCtx := skill.LoadUniversalContext(absRepo)
	csLines := strings.Count(universalCtx.CodingStandards, "\n")
	if strings.TrimSpace(universalCtx.CodingStandards) != "" && !strings.HasSuffix(universalCtx.CodingStandards, "\n") {
		csLines++
	}
	kgLines := strings.Count(universalCtx.KnownGotchas, "\n")
	if strings.TrimSpace(universalCtx.KnownGotchas) != "" && !strings.HasSuffix(universalCtx.KnownGotchas, "\n") {
		kgLines++
	}
	fmt.Printf("🧭 universal context: coding-standards (%d lines), known-gotchas (%d lines), sources: %s\n",
		csLines, kgLines, universalCtx.ShortSources())

	// Load the per-agent / per-scenario / per-phase hook registry.
	// Hooks layer on TOP of the universal context at specific call
	// sites — worker dispatch, judge passes, phase transitions — so
	// users can add narrowly-targeted guidance without code changes.
	hookSet := skill.LoadHookSet(absRepo)
	fmt.Printf("🪝 hooks loaded: agents=%d, scenarios=%d, phases=%d (sources: %s)\n",
		hookSet.AgentCount, hookSet.ScenarioCount, hookSet.PhaseCount, hookSet.ShortSources())

	// Auto-discover LiteLLM BEFORE we need a provider anywhere downstream
	// (prose SOW conversion, critique pass, override judge, native runner).
	// Without this the prose converter silently falls back to api.anthropic.com
	// and 401s when the only key available is a LiteLLM master key.
	if *runnerMode == "native" && *nativeBaseURL == "" {
		if d := litellmPkg.Discover(); d != nil {
			*nativeBaseURL = d.BaseURL
			if *nativeAPIKey == "" && d.APIKey != "" {
				*nativeAPIKey = d.APIKey
			}
			fmt.Printf("  litellm: auto-discovered %s (%d models)\n", d.BaseURL, len(d.Models))
		}
	}

	// Load SOW. Supports three input formats:
	//   - .json / .yaml / .yml → parsed directly
	//   - .txt / .md / prose   → converted via LLM (needs a provider)
	// The prose path requires a functional provider (native runner w/ key
	// OR Anthropic key) because it calls the configured model to turn
	// prose into a structured SOW. Cached to .stoke/sow-from-prose.json
	// keyed on source hash.
	var sow *plan.SOW
	if *sowFile != "" {
		prov, modelName := buildProseProvider(*runnerMode, *nativeAPIKey, *nativeBaseURL, *nativeModel)
		loaded, result, loadErr := plan.LoadSOWFile(*sowFile, absRepo, prov, modelName)
		if loadErr != nil {
			fatal("load SOW: %v", loadErr)
		}
		sow = loaded
		// Deterministic AC command scrub: runs BEFORE the critique
		// pass so obvious anti-patterns ($REPO_URL git clones,
		// "|| echo ok" fallbacks, npx wrappers, etc.) are stripped
		// locally without burning an LLM call. The scrub is safe
		// (regex-based, only removes known-bad subpatterns) and idempotent.
		// Whatever remains goes to the critique model, which now has
		// less noise to wade through.
		if scrubbed, scrubDiag := plan.ScrubSOW(sow); len(scrubDiag) > 0 {
			_ = scrubbed // ScrubSOW mutates in place; assignment is belt-and-suspenders
			fmt.Printf("  scrubbed %d AC command pattern(s) before critique:\n", len(scrubDiag))
			for _, d := range scrubDiag {
				fmt.Printf("    - %s\n", d)
			}
		}
		switch result.Format {
		case "prose":
			fmt.Printf("  converted prose SOW → %s\n", result.ConvertedPath)
			// Auto-critique + refine: turn the LLM's first-pass SOW
			// into something executable. Up to 2 critique/refine
			// cycles; stop when verdict == "ship".
			if *autoCritique && prov != nil {
				fmt.Printf("  running SOW critique pass...\n")
				refined, crit, critErr := plan.CritiqueAndRefine(sow, prov, modelName, 2)
				// Smart-loop philosophy: critique IS the supervisor
				// gate. If it produced a refined SOW we use it; if it
				// produced an error AND no usable refinement, we halt
				// rather than silently proceeding with a SOW the
				// critic flagged as broken. The previous behavior was
				// "warn and run anyway", which made critique
				// informational-only at exactly the moment it
				// mattered. Use --sow-critique=false to opt out
				// entirely if you really want to skip it.
				if critErr != nil && (refined == nil || refined == sow) {
					// Soft-fail: the critic couldn't salvage a refined
					// version (usually an LLM JSON parse issue downstream
					// of critique — hallucinated S0.5 sessions, orphan
					// task deps with new names, unclosed arrays). The
					// ORIGINAL sow already passed ParseSOW + ValidateSOW
					// upstream, so it's safe to proceed with unrefined
					// scope. Loudly log what we're giving up rather than
					// killing the whole run and forcing the operator to
					// re-trigger a $5+ conversion.
					fmt.Fprintf(os.Stderr, "  ⚠ critique gate: refinement failed (%v) — proceeding with the ORIGINAL prose-converted SOW. Scope will not benefit from critique's rule-6 tightening.\n", critErr)
				} else if critErr != nil {
					fmt.Fprintf(os.Stderr, "  critique note: %v (using refined SOW)\n", critErr)
				}
				if refined != nil {
					sow = refined
				}
				if crit != nil {
					fmt.Printf("  critique: %s (score %d/100)\n", crit.Verdict, crit.OverallScore)
					if crit.Summary != "" {
						fmt.Printf("    %s\n", crit.Summary)
					}
					if len(crit.Issues) > 0 {
						fmt.Printf("  %d issues flagged:\n", len(crit.Issues))
						for _, iss := range crit.Issues {
							tag := ""
							if iss.SessionID != "" {
								tag = " [" + iss.SessionID
								if iss.TaskID != "" {
									tag += "/" + iss.TaskID
								}
								tag += "]"
							}
							fmt.Printf("    - [%s]%s %s\n", iss.Severity, tag, iss.Description)
						}
					}
					// Persist the refined SOW next to the cache so a
					// human can inspect what the critic produced.
					if refinedPath := filepath.Join(absRepo, ".stoke", "sow-refined.json"); sow != nil {
						if data, mErr := json.MarshalIndent(sow, "", "  "); mErr == nil {
							_ = os.WriteFile(refinedPath, data, 0o600)
						}
					}
				}
			}
		case "yaml":
			fmt.Printf("  loaded YAML SOW: %s\n", result.OriginalPath)
		default:
			fmt.Printf("  loaded JSON SOW: %s\n", result.OriginalPath)
		}
	} else {
		sow, err = plan.LoadSOWFromDir(absRepo)
	}
	if err != nil {
		fatal("load SOW: %v", err)
	}

	// Auto-repair dangling task dependencies and empty-task slots
	// BEFORE validation. Earlier pipeline stages (ConvertProseToSOW,
	// CritiqueAndRefine) run the cleaner too, but the session-sizer
	// pass between them and this dispatch point can rename/split
	// sessions in ways that leave stragglers. Running the cleaner
	// one last time here is cheap and catches the residue so we
	// don't hit "plan warning: task T87 depends on unknown task T85"
	// at dispatch time — that warning was previously a silent
	// no-op; auto-dropping the dangling ref means downstream
	// scheduling is actually valid.
	if drops := plan.CleanTaskDependencies(sow); len(drops) > 0 {
		fmt.Fprintf(os.Stderr, "  🧹 auto-repair: %d dangling SOW reference(s) dropped before dispatch:\n", len(drops))
		for _, d := range drops {
			fmt.Fprintf(os.Stderr, "     - [%s/%s] %s — %s\n", d.SessionID, d.TaskID, d.Dropped, d.Reason)
		}
	}

	// Validate
	if validationErrs := plan.ValidateSOW(sow); len(validationErrs) > 0 {
		fmt.Fprintf(os.Stderr, "SOW validation errors:\n")
		for _, e := range validationErrs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		if *validate {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr)
	} else if *validate {
		fmt.Println("SOW is valid.")
		return
	}

	// Check infra env vars
	if missing := sow.ValidateInfraEnvVars(); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "Missing infrastructure env vars:\n")
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "  - %s\n", m)
		}
		fmt.Fprintln(os.Stderr)
	}

	// Auto-detect stack from repo
	detectedStack := plan.DetectStackFromRepo(absRepo)

	// Dry run: show summary
	ss := plan.NewSessionScheduler(sow, absRepo)
	ss.Resume = *resume
	// Resolve --continue-on-failure: explicit true/false overrides
	// everything; otherwise auto = on for multi-session SOWs, off for
	// single-session. This matches the user's expected behavior when
	// they hand Stoke a big multi-session scope: "build until it's
	// all done, not just S1".
	continueOnFailure := len(sow.Sessions) > 1 // auto default
	switch strings.ToLower(strings.TrimSpace(*continueOnFailureFlag)) {
	case "true", "yes", "1", "on":
		continueOnFailure = true
	case "false", "no", "0", "off":
		continueOnFailure = false
	case "", "auto":
		// keep auto default
	default:
		fmt.Fprintf(os.Stderr, "  warning: unknown --continue-on-failure value %q; using auto\n", *continueOnFailureFlag)
	}
	ss.ContinueOnFailure = continueOnFailure
	if continueOnFailure {
		fmt.Printf("  continue-on-failure: ON (will attempt all %d sessions, report failures at end)\n", len(sow.Sessions))
	}
	if *maxRetries > 0 {
		ss.MaxSessionRetries = *maxRetries
	}
	if *parallelSessions >= 2 {
		ss.ParallelSessions = *parallelSessions
		fmt.Printf("  parallel-sessions: ON (up to %d concurrent sessions via DAG scheduler)\n", *parallelSessions)
	} else if *workers >= 2 {
		// Implicit parallel-sessions when --workers >= 2. Without this,
		// preempt fix sessions sit behind their parent in sequential
		// Run() and the whole self-heal loop deadlocks. The explicit
		// --parallel-sessions flag still wins when set; this is only a
		// default for users who pass --workers alone.
		ss.ParallelSessions = *workers
		fmt.Printf("  parallel-sessions: ON (implicit from --workers=%d; preempt fix sessions require it)\n", *workers)
	}
	if *smokeEnabled {
		// Smoke gate: environment-aware runtime verification after a
		// session's ACs pass. Wired as a closure so it closes over
		// absRepo + the smoketest package, keeping the scheduler
		// package dependency-free.
		ss.SmokeGate = func(session plan.Session) (kind, reason, output string) {
			// Smoke runner carries its own 5-minute per-command
			// timeout via context.WithTimeout inside Run, so a fresh
			// background context here is safe — we don't need to
			// piggyback on the outer run ctx (which is out of scope
			// at this point in the flag-parsing block) and we'd
			// rather smoke complete on its own clock than be killed
			// mid-verification by an unrelated run-level cancellation.
			v := smoketest.Run(context.Background(), session, absRepo)
			return string(v.Kind), v.Reason, v.Output
		}
		fmt.Println("  smoke-gate: ON (environment-aware runtime verification after AC pass)")
	}
	if err := ss.LoadOrCreateState(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: SOW state init failed: %v\n", err)
	}
	// If a TUI shell is listening, wire session progress into its
	// Sessions pane. This is a no-op in the line REPL and CLI modes.
	if hook := currentShellProgress; hook != nil {
		// Session-start: flip to running state in the Sessions pane so
		// the user sees work begin before any tasks complete.
		ss.OnSessionStart = func(sessionID string, attempt int) {
			// Find the session definition for task/criteria counts
			for _, s := range sow.Sessions {
				if s.ID == sessionID {
					hook(tui.SessionDisplay{
						ID:            s.ID,
						Title:         s.Title,
						Status:        "running",
						TasksTotal:    len(s.Tasks),
						CriteriaTotal: len(s.AcceptanceCriteria),
					})
					break
				}
			}
		}
		ss.OnProgress = func(r plan.SessionResult) {
			status := "done"
			switch {
			case r.Skipped:
				status = "skipped"
			case r.Error != nil:
				status = "failed"
			case !r.AcceptanceMet:
				status = "failed"
			}
			tasksDone := 0
			for _, tr := range r.TaskResults {
				if tr.Success {
					tasksDone++
				}
			}
			critDone := 0
			for _, c := range r.Acceptance {
				if c.Passed {
					critDone++
				}
			}
			errStr := ""
			if r.Error != nil {
				errStr = r.Error.Error()
			}
			hook(tui.SessionDisplay{
				ID:            r.SessionID,
				Title:         r.Title,
				Status:        status,
				TasksDone:     tasksDone,
				TasksTotal:    len(r.TaskResults),
				CriteriaDone:  critDone,
				CriteriaTotal: len(r.Acceptance),
				LastError:     errStr,
			})
		}
		// Seed the sessions pane with pending entries so the user can see
		// what's coming before the first session runs.
		var seed []tui.SessionDisplay
		for _, s := range sow.Sessions {
			status := "pending"
			if ss.State() != nil && ss.State().IsSessionComplete(s.ID) {
				status = "done"
			}
			seed = append(seed, tui.SessionDisplay{
				ID:            s.ID,
				Title:         s.Title,
				Status:        status,
				TasksTotal:    len(s.Tasks),
				CriteriaTotal: len(s.AcceptanceCriteria),
			})
		}
		if seedFn := currentShellSessions; seedFn != nil {
			seedFn(seed)
		}
	}
	if *dryRun {
		fmt.Print(ss.DryRun())
		if detectedStack.Language != "" {
			fmt.Printf("\nDetected: %s", detectedStack.Language)
			if detectedStack.Monorepo != nil {
				fmt.Printf(" [%s]", detectedStack.Monorepo.Tool)
				if detectedStack.Monorepo.Manager != "" {
					fmt.Printf(" (%s)", detectedStack.Monorepo.Manager)
				}
			}
			fmt.Println()
		}
		return
	}

	// Auto-detect commands
	detected := config.DetectCommands(absRepo)
	if *buildC == "" {
		*buildC = detected.Build
	}
	if *testC == "" {
		*testC = detected.Test
	}
	if *lintC == "" {
		*lintC = detected.Lint
	}

	fmt.Printf("SOW %s\n", version)
	fmt.Printf("  sow:     %s (%d sessions, %d total tasks)\n", sow.Name, len(sow.Sessions), countSOWTasks(sow))
	fmt.Printf("  stack:   %s", sow.Stack.Language)
	if sow.Stack.Monorepo != nil {
		fmt.Printf(" [%s]", sow.Stack.Monorepo.Tool)
	}
	fmt.Println()
	fmt.Printf("  runner:  %s", *runnerMode)
	if *runnerMode == "native" && *nativeBaseURL != "" {
		fmt.Printf(" → %s", *nativeBaseURL)
	}
	if *runnerMode == "native" && *nativeModel != "" {
		fmt.Printf("  (%s)", *nativeModel)
	}
	fmt.Println()
	fmt.Printf("  workers: %d", *workers)
	if *parallelTasks > 1 {
		fmt.Printf("  (parallel-tasks: %d)", *parallelTasks)
	}
	fmt.Println()
	fmt.Printf("  build:   %s\n", orNone(*buildC))
	fmt.Printf("  test:    %s\n", orNone(*testC))
	fmt.Printf("  lint:    %s\n", orNone(*lintC))

	// Smart-loop banner: show which guards are active so the user knows
	// what's running. Only print the line if at least one feature is on
	// so the existing single-session quick-runs aren't bloated.
	if *runnerMode == "native" {
		var smartParts []string
		smartParts = append(smartParts, fmt.Sprintf("repair:%d", *maxRepairAttempts))
		if *autoCritique {
			smartParts = append(smartParts, "critique")
		}
		if *enableWisdom {
			smartParts = append(smartParts, "wisdom")
		}
		if *enableCrossReview {
			smartParts = append(smartParts, "cross-review")
		}
		if *strictScope {
			smartParts = append(smartParts, "strict-scope")
		}
		if *compactThreshold > 0 {
			smartParts = append(smartParts, fmt.Sprintf("compact@%d", *compactThreshold))
		}
		if *costBudget > 0 {
			smartParts = append(smartParts, fmt.Sprintf("budget=$%.2f", *costBudget))
		}
		if len(smartParts) > 0 {
			fmt.Printf("  smart:   %s\n", strings.Join(smartParts, ", "))
		}
	}
	fmt.Println()

	// Convert SOW to flat plan with session gates
	p := sow.ToPlan()

	// ROI filter
	var roiClass plan.ROIClass
	switch *roiFilter {
	case "high":
		roiClass = plan.ROIHigh
	case "medium":
		roiClass = plan.ROIMedium
	case "low":
		roiClass = plan.ROILow
	case "skip":
		roiClass = plan.ROISkip
	default:
		roiClass = plan.ROIMedium
	}
	kept, filtered := plan.FilterByROI(p.Tasks, roiClass)
	if len(filtered) > 0 {
		fmt.Printf("  ROI filter removed %d task(s)\n\n", len(filtered))
		p.Tasks = kept
	}

	// Type-route tasks
	for i := range p.Tasks {
		if p.Tasks[i].Type == "" {
			p.Tasks[i].Type = string(model.InferTaskType(p.Tasks[i].Description))
		}
	}

	// Auto-repair dangling task deps BEFORE validation. The sow flow
	// has a dedicated CleanTaskDependencies call for SOW-shaped
	// input; this fast path takes a plan.Plan and needs the
	// equivalent sweep so refiner-dropped deps don't blackhole the
	// DAG scheduler. Each dropped ref is surfaced as a warning with
	// the referenced-but-missing task ID.
	if drops := plan.CleanPlanTaskDependencies(p); len(drops) > 0 {
		fmt.Fprintf(os.Stderr, "  🧹 auto-repair: %d dangling plan reference(s) dropped:\n", len(drops))
		for _, d := range drops {
			fmt.Fprintf(os.Stderr, "     - task %s: dropped dep on missing %s\n", d.TaskID, d.Dropped)
		}
	}

	// Validate plan
	if planErrs := p.Validate(); len(planErrs) > 0 {
		for _, e := range planErrs {
			fmt.Fprintf(os.Stderr, "  plan warning: %s\n", e)
		}
	}

	// Execute session-by-session with acceptance criteria gates.
	// No wall-clock timeout by default: the supervisor is authoritative.
	var ctx context.Context
	var cancel context.CancelFunc
	if *timeout > 0 {
		sigCtx, sigCancel := signalContext(context.Background())
		defer sigCancel()
		ctx, cancel = context.WithTimeout(sigCtx, *timeout)
	} else {
		ctx, cancel = signalContext(context.Background())
	}
	defer cancel()

	// FAST PATH: when the native runner is selected, bypass runBuild
	// entirely. runBuild delegates to the single-task workflow engine
	// which expects a pre-existing codebase, git worktree, plan phase,
	// execute phase, verify phase, and merge — none of which are the
	// right shape for a greenfield multi-session SOW. The native fast
	// path drives the agentloop directly against absRepo for each task.
	var nativeExec func(ctx context.Context, session plan.Session) ([]plan.TaskExecResult, error)
	// runCostCapture is set at the end of the native-runner block so
	// the post-run meta-reasoner (which lives OUTSIDE that block's
	// scope) can populate MetaRunTelemetry.TotalCostUSD without
	// re-scoping the sharedSpent pointer.
	var runCostCapture *float64
	var runTotalCost float64
	if *runnerMode == "native" {
		nativeKey := *nativeAPIKey
		if nativeKey == "" {
			for _, k := range []string{"LITELLM_API_KEY", "LITELLM_MASTER_KEY", "ANTHROPIC_API_KEY"} {
				if v := os.Getenv(k); v != "" {
					nativeKey = v
					break
				}
			}
		}
		// Auto-discover LiteLLM proxy when no base URL provided.
		if *nativeBaseURL == "" {
			if d := litellmPkg.Discover(); d != nil {
				*nativeBaseURL = d.BaseURL
				if nativeKey == "" && d.APIKey != "" {
					nativeKey = d.APIKey
				}
				fmt.Printf("  litellm: auto-discovered %s (%d models)\n", d.BaseURL, len(d.Models))
			}
		}
		if nativeKey == "" && *nativeBaseURL != "" {
			nativeKey = provider.LocalLiteLLMStub
		}
		if nativeKey == "" {
			fatal("SOW fast path requires a native API key: set --native-api-key or one of LITELLM_API_KEY/LITELLM_MASTER_KEY/ANTHROPIC_API_KEY")
		}
		nativeModelName := *nativeModel
		if nativeModelName == "" {
			nativeModelName = "claude-sonnet-4-6"
		}

		// Precompute reviewer-side provider + model BEFORE the native
		// runner is constructed, because modelsource.ResolveRole for
		// the builder role needs to run first — otherwise overrides
		// like BUILDER_SOURCE=openrouter end up too late: the runner
		// has already been built with the legacy Anthropic values.
		// This is the codex-review P1 on fee0de4.
		reasoningKey := *reasoningAPIKey
		if reasoningKey == "" {
			reasoningKey = nativeKey
		}
		reasoningURL := *reasoningBaseURL
		if reasoningURL == "" {
			reasoningURL = *nativeBaseURL
		}
		var reasoningProv provider.Provider
		if reasoningURL != *nativeBaseURL || reasoningKey != nativeKey {
			reasoningProv = provider.NewAnthropicProvider(reasoningKey, reasoningURL)
		} else {
			reasoningProv = provider.NewAnthropicProvider(nativeKey, *nativeBaseURL)
		}
		reasoningModelChoice := *reasoningModel
		if reasoningModelChoice == "" {
			reasoningModelChoice = nativeModelName
		}

		// Resolve BUILDER_* first so nativeKey / nativeBaseURL /
		// nativeModelName are correct by the time we build the runner.
		nativeBaseURLForRunner := *nativeBaseURL
		if br, changed, err := modelsource.ResolveRole(modelsource.RoleBuilder,
			*builderModelFlag, *builderSourceFlag, *builderURLFlag, *builderAPIKeyFlag,
			nativeModelName, *nativeBaseURL, nativeKey); err != nil {
			fatal("builder model-source: %v", err)
		} else if changed && br != nil {
			fmt.Printf("  🧩 builder via modelsource: %s @ %s (source=%s)\n", br.Model, br.Endpoint, br.Source)
			if br.Model != "" {
				nativeModelName = br.Model
			}
			// Swap the runner's endpoint + key only when the builder
			// Resolved carried them (direct / openrouter). For litellm
			// we keep the legacy --native-base-url to preserve the
			// operator's explicit proxy configuration.
			if br.Endpoint != "" {
				nativeBaseURLForRunner = br.Endpoint
			}
			// The builder provider comes back ready to go, but the
			// existing engine.NewNativeRunner constructor takes raw
			// key + model rather than a provider; the simplest wiring
			// is to update the key/URL the constructor sees so the
			// runner's internal provider matches what ResolveRole picked.
			// When the operator supplied a direct API key via flag,
			// propagate it through so the runner authenticates at the
			// right endpoint.
			if br.Source == modelsource.SourceDirect || br.Source == modelsource.SourceOpenRouter {
				// Extract the API key the modelsource Build() resolved;
				// since Build() doesn't currently surface it separately,
				// prefer the operator's explicit flag when given.
				if *builderAPIKeyFlag != "" {
					nativeKey = *builderAPIKeyFlag
				}
			}
		}

		// Resolve REVIEWER_* after the builder so the reviewer's legacy
		// fallback (reasoningModelChoice / reasoningURL / reasoningKey)
		// reflects the final builder-resolved values.
		if rr, changed, err := modelsource.ResolveRole(modelsource.RoleReviewer,
			*reviewerModelFlag, *reviewerSourceFlag, *reviewerURLFlag, *reviewerAPIKeyFlag,
			reasoningModelChoice, reasoningURL, reasoningKey); err != nil {
			fatal("reviewer model-source: %v", err)
		} else if changed && rr != nil {
			fmt.Printf("  🧩 reviewer via modelsource: %s @ %s (source=%s)\n", rr.Model, rr.Endpoint, rr.Source)
			reasoningProv = rr.Provider
			if rr.Model != "" {
				reasoningModelChoice = rr.Model
			}
		}

		if reasoningModelChoice != nativeModelName || reasoningURL != *nativeBaseURL {
			fmt.Printf("  🔍 reviewer model split: workers=%s @ %s, reviewers/judges=%s @ %s\n",
				nativeModelName, *nativeBaseURL, reasoningModelChoice, reasoningURL)
		}

		runner := engine.NewNativeRunner(nativeKey, nativeModelName)
		runner.BaseURL = nativeBaseURLForRunner

		// Build a repo map once so every task prompt can inject the
		// ranked codebase view. If this fails we proceed without it.
		var sowRepoMap *repomap.RepoMap
		if *repomapBudget > 0 {
			if rm, rmErr := repomap.Build(absRepo); rmErr == nil {
				sowRepoMap = rm
			}
		}

		// Cost budget is tracked across the entire SOW run, not per
		// session — one shared pointer lets every session see the
		// cumulative spend.
		sharedSpent := new(float64)
		// Expose the counter to the outer-scope capture so the
		// post-run meta-reasoner can read the final cost after this
		// block exits.
		runCostCapture = sharedSpent

		// Load (or create) the CTO-approved ignore list so the
		// override flow can accumulate across runs.
		ignoreList, ignoreErr := convergence.LoadIgnores(absRepo)
		if ignoreErr != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not load ignores: %v\n", ignoreErr)
			ignoreList = &convergence.IgnoreList{Version: 1}
		}

		// Build an override judge using the same provider the native
		// runner is using. When it's unavailable, the override flow is
		// skipped gracefully.
		var overrideJudge convergence.OverrideJudge
		if prov := provider.NewAnthropicProvider(nativeKey, *nativeBaseURL); prov != nil {
			overrideJudge = &convergence.LLMOverrideJudge{
				Provider: prov,
				Model:    nativeModelName,
			}
		}

		// Reasoning provider / reviewer model already resolved above,
		// before the native runner was constructed, so modelsource
		// BUILDER_* / REVIEWER_* overrides land in the right place.

		// Continuation callback: turn CTO-surfaced continuations into
		// a new session the scheduler will pick up. Uses AppendSession
		// which extends the live session list.
		//
		// Cascade guard: we cap the continuation chain depth so an
		// unsolvable failing criterion can't spawn S1 -> S1-cont ->
		// S1-cont-cont -> ... indefinitely. The Sentinel SOW run
		// surfaced this: every continuation for an escalated session
		// got stuck on the same 1 criterion, the CTO judge kept
		// surfacing it as "still needed", and AppendSession kept
		// creating new -cont sessions for it. Each iteration burned
		// LLM calls and made zero progress.
		//
		// maxCascadeDepth: 2 allows one round of auto-remediation
		// (S1 -> S1-cont) plus one retry (S1-cont -> S1-cont-cont),
		// then halts. Any deeper cascade is classified as
		// "non-converging" and surfaced to the final SOW report for
		// operator attention.
		const maxCascadeDepth = 2
		continuationCallback := func(fromSession string, items []string, overrideCtx ContinuationContext) {
			if len(items) == 0 {
				return
			}
			// Count how deep in the cascade this continuation would
			// be. "-cont" suffixes in the parent ID tell us; each
			// suffix adds a depth level. e.g. "S1" -> depth 0, so
			// creating S1-cont = depth 1. "S1-cont-cont" -> depth 2,
			// so creating S1-cont-cont-cont = depth 3 (blocked).
			depth := strings.Count(fromSession, "-cont")
			if depth >= maxCascadeDepth {
				// Cascade cap reached. Instead of surrendering, hand
				// the diagnostic context to the root-cause planner:
				// a tool-authoritative agent that verifies the sticky
				// ACs' root cause against the repo and returns a
				// dependency-ordered DAG of fix tasks. We promote the
				// DAG via AppendSession so the scheduler picks up a
				// new session whose intra-task Dependencies drive
				// correct ordering. Falls through to the old hard-cap
				// behavior on any failure path (nil provider, planner
				// error, planner abandon).
				if reasoningProv == nil {
					fmt.Printf("  ✗ cascade cap reached for %s (depth %d) and no reasoning provider configured — surrendering\n", fromSession, depth)
					fmt.Printf("    items: %v\n", items)
					return
				}
				fmt.Printf("  ⏭ cascade cap reached for %s (depth %d). Invoking root-cause planner...\n", fromSession, depth)
				fmt.Printf("  🔬 researching: %d sticky AC, %d repair attempts already tried\n", len(overrideCtx.StickyACs), len(overrideCtx.RepairHistory))
				if block := universalCtx.PromptBlock(); strings.TrimSpace(block) != "" {
					fmt.Printf("  🧭 universal context injected (fix-DAG): %s\n", universalCtx.ShortSources())
				}
				dagInput := plan.FixDAGInput{
					RepoRoot:             absRepo,
					FromSessionID:        fromSession,
					FromSessionTitle:     overrideCtx.FromSessionTitle,
					StickyACs:            overrideCtx.StickyACs,
					RepairHistory:        overrideCtx.RepairHistory,
					SOWSpec:              overrideCtx.SOWSpec,
					UniversalPromptBlock: skill.ConcatPromptBlocks(universalCtx.PromptBlock(), hookSet.PromptBlock(skill.HookSelector{Kind: "agents", Name: "judge-fix-dag-planner"})),
				}
				dag, err := plan.PlanFixDAG(ctx, reasoningProv, reasoningModelChoice, dagInput)
				if err != nil {
					fmt.Printf("    ⚠ root-cause planner: %v — falling through to hard cap\n", err)
					return
				}
				if dag == nil || dag.Abandon || len(dag.Tasks) == 0 {
					reason := "no tasks proposed"
					if dag != nil && dag.AbandonReason != "" {
						reason = dag.AbandonReason
					}
					fmt.Printf("    ⏹ root-cause planner abandoned: %s\n", reason)
					return
				}
				fixSession, aerr := plan.ApplyFixDAG(*dag, fromSession, "root-cause fix from "+fromSession)
				if aerr != nil {
					fmt.Printf("    ⚠ apply fix DAG: %v — falling through\n", aerr)
					return
				}
				ss.AppendSession(fixSession)
				fmt.Printf("  ✅ promoted root-cause fix session %s with %d DAG task(s)\n", fixSession.ID, len(fixSession.Tasks))
				if strings.TrimSpace(dag.ResearchSummary) != "" {
					summary := dag.ResearchSummary
					if len(summary) > 300 {
						summary = summary[:297] + "..."
					}
					fmt.Printf("     research: %s\n", summary)
				}
				for _, t := range fixSession.Tasks {
					depNote := "no deps"
					if len(t.Dependencies) > 0 {
						short := make([]string, 0, len(t.Dependencies))
						prefix := fromSession + "-fix-"
						for _, d := range t.Dependencies {
							short = append(short, strings.TrimPrefix(d, prefix))
						}
						depNote = "deps: " + strings.Join(short, ", ")
					}
					desc := t.Description
					if len(desc) > 120 {
						desc = desc[:117] + "..."
					}
					fmt.Printf("     - %s: %s (%s)\n", t.ID, desc, depNote)
				}
				return
			}
			contID := fmt.Sprintf("%s-cont", fromSession)
			cont := plan.Session{
				ID:          contID,
				Title:       "continuation from " + fromSession,
				Description: "work surfaced by the CTO judge after session " + fromSession + " acceptance criteria failed",
			}
			for i, item := range items {
				cont.Tasks = append(cont.Tasks, plan.Task{
					ID:          fmt.Sprintf("%s-t%d", contID, i+1),
					Description: item,
				})
			}
			// No explicit criteria — the continuation session will
			// inherit baseline criteria from inferBaselineCriteria via
			// runSessionNative, so it still gets verified.
			ss.AppendSession(cont)
			fmt.Printf("  appended continuation session %s with %d tasks (cascade depth %d/%d)\n", contID, len(items), depth+1, maxCascadeDepth)
		}

		// Wisdom store: load any prior snapshot for this SOW so a
		// resume picks up learnings from earlier runs. New sessions
		// append to it and we persist after each session.
		var wisdomStore *wisdom.Store
		var wisdomProv provider.Provider
		if *enableWisdom {
			if store, wErr := LoadWisdom(absRepo, sow.ID); wErr == nil {
				wisdomStore = store
			} else {
				wisdomStore = wisdom.NewStore()
			}
			// Share the same provider as the build runner — usually
			// the same key + base URL works for the extraction call.
			wisdomProv = provider.NewAnthropicProvider(nativeKey, *nativeBaseURL)
		}

		// Cross-model reviewer: use the configured --review-model if
		// set, otherwise the same model as the build runner. We still
		// construct a separate Provider instance so the request config
		// can differ (future: lower temperature, different max tokens).
		var reviewProv provider.Provider
		if *enableCrossReview {
			reviewProv = provider.NewAnthropicProvider(nativeKey, *nativeBaseURL)
		}
		reviewModelName := *reviewModel
		if reviewModelName == "" {
			reviewModelName = nativeModelName
		}

		// Lead-dev briefing provider: reuses the same key + URL so
		// the pre-Phase-1 briefing pass runs against the same model
		// pool. Always constructed. The briefing pass runs once per
		// session before any task dispatches, so cost is bounded at
		// 1 extra LLM call per session.
		briefingProv := provider.NewAnthropicProvider(nativeKey, *nativeBaseURL)

		// Load the raw SOW text — prose source if the original was
		// prose, marshaled JSON otherwise. This gets injected into
		// the cached system prompt so the agent can always cross-
		// reference specific identifiers against the actual spec.
		rawSOWText := loadRawSOWText(*sowFile, sow)

		// Event bus for SOW native path: the default observers (flowtrack,
		// consent gate) plus the workspace reconciler, which watches
		// package.json edits and runs pnpm install between tasks so the
		// next task starts with a consistent node_modules graph. This
		// removes an entire class of "cannot find module" repair loops
		// the Sentinel SOW run kept hitting.
		sowBus := newEventBus()
		reconciler := hubbuiltin.NewWorkspaceReconciler(absRepo)
		reconciler.Register(sowBus)

		nativeCfg := sowNativeConfig{
			RepoRoot:          absRepo,
			Runner:            runner,
			EventBus:          sowBus,
			MaxTurns:          100,
			MaxRepairAttempts: *maxRepairAttempts,
			Model:             nativeModelName,
			SOWName:           sow.Name,
			// Shared overflow budget: once a task has promoted its
			// leftover scope to a new session, subsequent sibling
			// reviews short-circuit. Prevents the T6-style spiral
			// where the same reviewer-rejects-task cycle fires once
			// per sibling directive at depth 3.
			overflowBudget: &sync.Map{},
			SOWDesc:           sow.Description,
			RepoMap:           sowRepoMap,
			RepoMapBudget:     *repomapBudget,
			CostBudgetUSD:     *costBudget,
			spent:             sharedSpent,
			OverrideJudge:     overrideJudge,
			Ignores:           ignoreList,
			UniversalContext:  universalCtx,
			Hooks:             hookSet,
			OnContinuations: continuationCallback,
			// OnSessionEscalation: unconditional PlanFixDAG entry. Fires
			// on EVERY session escalation that has sticky failing ACs,
			// regardless of whether the CTO judge produced continuation
			// items. Without this, the planner never engaged on Sentinel
			// runs because the override judge consistently produced zero
			// continuations and the OnContinuations callback was the
			// only path to PlanFixDAG.
			OnSessionEscalation: func(fromSessionID, fromSessionTitle string, overrideCtx ContinuationContext) {
				if reasoningProv == nil || len(overrideCtx.StickyACs) == 0 {
					return
				}
				fmt.Printf("  ⏭ session %s escalated with %d sticky AC(s). Invoking root-cause planner...\n", fromSessionID, len(overrideCtx.StickyACs))
				if block := universalCtx.PromptBlock(); strings.TrimSpace(block) != "" {
					fmt.Printf("  🧭 universal context injected (fix-DAG): %s\n", universalCtx.ShortSources())
				}
				dagInput := plan.FixDAGInput{
					RepoRoot:             absRepo,
					FromSessionID:        fromSessionID,
					FromSessionTitle:     fromSessionTitle,
					StickyACs:            overrideCtx.StickyACs,
					RepairHistory:        overrideCtx.RepairHistory,
					SOWSpec:              overrideCtx.SOWSpec,
					UniversalPromptBlock: skill.ConcatPromptBlocks(universalCtx.PromptBlock(), hookSet.PromptBlock(skill.HookSelector{Kind: "agents", Name: "judge-fix-dag-planner"})),
				}
				dagCtx, dagCancel := context.WithTimeout(ctx, 10*time.Minute)
				defer dagCancel()
				dag, derr := plan.PlanFixDAG(dagCtx, reasoningProv, reasoningModelChoice, dagInput)
				if derr != nil {
					fmt.Printf("    ⚠ root-cause planner: %v — moving on\n", derr)
					return
				}
				if dag == nil || dag.Abandon || len(dag.Tasks) == 0 {
					reason := "no tasks proposed"
					if dag != nil && dag.AbandonReason != "" {
						reason = dag.AbandonReason
					}
					fmt.Printf("    ⏹ root-cause planner abandoned: %s\n", reason)
					return
				}
				fixSession, aerr := plan.ApplyFixDAG(*dag, fromSessionID, "root-cause fix from "+fromSessionTitle)
				if aerr != nil {
					fmt.Printf("    ⚠ apply fix DAG: %v — moving on\n", aerr)
					return
				}
				ss.AppendSession(fixSession)
				researchSummary := dag.ResearchSummary
				if len(researchSummary) > 200 {
					researchSummary = researchSummary[:200] + "…"
				}
				fmt.Printf("  ✅ promoted root-cause fix session %s with %d DAG task(s)\n     research: %s\n", fixSession.ID, len(fixSession.Tasks), researchSummary)
				for _, t := range fixSession.Tasks {
					depList := ""
					if len(t.Dependencies) > 0 {
						depList = " (deps: " + strings.Join(t.Dependencies, ", ") + ")"
					}
					desc := t.Description
					if len(desc) > 80 {
						desc = desc[:80] + "…"
					}
					fmt.Printf("     - %s: %s%s\n", t.ID, desc, depList)
				}
			},
			OnDecompOverflow: func(fromTaskID, fromSessionID string, subDirectives []string) {
				// Promote per-task decomp overflow to first-class scope.
				// When reviewAndFollowupRecursive hits its depth cap but
				// the decomposer still has productive sub-directives,
				// make each one a task in a new session rather than
				// dropping them. The new session gets briefing +
				// scope-aware review + decomposition with a fresh budget.
				if len(subDirectives) == 0 {
					return
				}
				newSessionID := fromSessionID + "-deep-" + fromTaskID
				newSession := plan.Session{
					ID:          newSessionID,
					Title:       "deep decomp overflow from " + fromTaskID,
					Description: "sub-directives promoted from " + fromTaskID + " after review recursion cap; each becomes its own reviewed task with fresh decomp budget",
				}
				for i, directive := range subDirectives {
					newSession.Tasks = append(newSession.Tasks, plan.Task{
						ID:          fmt.Sprintf("%s-t%d", newSessionID, i+1),
						Description: directive,
					})
				}
				ss.AppendSession(newSession)
				fmt.Printf("    ⬆ promoted %d overflow task(s) into new session %s\n", len(subDirectives), newSessionID)
			},
			OnTaskAbandon: func(originalTask plan.Task, fromSessionID, abandonReason string) bool {
				// Task-level escalation to the root-cause planner. When
				// the decomposer gives up, the task is not "done" — we
				// invoke PlanFixDAG scoped to this single task with its
				// abandon reason framed as a sticky AC. If the planner
				// produces a viable recovery plan, we append it as a new
				// session and the harness keeps trying. Only when the
				// planner ALSO abandons does the caller surface the task
				// as TRULY BLOCKED (operator-requiring signal).
				if reasoningProv == nil {
					return false
				}
				fmt.Printf("  ⏭ task %s abandoned by decomposer: %s — invoking root-cause planner\n", originalTask.ID, truncateForLog(abandonReason, 180))
				stickyAC := plan.StickyACContext{
					ACID:        "decomposer-abandon-" + originalTask.ID,
					Description: "task " + originalTask.ID + " (" + originalTask.Description + ") decomposer concluded structurally unfixable at its scope; find a broader recovery path",
					LastOutput:  abandonReason,
				}
				dagInput := plan.FixDAGInput{
					RepoRoot:             absRepo,
					FromSessionID:        fromSessionID,
					FromSessionTitle:     "task " + originalTask.ID,
					StickyACs:            []plan.StickyACContext{stickyAC},
					RepairHistory:        []string{"decomposer abandoned: " + abandonReason},
					SOWSpec:              loadRawSOWText(*sowFile, sow),
					UniversalPromptBlock: skill.ConcatPromptBlocks(universalCtx.PromptBlock(), hookSet.PromptBlock(skill.HookSelector{Kind: "agents", Name: "judge-fix-dag-planner"})),
				}
				dagCtx, dagCancel := context.WithTimeout(ctx, 10*time.Minute)
				defer dagCancel()
				dag, derr := plan.PlanFixDAG(dagCtx, reasoningProv, reasoningModelChoice, dagInput)
				if derr != nil {
					fmt.Printf("    ⚠ task-escalation planner: %v\n", derr)
					return false
				}
				if dag == nil || dag.Abandon || len(dag.Tasks) == 0 {
					reason := "no recovery plan"
					if dag != nil && dag.AbandonReason != "" {
						reason = dag.AbandonReason
					}
					fmt.Printf("    ⏹ task-escalation planner also abandoned: %s\n", reason)
					return false
				}
				fixSession, aerr := plan.ApplyFixDAG(*dag, fromSessionID, "task-escalation fix from "+originalTask.ID)
				if aerr != nil {
					fmt.Printf("    ⚠ apply task-escalation fix DAG: %v\n", aerr)
					return false
				}
				ss.AppendSession(fixSession)
				fmt.Printf("  ✅ promoted task-escalation fix session %s with %d task(s)\n", fixSession.ID, len(fixSession.Tasks))
				for _, t := range fixSession.Tasks {
					desc := t.Description
					if len(desc) > 80 {
						desc = desc[:80] + "…"
					}
					fmt.Printf("     - %s: %s\n", t.ID, desc)
				}
				return true
			},
			Wisdom:            wisdomStore,
			WisdomProvider:    wisdomProv,
			SOWID:             sow.ID,
			ReviewProvider:    reviewProv,
			ReviewModel:       reviewModelName,
			ReasoningProvider: reasoningProv,
			ReasoningModel:    reasoningModelChoice,
			BriefingProvider:  briefingProv,
			BriefingModel:     nativeModelName,
			StrictScope:       *strictScope,
			VerboseStream:     *verboseStream,
			ParallelWorkers:   *parallelTasks,
			CompactThreshold:  *compactThreshold,
			RawSOWText:        rawSOWText,
		}

		// Load up to 3 most recent meta-reports from prior runs on
		// this repo and render their prevention rules into a briefing
		// block. The block is threaded through every session's
		// lead-dev briefing pass so briefings can preempt failure
		// classes that already burned previous runs. Missing dir or
		// malformed reports degrade to "no learnings" — never an
		// error.
		if priors, perr := plan.LoadRecentMetaReports(absRepo, 3); perr == nil && len(priors) > 0 {
			nativeCfg.PriorLearnings = plan.FormatPriorLearningsForBriefing(priors)
			if nativeCfg.PriorLearnings != "" {
				fmt.Printf("  loaded %d prior meta-report(s) for briefing context\n", len(priors))
			}
		} else if perr != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not load prior meta-reports: %v\n", perr)
		}
		// sessionAttemptCount tracks how many times we've seen each
		// session ID so the header shows attempt N/total instead of
		// printing "--- Session S1: ..." twice and leaving the
		// operator unsure whether that's a retry, a continuation, or
		// a different session entirely. Attempts are bumped by the
		// SessionScheduler's retry loop — we just count visits.
		sessionAttemptCount := map[string]int{}
		nativeExec = func(ctx context.Context, session plan.Session) ([]plan.TaskExecResult, error) {
			sessionAttemptCount[session.ID]++
			attempt := sessionAttemptCount[session.ID]
			if attempt > 1 {
				fmt.Printf("\n--- Session %s: %s (native fast path · attempt %d) ---\n", session.ID, session.Title, attempt)
			} else {
				fmt.Printf("\n--- Session %s: %s (native fast path) ---\n", session.ID, session.Title)
			}
			fmt.Printf("  %d tasks\n", len(session.Tasks))
			// Per-task completion fast-path on session retry: skip
			// dispatching tasks whose declared output files already
			// exist on disk with substantive (non-stub) content. Only
			// fires on attempt > 1 because attempt 1's outputs are
			// the source of truth for what's on disk. Pure file I/O,
			// no LLM. Saves on the order of $1-3 per skipped task.
			cfgWithAttempt := nativeCfg
			cfgWithAttempt.SessionAttempt = attempt
			return runSessionNative(ctx, session, sow, cfgWithAttempt)
		}

		// Phase -0.5: session sizer. For each session, ask the
		// reasoning provider whether it's too broad to converge
		// in one pass. When the judge recommends a split, replace
		// the session in-place on the scheduler's SOW with the
		// materialized sub-sessions BEFORE ss.Run iterates. This
		// keeps the 13-task, 50+-file "Shared Packages" class of
		// session from overwhelming the downstream integration
		// reviewer. Noop when reasoningProv is nil or the session
		// is below the task-count floor.
		if applySessionSizerPass(ctx, sow, reasoningProv, reasoningModelChoice, rawSOWText, universalCtx, hookSet) {
			// Sizer mutated sow.Sessions. The scheduler's SOWState
			// was built earlier from the pre-split session list,
			// so its SessionRecord set is now stale: sub-sessions
			// have no records (recordSessionStart silently drops
			// progress) and replaced parents linger as orphans.
			// MergeSOW (called via LoadOrCreateState) reconciles:
			// it appends pending records for new sub-sessions and
			// marks the replaced parents as skipped so dashboards
			// stay accurate.
			if err := ss.LoadOrCreateState(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: SOW state re-init after sizer failed: %v\n", err)
			}
		}

		// Pre-flight env var classifier: ask the reasoning provider
		// which declared env vars are GENUINELY required at build/test
		// time (vs pure runtime concerns of the code being built).
		// Without this, the SOW reasoner's over-declarations —
		// EVENT_STREAM_URL, API_BASE_URL, etc. invented from SOW
		// prose — block entire sessions for variables that should
		// be deployment concerns, not build preconditions.
		//
		// Cached by SOW hash, so successive runs don't re-pay. Graceful
		// degradation: when no provider is available, every var is
		// marked "unsure" which the filter treats as non-gating,
		// restoring sensible behavior (build proceeds, ACs will catch
		// real failures).
		classification, cErr := plan.ClassifyEnvVars(ctx, reasoningProv, reasoningModelChoice, sow, loadRawSOWText(*sowFile, sow), absRepo)
		if cErr != nil {
			fmt.Printf("  ⚠ env-var classifier failed: %v — falling back to gating all declared vars\n", cErr)
		}
		if classification != nil && len(classification.Classifications) > 0 {
			buildReq := classification.BuildRequiredSet()
			ss.BuildRequiredEnvVars = buildReq
			runtimeOnly := 0
			for _, c := range classification.Classifications {
				if c.Category == plan.EnvVarRuntimeOnly {
					runtimeOnly++
				}
			}
			fmt.Printf("  🔎 env-var classifier: %d var(s) classified — %d build-required, %d runtime-only\n",
				len(classification.Classifications), len(buildReq), runtimeOnly)
			for _, c := range classification.Classifications {
				fmt.Printf("     - %s → %s (%s)\n", c.Variable, c.Category, c.Reason)
			}
		}
	}

	sessionExecFn := func(ctx context.Context, session plan.Session) ([]plan.TaskExecResult, error) {
		if nativeExec != nil {
			return nativeExec(ctx, session)
		}
		fmt.Printf("\n--- Session %s: %s ---\n", session.ID, session.Title)
		fmt.Printf("  %d tasks\n", len(session.Tasks))

		// Build a sub-plan from just this session's tasks
		sessionPlan := &plan.Plan{
			ID:          sow.ID + "-" + session.ID,
			Description: session.Title,
			Tasks:       session.Tasks,
		}

		// Use runBuild for the session's tasks
		sessionCfg := BuildConfig{
			RepoRoot:        absRepo,
			PolicyPath:      *policy,
			Workers:         *workers,
			AuthMode:        *authMode,
			ClaudeBinary:    *claudeBin,
			CodexBinary:     *codexBin,
			ClaudeConfigDir: *claudeConfigDir,
			CodexHome:       *codexHome,
			BuildCommand:    *buildC,
			TestCommand:     *testC,
			LintCommand:     *lintC,
			ROIFilter:       *roiFilter,
			SpecExec:        *specExec,
			Timeout:         *timeout,
			RunnerMode:      *runnerMode,
			NativeAPIKey:    *nativeAPIKey,
			NativeModel:     *nativeModel,
			NativeBaseURL:   *nativeBaseURL,
		}

		// Save the session plan temporarily
		tmpPlan := filepath.Join(absRepo, ".stoke", "session-plan.json")
		os.MkdirAll(filepath.Dir(tmpPlan), 0755)
		plan.SaveSOW(tmpPlan, &plan.SOW{}) // create .stoke dir
		if err := plan.Save(filepath.Dir(tmpPlan), sessionPlan); err != nil {
			// Fallback: save to repo root
			plan.Save(absRepo, sessionPlan)
		} else {
			sessionCfg.PlanPath = filepath.Join(filepath.Dir(tmpPlan), "stoke-plan.json")
		}

		buildReport, err := runBuild(sessionCfg)
		if err != nil {
			return nil, err
		}

		// Convert report to TaskExecResults
		var results []plan.TaskExecResult
		if buildReport != nil {
			for _, tr := range buildReport.Tasks {
				results = append(results, plan.TaskExecResult{
					TaskID:  tr.ID,
					Success: tr.Status == "done",
				})
			}
		}

		// If we didn't get per-task results, synthesize from the overall result
		if len(results) == 0 {
			for _, t := range session.Tasks {
				results = append(results, plan.TaskExecResult{
					TaskID:  t.ID,
					Success: buildReport != nil && buildReport.Success,
				})
			}
		}

		return results, nil
	}

	// --dump-task-prompts: bypass the scheduler entirely. Walk every
	// session's tasks, build their would-be prompts, write them to
	// .stoke/prompt-dump/, and exit. Lets the user verify spec
	// extraction without spending on an LLM run.
	if *dumpPrompts {
		count, dumpErr := dumpTaskPrompts(absRepo, sow, loadRawSOWText(*sowFile, sow))
		if dumpErr != nil {
			fatal("dump task prompts: %v", dumpErr)
		}
		fmt.Printf("\nWrote %d task prompt file(s) to %s\n", count, filepath.Join(absRepo, ".stoke", "prompt-dump"))
		fmt.Println("Inspect them to verify spec extraction, canonical identifiers, and task framing before a real run.")
		return
	}

	// Pre-flight env var audit: enumerate every infra env var declared
	// across all sessions and report which are unset BEFORE the run
	// starts. Without this, a 30-minute run can blow past a missing
	// env var on session 3 and silently skip it. Surfacing at startup
	// gives the operator a chance to set the var or accept the skip.
	// Now honors the classifier result — runtime-only vars don't
	// appear in the pre-flight warning either.
	{
		type missing struct {
			sessionID string
			vars      []string
		}
		var unset []missing
		buildReq := ss.BuildRequiredEnvVars // may be nil (= gate everything, legacy)
		for _, s := range sow.Sessions {
			reqs := sow.InfraForSession(s.ID)
			var miss []string
			for _, r := range reqs {
				for _, v := range r.EnvVars {
					if buildReq != nil && !buildReq[v] {
						continue // classifier said runtime-only
					}
					if os.Getenv(v) == "" {
						miss = append(miss, v)
					}
				}
			}
			if len(miss) > 0 {
				unset = append(unset, missing{sessionID: s.ID, vars: miss})
			}
		}
		if len(unset) > 0 {
			fmt.Println()
			fmt.Println("  ════════════════════════════════════════════════════════════════")
			fmt.Printf("  ⚠ pre-flight: %d session(s) declare unset infrastructure env vars\n", len(unset))
			for _, m := range unset {
				fmt.Printf("    - %s: missing %s\n", m.sessionID, strings.Join(m.vars, ", "))
			}
			fmt.Println("    these sessions will be marked BLOCKED at dispatch time.")
			fmt.Println("    set the vars above (export VAR=value) and re-run, OR accept the skips.")
			fmt.Println("  ════════════════════════════════════════════════════════════════")
			fmt.Println()
		}
	}

	// Feasibility gate: runs AFTER convert+critique+refine but BEFORE
	// the first task dispatches. Enforces the shippability contract:
	// every external service the SOW references must have usable API
	// documentation (in the SOW itself, or fetchable via Tavily /
	// WEBSEARCH_COMMAND). The harness refuses to build against an
	// external API it does not know how to call correctly; no mocks
	// are synthesized under any circumstance.
	//
	// Operator overrides (explicit, visible, never implicit):
	//   --force                  skip the gate
	//   --docs-dir <path>        additional docs the gate can read
	//   TAVILY_API_KEY=...       enables Tavily search
	//   WEBSEARCH_COMMAND=...    enables shell-wrapped MCP search
	if !*forceFeasibility {
		rawSOWForGate := loadRawSOWText(*sowFile, sow)
		if *docsDir != "" {
			if extra, err := readDocsDir(*docsDir); err == nil && extra != "" {
				rawSOWForGate = rawSOWForGate + "\n\n## Additional docs from --docs-dir\n\n" + extra
			} else if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ --docs-dir read failed: %v — proceeding without\n", err)
			}
		}
		searcher := websearch.DefaultFromEnv()
		if searcher != nil {
			fmt.Printf("  🔎 feasibility gate: web-search provider(s) available: %s\n", searcher.Name())
		}
		fRep := plan.EvaluateFeasibility(ctx, sow, rawSOWForGate, searcher)
		fmt.Print(fRep.FormatReport())
		if !fRep.AllShippable {
			fmt.Fprintln(os.Stderr, "  Run aborted by feasibility gate. See reasons above.")
			fmt.Fprintln(os.Stderr, "  Pass --force to proceed anyway (no mocks will be synthesized).")
			os.Exit(3)
		}
		// Stash fetched docs so they get injected into task briefings.
		// We do this by writing them to .stoke/external-docs/<service>.md
		// and pointing the briefing code at that directory — keeps
		// briefing plumbing unchanged while giving workers the real
		// documentation content.
		if len(fRep.FetchedDocsForTaskBrief) > 0 {
			if err := persistFetchedDocs(absRepo, fRep.FetchedDocsForTaskBrief); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ could not persist fetched docs: %v — proceeding\n", err)
			}
		}
	} else {
		fmt.Println("  ⚠ feasibility gate SKIPPED via --force (operator acknowledges no mocks will be synthesized)")
	}

	runStart := time.Now()
	results, err := ss.Run(ctx, sessionExecFn)
	runElapsed := time.Since(runStart)

	// Tally pass/fail/skipped counts up front so a 13-session build
	// has a clear summary even if you scroll past the per-session
	// detail. Then print one line per session.
	var passed, failed, skipped int
	for _, r := range results {
		switch {
		case r.Skipped:
			skipped++
		case r.Error != nil || !r.AcceptanceMet:
			failed++
		default:
			passed++
		}
	}
	fmt.Printf("\n=== SOW Results: %d passed, %d failed, %d skipped (of %d sessions) ===\n",
		passed, failed, skipped, len(sow.Sessions))
	for _, r := range results {
		status := "PASS"
		switch {
		case r.Skipped:
			status = "SKIP"
		case r.Error != nil:
			status = "FAIL"
		case !r.AcceptanceMet:
			status = "FAIL"
		}
		attemptStr := ""
		if r.Attempts > 1 {
			attemptStr = fmt.Sprintf(" (%d attempts)", r.Attempts)
		}
		fmt.Printf("  [%s] %s: %s%s\n", status, r.SessionID, r.Title, attemptStr)
		if r.Error != nil {
			fmt.Printf("    error: %v\n", r.Error)
		}
		if len(r.Acceptance) > 0 {
			fmt.Print(plan.FormatAcceptanceResults(r.Acceptance))
		}
	}
	if state := ss.State(); state != nil {
		fmt.Printf("\n  state: %s\n", plan.SOWStatePath(absRepo))
	}

	// Meta-reasoner: run-level learning pass. Gather deterministic
	// telemetry from the session results and ask the LLM to classify
	// this run's failures into root-cause classes, emitting one
	// machine-actionable prevention rule per class. The report is
	// persisted to .stoke/meta-reports/<run-id>.json so the next run
	// can load it via plan.LoadRecentMetaReports and inject the
	// prevention rules into its lead-dev briefings.
	//
	// Only runs in native mode (where we have a provider configured
	// and a sharedSpent cost counter) and only when there's at least
	// one session result to reason about.
	if *runnerMode == "native" && len(results) > 0 {
		metaKey := *nativeAPIKey
		if metaKey == "" {
			for _, k := range []string{"LITELLM_API_KEY", "LITELLM_MASTER_KEY", "ANTHROPIC_API_KEY"} {
				if v := os.Getenv(k); v != "" {
					metaKey = v
					break
				}
			}
		}
		var metaProv provider.Provider
		if metaKey != "" {
			metaProv = provider.NewAnthropicProvider(metaKey, *nativeBaseURL)
		}
		if metaProv != nil {
			// Build telemetry + per-session summaries + flat AC list
			// from the scheduler's results. All counters are
			// deterministic — the LLM never recounts.
			tel := plan.MetaRunTelemetry{
				Sessions:        len(results),
				SessionsPassed:  passed,
				SessionsFailed:  failed,
				SessionsSkipped: skipped,
				TotalElapsed:    runElapsed,
			}
			// Populate actual USD cost from the shared spend
			// counter the scheduler accumulates across every task
			// attempt. Without this, the persisted meta-report
			// showed cost: $0.00 for every run, defeating the
			// prior-learnings-by-cost heuristic.
			// sharedSpent lives in the native-runner block's scope;
			// runCostCapture (outer-scope var) points at the same
			// float64 so the post-run meta-reasoner can read the
			// final cost here. Without this the persisted meta
			// report always showed cost: $0.00.
			if runCostCapture != nil {
				runTotalCost = *runCostCapture
			}
			tel.TotalCostUSD = runTotalCost
			var summaries []plan.MetaSessionSummary
			var flatACs []plan.AcceptanceResult
			for _, r := range results {
				tel.TasksDispatched += len(r.TaskResults)
				for _, tr := range r.TaskResults {
					if tr.Success {
						tel.TasksCompleted++
					}
				}
				if r.Attempts > 0 {
					tel.TotalAttempts += r.Attempts
				}
				acsPassed := 0
				for _, ac := range r.Acceptance {
					if ac.Passed {
						acsPassed++
					}
				}
				errStr := ""
				if r.Error != nil {
					errStr = r.Error.Error()
				}
				summaries = append(summaries, plan.MetaSessionSummary{
					SessionID:     r.SessionID,
					Title:         r.Title,
					Attempts:      r.Attempts,
					AcceptanceMet: r.AcceptanceMet,
					Skipped:       r.Skipped,
					Error:         errStr,
					ACsTotal:      len(r.Acceptance),
					ACsPassed:     acsPassed,
				})
				flatACs = append(flatACs, r.Acceptance...)
			}

			metaModel := *nativeModel
			if metaModel == "" {
				metaModel = "claude-sonnet-4-6"
			}
			metaCtx, metaCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			report, merr := plan.RunMetaReasoning(metaCtx, metaProv, metaModel, plan.MetaReasonInput{
				RunID:            sow.ID,
				Telemetry:        tel,
				ACResults:        flatACs,
				SessionSummaries: summaries,
			})
			metaCancel()
			if merr != nil {
				fmt.Fprintf(os.Stderr, "\n  meta-reasoner: %v\n", merr)
			} else if report != nil {
				fmt.Println()
				fmt.Print(plan.FormatMetaReportForOperator(report))
				if serr := plan.SaveMetaReport(absRepo, report); serr != nil {
					fmt.Fprintf(os.Stderr, "  meta-reasoner: could not persist report: %v\n", serr)
				} else {
					fmt.Printf("  (persisted to %s)\n", filepath.Join(plan.MetaReportsDir(absRepo), report.RunID+".json"))
				}
			}
		}
	}

	switch {
	case err != nil && failed == 0:
		// Scheduler returned an error but counted no failures —
		// surface the error verbatim so the user can see what
		// happened.
		fmt.Fprintf(os.Stderr, "\nSOW execution failed: %v\n", err)
		os.Exit(1)
	case failed > 0:
		fmt.Fprintf(os.Stderr, "\nSOW finished with %d failed session(s).\n", failed)
		if passed > 0 {
			fmt.Fprintf(os.Stderr, "  %d session(s) passed; rerun with --resume to skip them.\n", passed)
		}
		os.Exit(1)
	default:
		fmt.Println("\nSOW completed successfully.")
	}
}

func countSOWTasks(sow *plan.SOW) int {
	n := 0
	for _, s := range sow.Sessions {
		n += len(s.Tasks)
	}
	return n
}

// --- plan: generate a plan file from codebase analysis ---

func planCmd(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	output := fs.String("output", "stoke-plan.json", "Output file")
	task := fs.String("task", "", "High-level task description")
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	dryRun := fs.Bool("dry-run", false, "Show prompt without executing")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	taskPrompt := *task
	if taskPrompt == "" {
		taskPrompt = "Analyze this codebase and identify tasks that need to be done"
	}

	prompt := fmt.Sprintf(`You are a planning agent. Read this codebase and produce a structured task plan.

High-level goal: %s

Output ONLY valid JSON in this format:
{
  "id": "plan-YYYYMMDD",
  "description": "Brief description",
  "tasks": [
    {"id": "TASK-1", "description": "Specific task", "files": ["src/file.ts"], "dependencies": [], "type": "refactor"}
  ]
}

Rules:
- Each task completable in one agent session (< 20 tool turns)
- List file dependencies between tasks
- Predict which files each task will modify
- Types: refactor, typesafety, docs, security, architecture, devops, concurrency, review
- Be specific: file paths, function names, expected behavior`, taskPrompt)

	if *dryRun {
		fmt.Println("PLAN PROMPT:")
		fmt.Println(prompt)
		return
	}

	fmt.Printf("⚡ STOKE plan\n  Launching Claude in read-only mode...\n\n")

	// Use app.RunConfig with plan-like settings
	// Create harness-owned task state for plan generation
	ts := taskstate.NewTaskState("plan")

	cfg := app.RunConfig{
		RepoRoot:        absRepo,
		Task:            prompt,
		TaskType:        "plan",
		DryRun:          false,
		PlanOnly:        true, // structurally read-only: no execute, no verify, no commit, no merge
		AuthMode:        "mode1",
		ClaudeBinary:    *claudeBin,
		ClaudeConfigDir: *claudeConfigDir,
		State:           ts,
		EventBus:        newEventBus(),
		OnEvent: func(ev stream.Event) {
			if ev.DeltaText != "" {
				fmt.Print(ev.DeltaText)
			}
		},
	}

	orchestrator, err := app.New(cfg)
	if err != nil {
		fatal("init: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := orchestrator.Run(ctx)
	if err != nil {
		fatal("plan: %v", err)
	}

	fmt.Printf("\n\nCost: $%.4f\n", result.TotalCostUSD)

	// Extract JSON from plan output (structurally read-only: no execute ran)
	planText := result.PlanOutput
	if planText == "" {
		// Fallback to rendered output
		planText = result.Render()
	}
	if idx := strings.Index(planText, "{"); idx >= 0 {
		if end := strings.LastIndex(planText, "}"); end > idx {
			jsonStr := planText[idx : end+1]
			if json.Valid([]byte(jsonStr)) {
				// Resolve output path relative to repo root (not cwd)
				// This ensures ship/build can find the plan when run from different directories
				outputPath := *output
				if !filepath.IsAbs(outputPath) {
					outputPath = filepath.Join(absRepo, outputPath)
				}
				if err := os.WriteFile(outputPath, []byte(jsonStr), 0644); err != nil {
					fatal("write plan: %v", err)
				}
				fmt.Printf("Plan saved to %s\n", outputPath)
				return
			}
		}
	}
	fmt.Println("Could not extract plan JSON from output. Run manually and save.")
}

// --- status: show session dashboard ---

func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	useSQLite := fs.Bool("sqlite", false, "Use SQLite session store")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	// Auto-detect store: if session.db exists and --sqlite not explicitly set, use SQLite
	var store session.SessionStore
	sqlitePath := filepath.Join(absRepo, ".stoke", "session.db")
	if *useSQLite || fileExists(sqlitePath) {
		sqlStore, err := session.NewSQLStore(absRepo)
		if err != nil {
			fatal("sqlite store: %v", err)
		}
		store = sqlStore
	} else {
		store = session.New(absRepo)
	}
	state, err := store.LoadState()
	if err != nil {
		fatal("load state: %v", err)
	}
	if state == nil {
		fmt.Println("No active session.")

		// Show learning if available
		learning, _ := store.LoadLearning()
		if learning != nil && len(learning.Patterns) > 0 {
			fmt.Println("\nLearned patterns from previous sessions:")
			for _, p := range learning.Patterns {
				fmt.Printf("  ● %s -> %s (%d occurrences)\n", p.Issue, p.Fix, p.Occurrences)
			}
		}
		return
	}

	done, failed, pending := 0, 0, 0
	for _, t := range state.Tasks {
		switch t.Status {
		case plan.StatusDone:
			done++
		case plan.StatusFailed:
			failed++
		default:
			pending++
		}
	}

	elapsed := time.Since(state.StartedAt).Round(time.Second)
	fmt.Printf("⚡ STOKE status\n\n")
	fmt.Printf("  Plan:    %s\n", state.PlanID)
	fmt.Printf("  Tasks:   %d done, %d failed, %d pending (of %d)\n", done, failed, pending, len(state.Tasks))
	fmt.Printf("  Cost:    $%.2f\n", state.TotalCostUSD)
	fmt.Printf("  Elapsed: %s\n", elapsed)
	fmt.Printf("  Saved:   %s\n\n", state.SavedAt.Format(time.RFC3339))

	for _, t := range state.Tasks {
		icon := "○"
		switch t.Status {
		case plan.StatusDone:
			icon = "✓"
		case plan.StatusFailed:
			icon = "✗"
		case plan.StatusActive:
			icon = "▸"
		}
		fmt.Printf("  %s %s: %s\n", icon, t.ID, trunc(t.Description, 60))
	}

	learning, _ := store.LoadLearning()
	if learning != nil && len(learning.Patterns) > 0 {
		fmt.Println("\n  Learned patterns:")
		for _, p := range learning.Patterns {
			fmt.Printf("    ● %s -> %s\n", trunc(p.Issue, 30), trunc(p.Fix, 30))
		}
	}

	// Show latest build report if available
	if latest, err := report.LoadLatest(absRepo); err == nil {
		fmt.Printf("\n  Last report: %s (%d/%d done, $%.2f)\n",
			latest.PlanID, latest.TasksDone, latest.TasksTotal, latest.TotalCost)
	}

	// Show ledger integrity status
	ledgerDir := filepath.Join(absRepo, ".stoke", "ledger")
	if fileExists(ledgerDir) {
		lg, err := ledger.New(ledgerDir)
		if err != nil {
			fmt.Printf("\n  Ledger integrity: FAILED (open error: %v)\n", err)
		} else {
			if err := lg.Verify(context.Background()); err != nil {
				fmt.Printf("\n  Ledger integrity: FAILED (%v)\n", err)
			} else {
				fmt.Printf("\n  Ledger integrity: OK\n")
			}
			lg.Close()
		}
	}
}

// --- scan: deterministic code scan + security surface mapping ---

func scanCmd(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root")
	securityFlag := fs.Bool("security", false, "Include security surface mapping")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	if !*jsonOut {
		fmt.Printf("⚡ STOKE scan\n\n")
	}

	// Run deterministic code scan
	result, err := scanpkg.ScanFiles(absRepo, scanpkg.DefaultRules(), nil)
	if err != nil {
		fatal("scan: %v", err)
	}

	if *jsonOut {
		type scanOutput struct {
			Scan            *scanpkg.ScanResult  `json:"scan"`
			SecuritySurface *scanpkg.SecurityMap  `json:"security_surface,omitempty"`
		}
		output := scanOutput{Scan: result}
		if *securityFlag {
			secMap, _ := scanpkg.MapSecuritySurface(absRepo, nil)
			output.SecuritySurface = secMap
		}
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			fatal("marshal JSON output: %v", err)
		}
		fmt.Println(string(data))
		if result.HasBlocking() {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("  %s\n\n", result.Summary())
	for _, f := range result.Findings {
		icon := "●"
		switch f.Severity {
		case "critical":
			icon = "✗"
		case "high":
			icon = "!"
		case "medium":
			icon = "~"
		case "low":
			icon = "○"
		}
		fmt.Printf("  %s [%s] %s:%d -- %s\n", icon, f.Severity, f.File, f.Line, f.Message)
		if f.Fix != "" {
			fmt.Printf("           Fix: %s\n", f.Fix)
		}
	}

	// Security surface mapping
	if *securityFlag {
		secMap, _ := scanpkg.MapSecuritySurface(absRepo, nil)
		if secMap != nil && len(secMap.Surfaces) > 0 {
			fmt.Printf("\n  Security surface (%d files):\n", secMap.FilesScanned)
			fmt.Printf("  %s\n", strings.Replace(secMap.Summary(), "\n", "\n  ", -1))
		}
	}

	if result.HasBlocking() {
		fmt.Println("\n  BLOCKING: critical/high issues must be resolved before merge")
		os.Exit(1)
	}
}

// --- audit: multi-perspective code review ---

func auditCmd(args []string) {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root")
	personas := fs.String("personas", "", "Comma-separated persona IDs (default: auto-select)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	dryRun := fs.Bool("dry-run", false, "Show prompts without executing")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	fmt.Printf("⚡ STOKE audit\n\n")

	// Run scan first to inform persona selection
	scanResult, _ := scanpkg.ScanFiles(absRepo, scanpkg.DefaultRules(), nil)
	securityMap, _ := scanpkg.MapSecuritySurface(absRepo, nil)

	// Select personas
	allPersonas := audit.DefaultPersonas()
	var selected []audit.Persona
	if *personas != "" {
		ids := strings.Split(*personas, ",")
		idSet := map[string]bool{}
		for _, id := range ids {
			idSet[strings.TrimSpace(id)] = true
		}
		for _, p := range allPersonas {
			if idSet[p.ID] {
				selected = append(selected, p)
			}
		}
	} else {
		selected = audit.SelectPersonas(allPersonas, securityMap, scanResult)
	}

	fmt.Printf("  Personas: ")
	names := make([]string, len(selected))
	for i, p := range selected {
		names[i] = p.Name
	}
	fmt.Println(strings.Join(names, ", "))
	fmt.Println()

	// Build and execute review requests
	for _, p := range selected {
		req := audit.ReviewRequest{
			Persona:     p,
			ScanResult:  scanResult,
			SecurityMap: securityMap,
		}
		prompt := audit.BuildPrompt(p, req)

		if *dryRun {
			fmt.Printf("--- %s ---\n", p.Name)
			fmt.Println(prompt[:min(len(prompt), 500)])
			if len(prompt) > 500 {
				fmt.Println("...")
			}
			fmt.Println()
			continue
		}

		if *jsonOut {
			data, err := json.MarshalIndent(req, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "  marshal error: %v\n", err)
				continue
			}
			fmt.Println(string(data))
			continue
		}

		// Execute the review via Claude Code headless
		claudeBin := "claude"
		runner := engine.NewClaudeRunner(claudeBin)
		auditRuntimeDir := filepath.Join(absRepo, ".stoke", "runtime", "audit-"+p.ID)
		if err := os.MkdirAll(auditRuntimeDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "  create runtime dir: %v\n", err)
			continue
		}
		spec := engine.RunSpec{
			Prompt:      prompt,
			WorktreeDir: absRepo,
			RuntimeDir:  auditRuntimeDir,
			Mode:        engine.AuthModeMode1,
			Phase: engine.PhaseSpec{
				Name:         "audit-" + p.ID,
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				MCPEnabled:   false,
				MaxTurns:     5,
			},
		}

		fmt.Printf("  Running %s...", p.Name)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		result, err := runner.Run(ctx, spec, nil)
		cancel()

		if err != nil {
			fmt.Printf(" error: %v\n", err)
			continue
		}
		fmt.Printf(" done ($%.4f)\n", result.CostUSD)
		if result.ResultText != "" {
			fmt.Printf("    %s\n\n", strings.Replace(result.ResultText, "\n", "\n    ", -1))
		}
	}

	if *dryRun {
		return
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// newEventBus creates a pre-configured event bus with standard observers and gates.
func newEventBus() *hub.Bus {
	bus := hub.New()
	bus.Register(hub.FlowTrackObserver(flowtrack.NewTracker(flowtrack.Config{})))
	bus.Register(hub.ConsentGate(consent.NewWorkflow(nil)))
	return bus
}

// --- pool: subscription utilization ---

func poolCmd(args []string) {
	fs := flag.NewFlagSet("pool", flag.ExitOnError)
	claudeConfigDir := fs.String("claude-config-dir", "", "Single CLAUDE_CONFIG_DIR")
	claudePoolsFlag := fs.String("claude-pools", "", "Comma-separated Claude pool dirs")
	fs.Parse(args)

	// Collect pool dirs
	var poolDirs []string
	if *claudePoolsFlag != "" {
		poolDirs = splitPools(*claudePoolsFlag)
	} else if *claudeConfigDir != "" {
		poolDirs = []string{*claudeConfigDir}
	} else if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		poolDirs = []string{env}
	}

	if len(poolDirs) == 0 {
		fmt.Println("No pool dirs. Pass --claude-config-dir, --claude-pools, or set CLAUDE_CONFIG_DIR.")
		return
	}

	fmt.Printf("⚡ STOKE pool (%d pool(s))\n\n", len(poolDirs))

	for i, dir := range poolDirs {
		token := readOAuthToken(dir)
		if token == "" {
			fmt.Printf("  pool %d (%s): no OAuth token\n\n", i+1, dir)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		data, err := subscriptions.PollClaudeUsage(ctx, token)
		cancel()
		if err != nil {
			fmt.Printf("  pool %d (%s): poll error: %v\n\n", i+1, dir, err)
			continue
		}

		if len(poolDirs) > 1 {
			fmt.Printf("  --- pool %d (%s) ---\n", i+1, filepath.Base(dir))
		}
		printWindow("5-hour", data.FiveHour)
		printWindow("7-day", data.SevenDay)
		if data.SevenDayOpus.Utilization > 0 || data.SevenDayOpus.ResetsAt != nil {
			printWindow("7-day (Opus)", data.SevenDayOpus)
		}
		fmt.Println()
	}
}

func printWindow(label string, w subscriptions.UsageWindow) {
	reset := ""
	if w.ResetsAt != nil {
		reset = fmt.Sprintf("  resets in %s", time.Until(*w.ResetsAt).Round(time.Minute))
	}
	fmt.Printf("  %-15s %s %.0f%%%s\n", label+":", bar(w.Utilization, 20), w.Utilization, reset)
}

// --- doctor ---

func doctorCmd(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	providers := fs.Bool("providers", false, "Check all providers in the fallback chain")
	fs.Parse(args)
	fmt.Print(app.Doctor(*claudeBin, *codexBin, *providers))
}

// --- yolo: interactive Claude Code with full Stoke guards ---

func yoloCmd(args []string) {
	fs := flag.NewFlagSet("yolo", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	ensureGitRepoOrFatal(absRepo)

	fmt.Printf("⚡ STOKE yolo\n")
	fmt.Printf("  repo: %s\n", absRepo)
	fmt.Printf("  mode: full access with Stoke guards\n\n")

	// Install hooks and generate settings
	fmt.Print("  Installing hooks... ")
	if err := hooks.InstallInRepo(absRepo); err != nil {
		fatal("install hooks: %v", err)
	}
	fmt.Println("done")

	fmt.Print("  Generating settings... ")
	settingsPath, err := hooks.GenerateSettings(absRepo, "yolo", "")
	if err != nil {
		fatal("generate settings: %v", err)
	}
	fmt.Println("done")

	fmt.Print("  Writing CLAUDE.md... ")
	if err := hooks.GenerateCLAUDEmd(absRepo, "yolo", ""); err != nil {
		fatal("write CLAUDE.md: %v", err)
	}
	fmt.Println("done")

	// Capture git state before
	beforeHash := gitHead(absRepo)

	// Build claude command: interactive mode (no -p), with settings
	claudeArgs := []string{"--settings", settingsPath}
	if *claudeConfigDir != "" {
		// Mode 1: use specified config dir for subscription auth
		os.Setenv("CLAUDE_CONFIG_DIR", *claudeConfigDir)
	}

	fmt.Printf("\n  Launching Claude Code (interactive, guarded)...\n")
	fmt.Printf("  Guards: git stash/push/rebase blocked, protected files locked, nested sessions blocked\n")
	fmt.Printf("  Press Ctrl+C or /exit to end session\n\n")

	// Launch interactive claude (stdin/stdout/stderr attached to terminal)
	cmd := exec.Command(*claudeBin, claudeArgs...)
	cmd.Dir = absRepo
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Build safe env (Mode 1: full auth scrubbing -- same policy as headless)
	configDir := *claudeConfigDir
	if configDir == "" {
		configDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	cmd.Env = engine.SafeEnvForClaudeMode1(configDir)

	if runErr := cmd.Run(); runErr != nil {
		// Non-zero exit is normal for interactive sessions (user pressed Ctrl+C)
		fmt.Printf("\n  Session ended.\n")
	}

	// Show what changed
	afterHash := gitHead(absRepo)
	if beforeHash != afterHash {
		fmt.Printf("\n  Changes made during session:\n")
		diffCmd := exec.Command("git", "log", "--oneline", beforeHash+".."+afterHash)
		diffCmd.Dir = absRepo
		diffCmd.Stdout = os.Stdout
		diffCmd.Run()
	} else {
		fmt.Printf("\n  No commits made during session.\n")
	}

	// Show modified files
	statusCmd := exec.Command("git", "status", "--short")
	statusCmd.Dir = absRepo
	statusOut, _ := statusCmd.Output()
	if len(strings.TrimSpace(string(statusOut))) > 0 {
		fmt.Printf("\n  Uncommitted changes:\n%s\n", string(statusOut))
	}

	// Cleanup CLAUDE.md (leave hooks for future sessions)
	os.Remove(filepath.Join(absRepo, "CLAUDE.md"))
}

// --- scope: interactive read-only Claude Code for planning ---

func scopeCmd(args []string) {
	fs := flag.NewFlagSet("scope", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	output := fs.String("output", "stoke-plan.json", "Output plan file")
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	task := fs.String("task", "", "Optional task brief to seed the session (used by chat-driven dispatch)")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	fmt.Printf("⚡ STOKE scope\n")
	fmt.Printf("  repo: %s\n", absRepo)
	fmt.Printf("  mode: read-only (no writes allowed)\n")
	fmt.Printf("  output: %s\n", *output)
	if strings.TrimSpace(*task) != "" {
		fmt.Printf("  brief: %s\n", truncOne(*task, 100))
	}
	fmt.Println()

	// Install hooks and generate read-only settings
	if err := hooks.InstallInRepo(absRepo); err != nil {
		fatal("install hooks: %v", err)
	}
	settingsPath, err := hooks.GenerateSettings(absRepo, "scope", *output)
	if err != nil {
		fatal("generate settings: %v", err)
	}
	if err := hooks.GenerateCLAUDEmdWithTask(absRepo, "scope", *output, *task); err != nil {
		fatal("write CLAUDE.md: %v", err)
	}

	// Generate empty MCP config for isolation (same as headless planning)
	emptyMCPPath := filepath.Join(absRepo, ".stoke", "generated", "empty-mcp-scope.json")
	if err := os.WriteFile(emptyMCPPath, []byte("{}"), 0644); err != nil {
		fatal("write empty MCP config: %v", err)
	}

	// MCP isolation: strict empty config + deny mcp__* tools
	// This matches the headless planning path's isolation level
	claudeArgs := []string{
		"--settings", settingsPath,
		"--strict-mcp-config",
		"--mcp-config", emptyMCPPath,
		"--disallowedTools", "mcp__*",
	}
	if *claudeConfigDir != "" {
		os.Setenv("CLAUDE_CONFIG_DIR", *claudeConfigDir)
	}

	fmt.Printf("  Launching Claude Code (scope mode)...\n")
	fmt.Printf("  Read any file. Write only to: %s\n", *output)
	fmt.Printf("  MCP: disabled (isolated like headless planning)\n")
	fmt.Printf("  Ask Claude to save the plan when ready.\n")
	fmt.Printf("  Press Ctrl+C or /exit to end session\n\n")

	cmd := exec.Command(*claudeBin, claudeArgs...)
	cmd.Dir = absRepo
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Mode 1 env (full auth scrubbing -- same policy as headless)
	scopeConfigDir := *claudeConfigDir
	if scopeConfigDir == "" {
		scopeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	cmd.Env = engine.SafeEnvForClaudeMode1(scopeConfigDir)

	cmd.Run()

	// Check if plan file was created during the session
	planPath := filepath.Join(absRepo, *output)
	if fileExists(planPath) {
		data, _ := os.ReadFile(planPath)
		if json.Valid(data) {
			fmt.Printf("\n  Plan saved: %s (%d bytes)\n", *output, len(data))
			fmt.Printf("  Next: stoke build --plan %s\n", *output)
		}
	} else {
		fmt.Printf("\n  No plan file found at %s\n", *output)
		fmt.Printf("  Tip: ask Claude to write the plan to %s during the session\n", *output)
	}

	os.Remove(filepath.Join(absRepo, "CLAUDE.md"))
}

// --- repair: orchestrated scan -> triage -> fix -> verify ---

func repairCmd(args []string) {
	fs := flag.NewFlagSet("repair", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	policy := fs.String("policy", "", "Path to stoke.policy.yaml")
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	codexHome := fs.String("codex-home", "", "CODEX_HOME")
	buildC := fs.String("build-cmd", "", "Build command")
	testC := fs.String("test-cmd", "", "Test command")
	lintC := fs.String("lint-cmd", "", "Lint command")
	securityFlag := fs.Bool("security", false, "Include security surface mapping")
	workers := fs.Int("workers", 2, "Max parallel agents")
	dryRun := fs.Bool("dry-run", false, "Show repair plan without executing")
	authMode := fs.String("mode", "mode1", "Auth mode")
	timeout := fs.Duration("timeout", 0, "Hard wall-clock timeout (0 = supervisor-driven, recommended)")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	ensureGitRepoOrFatal(absRepo)

	// Auto-detect commands
	detected := config.DetectCommands(absRepo)
	if *buildC == "" {
		*buildC = detected.Build
	}
	if *testC == "" {
		*testC = detected.Test
	}
	if *lintC == "" {
		*lintC = detected.Lint
	}

	fmt.Printf("⚡ STOKE repair\n")
	fmt.Printf("  repo: %s\n", absRepo)
	fmt.Printf("  build: %s\n", orNone(*buildC))
	fmt.Printf("  test:  %s\n", orNone(*testC))
	fmt.Printf("  lint:  %s\n\n", orNone(*lintC))

	// Phase 1: Scan
	fmt.Println("Phase 1: Deterministic scan")
	scanResult, scanErr := scanpkg.ScanFiles(absRepo, scanpkg.DefaultRules(), nil)
	if scanErr != nil {
		fatal("scan: %v", scanErr)
	}

	// Security surface mapping
	var secMap *scanpkg.SecurityMap
	if *securityFlag {
		fmt.Println("  + Security surface mapping")
		secMap, _ = scanpkg.MapSecuritySurface(absRepo, nil)
	}

	findings := scanResult.Findings
	fmt.Printf("  Found %d findings across %d files\n", len(findings), scanResult.FilesScanned)

	if len(findings) == 0 {
		fmt.Println("  No findings. Codebase is clean.")
		return
	}

	// Phase 2: Convert findings to fix tasks
	fmt.Println("\nPhase 2: Generating repair plan")

	// Group findings by file for efficient fixing
	byFile := map[string][]scanpkg.Finding{}
	for _, f := range findings {
		byFile[f.File] = append(byFile[f.File], f)
	}

	var tasks []plan.Task
	taskNum := 1
	for file, fileFindings := range byFile {
		// Group by severity
		var descriptions []string
		for _, f := range fileFindings {
			descriptions = append(descriptions, fmt.Sprintf("[%s] %s (line %d): %s", f.Severity, f.Rule, f.Line, f.Message))
		}

		taskDesc := fmt.Sprintf("Fix %d finding(s) in %s:\n%s", len(fileFindings), file, strings.Join(descriptions, "\n"))
		if len(taskDesc) > 500 {
			taskDesc = taskDesc[:500] + "..."
		}

		tasks = append(tasks, plan.Task{
			ID:          fmt.Sprintf("REPAIR-%d", taskNum),
			Description: taskDesc,
			Files:       []string{file},
			Type:        "repair",
		})
		taskNum++
	}

	repairPlan := &plan.Plan{
		ID:          fmt.Sprintf("repair-%s", time.Now().Format("20060102-150405")),
		Description: fmt.Sprintf("Auto-generated repair plan: %d tasks from %d scan findings", len(tasks), len(findings)),
		Tasks:       tasks,
	}

	// Save repair plan
	repairPlanPath := filepath.Join(absRepo, ".stoke", "repair-plan.json")
	if err := os.MkdirAll(filepath.Dir(repairPlanPath), 0755); err != nil {
		fatal("create dir: %v", err)
	}
	planData, err := json.MarshalIndent(repairPlan, "", "  ")
	if err != nil {
		fatal("marshal repair plan: %v", err)
	}
	if err := os.WriteFile(repairPlanPath, planData, 0644); err != nil {
		fatal("write repair plan: %v", err)
	}

	fmt.Printf("  Generated %d repair tasks\n", len(tasks))
	for _, t := range tasks {
		icon := "○"
		switch {
		case strings.Contains(t.Description, "[critical]"):
			icon = "✗"
		case strings.Contains(t.Description, "[high]"):
			icon = "!"
		}
		fmt.Printf("  %s %s: %s\n", icon, t.ID, trunc(t.Description, 60))
	}

	if *dryRun {
		fmt.Printf("\n  Repair plan: %s\n", repairPlanPath)
		fmt.Println("  Run without --dry-run to execute repairs.")

		if secMap != nil {
			fmt.Printf("\n  Security surface: %d surfaces across %d files\n", len(secMap.Surfaces), secMap.FilesScanned)
		}
		return
	}

	// Phase 3: Execute repairs through the anti-deception build pipeline
	fmt.Println("\nPhase 3: Executing repairs")

	// Use the standard build pipeline -- reuses ALL anti-deception enforcement
	buildArgs := []string{
		"--plan", repairPlanPath,
		"--repo", absRepo,
		"--workers", fmt.Sprintf("%d", *workers),
		"--mode", *authMode,
		"--claude-bin", *claudeBin,
		"--codex-bin", *codexBin,
	}
	if *policy != "" {
		buildArgs = append(buildArgs, "--policy", *policy)
	}
	if *claudeConfigDir != "" {
		buildArgs = append(buildArgs, "--claude-config-dir", *claudeConfigDir)
	}
	if *codexHome != "" {
		buildArgs = append(buildArgs, "--codex-home", *codexHome)
	}
	if *buildC != "" {
		buildArgs = append(buildArgs, "--build-cmd", *buildC)
	}
	if *testC != "" {
		buildArgs = append(buildArgs, "--test-cmd", *testC)
	}
	if *lintC != "" {
		buildArgs = append(buildArgs, "--lint-cmd", *lintC)
	}
	_ = timeout // timeout is handled by buildCmd internally

	buildCmd(buildArgs)

	// Phase 4: Re-scan
	fmt.Println("\nPhase 4: Re-scanning to verify repairs")
	rescanResult, _ := scanpkg.ScanFiles(absRepo, scanpkg.DefaultRules(), nil)
	remaining := len(rescanResult.Findings)

	fmt.Printf("\n  Before: %d findings\n", len(findings))
	fmt.Printf("  After:  %d findings\n", remaining)
	fmt.Printf("  Fixed:  %d\n", len(findings)-remaining)

	if remaining > 0 {
		fmt.Printf("\n  Remaining findings:\n")
		for _, f := range rescanResult.Findings {
			fmt.Printf("    [%s] %s:%d %s\n", f.Severity, f.File, f.Line, f.Message)
		}
	}

	// Phase 5: Report
	fmt.Println("\nPhase 5: Repair report")
	reportPath := filepath.Join(absRepo, ".stoke", "reports", "repair-report.json")
	if err := os.MkdirAll(filepath.Dir(reportPath), 0755); err != nil {
		fatal("create reports dir: %v", err)
	}
	type repairReportData struct {
		Timestamp       string `json:"timestamp"`
		BeforeFindings  int    `json:"before_findings"`
		AfterFindings   int    `json:"after_findings"`
		TasksGenerated  int    `json:"tasks_generated"`
		PlanID          string `json:"plan_id"`
		SecurityScanned bool   `json:"security_scanned"`
	}
	repairReport := repairReportData{
		Timestamp:       time.Now().Format(time.RFC3339),
		BeforeFindings:  len(findings),
		AfterFindings:   remaining,
		TasksGenerated:  len(tasks),
		PlanID:          repairPlan.ID,
		SecurityScanned: *securityFlag,
	}
	reportData, err := json.MarshalIndent(repairReport, "", "  ")
	if err != nil {
		fatal("marshal report: %v", err)
	}
	if err := os.WriteFile(reportPath, reportData, 0644); err != nil {
		fatal("write report: %v", err)
	}
	fmt.Printf("  Report: %s\n", reportPath)
}

// --- ship: the convergence loop (replaces you) ---
// Build -> Review -> Fix -> Review -> Fix -> ... until reviewer says ship it.
// Uses Claude Code (Opus) as the builder, Codex as the reviewer.
// Each round: execute tasks, comprehensive multi-vector review, parse blocking fixes, repeat.

func shipCmd(args []string) {
	fs := flag.NewFlagSet("ship", flag.ExitOnError)
	repo := fs.String("repo", ".", "Git repository root")
	task := fs.String("task", "", "What to build")
	planFile := fs.String("plan", "", "Existing plan file (skip plan generation)")
	policy := fs.String("policy", "", "Path to stoke.policy.yaml")
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	claudeConfigDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR")
	codexHome := fs.String("codex-home", "", "CODEX_HOME")
	buildC := fs.String("build-cmd", "", "Build command")
	testC := fs.String("test-cmd", "", "Test command")
	lintC := fs.String("lint-cmd", "", "Lint command")
	maxRounds := fs.Int("max-rounds", 5, "Maximum build-review-fix rounds")
	workers := fs.Int("workers", 2, "Max parallel agents")
	authMode := fs.String("mode", "mode1", "Auth mode")
	timeout := fs.Duration("timeout", 0, "Hard wall-clock timeout (0 = supervisor-driven, recommended)")
	dryRun := fs.Bool("dry-run", false, "Show what would happen")
	fs.Parse(args)

	if *task == "" && *planFile == "" {
		fmt.Fprintln(os.Stderr, "--task or --plan required")
		fs.Usage()
		os.Exit(2)
	}

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}
	ensureGitRepoOrFatal(absRepo)

	detected := config.DetectCommands(absRepo)
	if *buildC == "" {
		*buildC = detected.Build
	}
	if *testC == "" {
		*testC = detected.Test
	}
	if *lintC == "" {
		*lintC = detected.Lint
	}

	fmt.Printf("⚡ STOKE ship\n")
	fmt.Printf("  repo:       %s\n", absRepo)
	fmt.Printf("  task:       %s\n", orNone(*task))
	fmt.Printf("  max rounds: %d\n", *maxRounds)
	fmt.Printf("  workers:    %d\n", *workers)
	fmt.Printf("  build:      %s\n", orNone(*buildC))
	fmt.Printf("  test:       %s\n", orNone(*testC))
	fmt.Printf("  lint:       %s\n\n", orNone(*lintC))

	if *dryRun {
		fmt.Println("DRY RUN: would execute the following loop:")
		fmt.Println("  1. Plan (or use existing plan)")
		fmt.Println("  2. Build all tasks (parallel, anti-deception enforced)")
		fmt.Println("  3. Comprehensive review: code, arch, security, scaling, tests, UX, docs")
		fmt.Println("  4. If blocking fixes found -> generate fix tasks -> go to 2")
		fmt.Println("  5. Repeat until reviewer says ship or max rounds hit")
		return
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if *timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	} else {
		ctx, cancel = signalContext(context.Background())
	}
	defer cancel()

	startTime := time.Now()
	var currentPlanPath string
	var totalCost float64

	// Round 0: Generate or load plan
	if *planFile != "" {
		currentPlanPath = *planFile
		fmt.Printf("Using existing plan: %s\n\n", currentPlanPath)
	} else {
		fmt.Println("Round 0: Generating plan")
		planArgs := []string{
			"--task", *task,
			"--repo", absRepo,
			"--claude-bin", *claudeBin,
		}
		if *claudeConfigDir != "" {
			planArgs = append(planArgs, "--claude-config-dir", *claudeConfigDir)
		}
		planCmd(planArgs)
		currentPlanPath = filepath.Join(absRepo, "stoke-plan.json")
		if !fileExists(currentPlanPath) {
			fatal("plan generation failed: no stoke-plan.json")
		}
		fmt.Println()
	}

	// The convergence loop
	shipped := false
	shipBlockedReason := "loop did not complete"

	for round := 1; round <= *maxRounds; round++ {
		fmt.Printf("═══ Round %d/%d ═══\n\n", round, *maxRounds)

		// Step 1: Build (using runBuild directly to get proper success/failure result)
		fmt.Printf("Step 1: Building from %s\n", filepath.Base(currentPlanPath))

		// Build pool directories from CLI flags
		var claudePoolDirs, codexPoolDirs []string
		if *claudeConfigDir != "" {
			claudePoolDirs = []string{*claudeConfigDir}
		}
		if *codexHome != "" {
			codexPoolDirs = []string{*codexHome}
		}

		buildCfg := BuildConfig{
			RepoRoot:        absRepo,
			PlanPath:        currentPlanPath,
			PolicyPath:      *policy,
			Workers:         *workers,
			AuthMode:        *authMode,
			ClaudeBinary:    *claudeBin,
			CodexBinary:     *codexBin,
			ClaudeConfigDir: *claudeConfigDir,
			CodexHome:       *codexHome,
			ClaudePoolDirs:  claudePoolDirs,
			CodexPoolDirs:   codexPoolDirs,
			BuildCommand:    *buildC,
			TestCommand:     *testC,
			LintCommand:     *lintC,
			ROIFilter:       "skip", // no ROI filtering in ship mode
			UseSQLite:       true,   // ship mode always uses SQLite for concurrency safety
			Timeout:         *timeout,
		}

		buildReport, buildErr := runBuild(buildCfg)
		if buildErr != nil {
			shipBlockedReason = fmt.Sprintf("build step failed: %v", buildErr)
			break
		}
		totalCost += buildReport.TotalCost

		// CRITICAL: Gate on build success before proceeding to review
		// This prevents false-progress where failed builds get reviewed
		if !buildReport.Success {
			shipBlockedReason = fmt.Sprintf("build round %d failed: %d task(s) failed", round, buildReport.TasksFailed)
			fmt.Printf("\n  Build incomplete: %d/%d tasks failed\n", buildReport.TasksFailed, buildReport.TasksTotal)
			fmt.Println("  Cannot proceed to review with failed tasks.")
			break
		}
		fmt.Printf("  Build complete: %d/%d tasks succeeded\n\n", buildReport.TasksDone, buildReport.TasksTotal)

		// Check plan-level ship blockers and cross-phase verification.
		// These are set by the planner and MUST be satisfied before shipping.
		var shipBlockers []string
		var crossPhaseChecks []string
		if planObj, planErr := plan.LoadFile(currentPlanPath); planErr == nil {
			shipBlockers = planObj.ShipBlockers
			crossPhaseChecks = planObj.CrossPhaseVerification
			if len(shipBlockers) > 0 {
				fmt.Printf("  Ship blockers from plan (will be verified by reviewer):\n")
				for _, b := range shipBlockers {
					fmt.Printf("    - %s\n", b)
				}
			}
			if len(crossPhaseChecks) > 0 {
				fmt.Printf("  Cross-phase verification (will be verified by reviewer):\n")
				for _, v := range crossPhaseChecks {
					fmt.Printf("    - %s\n", v)
				}
				fmt.Println()
			}
		}

		// Step 2: Comprehensive review (opposite-family model, direct runner call)
		// NOT using PlanOnly workflow -- that runs a plan prompt on Claude.
		// This calls the reviewer engine directly with the review prompt.
		// ShipBlockers and CrossPhaseVerification are injected into the review prompt
		// so the reviewer must explicitly verify each one.
		fmt.Println("Step 2: Comprehensive review (opposite-family)")
		reviewPrompt := buildShipReviewPrompt(*task, round, shipBlockers, crossPhaseChecks)

		// Use Codex as reviewer (opposite family from Claude builder)
		reviewRunner := engine.NewCodexRunner(*codexBin)
		shipRuntimeDir := filepath.Join(absRepo, ".stoke", "runtime", fmt.Sprintf("ship-review-round-%d", round))
		if err := os.MkdirAll(shipRuntimeDir, 0o755); err != nil {
			fatal("create runtime dir: %v", err)
		}
		reviewSpec := engine.RunSpec{
			Prompt:        reviewPrompt,
			WorktreeDir:   absRepo,
			RuntimeDir:    shipRuntimeDir,
			Mode:          engine.AuthMode(*authMode),
			PoolConfigDir: *codexHome, // default: CLI flag for Codex config
			Phase: engine.PhaseSpec{
				Name:         fmt.Sprintf("ship-review-round-%d", round),
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				MCPEnabled:   false,
				MaxTurns:     10,
				Sandbox:      true,
				ReadOnly:     true,
			},
		}

		// Override with discovered pool if available (pool > CLI flag)
		if discoveredPools := autoDiscoverPools(); discoveredPools != nil {
			pool, acqErr := discoveredPools.Acquire(subscriptions.ProviderCodex, fmt.Sprintf("ship-review-%d", round))
			if acqErr == nil {
				reviewSpec.PoolConfigDir = pool.ConfigDir
				defer discoveredPools.Release(pool.ID, false)
			}
		}

		fmt.Printf("  Reviewer: codex (read-only, %d max turns)\n", reviewSpec.Phase.MaxTurns)
		reviewResult, reviewErr := reviewRunner.Run(ctx, reviewSpec, func(ev stream.Event) {
			if ev.DeltaText != "" {
				fmt.Print(ev.DeltaText)
			}
		})
		totalCost += reviewResult.CostUSD
		fmt.Printf("\n  Review cost: $%.4f\n", reviewResult.CostUSD)

		if reviewErr != nil {
			fmt.Printf("  Review failed: %v\n", reviewErr)
			// Fallback: try Claude as reviewer
			fmt.Println("  Falling back to Claude reviewer...")
			fallbackRunner := engine.NewClaudeRunner(*claudeBin)
			fbRuntimeDir := filepath.Join(absRepo, ".stoke", "runtime", fmt.Sprintf("ship-review-round-%d-fallback", round))
			if err := os.MkdirAll(fbRuntimeDir, 0o755); err != nil {
				fatal("create runtime dir: %v", err)
			}
			fallbackSpec := engine.RunSpec{
				Prompt:        reviewPrompt,
				WorktreeDir:   absRepo,
				RuntimeDir:    fbRuntimeDir,
				Mode:          engine.AuthMode(*authMode),
				PoolConfigDir: *claudeConfigDir, // default: CLI flag for Claude config (NOT leaked from Codex)
				Phase: engine.PhaseSpec{
					Name:         fmt.Sprintf("ship-review-round-%d-fallback", round),
					BuiltinTools: []string{"Read", "Glob", "Grep"},
					MCPEnabled:   false,
					MaxTurns:     10,
					Sandbox:      true,
					ReadOnly:     true,
				},
			}
			// Override with discovered pool if available
			if discoveredPools := autoDiscoverPools(); discoveredPools != nil {
				pool, acqErr := discoveredPools.Acquire(subscriptions.ProviderClaude, fmt.Sprintf("ship-review-%d-fb", round))
				if acqErr == nil {
					fallbackSpec.PoolConfigDir = pool.ConfigDir
					defer discoveredPools.Release(pool.ID, false)
				}
			}
			reviewResult, reviewErr = fallbackRunner.Run(ctx, fallbackSpec, func(ev stream.Event) {
				if ev.DeltaText != "" {
					fmt.Print(ev.DeltaText)
				}
			})
			totalCost += reviewResult.CostUSD
			if reviewErr != nil {
				fmt.Printf("  Both reviewers failed: %v\n", reviewErr)
				shipBlockedReason = fmt.Sprintf("both reviewers failed: %v", reviewErr)
				break
			}
		}

		reviewOutput := reviewResult.ResultText

		// Step 3: Parse review verdict (fail-closed: malformed = not shipping)
		fmt.Println("\nStep 3: Parsing review verdict")
		verdict, parseErr := parseShipVerdict(reviewOutput)
		if parseErr != nil {
			fmt.Printf("\n✗ Review output is not valid JSON. NOT shipping.\n")
			fmt.Printf("  Parse error: %v\n", parseErr)
			fmt.Printf("  Raw output (first 500 chars): %s\n", trunc(reviewOutput, 500))
			shipBlockedReason = fmt.Sprintf("review returned invalid JSON (round %d): %v", round, parseErr)
			if round == *maxRounds {
				fmt.Println("  Max rounds reached. Review never produced valid JSON.")
			} else {
				fmt.Println("  Will retry review in next round.")
			}
			continue
		}

		if verdict.Ship && len(verdict.BlockingFixes) == 0 {
			fmt.Printf("\n✓ REVIEWER APPROVED (round %d)\n", round)
			fmt.Printf("  Verdict: %s\n", verdict.Summary)
			if len(verdict.Notes) > 0 {
				fmt.Println("  Notes:")
				for _, n := range verdict.Notes {
					fmt.Printf("    - %s\n", n)
				}
			}
			shipped = true
			shipBlockedReason = ""
			break
		}

		if len(verdict.BlockingFixes) == 0 && !verdict.Ship {
			// Reviewer said don't ship but gave no fixes -- treat as blocker
			fmt.Printf("\n✗ Reviewer said no but provided no fixes.\n")
			fmt.Printf("  Summary: %s\n", verdict.Summary)
			shipBlockedReason = "reviewer rejected: " + verdict.Summary
			break
		}

		fmt.Printf("\n✗ Round %d: %d blocking fixes required\n", round, len(verdict.BlockingFixes))
		for i, fix := range verdict.BlockingFixes {
			fmt.Printf("  %d. [%s] %s\n", i+1, fix.Category, trunc(fix.Description, 70))
		}

		if round == *maxRounds {
			fmt.Printf("\n⚠ Max rounds (%d) reached. %d blocking fixes remain.\n", *maxRounds, len(verdict.BlockingFixes))
			fmt.Println("  Run again or fix manually.")
			shipBlockedReason = fmt.Sprintf("max rounds (%d) reached with %d blocking fixes", *maxRounds, len(verdict.BlockingFixes))
			break
		}

		// Step 4: Generate fix plan from review findings
		fmt.Printf("\nStep 4: Generating fix plan for round %d\n", round+1)
		var fixTasks []plan.Task
		for i, fix := range verdict.BlockingFixes {
			fixTasks = append(fixTasks, plan.Task{
				ID:          fmt.Sprintf("FIX-R%d-%d", round, i+1),
				Description: fmt.Sprintf("[%s] %s", fix.Category, fix.Description),
				Files:       fix.Files,
				Type:        "fix",
			})
		}

		fixPlan := &plan.Plan{
			ID:                     fmt.Sprintf("fix-round-%d", round+1),
			Description:            fmt.Sprintf("Round %d fixes: %d blocking issues from review", round+1, len(fixTasks)),
			Tasks:                  fixTasks,
			ShipBlockers:           shipBlockers,
			CrossPhaseVerification: crossPhaseChecks,
		}

		fixPlanPath := filepath.Join(absRepo, ".stoke", fmt.Sprintf("fix-plan-round-%d.json", round+1))
		if err := os.MkdirAll(filepath.Dir(fixPlanPath), 0755); err != nil {
			fatal("create dir: %v", err)
		}
		fixData, err := json.MarshalIndent(fixPlan, "", "  ")
		if err != nil {
			fatal("marshal fix plan: %v", err)
		}
		if err := os.WriteFile(fixPlanPath, fixData, 0644); err != nil {
			fatal("write fix plan: %v", err)
		}
		currentPlanPath = fixPlanPath
		fmt.Printf("  Fix plan: %s (%d tasks)\n\n", fixPlanPath, len(fixTasks))
	}

	elapsed := time.Since(startTime)
	if shipped {
		fmt.Printf("\n═══ Ship approved ═══\n")
		fmt.Printf("  Duration: %s\n", elapsed.Round(time.Second))
		fmt.Printf("  Total cost: $%.4f\n", totalCost)
		return
	}

	fmt.Printf("\n═══ Ship blocked ═══\n")
	fmt.Printf("  Reason: %s\n", shipBlockedReason)
	fmt.Printf("  Duration: %s\n", elapsed.Round(time.Second))
	fmt.Printf("  Total cost: $%.4f\n", totalCost)
	os.Exit(1)
}

// buildShipReviewPrompt creates the comprehensive review prompt.
// ShipBlockers and CrossPhaseVerification are injected as mandatory check items
// that the reviewer must explicitly verify. They are treated as quoted data, not
// raw instructions, to prevent planner output from acting as prompt injection.
func buildShipReviewPrompt(task string, round int, shipBlockers, crossPhaseChecks []string) string {
	roundContext := ""
	if round > 1 {
		roundContext = fmt.Sprintf("\nThis is review round %d. Previous rounds found blocking issues that were fixed. Check if the fixes are correct AND look for any new issues introduced by the fixes.\n", round)
	}

	blockerSection := ""
	if len(shipBlockers) > 0 {
		blockerSection = "\n## MANDATORY SHIP BLOCKERS (from plan)\nThe planner identified these as MUST-BE-TRUE before shipping. You MUST verify each one.\nIf ANY blocker is not satisfied, set \"ship\": false and include it in blocking_fixes.\n"
		for i, b := range shipBlockers {
			blockerSection += fmt.Sprintf("  %d. [BLOCKER] %q\n", i+1, b)
		}
	}

	crossPhaseSection := ""
	if len(crossPhaseChecks) > 0 {
		crossPhaseSection = "\n## CROSS-PHASE VERIFICATION (from plan)\nThese integration checks span multiple tasks. Verify each one holds true.\nIf ANY check fails, include it in blocking_fixes.\n"
		for i, c := range crossPhaseChecks {
			crossPhaseSection += fmt.Sprintf("  %d. [CROSS-PHASE] %q\n", i+1, c)
		}
	}

	return fmt.Sprintf(`You are a senior staff engineer doing a comprehensive pre-ship review.
%s%s%s
Review this codebase for the following vectors. For each vector, evaluate the CURRENT state of the code.

Task that was implemented: %s

Return ONLY valid JSON:
{
  "ship": true/false,
  "summary": "one-line verdict",
  "blocking_fixes": [
    {
      "category": "code|architecture|security|scaling|tests|ux|docs",
      "severity": "critical|high",
      "description": "what is wrong and how to fix it",
      "files": ["path/to/file.ts"]
    }
  ],
  "notes": ["non-blocking observations"]
}

Review vectors:

1. CODE QUALITY
   - No place` + "holder" + ` code (` + "TO" + `DO, FIX` + "ME" + `, Not` + "Implemented" + `Error)
   - No type bypasses (ts` + "-" + `ignore, as ` + "any" + `, eslint` + "-disable" + `)
   - No empty catch blocks
   - No hardcoded secrets
   - Error handling is real (not swallowed)
   - No dead code or unused imports

2. ARCHITECTURE
   - Changes are coherent with existing patterns
   - No circular dependencies introduced
   - Separation of concerns maintained
   - No tight coupling to implementation details

3. SECURITY
   - Input validation on all entry points
   - No SQL injection (raw queries with interpolation)
   - No XSS (unsanitized output)
   - Auth/authz checks on protected routes
   - Secrets not in source code

4. SCALING
   - No N+1 query patterns
   - No unbounded loops or memory allocations
   - Connection pooling where needed
   - Pagination on list endpoints

5. TEST COVERAGE
   - New functionality has tests
   - Tests assert real behavior (not tautological)
   - Edge cases covered
   - No test.todo or .skip
   - Error paths tested

6. UX (if applicable)
   - Loading states handled
   - Error states shown to user
   - Form validation present
   - Accessibility basics (labels, aria)

7. DOCS
   - README reflects current state
   - API changes documented
   - Breaking changes called out
   - Setup/install instructions work

Rules:
- "ship": true means ZERO blocking fixes. Only set this if genuinely ready.
- Only include blocking_fixes for issues that would cause bugs, security holes, or user-facing failures.
- Notes are for improvements that are nice-to-have but not blocking.
- Be specific: file paths, line numbers, exact descriptions.
- If this is round 2+, verify previous fixes are actually correct.
`, roundContext, blockerSection, crossPhaseSection, task)
}

// shipVerdict is the parsed output of the comprehensive review.
type shipVerdict struct {
	Ship          bool
	Summary       string
	BlockingFixes []shipFix
	Notes         []string
}

type shipFix struct {
	Category    string   `json:"category"`
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

func parseShipVerdict(raw string) (shipVerdict, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	var parsed struct {
		Ship          bool      `json:"ship"`
		Summary       string    `json:"summary"`
		BlockingFixes []shipFix `json:"blocking_fixes"`
		Notes         []string  `json:"notes"`
	}

	err := json.Unmarshal([]byte(s), &parsed)
	if err != nil {
		// Try to find JSON in the output
		if idx := strings.Index(s, "{"); idx >= 0 {
			if end := strings.LastIndex(s, "}"); end > idx {
				err = json.Unmarshal([]byte(s[idx:end+1]), &parsed)
			}
		}
	}

	if err != nil {
		return shipVerdict{}, fmt.Errorf("invalid review JSON: %w", err)
	}

	return shipVerdict{
		Ship:          parsed.Ship,
		Summary:       parsed.Summary,
		BlockingFixes: parsed.BlockingFixes,
		Notes:         parsed.Notes,
	}, nil
}

// --- add-claude: register a Claude subscription pool ---

func addClaudeCmd(args []string) {
	fs := flag.NewFlagSet("add-claude", flag.ExitOnError)
	claudeBin := fs.String("claude-bin", "claude", "Claude binary")
	label := fs.String("label", "", "Pool label (e.g. 'Work account', 'Personal')")
	fs.Parse(args)

	fmt.Println("⚡ STOKE add-claude")
	fmt.Println()

	poolID, err := pools.AddClaude(*claudeBin, *label)
	if err != nil {
		fatal("add-claude: %v", err)
	}

	fmt.Printf("\n  Pool %s ready.\n", poolID)
	fmt.Println("  Stoke will automatically use all registered pools for parallel execution.")
}

// --- add-codex: register a Codex subscription pool ---

func addCodexCmd(args []string) {
	fs := flag.NewFlagSet("add-codex", flag.ExitOnError)
	codexBin := fs.String("codex-bin", "codex", "Codex binary")
	label := fs.String("label", "", "Pool label (e.g. 'Work OpenAI', 'Personal')")
	fs.Parse(args)

	fmt.Println("⚡ STOKE add-codex")
	fmt.Println()

	poolID, err := pools.AddCodex(*codexBin, *label)
	if err != nil {
		fatal("add-codex: %v", err)
	}

	fmt.Printf("\n  Pool %s ready.\n", poolID)
	fmt.Println("  Stoke will automatically use all registered pools for parallel execution.")
}

// --- pools: list registered pools ---

func poolsCmd(args []string) {
	manifest, err := pools.LoadManifest()
	if err != nil {
		fatal("load pools: %v", err)
	}

	if len(manifest.Pools) == 0 {
		fmt.Println("No pools registered.")
		fmt.Println("  Add one: stoke add-claude")
		return
	}

	fmt.Printf("⚡ STOKE pools (%d registered)\n\n", len(manifest.Pools))
	for _, p := range manifest.Pools {
		status := "ready"
		token := readOAuthToken(p.ConfigDir)
		if token == "" {
			status = "no token (re-login needed)"
		}

		fmt.Printf("  %-12s %-20s %s\n", p.ID, p.Label, status)
		fmt.Printf("  %-12s dir: %s\n", "", p.ConfigDir)
		if !p.LastUsed.IsZero() {
			fmt.Printf("  %-12s last used: %s\n", "", p.LastUsed.Format("2006-01-02 15:04"))
		}
		fmt.Println()
	}

	fmt.Printf("  Claude pools: %d\n", len(manifest.ClaudeDirs()))
	fmt.Printf("  Codex pools:  %d\n", len(manifest.CodexDirs()))
}

// --- remove-pool: unregister a pool ---

func removePoolCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: stoke remove-pool <pool-id>")
		os.Exit(2)
	}

	poolID := args[0]
	fmt.Printf("Removing pool %s... ", poolID)

	if err := pools.RemovePool(poolID); err != nil {
		fatal("%v", err)
	}
	fmt.Println("done.")
}

// autoDiscoverPools loads pool dirs from the manifest for use in build/ship.
func autoDiscoverPools() *subscriptions.Manager {
	manifest, err := pools.LoadManifest()
	if err != nil || len(manifest.Pools) == 0 {
		return nil
	}

	var poolConfigs []subscriptions.Pool
	for _, p := range manifest.Pools {
		provider := subscriptions.ProviderClaude
		if p.Provider == "codex" {
			provider = subscriptions.ProviderCodex
		}
		poolConfigs = append(poolConfigs, subscriptions.Pool{
			ID:        p.ID,
			Provider:  provider,
			ConfigDir: p.ConfigDir,
		})
	}

	if len(poolConfigs) == 0 {
		return nil
	}
	return subscriptions.NewManager(poolConfigs)
}

func gitHead(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// --- helpers ---

// SmartDefaults captures the auto-detected configuration that `stoke` uses
// when launched bare with no arguments. The user explicitly asked for
// "use all smart settings / use local litellm / use native executor" to be the
// default behavior with zero flags.
type SmartDefaults struct {
	RunnerMode    string // claude, codex, native
	NativeBaseURL string // e.g. http://localhost:8000 for LiteLLM
	NativeAPIKey  string // from env: LITELLM_API_KEY, ANTHROPIC_API_KEY, OPENAI_API_KEY
	NativeModel   string // e.g. claude-sonnet-4-6
	Notes         []string // human-readable explanation of decisions
}

// detectSmartDefaults probes the local environment for the best default
// runner. Priority:
//  1. LITELLM_BASE_URL set or http://localhost:8000 reachable → native runner
//     speaking Anthropic protocol to LiteLLM (works with LiteLLM routing to
//     Minimax, OpenRouter, Anthropic, etc.).
//  2. claude binary on PATH → claude runner (subprocess).
//  3. codex binary on PATH → codex runner (subprocess).
//  4. ANTHROPIC_API_KEY set → native runner direct to api.anthropic.com.
//  5. Fall back to claude runner (will fail loudly if not installed).
func detectSmartDefaults() SmartDefaults {
	d := SmartDefaults{
		NativeModel: "claude-sonnet-4-6",
	}
	if m := os.Getenv("STOKE_NATIVE_MODEL"); m != "" {
		d.NativeModel = m
	}

	// 1+2. LiteLLM auto-discovery: checks LITELLM_BASE_URL env, probes
	// common ports (4000, 8000, 7813, 8080, 4100, 8888), and falls back
	// to parsing ~/.litellm/config.yaml.
	if disc := litellmPkg.Discover(); disc != nil {
		d.RunnerMode = "native"
		d.NativeBaseURL = disc.BaseURL
		d.NativeAPIKey = disc.APIKey
		if d.NativeAPIKey == "" {
			d.NativeAPIKey = provider.LocalLiteLLMStub
		}
		d.Notes = append(d.Notes, fmt.Sprintf("LiteLLM auto-discovered at %s (%d models) → native runner", disc.BaseURL, len(disc.Models)))
		return d
	}

	// 3. Claude binary
	if _, err := exec.LookPath("claude"); err == nil {
		d.RunnerMode = "claude"
		d.Notes = append(d.Notes, "claude binary on PATH → claude runner")
		return d
	}

	// 4. Codex binary
	if _, err := exec.LookPath("codex"); err == nil {
		d.RunnerMode = "codex"
		d.Notes = append(d.Notes, "codex binary on PATH → codex runner")
		return d
	}

	// 5. Anthropic API key
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		d.RunnerMode = "native"
		d.NativeAPIKey = key
		d.Notes = append(d.Notes, "ANTHROPIC_API_KEY set → native runner direct to api.anthropic.com")
		return d
	}

	d.RunnerMode = "claude"
	d.Notes = append(d.Notes, "no runner detected — defaulting to claude (will require `claude` binary)")
	return d
}

// firstNonEmpty returns the first non-empty string from the argument list.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// probeReachable performs a quick GET to check if a URL responds at all.
// Used for LiteLLM autodetection. Any HTTP response (including 401/404) counts
// as "something is listening" — we are not validating auth here.
func probeReachable(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 600
}

// runSOWWithDefaults executes a SOW string using the smart-defaults runner.
// Used by the /build-from-scope slash command.
func runSOWWithDefaults(absRepo, sowText string, defaults SmartDefaults) {
	// Persist the SOW to .stoke/sow-from-scope.json so the existing sowCmd
	// loader can pick it up. Accepts both JSON and YAML — sowCmd handles both.
	stokeDir := filepath.Join(absRepo, ".stoke")
	if err := os.MkdirAll(stokeDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "  Error creating .stoke: %v\n", err)
		return
	}
	sowPath := filepath.Join(stokeDir, "sow-from-scope.json")
	if !strings.HasPrefix(strings.TrimSpace(sowText), "{") {
		// Looks like YAML — write to .yaml extension instead.
		sowPath = filepath.Join(stokeDir, "sow-from-scope.yaml")
	}
	if err := os.WriteFile(sowPath, []byte(sowText), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "  Error writing SOW: %v\n", err)
		return
	}
	fmt.Printf("  SOW written to %s\n\n", sowPath)

	args := []string{
		"--repo", absRepo,
		"--file", sowPath,
		"--runner", defaults.RunnerMode,
	}
	if defaults.NativeBaseURL != "" {
		args = append(args, "--native-base-url", defaults.NativeBaseURL)
	}
	if defaults.NativeAPIKey != "" {
		args = append(args, "--native-api-key", defaults.NativeAPIKey)
	}
	if defaults.NativeModel != "" {
		args = append(args, "--native-model", defaults.NativeModel)
	}
	sowCmd(args)
}

// readPastedSOW reads multi-line input from the REPL until a blank line
// followed by END (or EOF) marker. Used by /build-from-scope to accept
// pasted SOW content directly in the shell.
func readPastedSOW(scanner *bufio.Scanner) string {
	var b strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "END" || strings.TrimSpace(line) == "EOF" {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// launchREPL starts the Stoke interactive shell.
// Slash commands dispatch to orchestrated workflows.
// Free text goes through claude -p as a single task.
func launchREPL() {
	absRepo, _ := filepath.Abs(".")

	// Smart defaults: detect LiteLLM, claude/codex binaries, API keys.
	// User explicitly asked for "use all smart settings / use local litellm /
	// use native executor" to be the zero-flag default.
	defaults := detectSmartDefaults()

	// Stand up the chat session so free text becomes a real
	// conversation instead of a /run dispatch. If no provider is
	// available, chatSession is nil and the OnChat handler falls
	// back to the legacy "run the text as a task" path with a note.
	chatSession, chatErr := buildChatSession(defaults)
	dispatcher := &stokeDispatcher{absRepo: absRepo, defaults: defaults}

	// Banner
	fmt.Printf("\n  \033[1;36mStoke %s\033[0m  supervised AI build orchestrator\n", version)
	fmt.Printf("  Smart defaults active:\n")
	fmt.Printf("    runner:  %s\n", defaults.RunnerMode)
	if defaults.NativeBaseURL != "" {
		fmt.Printf("    base:    %s\n", defaults.NativeBaseURL)
	}
	if defaults.NativeModel != "" {
		fmt.Printf("    model:   %s\n", defaults.NativeModel)
	}
	fmt.Printf("    super:   boulder (no wall-clock timeouts)\n")
	if chatSession != nil {
		fmt.Printf("    chat:    %s\n", providerHint(defaults))
	} else if chatErr != nil {
		fmt.Printf("    chat:    \033[33m%s\033[0m\n", describeChatFailure(chatErr))
	}
	for _, note := range defaults.Notes {
		fmt.Printf("    note:    %s\n", note)
	}
	fmt.Println()
	if chatSession != nil {
		fmt.Println("  Type naturally to chat. When you agree (\"ya build it\", \"make that a scope\"),")
		fmt.Println("  Stoke dispatches the conversation to the right command automatically.")
	} else {
		fmt.Println("  Type naturally to kick off a /run task — or use slash commands directly.")
	}
	fmt.Println("  Slash commands: /ship /build /scope /run /plan /audit /scan /status /help /quit")
	fmt.Println()

	r := repl.New(absRepo)
	r.RegisterBuiltins()

	// /build-from-scope: paste a SOW directly or pass a file path.
	r.Register(repl.Command{
		Name: "build-from-scope",
		Description: "Build a project from a pasted or file-based Statement of Work",
		Usage: "/build-from-scope [<path-to-sow.json>]\n               (with no path: paste SOW, then 'END' on a blank line)",
		Run: func(args string) {
			arg := strings.TrimSpace(args)
			var sowText string
			if arg != "" && !strings.HasPrefix(arg, "{") {
				// Treat as a file path
				path := arg
				if !filepath.IsAbs(path) {
					path = filepath.Join(absRepo, arg)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					fmt.Printf("  Error reading SOW file %q: %v\n", path, err)
					return
				}
				sowText = string(data)
				fmt.Printf("  Loaded SOW from %s (%d bytes)\n", path, len(sowText))
			} else if strings.HasPrefix(arg, "{") {
				// Inline JSON on the command line
				sowText = arg
			} else {
				// Heredoc paste mode
				fmt.Println("  Paste your SOW (JSON or YAML), then type END on a blank line:")
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
				sowText = readPastedSOW(scanner)
				fmt.Printf("  Received %d bytes\n", len(sowText))
			}
			if strings.TrimSpace(sowText) == "" {
				fmt.Println("  No SOW provided. Aborting.")
				return
			}
			runSOWWithDefaults(absRepo, sowText, defaults)
		},
	})

	// Register all slash commands
	r.Register(repl.Command{
		Name: "ship", Description: "Build -> review -> fix -> ... until ship-ready",
		Usage: "/ship Add JWT auth and rate limiting",
		Run: func(args string) {
			if args == "" {
				fmt.Println("  Usage: /ship <what to build>")
				return
			}
			shipCmd([]string{"--task", args, "--repo", absRepo})
		},
	})

	r.Register(repl.Command{
		Name: "build", Description: "Execute plan with parallel agents",
		Usage: "/build [plan-file]",
		Run: func(args string) {
			planPath := "stoke-plan.json"
			if args != "" {
				planPath = args
			}
			buildCmd([]string{"--plan", planPath, "--repo", absRepo})
		},
	})

	r.Register(repl.Command{
		Name: "scope", Description: "Interactive read-only session for planning",
		Run: func(args string) {
			scopeCmd([]string{"--repo", absRepo})
		},
	})

	r.Register(repl.Command{
		Name: "repair", Description: "Scan -> fix -> verify cycle",
		Usage: "/repair [--security] [--dry-run]",
		Run: func(args string) {
			repairArgs := []string{"--repo", absRepo}
			if strings.Contains(args, "--security") {
				repairArgs = append(repairArgs, "--security")
			}
			if strings.Contains(args, "--dry-run") {
				repairArgs = append(repairArgs, "--dry-run")
			}
			repairCmd(repairArgs)
		},
	})

	r.Register(repl.Command{
		Name: "scan", Description: "Deterministic code scan",
		Usage: "/scan [--security] [--json]",
		Run: func(args string) {
			scanArgs := []string{"--repo", absRepo}
			if strings.Contains(args, "--security") {
				scanArgs = append(scanArgs, "--security")
			}
			if strings.Contains(args, "--json") {
				scanArgs = append(scanArgs, "--json")
			}
			scanCmd(scanArgs)
		},
	})

	r.Register(repl.Command{
		Name: "audit", Description: "Multi-persona AI review",
		Usage: "/audit [--dry-run]",
		Run: func(args string) {
			auditArgs := []string{"--repo", absRepo}
			if strings.Contains(args, "--dry-run") {
				auditArgs = append(auditArgs, "--dry-run")
			}
			auditCmd(auditArgs)
		},
	})

	r.Register(repl.Command{
		Name: "plan", Description: "Generate task plan (headless)",
		Usage: "/plan <goal>",
		Run: func(args string) {
			if args == "" {
				fmt.Println("  Usage: /plan <what to plan>")
				return
			}
			planCmd([]string{"--task", args, "--repo", absRepo})
		},
	})

	r.Register(repl.Command{
		Name: "run", Description: "Execute single task through full pipeline",
		Usage: "/run <task description>",
		Run: func(args string) {
			if args == "" {
				fmt.Println("  Usage: /run <task description>")
				return
			}
			runCmd([]string{"--task", args, "--repo", absRepo})
		},
	})

	r.Register(repl.Command{
		Name: "yolo", Description: "Launch Claude Code with full Stoke guards",
		Run: func(args string) {
			yoloCmd([]string{"--repo", absRepo})
		},
	})

	r.Register(repl.Command{
		Name: "findings", Description: "Show convergence findings for a mission",
		Usage: "/findings <mission-id> [--severity blocking] [--all] [--json]",
		Run: func(args string) {
			if args == "" {
				fmt.Println("  Usage: /findings <mission-id> [--severity blocking] [--category test] [--all] [--json]")
				return
			}
			parts := strings.Fields(args)
			cmdArgs := []string{"--id", parts[0]}
			cmdArgs = append(cmdArgs, parts[1:]...)
			missionFindingsCmd(cmdArgs)
		},
	})

	r.Register(repl.Command{
		Name: "status", Description: "Show session dashboard",
		Run: func(args string) {
			statusCmd([]string{"--repo", absRepo})
		},
	})

	r.Register(repl.Command{
		Name: "pool", Description: "Show subscription utilization",
		Run: func(args string) {
			poolCmd([]string{})
		},
	})

	r.Register(repl.Command{
		Name: "add-claude", Description: "Add a Claude Max subscription to the pool",
		Usage: "/add-claude [label]",
		Run: func(args string) {
			addClaudeCmd([]string{"--label", args})
		},
	})

	r.Register(repl.Command{
		Name: "add-codex", Description: "Add a Codex/OpenAI subscription to the pool",
		Usage: "/add-codex [label]",
		Run: func(args string) {
			addCodexCmd([]string{"--label", args})
		},
	})

	r.Register(repl.Command{
		Name: "pools", Description: "List all registered subscription pools",
		Run: func(args string) {
			poolsCmd([]string{})
		},
	})

	r.Register(repl.Command{
		Name: "remove-pool", Description: "Remove a pool by ID",
		Usage: "/remove-pool <pool-id>",
		Run: func(args string) {
			if args == "" {
				fmt.Println("  Usage: /remove-pool <pool-id>")
				return
			}
			removePoolCmd([]string{args})
		},
	})

	r.Register(repl.Command{
		Name: "help", Description: "Show available commands",
		Run: func(args string) {}, // handled by REPL itself
	})

	// Free text -> the smart chat session. The LLM chats until the user
	// agrees on something, then emits a dispatcher tool call that routes
	// back into runCmd/shipCmd/scopeCmd/etc. via stokeDispatcher. If no
	// provider is available (chatSession == nil), chatOnceREPL falls back
	// to the old "run the text as a task" behavior with a warning.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.OnChat = func(input string) {
		chatOnceREPL(ctx, chatSession, dispatcher, input)
	}

	r.Run()
}

// currentShellProgress and currentShellSessions are package-level hooks the
// TUI shell sets while dispatching a command. sowCmd checks them to stream
// session progress into the Sessions pane. When nil, sowCmd runs exactly
// as it does in CLI mode.
var (
	currentShellProgress func(tui.SessionDisplay)
	currentShellSessions func([]tui.SessionDisplay)
)

// launchShell starts the full-screen Bubble Tea shell. Smart defaults
// autodetect the runner/base/model the same way launchREPL does. Slash
// commands and free text route through the same dispatchers as the line
// REPL, but their stdout is captured into the shell's log pane instead of
// going directly to the terminal.
//
// Known limitation: commands that read from stdin interactively (e.g. the
// /interview flow) don't work in full-screen mode yet — the TUI owns
// stdin. Users who need interactive commands should use the line REPL.
func launchShell(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	defaults := detectSmartDefaults()
	chatSession, chatErr := buildChatSession(defaults)
	cfg := tui.ShellConfig{
		RepoRoot:   absRepo,
		Version:    version,
		Runner:     defaults.RunnerMode,
		BaseURL:    defaults.NativeBaseURL,
		Model:      defaults.NativeModel,
		Supervisor: "boulder (no wall-clock timeouts)",
		Notes:      defaults.Notes,
	}
	if chatSession != nil {
		cfg.Notes = append(cfg.Notes, "chat mode "+providerHint(defaults)+" — type to talk, agree to dispatch")
	} else if chatErr != nil {
		cfg.Notes = append(cfg.Notes, describeChatFailure(chatErr))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := func(sh *tui.Shell, input string) string {
		// Capture stdout/stderr during the command's execution and stream
		// each line into the shell's log pane via sh.Append. This stays
		// active for the whole handler because dispatcher tools run
		// subcommands whose stdout should still land in the log pane.
		restore, captureDone := captureStdoutTo(sh)
		// Wire session-progress hooks so sowCmd pushes into the Sessions pane.
		currentShellProgress = func(s tui.SessionDisplay) { sh.UpdateSession(s) }
		currentShellSessions = func(list []tui.SessionDisplay) { sh.SetSessions(list) }
		defer func() {
			currentShellProgress = nil
			currentShellSessions = nil
			restore()
			<-captureDone
		}()

		if strings.HasPrefix(input, "/") {
			return dispatchSlash(sh, absRepo, defaults, input)
		}
		// Free text -> smart chat. Dispatcher tool calls route back
		// through stokeDispatcher to the real pipeline.
		disp := &stokeDispatcher{absRepo: absRepo, defaults: defaults, sh: sh}
		chatOnceShell(ctx, sh, chatSession, disp, input)
		return "chat"
	}

	shell := tui.NewShell(cfg, handler)
	if err := shell.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
}

// dispatchSlash routes a slash command inside the full-screen shell to the
// appropriate Cmd. Returns a short status message. All command stdout is
// already being captured by the caller's captureStdoutTo wrapper.
func dispatchSlash(sh *tui.Shell, absRepo string, defaults SmartDefaults, input string) string {
	line := strings.TrimPrefix(input, "/")
	parts := strings.SplitN(line, " ", 2)
	name := strings.ToLower(parts[0])
	rest := ""
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}

	switch name {
	case "help", "?":
		sh.Append("Available commands:")
		sh.Append("  /build-from-scope <path>  Build from a SOW (JSON or YAML)")
		sh.Append("  /interview <task>         Socratic clarify, then dispatch")
		sh.Append("  /ship <goal>              Build → review → fix loop")
		sh.Append("  /build [plan.json]        Execute plan with parallel agents")
		sh.Append("  /plan <goal>              Generate task plan")
		sh.Append("  /run <task>               Single task through full pipeline")
		sh.Append("  /scope                    Read-only scope session")
		sh.Append("  /scan [--security]        Deterministic code scan")
		sh.Append("  /audit                    Multi-persona review")
		sh.Append("  /status                   Show session dashboard")
		sh.Append("  /pool                     Show subscription utilization")
		sh.Append("  /pools                    List all pools")
		sh.Append("  /quit                     Exit")
		return "help shown"
	case "build-from-scope":
		return handleBuildFromScope(sh, absRepo, defaults, rest)
	case "interview":
		if rest == "" {
			return "missing arg: /interview <task description>"
		}
		return handleInterview(sh, absRepo, defaults, rest)
	case "ship":
		if rest == "" {
			return "missing arg: /ship <goal>"
		}
		shipCmd([]string{"--task", rest, "--repo", absRepo})
		return "ship done"
	case "build":
		planPath := "stoke-plan.json"
		if rest != "" {
			planPath = rest
		}
		buildCmd([]string{"--plan", planPath, "--repo", absRepo})
		return "build done"
	case "plan":
		if rest == "" {
			return "missing arg: /plan <goal>"
		}
		planCmd([]string{"--task", rest, "--repo", absRepo})
		return "plan done"
	case "run":
		if rest == "" {
			return "missing arg: /run <task>"
		}
		rargs := []string{"--task", rest, "--repo", absRepo, "--runner", defaults.RunnerMode}
		if defaults.NativeBaseURL != "" {
			rargs = append(rargs, "--native-base-url", defaults.NativeBaseURL)
		}
		if defaults.NativeAPIKey != "" {
			rargs = append(rargs, "--native-api-key", defaults.NativeAPIKey)
		}
		if defaults.NativeModel != "" {
			rargs = append(rargs, "--native-model", defaults.NativeModel)
		}
		runCmdSafe(rargs)
		return "run done"
	case "scope":
		scopeCmd([]string{"--repo", absRepo})
		return "scope done"
	case "scan":
		scanArgs := []string{"--repo", absRepo}
		if strings.Contains(rest, "--security") {
			scanArgs = append(scanArgs, "--security")
		}
		scanCmd(scanArgs)
		return "scan done"
	case "audit":
		auditCmd([]string{"--repo", absRepo})
		return "audit done"
	case "status":
		statusCmd([]string{"--repo", absRepo})
		return "status shown"
	case "pool":
		poolCmd([]string{})
		return "pool shown"
	case "pools":
		poolsCmd([]string{})
		return "pools shown"
	default:
		return fmt.Sprintf("unknown command: /%s (try /help)", name)
	}
}

// handleBuildFromScope is the TUI version of the build-from-scope slash
// command: loads a SOW from a path or inline, writes it to .stoke, runs
// sowCmd with smart defaults. Output goes through the captured stdout.
func handleBuildFromScope(sh *tui.Shell, absRepo string, defaults SmartDefaults, arg string) string {
	var sowText string
	if arg == "" {
		sh.Append("  /build-from-scope requires a file path in TUI mode. Use the line REPL for paste mode.")
		return "missing SOW"
	}
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(absRepo, arg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		sh.Append("  Error reading SOW file %q: %v", path, err)
		return "load failed"
	}
	sowText = string(data)
	sh.Append("  Loaded SOW from %s (%d bytes)", path, len(sowText))

	runSOWWithDefaults(absRepo, sowText, defaults)
	return "sow done"
}

// handleInterview runs the Socratic deep-interview flow inside the TUI.
// Each question is posted to the log via shell.Append and the user's answer
// is gathered via shell.Prompt — a modal text-input mode that the shell
// supports natively. After all questions are answered, the clarified scope
// is dispatched to runCmd just like the line REPL's interview command.
func handleInterview(sh *tui.Shell, absRepo string, defaults SmartDefaults, task string) string {
	session := interview.NewSession(task)
	sh.Append("")
	sh.Append("Deep Interview: %s", task)
	sh.Append("Answer each question. Type 'skip' to use the default, 'done' to finish early.")
	sh.Append("")

	for !session.IsComplete() {
		q := session.NextQuestion()
		if q == nil {
			break
		}
		sh.Append("[%s] %s", q.Phase, q.Question)
		if q.Default != "" {
			sh.Append("  (default: %s)", q.Default)
		}
		ans := sh.Prompt(string(q.Phase) + ": " + q.Question)
		answer := strings.TrimSpace(ans)
		switch strings.ToLower(answer) {
		case "skip", "s":
			session.Skip()
			sh.Append("  (skipped)")
		case "done", "d":
			for !session.IsComplete() {
				session.Skip()
			}
		case "":
			session.Skip()
			sh.Append("  (using default)")
		default:
			session.Answer(answer)
		}
	}

	scope := session.Synthesize()
	sh.Append("")
	sh.Append("=== Clarified Scope ===")
	for _, line := range strings.Split(scope.ToPrompt(), "\n") {
		sh.Append("%s", line)
	}
	sh.Append("Confidence: %.0f%%", scope.Confidence*100)
	sh.Append("")

	// Dispatch the clarified prompt through runCmd with smart defaults
	rargs := []string{"--task", scope.ToPrompt(), "--repo", absRepo, "--runner", defaults.RunnerMode}
	if defaults.NativeBaseURL != "" {
		rargs = append(rargs, "--native-base-url", defaults.NativeBaseURL)
	}
	if defaults.NativeAPIKey != "" {
		rargs = append(rargs, "--native-api-key", defaults.NativeAPIKey)
	}
	if defaults.NativeModel != "" {
		rargs = append(rargs, "--native-model", defaults.NativeModel)
	}
	runCmdSafe(rargs)
	return "interview done"
}

// runCmdSafe is a wrapper around runCmd that recovers from unexpected
// panics so a bad free-text dispatch doesn't take down the TUI.
func runCmdSafe(args []string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("  runCmd panic: %v\n", r)
		}
	}()
	runCmd(args)
}

// captureToFunc is the underlying capture pipeline used by both the live
// shell capture and the test sink. It redirects os.Stdout/os.Stderr into a
// pipe and feeds each line (ANSI-stripped) to emit. Returns (restore, done).
//
// This is the single source of truth for the stdout-capture goroutine; the
// production captureStdoutTo wraps it with a tui.Shell sink, and the unit
// test wraps it with a recording sink. Keeping the implementation in one
// place means a fix here is automatically tested.
func captureToFunc(emit func(string)) (restore func(), done chan struct{}) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	done = make(chan struct{})

	r, w, err := os.Pipe()
	if err != nil {
		close(done)
		return func() {}, done
	}
	os.Stdout = w
	os.Stderr = w

	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		var pending []byte
		for {
			n, err := r.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				for {
					idx := bytes.IndexByte(pending, '\n')
					if idx < 0 {
						break
					}
					line := string(pending[:idx])
					pending = pending[idx+1:]
					emit(stripANSI(line))
				}
			}
			if err != nil {
				if len(pending) > 0 {
					emit(stripANSI(string(pending)))
				}
				return
			}
		}
	}()

	restore = func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		w.Close()
	}
	return restore, done
}

// captureStdoutTo redirects os.Stdout and os.Stderr into the shell's log
// pane for the duration of a command. Returns a restore function and a
// channel that closes when the capture goroutine exits (call restore then
// wait on the channel to guarantee all output has been flushed).
//
// Thin wrapper over captureToFunc — both production and test paths share
// the same goroutine implementation.
func captureStdoutTo(sh *tui.Shell) (restore func(), done chan struct{}) {
	return captureToFunc(func(s string) { sh.Append("%s", s) })
}

// stripANSI removes simple CSI escape sequences from a string so they
// don't pollute the log pane. The shell has its own styling.
func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until the terminating letter
			j := i + 2
			for j < len(s) && !((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// extractVerifyMetrics extracts suite-level test pass/fail and diff size
// from verification outcomes. Returns (testsPassed, testsFailed, diffLines).
//
// NOTE: These are suite-level signals (0 or 1), not individual test counts,
// because verify.Outcome only carries a boolean Success per step. The specexec
// scorer treats these as "suite passed" vs "suite failed" — a 1000-test suite
// and a 1-test suite both score the same. This is intentional: the scorer's
// job is to pick the best strategy, not to measure test quality.
//
// DiffLines is estimated from the number of changed files. This is a coarse
// proxy; a proper implementation would parse `git diff --stat` output.
func extractVerifyMetrics(outcomes []verify.Outcome, filesChanged []string) (int, int, int) {
	passed, failed := 0, 0
	for _, o := range outcomes {
		if o.Skipped {
			continue
		}
		if o.Name == "test" {
			if o.Success {
				passed = 1
			} else {
				failed = 1
			}
		}
	}
	// Estimate diff size from file count. This is intentionally rough —
	// strategies that produce any files outrank plan-only strategies (0 files).
	diffLines := len(filesChanged) * 50
	return passed, failed, diffLines
}

// markTask updates a task's status in the plan (for session persistence).

func markTask(p *plan.Plan, taskID string, status plan.Status) {
	for i := range p.Tasks {
		if p.Tasks[i].ID == taskID {
			p.Tasks[i].Status = status
			return
		}
	}
}

// checkResume loads prior session state and marks completed tasks in the plan.
func checkResume(store session.SessionStore, p *plan.Plan) {
	prev, _ := store.LoadState()
	if prev == nil {
		return
	}
	done := 0
	for _, t := range prev.Tasks {
		if t.Status == plan.StatusDone {
			done++
		}
	}
	if done >= len(prev.Tasks) {
		return
	}
	fmt.Printf("  Resuming: %d/%d done\n\n", done, len(prev.Tasks))
	completed := map[string]bool{}
	for _, t := range prev.Tasks {
		if t.Status == plan.StatusDone {
			completed[t.ID] = true
		}
	}
	for i := range p.Tasks {
		if completed[p.Tasks[i].ID] {
			p.Tasks[i].Status = plan.StatusDone
		}
	}
}

// buildRunConfig creates an app.RunConfig for a task with the given flags.
// buildRunConfigOpts holds optional fields for buildRunConfig that don't fit in the base signature.
type buildRunConfigOpts struct {
	Boulder     *boulder.Enforcer
	CostTracker *costtrack.Tracker
	TestGraph   *testselect.Graph
	RepoMap     *repomap.RepoMap
	EventBus    *hub.Bus
}

func buildRunConfig(absRepo, policyPath string, task plan.Task, authMode, claudeBin, codexBin, claudeConfigDir, codexHome, buildCmd, testCmd, lintCmd string, pools *subscriptions.Manager, worktrees *worktree.Manager, state *taskstate.TaskState, wisdomStore *wisdom.Store, onEvent func(stream.Event), opts *buildRunConfigOpts) app.RunConfig {
	cfg := app.RunConfig{
		RepoRoot:         absRepo,
		PolicyPath:       policyPath,
		Task:             task.Description,
		TaskType:         task.Type,
		TaskVerification: task.Verification,
		AllowedFiles:     task.Files,
		DryRun:           false,
		PlanOnly:         task.PlanOnly,
		AuthMode:         app.AuthMode(authMode),
		ClaudeBinary:     claudeBin,
		CodexBinary:      codexBin,
		ClaudeConfigDir:  claudeConfigDir,
		CodexHome:        codexHome,
		Pools:            pools,
		Worktrees:        worktrees,
		State:            state,
		Wisdom:           wisdomStore,
		BuildCommand:     buildCmd,
		TestCommand:      testCmd,
		LintCommand:      lintCmd,
		OnEvent:          onEvent,
		Recorder:         replay.NewRecorder(task.ID+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10), task.ID),
	}
	if opts != nil {
		cfg.Boulder = opts.Boulder
		cfg.CostTracker = opts.CostTracker
		cfg.TestGraph = opts.TestGraph
		cfg.RepoMap = opts.RepoMap
		cfg.EventBus = opts.EventBus
	}
	return cfg
}

func readOAuthToken(configDir string) string {
	data, err := os.ReadFile(filepath.Join(configDir, ".credentials.json"))
	if err != nil {
		return ""
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &creds) != nil {
		return ""
	}
	return creds.ClaudeAiOauth.AccessToken
}

func bar(pct float64, w int) string {
	n := int(pct / 100 * float64(w))
	if n > w {
		n = w
	}
	if n < 0 {
		n = 0
	}
	return strings.Repeat("█", n) + strings.Repeat("░", w-n)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// loadRawSOWText returns the raw SOW text that should be injected into
// every task's cached system prompt. When sowFilePath points to a file
// (prose .md, .json, .yaml, .txt) we read it directly — for prose this
// IS the spec, and for structured files the verbatim user input is
// more faithful than a round-tripped marshaled copy.
//
// When sowFilePath is empty (the default-lookup path), we fall back to
// marshaling the parsed SOW back to JSON.
func loadRawSOWText(sowFilePath string, sow *plan.SOW) string {
	if sowFilePath != "" {
		if data, err := os.ReadFile(sowFilePath); err == nil && len(data) > 0 {
			return string(data)
		}
	}
	if sow == nil {
		return ""
	}
	data, err := json.MarshalIndent(sow, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

// buildProseProvider returns a one-shot provider and model name the prose
// SOW converter can use. It mirrors the same runner-selection logic used by
// buildRunners in internal/app: prefer explicit NativeAPIKey, then env vars,
// then LiteLLM stub. Returns (nil, "") if no provider can be constructed —
// in that case sowCmd will surface a clear error telling the user to pass
// a native runner config or a real SOW file.
func buildProseProvider(runnerMode, apiKey, baseURL, model string) (provider.Provider, string) {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	// Only build a provider when the user has actually asked for a native
	// runner or supplied a key. If they're still using the default claude
	// runner, we shouldn't silently spin up a new API client.
	if runnerMode != "native" && apiKey == "" {
		return nil, ""
	}
	if apiKey == "" {
		for _, k := range []string{"LITELLM_API_KEY", "LITELLM_MASTER_KEY", "ANTHROPIC_API_KEY"} {
			if v := os.Getenv(k); v != "" {
				apiKey = v
				break
			}
		}
	}
	if apiKey == "" && baseURL != "" {
		apiKey = provider.LocalLiteLLMStub
	}
	if apiKey == "" {
		return nil, ""
	}
	return provider.NewAnthropicProvider(apiKey, baseURL), model
}

// readDocsDir concatenates every .md / .txt file under dir into one
// string. Non-existent dir returns ("", nil) — the caller treats that
// as "no extra docs supplied," not a failure. Used by the feasibility
// gate so operators can keep vendor docs alongside the repo and have
// stoke consult them without pasting the full text into the SOW.
func readDocsDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		b, err := os.ReadFile(dir)
		return string(b), err
	}
	var b strings.Builder
	err = filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" && ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable; docs-dir is best-effort
		}
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", rel, string(data))
		return nil
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

// persistFetchedDocs writes the feasibility gate's fetched
// documentation to <repoRoot>/.stoke/external-docs/<service>.md so
// subsequent runs can reuse it (cache hit) and so task briefings
// can reference the file path rather than re-embedding the text.
func persistFetchedDocs(repoRoot string, docs map[string]string) error {
	dir := filepath.Join(repoRoot, ".stoke", "external-docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for service, content := range docs {
		safe := strings.ToLower(service)
		safe = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				return r
			}
			return '-'
		}, safe)
		if safe == "" {
			continue
		}
		p := filepath.Join(dir, safe+".md")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
	}
	return nil
}

// ensureGitRepoOrFatal is the "auto-init git" convenience wrapper used by
// commands that need a workable git repo (run, build, sow, ship, repair,
// yolo). Empty or non-git target directories are initialized automatically;
// existing repos are left alone. Prints a one-line notice when it had to
// create a repo so the user isn't surprised.
func ensureGitRepoOrFatal(absRepo string) {
	created, err := worktree.EnsureRepo(context.Background(), absRepo)
	if err != nil {
		fatal("ensure git repo: %v", err)
	}
	if created {
		fmt.Printf("  initialized new git repo at %s\n", absRepo)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func splitPools(s string) []string {
	var dirs []string
	for _, d := range strings.Split(s, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func usage() {
	fmt.Printf(`stoke %s — AI coding orchestrator

USAGE:
  stoke <command> [flags]

COMMANDS:
  (no args)       Launch the line REPL with smart defaults
  tui             Launch the full-screen Bubble Tea shell (command input +
                  live mission monitoring). Falls back to line REPL if no TTY.
  run             Execute single task: PLAN -> EXECUTE -> VERIFY -> COMMIT
  build           Execute multi-task plan with parallel agents
  sow             Execute Statement of Work (multi-session with acceptance gates)
  plan            Generate a task plan from codebase analysis (headless)
  scope           Interactive scoping session with research loop (read-only)
  yolo            Launch Claude Code interactively with full Stoke guards
  repair          Scan -> auto-generate fix plan -> build -> re-verify
  ship            Build -> review -> fix -> review -> ... until ship-ready
  scan            Deterministic code scan (secrets, eval, TODO, debug output)
  audit           Multi-perspective review (security, perf, reliability, ops)
  inspect         Standalone codebase audit: hygiene + integration review (no SOW)
  watch           Live operator dashboard for an in-flight SOW run
  status          Show session dashboard (progress, cost, learning)
  pool            Show subscription pool utilization
  add-claude      Add a Claude Max subscription to the pool
  add-codex       Add a Codex/OpenAI subscription to the pool
  pools           List all registered subscription pools
  remove-pool     Remove a pool by ID
  mcp-serve       Start the codebase MCP server (exposes project to Claude Code)
  mcp-serve-stoke Start the Stoke MCP server (exposes Stoke as a tool to Claude Code)
  doctor          Check tool dependencies
  version         Print version

RUN FLAGS:
  --task <prompt>      Task description (required)
  --task-type <type>   Override inferred type
  --repo <path>        Repository root (default: .)
  --dry-run            Show commands without executing
  --build-cmd <cmd>    Build command (auto-detected)
  --test-cmd <cmd>     Test command (auto-detected)
  --lint-cmd <cmd>     Lint command (auto-detected)

BUILD FLAGS:
  --plan <path>        Plan file (default: stoke-plan.json)
  --workers <n>        Max parallel agents (default: 4)
  --claude-pools <dirs> Comma-separated Claude pool dirs (multi-pool)
  --codex-pools <dirs>  Comma-separated Codex pool dirs (multi-pool)
  --roi <level>        ROI filter: high, medium, low, skip (default: medium)
  --sqlite             Use SQLite session store instead of JSON
  --interactive        Launch interactive Bubble Tea TUI
  --dry-run            Show plan without executing

SOW FLAGS:
  Source:
    --file <path>           SOW file: .json, .yaml, .yml, or prose .md/.txt
                            (auto-converted via LLM, cached). Default lookup:
                            stoke-sow.{json,yaml,yml} in repo root.
    --validate              Validate SOW and exit
    --dry-run               Show SOW summary (with acceptance commands) and exit

  Runner:
    --runner <mode>         claude | codex | native | hybrid (default: claude)
    --native-api-key <key>  API key for native runner (or LITELLM_API_KEY /
                            LITELLM_MASTER_KEY / ANTHROPIC_API_KEY env)
    --native-base-url <url> LiteLLM/custom proxy URL (e.g. http://localhost:8000)
    --native-model <name>   Model name (default: claude-sonnet-4-6)
    --workers <n>           Max parallel agents per session (default: 4)
    --parallel-tasks <n>    Concurrent tasks within a session, dependency- and
                            file-disjoint (default: 1 = sequential)

  Multi-session control:
    --resume                Skip sessions already completed in .stoke/sow-state.json
    --continue-on-failure   true | false | auto (default: auto = on for >1 sessions).
                            "On" attempts every session and reports failures at end.
    --session-retries <n>   Per-session retry budget (default: 2)
    --repair-attempts <n>   Per-session self-repair attempts inside the native loop
                            (run acceptance, feed failures back, retry; default: 3)

  Smart loop:
    --sow-critique          When prose SOW converted, run LLM critique+refine pass
                            (default: true)
    --wisdom                Capture per-session learnings and inject into later
                            sessions (default: true)
    --cross-review          After each successful session, run a cross-model code
                            review over the git diff (default: true)
    --review-model <name>   Model name for cross-review (default: same as native)
    --strict-scope          Fail sessions that touched files outside declared
                            scope (default: false, warn-only)
    --repomap-tokens <n>    Max chars of ranked repo map injected into task prompts
                            (default: 3000, 0 = disable)
    --compact-threshold <n> Per-task input token estimate that triggers progressive
                            context compaction inside the agent loop (default:
                            100000, 0 = disable)
    --cost-budget <usd>     Total cost budget across the SOW run, halts when
                            exceeded (default: 0 = unlimited)
    --specexec              Enable speculative parallel execution (4 strategies)
    --roi <level>           ROI filter: high | medium | low | skip (default: medium)

  Safety:
    --timeout <duration>    Wall-clock cap (default: 0 = supervisor-driven)
    --policy <path>         Path to stoke.policy.yaml

PLAN FLAGS:
  --task <goal>        High-level goal description
  --output <path>      Output file (default: stoke-plan.json)
  --dry-run            Show prompt without executing

SCAN FLAGS:
  --security           Include security surface mapping
  --json               Output as JSON

AUDIT FLAGS:
  --personas <ids>     Comma-separated persona IDs (default: auto-select)
  --dry-run            Show prompts without executing
  --json               Output as JSON

SHIP FLAGS:
  --task <goal>        What to build (or --plan <path>)
  --max-rounds <n>     Maximum build-review-fix rounds (default: 5)
  --workers <n>        Max parallel agents (default: 2)
  --dry-run            Show what would happen

REPAIR FLAGS:
  --security           Include security surface mapping
  --workers <n>        Max parallel agents (default: 2)
  --dry-run            Show repair plan without executing

QUICKSTART:
  stoke ship --task "Add JWT auth and rate limiting"
  stoke yolo --repo .
  stoke scope --repo .
  stoke repair --repo . --dry-run
  stoke run --task "Add rate limiting" --dry-run
  stoke plan --task "Add JWT auth"
  stoke build --plan stoke-plan.json --workers 4
  stoke scan --security
  stoke audit --dry-run
  stoke sow --dry-run
  stoke sow --runner native --native-base-url http://localhost:8000
  stoke pool --claude-config-dir ~/.claude

`, version)
}

// serveCmd starts the Stoke HTTP API server with optional mission orchestration.
func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8420, "HTTP server port")
	token := fs.String("token", os.Getenv("STOKE_API_TOKEN"), "Bearer token for auth (or STOKE_API_TOKEN)")
	repo := fs.String("repo", ".", "Repository root")
	dataDir := fs.String("data-dir", ".stoke", "Data directory for mission/research stores")
	fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fatal("resolve repo: %v", err)
	}

	bus := server.NewEventBus()
	srv := server.New(*port, *token, bus)

	// Dashboard state: created early so both orchestrator and API can use it.
	dashState := server.NewDashboardState()

	// Try to create orchestrator for mission API
	orch, orchErr := createOrchestrator(absRepo, *dataDir)
	if orchErr != nil {
		fmt.Fprintf(os.Stderr, "warn: mission API disabled: %v\n", orchErr)
	} else {
		server.RegisterMissionAPI(srv, orch)
		defer orch.Close()
		fmt.Fprintf(os.Stderr, "mission API enabled\n")

		// Bridge hub events to the server's EventBus for SSE/WebSocket clients
		// and to the dashboard state for REST API queries.
		if orch.EventBus() != nil {
			server.BridgeHubToEventBus(orch.EventBus(), bus)
			server.BridgeHubToDashboard(orch.EventBus(), dashState)
		}
	}

	// Register dashboard API (works even without orchestrator).
	server.RegisterDashboardAPI(srv, nil, nil, dashState)
	server.RegisterDashboardUI(srv)

	fmt.Fprintf(os.Stderr, "stoke serve listening on :%d\n", *port)
	fmt.Fprintf(os.Stderr, "dashboard: http://localhost:%d/\n", *port)

	sigCtx, sigCancel := signalContext(context.Background())
	defer sigCancel()

	// Run server in goroutine, shut down on signal
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-sigCtx.Done():
		fmt.Fprintf(os.Stderr, "stoke serve: shutting down\n")
	case err := <-errCh:
		if err != nil {
			fatal("serve: %v", err)
		}
	}
}

// provisionEnv creates and provisions an execution environment from BuildConfig.
func provisionEnv(ctx context.Context, cfg BuildConfig, repoRoot string) (env.Environment, *env.Handle, error) {
	spec := env.Spec{
		Backend:   env.Backend(cfg.EnvBackend),
		BaseImage: cfg.EnvImage,
		Size:      cfg.EnvSize,
		RepoRoot:  repoRoot,
		Env:       map[string]string{},
	}

	var backend env.Environment
	switch env.Backend(cfg.EnvBackend) {
	case env.BackendDocker:
		backend = docker.New()
	case env.BackendFly:
		for _, v := range []string{"FLARE_API_URL", "FLARE_API_KEY", "FLARE_APP_NAME", "FLARE_REGION", "FLARE_SSH_KEY"} {
			if os.Getenv(v) == "" {
				return nil, nil, fmt.Errorf("fly backend requires %s env var", v)
			}
		}
		backend = fly.New(fly.Config{
			APIURL:     os.Getenv("FLARE_API_URL"),
			Token:      os.Getenv("FLARE_API_KEY"),
			AppName:    os.Getenv("FLARE_APP_NAME"),
			Region:     os.Getenv("FLARE_REGION"),
			SSHKeyPath: os.Getenv("FLARE_SSH_KEY"),
		})
	case env.BackendEmber:
		for _, v := range []string{"EMBER_API_URL", "EMBER_API_KEY", "EMBER_SSH_KEY"} {
			if os.Getenv(v) == "" {
				return nil, nil, fmt.Errorf("ember backend requires %s env var", v)
			}
		}
		backend = ember.New(ember.Config{
			APIURL:     os.Getenv("EMBER_API_URL"),
			Token:      os.Getenv("EMBER_API_KEY"),
			SSHKeyPath: os.Getenv("EMBER_SSH_KEY"),
		})
	case env.BackendSSH:
		if os.Getenv("STOKE_SSH_HOST") == "" {
			return nil, nil, fmt.Errorf("ssh backend requires STOKE_SSH_HOST env var")
		}
		backend = envssh.New(envssh.Config{
			Host:    os.Getenv("STOKE_SSH_HOST"),
			User:    os.Getenv("STOKE_SSH_USER"),
			KeyPath: os.Getenv("STOKE_SSH_KEY"),
		})
	default:
		return nil, nil, fmt.Errorf("unknown env backend: %s", cfg.EnvBackend)
	}

	handle, err := backend.Provision(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	return backend, handle, nil
}

// createOrchestrator builds an orchestrate.Orchestrator for the serve command.
func createOrchestrator(repoRoot, dataDir string) (*orchestrate.Orchestrator, error) {
	absData := filepath.Join(repoRoot, dataDir)
	if err := os.MkdirAll(absData, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return orchestrate.New(orchestrate.Config{
		RepoRoot: repoRoot,
		StoreDir: absData,
		EventBus: newEventBus(),
	})
}
