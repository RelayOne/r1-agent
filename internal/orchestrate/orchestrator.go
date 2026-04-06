// Package orchestrate wires the mission store, convergence validator,
// research store, and handoff chain into a unified mission execution pipeline.
//
// The orchestrator provides the integration layer between the mission
// lifecycle and Stoke's existing workflow engine. It:
//
//   - Creates missions from user intent with acceptance criteria
//   - Manages the convergence loop (Validating ↔ Executing)
//   - Bridges convergence findings to mission gaps
//   - Coordinates research storage and retrieval
//   - Records handoffs between agent invocations
//   - Builds enriched context for each execution phase
//
// Usage:
//
//	orch, err := orchestrate.New(orchestrate.Config{
//	    StoreDir: "/path/to/data",
//	})
//	defer orch.Close()
//
//	m, err := orch.CreateMission("Add JWT auth", "Full JWT with rate limiting", criteria)
//	ctx, err := orch.BuildAgentContext(m.ID, mission.DefaultContextConfig())
//	report, err := orch.RunConvergence(m.ID, files)
package orchestrate

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/baseline"
	projconfig "github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/handoff"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/mission"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/research"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/verify"
	"github.com/ericmacdougall/stoke/internal/wisdom"
	"github.com/ericmacdougall/stoke/internal/workflow"
	"github.com/ericmacdougall/stoke/internal/worktree"
)

// defaultConsensusModels is used when Config.ConsensusModels is empty.
var defaultConsensusModels = []string{"claude", "codex"}

// Config configures the mission orchestrator.
type Config struct {
	// StoreDir is the directory for all persistent data (missions, research, etc.).
	StoreDir string `json:"store_dir"`

	// RepoRoot is the git repository root for file scanning and validation.
	// If empty, file-based research and validation are skipped.
	RepoRoot string `json:"repo_root"`

	// ConsensusModels lists the model names used for completion consensus.
	// Default: ["claude", "codex"].
	ConsensusModels []string `json:"consensus_models"`

	// RequiredConsensus is the number of models needed for completion consensus.
	// Default: 2.
	RequiredConsensus int `json:"required_consensus"`

	// MaxConvergenceLoops limits convergence loop iterations.
	// Default: 5.
	MaxConvergenceLoops int `json:"max_convergence_loops"`

	// ExecuteFn is the optional callback for the execute handler.
	// It bridges mission execution to the workflow engine.
	// Receives the mission, the full mission-aware prompt, and the raw task description.
	// If nil, the execute handler records tasks but does not run them.
	ExecuteFn func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) (filesChanged []string, err error) `json:"-"`

	// ValidateFn is the optional callback for adversarial LLM validation (Layer 3).
	// Receives the mission and the adversarial validation prompt.
	// Returns structured findings from the model. If nil, Layer 3 is skipped.
	ValidateFn func(ctx context.Context, m *mission.Mission, prompt string) (findings string, err error) `json:"-"`

	// ConsensusModelFn is the optional callback for gathering adversarial model verdicts.
	// Receives the mission ID, model name, and the full adversarial consensus prompt.
	// If nil, consensus is auto-approved.
	ConsensusModelFn func(ctx context.Context, missionID, model, prompt string) (verdict, reasoning string, gapsFound []string, err error) `json:"-"`

	// DiscoveryFn is the optional callback for agentic discovery in the research phase.
	// Unlike static search, this drives a multi-turn model loop that traces code paths,
	// maps consumer/producer relationships, and verifies cross-surface reachability.
	// If nil, the research handler falls back to deterministic multi-signal search.
	DiscoveryFn func(ctx context.Context, m *mission.Mission, prompt string) (findings string, err error) `json:"-"`

	// ValidateDiscoveryFn is the optional callback for agentic validation (Layer 4).
	// Drives a multi-turn model loop that traces code flow, checks consumer contracts,
	// verifies permissions/security/scalability, and reasons about intent satisfaction
	// across all surfaces (mobile, web, desktop, API, MCP, CLI).
	// If nil, only Layers 1-3 run during validation.
	ValidateDiscoveryFn func(ctx context.Context, m *mission.Mission, prompt string) (findings string, err error) `json:"-"`

	// DecomposeFn asks a model to break a large scope into minimum-viable work items.
	// Returns JSON: {"action":"execute"} if scope is small enough, or
	// {"action":"decompose","items":[...]} with a DAG of sub-tasks.
	// If nil, the execute handler uses monolithic execution instead of DAG-based.
	DecomposeFn func(ctx context.Context, m *mission.Mission, prompt string) (string, error) `json:"-"`

	// WorkNodeFn executes a single minimum-scope work node.
	// Receives the node prompt and the node scope.
	// Returns files changed and any error.
	WorkNodeFn func(ctx context.Context, m *mission.Mission, prompt string, scope string) (filesChanged []string, err error) `json:"-"`

	// MaxDAGWorkers controls parallelism in the DAG execute handler.
	// Default: 3.
	MaxDAGWorkers int `json:"max_dag_workers"`

	// MaxDAGDepth controls maximum recursion depth for work decomposition.
	// Default: 4.
	MaxDAGDepth int `json:"max_dag_depth"`

	// ValidateStepFn adversarially validates a single step's output.
	// Used by micro-convergence loops at every level: work nodes,
	// decompositions, research findings, and plan steps.
	// If nil, steps execute once without convergence validation.
	ValidateStepFn func(ctx context.Context, m *mission.Mission, prompt string) (response string, err error) `json:"-"`

	// MaxMicroIterations caps the execute→validate→fix cycle for each step.
	// Default: 3.
	MaxMicroIterations int `json:"max_micro_iterations"`

	// ModelAskFn sends a prompt to a specific model by name.
	// Used for multi-model convergence where all models answer independently
	// and an arbiter combines/reviews until convergence.
	// If nil, falls back to single-model convergence via ValidateStepFn.
	ModelAskFn mission.ModelAskFn `json:"-"`

	// ConvergenceModels lists models available for multi-model convergence.
	// Each answers independently; arbiter combines and judges completeness.
	ConvergenceModels []string `json:"convergence_models"`

	// ArbiterModel decides when answers are complete. Should be strongest model.
	// Defaults to first model in ConvergenceModels.
	ArbiterModel string `json:"arbiter_model"`

	// MaxConvergenceDepth is the safety circuit breaker for recursive convergence.
	// NOT the convergence condition — the arbiter decides that. Default: 20.
	MaxConvergenceDepth int `json:"max_convergence_depth"`

	// --- Workflow engine infrastructure (optional) ---
	// When set, WorkflowExecuteFn() returns an ExecuteFn that routes through
	// the full workflow.Engine (plan → execute → verify → merge) instead of
	// a raw model session.

	// PolicyPath is the path to stoke.policy.yaml.
	PolicyPath string `json:"policy_path"`

	// Runners provides Claude and Codex execution engines.
	Runners *engine.Registry `json:"-"`

	// Pools manages subscription pool allocation.
	Pools *subscriptions.Manager `json:"-"`

	// Worktrees provides git worktree management.
	Worktrees *worktree.Manager `json:"-"`

	// Wisdom provides cross-task learning injection.
	Wisdom *wisdom.Store `json:"-"`

	// CostTracker tracks per-session costs.
	CostTracker *costtrack.Tracker `json:"-"`

	// ClaudeConfigDir is the config dir for Claude instances.
	ClaudeConfigDir string `json:"claude_config_dir"`

	// CodexHome is the home dir for Codex instances.
	CodexHome string `json:"codex_home"`

	// OnEvent is the streaming event callback.
	OnEvent engine.OnEventFunc `json:"-"`

	// EventBus is the unified event bus (nil = no events).
	EventBus *hub.Bus `json:"-"`
}

// Orchestrator is the unified integration layer for mission-driven execution.
// It owns the lifecycle of all stores and provides the API surface for
// the workflow engine and CLI to interact with missions.
type Orchestrator struct {
	mu sync.RWMutex

	store     *mission.Store
	research  *research.Store
	validator *convergence.Validator
	chain     *handoff.Chain

	// baselines maps mission ID → pre-work baseline snapshot.
	// Captured at mission creation so we can classify failures later.
	baselines map[string]*baseline.Snapshot

	// verifyCmds are the auto-detected or configured build/test/lint commands.
	verifyCmds *baseline.Commands

	// projectInfo describes the detected project type, framework, and capabilities.
	projectInfo projconfig.ProjectInfo

	config Config
}

// New creates a fully-wired mission orchestrator.
// Opens or creates all backing stores in the configured directory.
func New(config Config) (*Orchestrator, error) {
	if config.StoreDir == "" {
		return nil, fmt.Errorf("orchestrator: store directory must not be empty")
	}
	if config.RequiredConsensus <= 0 {
		config.RequiredConsensus = 2
	}
	if config.MaxConvergenceLoops <= 0 {
		config.MaxConvergenceLoops = 5
	}
	if len(config.ConsensusModels) == 0 {
		config.ConsensusModels = defaultConsensusModels
	}

	missionDir := filepath.Join(config.StoreDir, "missions")
	researchDir := filepath.Join(config.StoreDir, "research")

	for _, dir := range []string{missionDir, researchDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("create directory %q: %w", dir, err)
		}
	}

	mStore, err := mission.NewStore(missionDir)
	if err != nil {
		return nil, fmt.Errorf("open mission store: %w", err)
	}

	rStore, err := research.NewStore(researchDir)
	if err != nil {
		mStore.Close()
		return nil, fmt.Errorf("open research store: %w", err)
	}

	chain, err := handoff.NewChain(mStore)
	if err != nil {
		rStore.Close()
		mStore.Close()
		return nil, fmt.Errorf("create handoff chain: %w", err)
	}
	validator := convergence.NewValidatorForProject(config.RepoRoot)

	// Detect project type and capabilities
	var projInfo projconfig.ProjectInfo
	if config.RepoRoot != "" {
		projInfo = projconfig.DetectProject(config.RepoRoot)
		if projInfo.Type != "" {
			log.Printf("[orchestrator] detected project: type=%s frontend=%v framework=%q tests=%q storybook=%v",
				projInfo.Type, projInfo.HasFrontend, projInfo.UIFramework, projInfo.TestFramework, projInfo.HasStorybook)
		}
	}

	// Auto-detect build/test/lint commands from the repo
	var verifyCmds *baseline.Commands
	if config.RepoRoot != "" {
		cmds := baseline.AutoDetect(config.RepoRoot)
		if cmds.Build != "" || cmds.Test != "" || cmds.Lint != "" {
			verifyCmds = &cmds
			log.Printf("[orchestrator] detected verification commands: build=%q test=%q lint=%q",
				cmds.Build, cmds.Test, cmds.Lint)
		}
	}

	log.Printf("[orchestrator] initialized at %s", config.StoreDir)
	orch := &Orchestrator{
		store:      mStore,
		research:   rStore,
		validator:  validator,
		chain:      chain,
		baselines:   make(map[string]*baseline.Snapshot),
		verifyCmds:  verifyCmds,
		projectInfo: projInfo,
		config:      config,
	}

	// Auto-wire workflow-backed execution if infrastructure is configured
	// but no explicit ExecuteFn was provided.
	if config.ExecuteFn == nil {
		if wfFn := orch.WorkflowExecuteFn(); wfFn != nil {
			orch.config.ExecuteFn = wfFn
			log.Printf("[orchestrator] workflow-backed execution enabled")
		}
	}

	return orch, nil
}

// Close shuts down all backing stores. Must be called on shutdown.
func (o *Orchestrator) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	var errs []string
	if err := o.store.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("mission store: %v", err))
	}
	if err := o.research.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("research store: %v", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("orchestrator close errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Store returns the underlying mission store.
func (o *Orchestrator) Store() *mission.Store {
	return o.store
}

// ResearchStore returns the underlying research store.
func (o *Orchestrator) ResearchStore() *research.Store {
	return o.research
}

// HandoffChain returns the underlying handoff chain.
func (o *Orchestrator) HandoffChain() *handoff.Chain {
	return o.chain
}

// Validator returns the underlying convergence validator.
func (o *Orchestrator) Validator() *convergence.Validator {
	return o.validator
}

// WorkflowExecuteFn returns an ExecuteFn that routes task execution through
// the full workflow.Engine (plan → execute → verify → cross-model review → merge).
// This gives missions the full 11-layer policy enforcement instead of raw model sessions.
//
// Requires Config.Runners, Config.Worktrees, and Config.PolicyPath to be set.
// Falls back gracefully: returns nil if workflow infrastructure is not configured.
func (o *Orchestrator) WorkflowExecuteFn() func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
	if o.config.Runners == nil || o.config.PolicyPath == "" {
		return nil
	}

	return func(ctx context.Context, m *mission.Mission, prompt, taskDesc string) ([]string, error) {
		policy, err := projconfig.LoadPolicy(o.config.PolicyPath)
		if err != nil {
			return nil, fmt.Errorf("workflow bridge: load policy: %w", err)
		}

		// Auto-detect verification commands
		var buildCmd, testCmd, lintCmd string
		if o.config.RepoRoot != "" {
			detected := projconfig.DetectCommands(o.config.RepoRoot)
			buildCmd = detected.Build
			testCmd = detected.Test
			lintCmd = detected.Lint
		}
		if o.verifyCmds != nil {
			if o.verifyCmds.Build != "" {
				buildCmd = o.verifyCmds.Build
			}
			if o.verifyCmds.Test != "" {
				testCmd = o.verifyCmds.Test
			}
			if o.verifyCmds.Lint != "" {
				lintCmd = o.verifyCmds.Lint
			}
		}

		// Use provided pools or create default
		pools := o.config.Pools
		if pools == nil {
			pools = subscriptions.NewManager([]subscriptions.Pool{
				{ID: "claude-1", Provider: subscriptions.ProviderClaude, ConfigDir: o.config.ClaudeConfigDir},
				{ID: "codex-1", Provider: subscriptions.ProviderCodex, ConfigDir: o.config.CodexHome},
			})
		}

		// Use provided worktree manager or create per-task
		var wm workflow.WorktreeManager
		if o.config.Worktrees != nil {
			wm = o.config.Worktrees
		} else if o.config.RepoRoot != "" {
			wm = worktree.NewManager(o.config.RepoRoot)
		} else {
			return nil, fmt.Errorf("workflow bridge: no worktree manager and no repo root configured")
		}

		taskType := model.InferTaskType(taskDesc)

		wf := workflow.Engine{
			RepoRoot:        o.config.RepoRoot,
			Task:            prompt,
			TaskType:        taskType,
			WorktreeName:    fmt.Sprintf("mission-%s-%s", m.ID, slugify(taskDesc)),
			AuthMode:        engine.AuthModeSubscription,
			Policy:          policy,
			Pools:           pools,
			Worktrees:       wm,
			Runners:         *o.config.Runners,
			Verifier:        verify.NewPipeline(buildCmd, testCmd, lintCmd),
			ClaudeConfigDir: o.config.ClaudeConfigDir,
			CodexHome:       o.config.CodexHome,
			OnEvent:         o.config.OnEvent,
			State:           taskstate.NewTaskState(m.ID + "/" + slugify(taskDesc)),
			Wisdom:          o.config.Wisdom,
			CostTracker:     o.config.CostTracker,
			EventBus:        o.config.EventBus,
		}

		result, err := wf.Run(ctx)
		if err != nil {
			return nil, fmt.Errorf("workflow execution: %w", err)
		}

		return result.FilesChanged, nil
	}
}

// slugify produces a short, filesystem-safe slug from a task description.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
	// Collapse runs of dashes and trim
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// --- Mission Lifecycle ---

// CreateMission creates a new mission from user intent. Generates a unique ID,
// sets up acceptance criteria, and persists to the store.
func (o *Orchestrator) CreateMission(title, intent string, criteria []string) (*mission.Mission, error) {
	if title == "" || intent == "" {
		return nil, fmt.Errorf("mission title and intent must not be empty")
	}

	id := fmt.Sprintf("m-%d", time.Now().UnixNano())

	var mCriteria []mission.Criterion
	for i, desc := range criteria {
		mCriteria = append(mCriteria, mission.Criterion{
			ID:          fmt.Sprintf("c-%d", i+1),
			Description: desc,
		})
	}

	m := &mission.Mission{
		ID:       id,
		Title:    title,
		Intent:   intent,
		Criteria: mCriteria,
	}

	if err := o.store.Create(m); err != nil {
		return nil, fmt.Errorf("create mission: %w", err)
	}

	// Capture baseline: snapshot the current build/test/lint state BEFORE any work.
	// If tests are already red, that becomes a gap the mission must fix.
	if o.verifyCmds != nil && o.config.RepoRoot != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		snap, err := baseline.Capture(ctx, o.config.RepoRoot, *o.verifyCmds)
		if err != nil {
			log.Printf("[orchestrator] baseline capture failed for %s: %v (continuing without baseline)", id, err)
		} else {
			o.mu.Lock()
			o.baselines[id] = snap
			o.mu.Unlock()
			if !snap.AllPass {
				log.Printf("[orchestrator] WARNING: baseline has pre-existing failures for %s: %s", id, snap.FailureSummary())
			} else {
				log.Printf("[orchestrator] baseline captured for %s: all %d checks pass", id, len(snap.Results))
			}
			// Persist baseline to disk for crash recovery
			baselinePath := filepath.Join(o.config.StoreDir, "baselines", id+".json")
			if saveErr := snap.Save(baselinePath); saveErr != nil {
				log.Printf("[orchestrator] failed to save baseline for %s: %v", id, saveErr)
			}
		}
	}

	log.Printf("[orchestrator] created mission %s: %s (%d criteria)", id, title, len(criteria))
	return m, nil
}

// AdvanceMission transitions a mission to the next phase with reason tracking.
func (o *Orchestrator) AdvanceMission(missionID string, to mission.Phase, reason, agent string) error {
	return o.store.Advance(missionID, to, reason, agent)
}

// GetMission retrieves a mission by ID.
func (o *Orchestrator) GetMission(missionID string) (*mission.Mission, error) {
	return o.store.Get(missionID)
}

// ListMissions returns missions filtered by phase (empty for all).
func (o *Orchestrator) ListMissions(phase mission.Phase) ([]*mission.Mission, error) {
	return o.store.List(phase)
}

// --- Convergence ---

// RunConvergence validates the given files against the convergence rules and
// the mission's acceptance criteria. It:
//  1. Runs the adversarial rule engine
//  2. Maps findings to mission gaps (creates or updates)
//  3. Returns the convergence report
func (o *Orchestrator) RunConvergence(missionID string, files []convergence.FileInput) (*convergence.Report, error) {
	m, err := o.store.Get(missionID)
	if err != nil {
		return nil, fmt.Errorf("get mission: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("mission %q not found", missionID)
	}

	var criteriaDescs []string
	for _, c := range m.Criteria {
		if !c.Satisfied {
			criteriaDescs = append(criteriaDescs, c.Description)
		}
	}

	var report *convergence.Report
	if len(criteriaDescs) > 0 {
		report = o.validator.ValidateWithCriteria(missionID, files, criteriaDescs)
	} else {
		report = o.validator.Validate(missionID, files)
	}

	for i, f := range report.Findings {
		gapID := fmt.Sprintf("cv-%s-%d", missionID, i)
		err := o.store.AddGap(&mission.Gap{
			ID:          gapID,
			MissionID:   missionID,
			Category:    string(f.Category),
			Severity:    string(f.Severity),
			Description: f.Description,
			File:        f.File,
			Line:        f.Line,
			Suggestion:  f.Suggestion,
		})
		if err != nil {
			log.Printf("[orchestrator] failed to add gap %s: %v", gapID, err)
		}
	}

	log.Printf("[orchestrator] convergence for %s: score=%.2f converged=%v findings=%d",
		missionID, report.Score, report.IsConverged, len(report.Findings))
	return report, nil
}

// CheckConvergence returns the current convergence status without running validation.
func (o *Orchestrator) CheckConvergence(missionID string) (*mission.ConvergenceStatus, error) {
	return o.store.GetConvergenceStatus(missionID, o.config.RequiredConsensus)
}

// --- Research ---

// AddResearch stores a research finding linked to a mission.
func (o *Orchestrator) AddResearch(missionID string, entry *research.Entry) error {
	entry.MissionID = missionID
	return o.research.Add(entry)
}

// SearchResearch finds research entries matching a query.
func (o *Orchestrator) SearchResearch(query string, limit int) ([]research.SearchResult, error) {
	return o.research.Search(query, limit)
}

// --- Handoffs ---

// RecordHandoff records an agent-to-agent context transfer.
func (o *Orchestrator) RecordHandoff(record handoff.Record) error {
	return o.chain.Handoff(record)
}

// GetHandoffContext builds context from handoff history, sized to fit maxTokens.
func (o *Orchestrator) GetHandoffContext(missionID string, maxTokens int) (string, error) {
	return o.chain.BuildContext(missionID, maxTokens)
}

// --- Context Building ---

// BuildAgentContext generates the full enriched context for an agent
// about to work on a mission. Includes mission state, criteria, gaps,
// research findings, and handoff history.
func (o *Orchestrator) BuildAgentContext(missionID string, config mission.ContextConfig) (string, error) {
	adapter := &contextAdapter{orch: o}
	cb, err := mission.NewContextBuilder(o.store, adapter)
	if err != nil {
		return "", err
	}
	return cb.BuildContext(missionID, config)
}

// --- Runner ---

// NewRunner creates a fully-wired mission runner with all phase handlers
// registered. The handlers are configured using the orchestrator's stores,
// validator, and config callbacks.
func (o *Orchestrator) NewRunner(config mission.RunnerConfig) (*mission.Runner, error) {
	return o.NewRunnerForMission(config, "")
}

// NewRunnerForMission creates a fully-wired runner with the baseline
// for a specific mission loaded. If missionID is empty, no baseline is used.
func (o *Orchestrator) NewRunnerForMission(config mission.RunnerConfig, missionID string) (*mission.Runner, error) {
	runner, err := mission.NewRunner(o.store, config)
	if err != nil {
		return nil, err
	}

	// Build context adapter for research/handoff enrichment in prompts
	var ctxSource mission.ContextSource
	ctxSource = &contextAdapter{orch: o}

	deps := mission.HandlerDeps{
		Store:            o.store,
		ContextSource:    ctxSource,
		Validator:        o.validator,
		RepoRoot:         o.config.RepoRoot,
		ProjectInfo:      o.projectInfo,
		Metrics:          mission.NewMetrics(),
		VerifyCommands:   o.verifyCmds,
		ExecuteFn:           o.config.ExecuteFn,
		ValidateFn:          o.config.ValidateFn,
		ConsensusModelFn:    o.config.ConsensusModelFn,
		DiscoveryFn:         o.config.DiscoveryFn,
		ValidateDiscoveryFn: o.config.ValidateDiscoveryFn,
		DecomposeFn:         o.config.DecomposeFn,
		WorkNodeFn:          o.config.WorkNodeFn,
		MaxDAGWorkers:       o.config.MaxDAGWorkers,
		MaxDAGDepth:         o.config.MaxDAGDepth,
		ValidateStepFn:      o.config.ValidateStepFn,
		MaxMicroIterations:  o.config.MaxMicroIterations,
		ModelAskFn:          o.config.ModelAskFn,
		ConvergenceModels:   o.config.ConvergenceModels,
		ArbiterModel:        o.config.ArbiterModel,
		MaxConvergenceDepth: o.config.MaxConvergenceDepth,
		RecordResearchFn: func(missionID, topic, content string) error {
			return o.research.Add(&research.Entry{
				ID:        fmt.Sprintf("disc-%s-%d", missionID, time.Now().UnixNano()),
				MissionID: missionID,
				Topic:     topic,
				Content:   content,
				Source:    "agentic-discovery",
				Tags:      []string{"discovery", "auto"},
			})
		},
	}

	// Load baseline for this mission if available
	if missionID != "" {
		o.mu.RLock()
		snap := o.baselines[missionID]
		o.mu.RUnlock()

		if snap == nil {
			// Try loading from disk (crash recovery)
			baselinePath := filepath.Join(o.config.StoreDir, "baselines", missionID+".json")
			if loaded, err := baseline.Load(baselinePath); err == nil {
				snap = loaded
				o.mu.Lock()
				o.baselines[missionID] = snap
				o.mu.Unlock()
			}
		}
		deps.Baseline = snap
	}

	for _, reg := range []struct {
		phase   mission.Phase
		handler mission.PhaseHandler
	}{
		{mission.PhaseCreated, mission.NewResearchHandler(deps)},
		{mission.PhaseResearching, mission.NewPlanHandler(deps)},
		{mission.PhasePlanning, mission.NewExecuteHandler(deps)},
		{mission.PhaseExecuting, mission.NewDAGExecuteHandler(deps)},
		{mission.PhaseValidating, mission.NewValidateHandler(deps)},
		{mission.PhaseConverged, mission.NewConsensusHandler(deps, o.config.ConsensusModels)},
	} {
		if err := runner.RegisterHandler(reg.phase, reg.handler); err != nil {
			return nil, err
		}
	}

	return runner, nil
}

// GetBaseline returns the pre-mission baseline snapshot for a mission.
// Returns nil if no baseline was captured.
func (o *Orchestrator) GetBaseline(missionID string) *baseline.Snapshot {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.baselines[missionID]
}

// RunMission creates a runner with default config and drives a mission to completion.
// The runner includes the mission's baseline snapshot so the validate handler
// can classify failures as pre-existing vs. introduced. Both are blocking.
func (o *Orchestrator) RunMission(ctx context.Context, missionID string) (*mission.RunSummary, error) {
	runner, err := o.NewRunnerForMission(mission.DefaultRunnerConfig(), missionID)
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}
	return runner.Run(ctx, missionID)
}

// --- Consensus ---

// RecordConsensus saves a model's completion judgment.
func (o *Orchestrator) RecordConsensus(missionID, model, verdict, reasoning string, gapsFound []string) error {
	return o.store.RecordConsensus(&mission.ConsensusRecord{
		MissionID: missionID,
		Model:     model,
		Verdict:   verdict,
		Reasoning: reasoning,
		GapsFound: gapsFound,
	})
}

// HasConsensus checks whether enough models agree the mission is complete.
func (o *Orchestrator) HasConsensus(missionID string) (bool, error) {
	return o.store.HasConsensus(missionID, o.config.RequiredConsensus)
}

// --- Gaps ---

// OpenGaps returns unresolved gaps for a mission, ordered by severity.
func (o *Orchestrator) OpenGaps(missionID string) ([]mission.Gap, error) {
	return o.store.OpenGaps(missionID)
}

// AllGaps returns all gaps (open and resolved) for a mission.
func (o *Orchestrator) AllGaps(missionID string) ([]mission.Gap, error) {
	return o.store.AllGaps(missionID)
}

// --- Internal Adapters ---

// contextAdapter bridges the Orchestrator to mission.ContextSource so the
// ContextBuilder can pull research and handoff data.
type contextAdapter struct {
	orch *Orchestrator
}

func (a *contextAdapter) SearchResearch(query string, limit int) ([]mission.ResearchEntry, error) {
	results, err := a.orch.research.Search(query, limit)
	if err != nil {
		return nil, err
	}
	var entries []mission.ResearchEntry
	for _, r := range results {
		entries = append(entries, mission.ResearchEntry{
			Topic:   r.Entry.Topic,
			Query:   r.Entry.Query,
			Content: r.Entry.Content,
			Source:  r.Entry.Source,
		})
	}
	return entries, nil
}

func (a *contextAdapter) GetResearchByMission(missionID string) ([]mission.ResearchEntry, error) {
	results, err := a.orch.research.ByMission(missionID)
	if err != nil {
		return nil, err
	}
	var entries []mission.ResearchEntry
	for _, r := range results {
		entries = append(entries, mission.ResearchEntry{
			Topic:   r.Topic,
			Query:   r.Query,
			Content: r.Content,
			Source:  r.Source,
		})
	}
	return entries, nil
}

func (a *contextAdapter) GetHandoffContext(missionID string, maxTokens int) (string, error) {
	return a.orch.chain.BuildContext(missionID, maxTokens)
}
