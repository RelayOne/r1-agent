// Phase handlers bridge the mission lifecycle to concrete execution.
//
// Each handler implements PhaseHandler and delegates to appropriate
// subsystems. Handlers are designed as composable building blocks:
//
//   - ResearchHandler: Gathers information needed for planning
//   - PlanHandler: Creates a structured implementation plan
//   - ExecuteHandler: Runs the implementation via the workflow engine
//   - ValidateHandler: Runs adversarial convergence validation (3 layers)
//   - ConsensusHandler: Gathers multi-model adversarial completion votes
//
// Every handler builds a MissionContext from the store, uses it to generate
// a mission-aware prompt via the prompts package, and passes that prompt to
// the appropriate callback function. This ensures agents always receive:
//   - Full mission state (criteria, gaps, convergence status)
//   - Research findings and handoff history
//   - The adversarial framing that prevents rationalization
//
// Handlers are stateless — all state flows through the mission store.
// They receive the current Mission and return a PhaseResult.
package mission

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/RelayOne/r1-agent/internal/baseline"
	"github.com/RelayOne/r1-agent/internal/config"
	"github.com/RelayOne/r1-agent/internal/convergence"
	"github.com/RelayOne/r1-agent/internal/depgraph"
	"github.com/RelayOne/r1-agent/internal/hub"
	"github.com/RelayOne/r1-agent/internal/prompts"
	"github.com/RelayOne/r1-agent/internal/skill"
	"github.com/RelayOne/r1-agent/internal/symindex"
	"github.com/RelayOne/r1-agent/internal/tfidf"
)

// HandlerDeps bundles the dependencies that phase handlers need.
// This avoids circular imports by letting the orchestration layer
// inject concrete implementations.
type HandlerDeps struct {
	// Store is the mission persistence layer.
	Store *Store

	// ContextSource provides research and handoff data for prompt building.
	// If nil, prompts are built without research/handoff enrichment.
	ContextSource ContextSource

	// Validator is the convergence rule engine.
	Validator *convergence.Validator

	// RepoRoot is the git repository root for file scanning.
	RepoRoot string

	// ProjectInfo describes the detected project type, framework, and capabilities.
	// Used to conditionally enable UX rules and tailor prompts for frontend projects.
	ProjectInfo config.ProjectInfo

	// Metrics tracks operational statistics.
	Metrics *Metrics

	// VerifyCommands holds the build/test/lint commands to run during validation.
	// If nil, the validate handler runs static analysis only.
	// When set, the handler runs actual verification commands and treats
	// any failure — pre-existing or introduced — as a blocking gap.
	VerifyCommands *baseline.Commands

	// Baseline is the pre-mission snapshot of build/test/lint state.
	// If set, the validate handler compares against it to classify
	// failures as pre-existing vs. introduced. Both are blocking.
	Baseline *baseline.Snapshot

	// ExecuteFn is called by the execute handler to run a task through
	// the workflow engine. It receives the mission, the full mission-aware
	// prompt (built from BuildMissionExecutePrompt), and the raw task description.
	// Returns the files changed and any error.
	ExecuteFn func(ctx context.Context, m *Mission, prompt string, taskDesc string) (filesChanged []string, err error)

	// ValidateFn is called by the validate handler for adversarial LLM validation
	// (Layer 3). It receives the mission and the full adversarial validation prompt
	// (built from BuildMissionValidatePrompt). Returns structured JSON findings.
	// If nil, Layer 3 is skipped (only live verification and static analysis run).
	ValidateFn func(ctx context.Context, m *Mission, prompt string) (findings string, err error)

	// ConsensusModelFn is called to get a model's adversarial verdict on mission
	// completion. It receives the mission ID, model name, and the full adversarial
	// consensus prompt (built from BuildMissionConsensusPrompt with the validation
	// report embedded). Returns the verdict and reasoning.
	ConsensusModelFn func(ctx context.Context, missionID, model, prompt string) (verdict, reasoning string, gapsFound []string, err error)

	// SymbolIndex is an optional pre-built symbol index for the repo.
	// If nil, the research handler builds one on-demand.
	SymbolIndex *symindex.Index

	// DepGraph is an optional pre-built dependency graph.
	// If nil, the research handler builds one on-demand.
	DepGraph *depgraph.Graph

	// TFIDFIndex is an optional pre-built TF-IDF search index.
	// If nil, the research handler builds one on-demand.
	TFIDFIndex *tfidf.Index

	// DiscoveryFn runs an agentic discovery loop: the model iteratively
	// queries the codebase to map what exists, what's missing, what
	// consumes/produces, and whether users can reach functionality.
	//
	// Unlike static analysis, discovery is adversarial and multi-turn:
	// the model reads code, asks questions, searches for answers, and
	// loops until it has mapped the full intent surface.
	//
	// Returns structured discovery findings: what was found, what's
	// missing, consumer/producer mapping, reachability assessment.
	//
	// If nil, only deterministic search signals are used.
	DiscoveryFn func(ctx context.Context, m *Mission, prompt string) (findings string, err error)

	// ValidateDiscoveryFn runs an agentic validation loop: the model
	// reads the code changes against the intent and criteria, traces
	// call paths, verifies consumer contracts, and checks reachability
	// across all surfaces (mobile, web, desktop, API, MCP).
	//
	// Unlike single-shot validation, this is a multi-turn loop where
	// the model can:
	//   - Read specific files to trace code flow
	//   - Check if new APIs are consumed by all expected surfaces
	//   - Verify permissions, security, scalability patterns
	//   - Confirm users can actually reach the new functionality
	//
	// Returns structured JSON findings with gaps.
	// If nil, falls back to the single-shot ValidateFn.
	ValidateDiscoveryFn func(ctx context.Context, m *Mission, prompt string) (findings string, err error)

	// RecordResearchFn persists a research finding so downstream phases can access it.
	// Called by the research handler to store discovery results.
	// If nil, discovery output is only stored as PhaseResult artifacts (not in research store).
	RecordResearchFn func(missionID, topic, content string) error

	// DecomposeFn asks a model to break a large scope into minimum-viable work items.
	// Returns JSON: {"action":"execute"} if scope is small enough, or
	// {"action":"decompose","items":[...]} with a DAG of sub-tasks.
	// Used by the DAG execute handler for recursive work decomposition.
	DecomposeFn func(ctx context.Context, m *Mission, prompt string) (string, error)

	// WorkNodeFn executes a single minimum-scope work node.
	// Receives the node prompt (built from BuildWorkNodePrompt) and the node scope.
	// Returns files changed and any error, like ExecuteFn but for minimum scope.
	WorkNodeFn func(ctx context.Context, m *Mission, prompt string, scope string) (filesChanged []string, err error)

	// MaxDAGWorkers controls parallelism in the DAG execute handler.
	// Default: 3 (conservative to avoid resource contention).
	MaxDAGWorkers int

	// MaxDAGDepth controls maximum recursion depth for work decomposition.
	// Default: 4 (root → sub-task → sub-sub-task → leaf).
	MaxDAGDepth int

	// ValidateStepFn adversarially validates a single step's output.
	// Receives a validation prompt and returns the model's assessment.
	// Used by micro-convergence loops at every level: work nodes,
	// decompositions, research findings, and plan steps.
	// If nil, steps execute once without convergence validation.
	ValidateStepFn func(ctx context.Context, m *Mission, prompt string) (response string, err error)

	// MaxMicroIterations caps the execute→validate→fix cycle for each step.
	// Default: 3.
	MaxMicroIterations int

	// ModelAskFn sends a prompt to a specific model by name.
	// Used by ConvergedAnswer for multi-model convergence at every step.
	// If nil, falls back to single-model MicroConvergence via ValidateStepFn.
	ModelAskFn ModelAskFn

	// ConvergenceModels lists the models available for multi-model convergence.
	// Each model answers independently; an arbiter combines and judges.
	// If empty or ModelAskFn is nil, falls back to MicroConvergence.
	ConvergenceModels []string

	// ArbiterModel is the model that combines answers, reviews for conflicts,
	// and decides whether the answer is complete. Should be strongest available.
	// Defaults to first model in ConvergenceModels.
	ArbiterModel string

	// MaxConvergenceDepth is the safety circuit breaker for recursive convergence.
	// NOT the convergence condition — the arbiter decides that. Default: 20.
	MaxConvergenceDepth int

	// EventBus is the unified event bus for emitting mission lifecycle events.
	// If nil, no events are emitted.
	EventBus *hub.Bus
}

// buildMissionContext constructs a prompts.MissionContext for prompt generation.
// This is the bridge between the mission store and the prompt templates.
func buildMissionContext(deps HandlerDeps, m *Mission) prompts.MissionContext {
	mc := prompts.MissionContext{
		MissionID:     m.ID,
		Title:         m.Title,
		Intent:        m.Intent,
		Phase:         string(m.Phase),
		HasFrontend:   deps.ProjectInfo.HasFrontend,
		UIFramework:   deps.ProjectInfo.UIFramework,
		TestFramework: deps.ProjectInfo.TestFramework,
		HasStorybook:  deps.ProjectInfo.HasStorybook,
	}

	// Populate files changed from prior execution (stored in metadata by runner)
	if files, ok := m.Metadata["files_changed"]; ok && files != "" {
		mc.PriorContext = fmt.Sprintf("## Files Changed in Last Execution\n%s\n", files)
	}

	// Build criteria block
	if len(m.Criteria) > 0 {
		var cb strings.Builder
		cb.WriteString("## Acceptance Criteria\n")
		satisfied := 0
		for _, c := range m.Criteria {
			if c.Satisfied {
				satisfied++
				fmt.Fprintf(&cb, "- [x] %s\n", c.Description)
			} else {
				fmt.Fprintf(&cb, "- [ ] %s\n", c.Description)
			}
		}
		fmt.Fprintf(&cb, "\nProgress: %d/%d criteria satisfied\n", satisfied, len(m.Criteria))
		mc.CriteriaBlock = cb.String()
	}

	// Build gaps block
	if deps.Store != nil {
		gaps, _ := deps.Store.OpenGaps(m.ID)
		if len(gaps) > 0 {
			var gb strings.Builder
			gb.WriteString("## Open Gaps (must resolve)\n")
			for _, g := range gaps {
				fmt.Fprintf(&gb, "- [%s] %s", g.Severity, g.Description)
				if g.File != "" {
					fmt.Fprintf(&gb, " (%s", g.File)
					if g.Line > 0 {
						fmt.Fprintf(&gb, ":%d", g.Line)
					}
					gb.WriteString(")")
				}
				gb.WriteString("\n")
				if g.Suggestion != "" {
					fmt.Fprintf(&gb, "  Suggestion: %s\n", g.Suggestion)
				}
			}
			mc.GapsBlock = gb.String()
		}

		// Build convergence status
		status, err := deps.Store.GetConvergenceStatus(m.ID, 2)
		if err == nil {
			mc.StatusBlock = fmt.Sprintf("## Convergence Status\n"+
				"Criteria: %d/%d satisfied | Open gaps: %d (blocking: %d) | Consensus: %v\n",
				status.SatisfiedCriteria, status.TotalCriteria,
				status.OpenGapCount, status.BlockingGapCount, status.HasConsensus)
		}
	}

	// Build research block
	if deps.ContextSource != nil {
		entries, err := deps.ContextSource.GetResearchByMission(m.ID)
		if err == nil && len(entries) > 0 {
			var rb strings.Builder
			rb.WriteString("## Research Findings\n")
			for _, e := range entries {
				fmt.Fprintf(&rb, "### %s\n", e.Topic)
				if e.Query != "" {
					fmt.Fprintf(&rb, "Query: %s\n", e.Query)
				}
				fmt.Fprintf(&rb, "%s\n\n", e.Content)
			}
			mc.ResearchBlock = rb.String()
		}

		// Build handoff block
		handoffCtx, err := deps.ContextSource.GetHandoffContext(m.ID, 2000)
		if err == nil && handoffCtx != "" {
			mc.HandoffBlock = handoffCtx
		}
	}

	return mc
}

// NewResearchHandler creates a handler for the Researching phase.
//
// When DiscoveryFn is configured, it drives a multi-turn model loop that
// iteratively queries the codebase via MCP tools (search_symbols,
// get_dependencies, search_content, get_file_symbols, impact_analysis).
// The model reasons about intent, traces code paths, maps consumer/producer
// relationships, and verifies cross-surface reachability — fundamentally
// superior to any static signal combination.
//
// When DiscoveryFn is NOT configured, falls back to deterministic multi-signal
// search (TF-IDF, symbol index, dependency graph expansion).
func NewResearchHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()
		emitMissionEvent(ctx, deps.EventBus, &hub.Event{
			Type: hub.EventMissionResearchStart, TaskID: m.ID, Phase: "research",
		})

		mc := buildMissionContext(deps, m)
		researchPrompt := prompts.BuildMissionResearchPrompt(mc)

		relevantFiles := make(map[string]float64) // file -> relevance score
		var artifacts = map[string]string{"prompt": researchPrompt}

		// Primary path: Agentic discovery loop (model-driven, multi-turn)
		// The model iteratively queries the codebase to build a complete
		// map of what exists, what's missing, consumers, producers, and
		// reachability. The model has access to all deterministic signals
		// (TF-IDF, symbols, dependencies) as MCP tools and can invoke
		// them with reasoning about what to look for next.
		if deps.DiscoveryFn != nil {
			discoveryPrompt := prompts.BuildMissionDiscoveryPrompt(mc)
			researchScope := fmt.Sprintf("Mission: %s\nIntent: %s\nCriteria:\n%s", m.Title, m.Intent, mc.CriteriaBlock)

			// Convergent research via ConvergeStep: multi-model → single-model → single-shot
			discoveryResult, _, err := ConvergeStep(ctx, convergeStepDeps{
				ModelAskFn:    deps.ModelAskFn,
				Models:        deps.ConvergenceModels,
				ArbiterModel:  deps.ArbiterModel,
				MaxDepth:      deps.MaxConvergenceDepth,
				MaxIterations: deps.MaxMicroIterations,
				BiggerMission: researchScope,
				Mission:       fmt.Sprintf("Research the codebase for:\n%s", researchScope),
				StepName:      fmt.Sprintf("research:%s", m.ID),
				ExecuteFn: func(rCtx context.Context, feedback string) (string, error) {
					prompt := discoveryPrompt
					if feedback != "" {
						prompt += "\n\n" + feedback
					}
					return deps.DiscoveryFn(rCtx, m, prompt)
				},
				ValidateFn: func(rCtx context.Context, scope, output string) ([]string, error) {
					if deps.ValidateStepFn == nil {
						return nil, nil
					}
					valPrompt := prompts.BuildResearchValidationPrompt(scope, output)
					valResp, vErr := deps.ValidateStepFn(rCtx, m, valPrompt)
					if vErr != nil {
						return nil, vErr
					}
					return ParseValidationGaps(valResp), nil
				},
			})
			if err == nil && discoveryResult != "" {
				artifacts["discovery"] = discoveryResult
				// Parse structured output from the discovery loop
				for i, line := range strings.Split(discoveryResult, "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "FILE:") {
						path := strings.TrimSpace(strings.TrimPrefix(line, "FILE:"))
						if path != "" {
							relevantFiles[path] += 2.0
						}
					} else if strings.HasPrefix(line, "GAP:MAJOR:") {
						gapDesc := strings.TrimSpace(strings.TrimPrefix(line, "GAP:MAJOR:"))
						if gapDesc != "" {
							gapID := fmt.Sprintf("disc-res-%s-%d-%d", m.ID, time.Now().UnixNano(), i)
							deps.Store.AddGap(&Gap{
								ID:          gapID,
								MissionID:   m.ID,
								Category:    "discovery-research",
								Severity:    "major",
								Description: truncateOutput(gapDesc, 1000),
								Suggestion:  "Address during execution phase",
							})
						}
					} else if strings.HasPrefix(line, "GAP:") {
						gapDesc := strings.TrimSpace(strings.TrimPrefix(line, "GAP:"))
						if gapDesc != "" {
							gapID := fmt.Sprintf("disc-res-%s-%d-%d", m.ID, time.Now().UnixNano(), i)
							deps.Store.AddGap(&Gap{
								ID:          gapID,
								MissionID:   m.ID,
								Category:    "discovery-research",
								Severity:    "blocking",
								Description: truncateOutput(gapDesc, 1000),
								Suggestion:  "Address during execution phase",
							})
						}
					}
				}
			}
			// Persist discovery output to research store for downstream phases
			if deps.RecordResearchFn != nil {
				if discoveryResult, ok := artifacts["discovery"]; ok && discoveryResult != "" {
					deps.RecordResearchFn(m.ID, "Agentic Discovery", discoveryResult)
				}
			}
			// When DiscoveryFn is configured, we skip the deterministic fallback.
			// The model already has access to TF-IDF, symbols, and dependency
			// graph as MCP tools and can invoke them with intent-aware reasoning.
		} else if deps.RepoRoot != "" {
			// Fallback: Deterministic multi-signal search
			// Only used when DiscoveryFn is not configured.
			log.Printf("[mission] %s: agentic discovery disabled, using deterministic fallback", m.ID)
			exts := []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java",
				".css", ".scss", ".html", ".vue", ".svelte", ".yaml", ".yml", ".json"}

			// Signal 1: TF-IDF content search — find files whose content relates to the intent
			tfidfIdx := deps.TFIDFIndex
			if tfidfIdx == nil {
				tfidfIdx, _ = tfidf.Build(deps.RepoRoot, exts)
			}
			if tfidfIdx != nil {
				queries := []string{m.Intent}
				for _, c := range m.Criteria {
					if !c.Satisfied {
						queries = append(queries, c.Description)
					}
				}
				for _, q := range queries {
					results := tfidfIdx.Search(q, 20)
					for _, r := range results {
						if r.Score > 0 {
							relevantFiles[r.Path] += r.Score
						}
					}
				}
			}

			// Signal 2: Symbol index — find symbols whose names relate to the intent
			symIdx := deps.SymbolIndex
			if symIdx == nil {
				symIdx, _ = symindex.Build(deps.RepoRoot)
			}
			if symIdx != nil {
				keywords := extractMissionKeywords(m.Intent)
				for _, kw := range keywords {
					syms := symIdx.Search(kw)
					for _, s := range syms {
						relevantFiles[s.File] += 0.5
					}
				}
				for _, c := range m.Criteria {
					for _, kw := range extractMissionKeywords(c.Description) {
						syms := symIdx.Search(kw)
						for _, s := range syms {
							relevantFiles[s.File] += 0.3
						}
					}
				}
			}

			// Signal 3: Dependency graph — expand via impact analysis
			graph := deps.DepGraph
			if graph == nil {
				graph, _ = depgraph.Build(deps.RepoRoot, exts)
			}
			if graph != nil {
				topFiles := topN(relevantFiles, 15)
				for _, f := range topFiles {
					for _, dep := range graph.Dependents(f) {
						relevantFiles[dep] += 0.2
					}
					for _, dep := range graph.Dependencies(f) {
						relevantFiles[dep] += 0.15
					}
				}

				var graphSummary strings.Builder
				for _, f := range topFiles {
					deps := graph.Dependencies(f)
					dependents := graph.Dependents(f)
					if len(deps) > 0 || len(dependents) > 0 {
						graphSummary.WriteString(fmt.Sprintf("%s:\n", f))
						if len(deps) > 0 {
							graphSummary.WriteString(fmt.Sprintf("  imports: %s\n", strings.Join(deps[:min(len(deps), 5)], ", ")))
						}
						if len(dependents) > 0 {
							graphSummary.WriteString(fmt.Sprintf("  imported by: %s\n", strings.Join(dependents[:min(len(dependents), 5)], ", ")))
						}
					}
				}
				if graphSummary.Len() > 0 {
					artifacts["dependency_map"] = graphSummary.String()
				}
			}
		}

		if deps.Metrics != nil {
			deps.Metrics.RecordResearchQuery()
		}

		// Rank and select top results
		findings := topN(relevantFiles, 30)

		summary := fmt.Sprintf("Semantic search found %d relevant files across %d signals",
			len(findings), countSignals(deps))
		if len(findings) > 0 {
			summary += ": " + strings.Join(findings[:min(len(findings), 5)], ", ")
			if len(findings) > 5 {
				summary += fmt.Sprintf(" (+%d more)", len(findings)-5)
			}
		}

		return &PhaseResult{
			Phase:        PhaseResearching,
			Summary:      summary,
			FilesChanged: findings,
			Artifacts:    artifacts,
			Duration:     time.Since(start),
			Agent:        "research-handler",
		}, nil
	}
}

// topN returns the top N files sorted by relevance score (descending).
func topN(scores map[string]float64, n int) []string {
	type scored struct {
		path  string
		score float64
	}
	items := make([]scored, 0, len(scores))
	for path, score := range scores {
		items = append(items, scored{path, score})
	}
	// Sort by score descending
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	result := make([]string, 0, n)
	for i, item := range items {
		if i >= n {
			break
		}
		result = append(result, item.path)
	}
	return result
}

// countSignals returns how many search signals are available.
func countSignals(deps HandlerDeps) int {
	if deps.DiscoveryFn != nil {
		return 3 // agentic discovery: strongest signal (model-driven multi-turn analysis)
	}
	count := 0
	if deps.RepoRoot != "" {
		count++ // TF-IDF (deterministic fallback)
	}
	return count
}

// NewPlanHandler creates a handler for the Planning phase.
// It generates a structured plan based on mission criteria and builds
// the planning prompt that includes research context and gap history.
func NewPlanHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()
		emitMissionEvent(ctx, deps.EventBus, &hub.Event{
			Type: hub.EventMissionPlanStart, TaskID: m.ID, Phase: "plan",
		})

		mc := buildMissionContext(deps, m)
		planPrompt := prompts.BuildMissionPlanPrompt(mc)

		var planItems []string
		for _, c := range m.Criteria {
			if !c.Satisfied {
				planItems = append(planItems, fmt.Sprintf("Implement: %s", c.Description))
			}
		}

		// Include open gaps from discovery as additional plan items
		gaps, _ := deps.Store.OpenGaps(m.ID)
		for _, g := range gaps {
			planItems = append(planItems, fmt.Sprintf("Fix [%s]: %s", g.Severity, g.Description))
		}

		criteriaCount := 0
		for _, c := range m.Criteria {
			if !c.Satisfied {
				criteriaCount++
			}
		}

		planText := strings.Join(planItems, "\n")

		// Convergent planning via ConvergeStep: multi-model → single-model → single-shot
		planScope := fmt.Sprintf("Mission: %s\nIntent: %s\nCriteria:\n%s\nGaps:\n%s",
			m.Title, m.Intent, mc.CriteriaBlock, mc.GapsBlock)
		if deps.ExecuteFn != nil {
			output, _, err := ConvergeStep(ctx, convergeStepDeps{
				ModelAskFn:    deps.ModelAskFn,
				Models:        deps.ConvergenceModels,
				ArbiterModel:  deps.ArbiterModel,
				MaxDepth:      deps.MaxConvergenceDepth,
				MaxIterations: deps.MaxMicroIterations,
				BiggerMission: planScope,
				Mission:       fmt.Sprintf("Create a complete implementation plan for:\n%s", planScope),
				StepName:      fmt.Sprintf("plan:%s", m.ID),
				ExecuteFn: func(pCtx context.Context, feedback string) (string, error) {
					prompt := planPrompt
					if feedback != "" {
						prompt += "\n\n" + feedback
					}
					_, execErr := deps.ExecuteFn(pCtx, m, prompt, "generate plan")
					if execErr != nil {
						// Fallback to the deterministic plan when the
						// model run fails; log so the fallback signal
						// isn't silently lost.
						log.Printf("mission: plan generation failed for %s, using deterministic plan: %v", m.ID, execErr)
						return planText, nil
					}
					updatedGaps, _ := deps.Store.OpenGaps(m.ID)
					var items []string
					for _, c := range m.Criteria {
						if !c.Satisfied {
							items = append(items, fmt.Sprintf("Implement: %s", c.Description))
						}
					}
					for _, g := range updatedGaps {
						items = append(items, fmt.Sprintf("Fix [%s]: %s", g.Severity, g.Description))
					}
					return strings.Join(items, "\n"), nil
				},
				ValidateFn: func(pCtx context.Context, scope, pOutput string) ([]string, error) {
					if deps.ValidateStepFn == nil {
						return nil, nil
					}
					valPrompt := prompts.BuildPlanValidationPrompt(scope, pOutput)
					valResp, err := deps.ValidateStepFn(pCtx, m, valPrompt)
					if err != nil {
						return nil, err
					}
					return ParseValidationGaps(valResp), nil
				},
			})
			if err == nil {
				planText = output
			}
		}

		summary := fmt.Sprintf("Plan: %d tasks (%d criteria, %d gaps)",
			len(planItems), criteriaCount, len(gaps))

		return &PhaseResult{
			Phase:   PhasePlanning,
			Summary: summary,
			Artifacts: map[string]string{
				"plan":   planText,
				"prompt": planPrompt,
			},
			Duration: time.Since(start),
			Agent:    "plan-handler",
		}, nil
	}
}

// NewExecuteHandler creates a handler for the Executing phase.
// It builds the full mission-aware execute prompt (with criteria, gaps,
// research context, and verification requirements) and passes it to ExecuteFn.
func NewExecuteHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()
		emitMissionEvent(ctx, deps.EventBus, &hub.Event{
			Type: hub.EventMissionExecuteStart, TaskID: m.ID, Phase: "execute",
		})

		mc := buildMissionContext(deps, m)

		// Build task description from unsatisfied criteria and open gaps
		var taskParts []string
		unsatisfied, _ := deps.Store.UnsatisfiedCriteria(m.ID)
		for _, c := range unsatisfied {
			taskParts = append(taskParts, c.Description)
		}

		gaps, _ := deps.Store.OpenGaps(m.ID)
		for _, g := range gaps {
			taskParts = append(taskParts, fmt.Sprintf("[%s] %s", g.Severity, g.Description))
		}

		taskDesc := fmt.Sprintf("Mission: %s\nIntent: %s\n\nRemaining work:\n- %s",
			m.Title, m.Intent, strings.Join(taskParts, "\n- "))

		// Build verification requirements from criteria
		var verification []string
		for _, c := range m.Criteria {
			if !c.Satisfied {
				verification = append(verification, c.Description)
			}
		}

		// Build the full mission-aware execute prompt with skill injection.
		executePrompt := prompts.BuildMissionExecutePrompt(mc, taskDesc, verification)
		if deps.RepoRoot != "" {
			reg := skill.DefaultRegistry(deps.RepoRoot)
			_ = reg.Load()
			executePrompt = reg.InjectPrompt(executePrompt)
		}

		var allFiles []string
		if deps.ExecuteFn == nil {
			// No execute function — nothing to converge on
			if deps.Metrics != nil {
				deps.Metrics.RecordPhaseTransition("executing", time.Since(start))
			}
			return &PhaseResult{
				Phase:    PhaseExecuting,
				Summary:  fmt.Sprintf("No execute function configured, %d work items skipped", len(taskParts)),
				Artifacts: map[string]string{"prompt": executePrompt},
				Duration: time.Since(start),
				Agent:    "execute-handler",
			}, nil
		}
		{
			// Convergent execution via ConvergeStep
			_, _, err := ConvergeStep(ctx, convergeStepDeps{
				ModelAskFn:    deps.ModelAskFn,
				Models:        deps.ConvergenceModels,
				ArbiterModel:  deps.ArbiterModel,
				MaxDepth:      deps.MaxConvergenceDepth,
				MaxIterations: deps.MaxMicroIterations,
				BiggerMission: taskDesc,
				Mission:       fmt.Sprintf("Execute the following work:\n%s", taskDesc),
				StepName:      fmt.Sprintf("execute:%s", m.ID),
				ExecuteFn: func(eCtx context.Context, feedback string) (string, error) {
					prompt := executePrompt
					if feedback != "" {
						prompt += "\n\n" + feedback
					}
					filesChanged, err := deps.ExecuteFn(eCtx, m, prompt, taskDesc)
					if err != nil {
						return "", err
					}
					allFiles = append(allFiles, filesChanged...)
					return fmt.Sprintf("Files changed: %s", strings.Join(filesChanged, ", ")), nil
				},
				ValidateFn: func(eCtx context.Context, scope, output string) ([]string, error) {
					if deps.ValidateStepFn == nil {
						return nil, nil
					}
					valPrompt := prompts.BuildNodeValidationPrompt("implement", scope, output)
					valResp, err := deps.ValidateStepFn(eCtx, m, valPrompt)
					if err != nil {
						return nil, err
					}
					return ParseValidationGaps(valResp), nil
				},
			})
			if err != nil {
				return nil, fmt.Errorf("execute: %w", err)
			}
		}

		if deps.Metrics != nil {
			deps.Metrics.RecordPhaseTransition("executing", time.Since(start))
		}

		return &PhaseResult{
			Phase:        PhaseExecuting,
			Summary:      fmt.Sprintf("Executed %d work items, %d files changed", len(taskParts), len(dedupeStrings(allFiles))),
			FilesChanged: dedupeStrings(allFiles),
			Artifacts: map[string]string{
				"prompt": executePrompt,
			},
			Duration: time.Since(start),
			Agent:    "convergent-execute-handler",
		}, nil
	}
}

// NewDAGExecuteHandler creates a DAG-aware execute handler that recursively
// decomposes work into minimum-scope sub-tasks and executes them in parallel.
//
// Instead of giving one agent the entire mission scope, this handler:
//  1. Asks DecomposeFn to break the scope into minimum-viable work items
//  2. Builds a WorkDAG with dependency and file-conflict awareness
//  3. Dispatches work items in parallel via WorkNodeFn
//  4. If a work item is still too large, it recursively decomposes further
//  5. Aggregates results and gaps from all work items
//
// Falls back to the monolithic NewExecuteHandler if DecomposeFn or WorkNodeFn
// is not configured.
func NewDAGExecuteHandler(deps HandlerDeps) PhaseHandler {
	// Fall back to monolithic handler if DAG callbacks aren't configured
	if deps.DecomposeFn == nil || deps.WorkNodeFn == nil {
		return NewExecuteHandler(deps)
	}

	maxWorkers := deps.MaxDAGWorkers
	if maxWorkers <= 0 {
		maxWorkers = 3
	}
	maxDepth := deps.MaxDAGDepth
	if maxDepth <= 0 {
		maxDepth = 4
	}

	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()
		mc := buildMissionContext(deps, m)

		// Build the top-level scope from unsatisfied criteria and open gaps
		var scopeParts []string
		unsatisfied, _ := deps.Store.UnsatisfiedCriteria(m.ID)
		for _, c := range unsatisfied {
			scopeParts = append(scopeParts, c.Description)
		}
		gaps, _ := deps.Store.OpenGaps(m.ID)
		for _, g := range gaps {
			scopeParts = append(scopeParts, fmt.Sprintf("[%s] %s", g.Severity, g.Description))
		}

		rootScope := fmt.Sprintf("Mission: %s\nIntent: %s\n\nRemaining work:\n- %s",
			m.Title, m.Intent, strings.Join(scopeParts, "\n- "))

		// convergeStepForMission builds a convergeStepDeps from handler deps.
		// Uses multi-model ConvergedAnswer when available, falls back to
		// single-model MicroConvergence, falls back to single-shot.
		buildConvergeDeps := func(stepName, mission string, executeFn func(context.Context, string) (string, error), validateFn func(context.Context, string, string) ([]string, error)) convergeStepDeps {
			return convergeStepDeps{
				ModelAskFn:    deps.ModelAskFn,
				Models:        deps.ConvergenceModels,
				ArbiterModel:  deps.ArbiterModel,
				MaxDepth:      deps.MaxConvergenceDepth,
				ValidateFn:    validateFn,
				MaxIterations: deps.MaxMicroIterations,
				ExecuteFn:     executeFn,
				BiggerMission: rootScope,
				Mission:       mission,
				StepName:      stepName,
			}
		}

		// Convergent decomposition: models decompose → arbiter validates → recurse until converged
		decompOutput, _, err := ConvergeStep(ctx, buildConvergeDeps(
			fmt.Sprintf("decompose:%s", m.ID),
			fmt.Sprintf("Break down this scope into minimum-viable work items:\n\n%s", rootScope),
			func(execCtx context.Context, feedback string) (string, error) {
				prompt := prompts.BuildDecompositionPrompt(mc, "implement", rootScope, 0, maxDepth)
				if feedback != "" {
					prompt += "\n\n" + feedback
				}
				return deps.DecomposeFn(execCtx, m, prompt)
			},
			func(execCtx context.Context, scope, output string) ([]string, error) {
				if deps.ValidateStepFn == nil {
					return nil, nil
				}
				items, direct := parseDecomposition(output)
				if direct || len(items) == 0 {
					return nil, nil
				}
				valPrompt := prompts.BuildDecompositionValidationPrompt(scope, formatDecompositionForValidation(items))
				valResp, err := deps.ValidateStepFn(execCtx, m, valPrompt)
				if err != nil {
					return nil, err
				}
				return ParseValidationGaps(valResp), nil
			},
		))
		if err != nil {
			return nil, fmt.Errorf("convergent decompose: %w", err)
		}

		workItems, shouldExecuteDirectly := parseDecomposition(decompOutput)
		if shouldExecuteDirectly || len(workItems) == 0 {
			log.Printf("[mission] %s: scope small enough for direct execution", m.ID)
			return NewExecuteHandler(deps)(ctx, m)
		}

		log.Printf("[mission] %s: decomposed into %d work items, executing via DAG (max %d workers, max depth %d)",
			m.ID, len(workItems), maxWorkers, maxDepth)

		// Build the WorkDAG — each node converges independently via ConvergeStep
		executor := func(execCtx context.Context, node *WorkNode, mCtx prompts.MissionContext) (*WorkResult, error) {
			nodeStart := time.Now()

			// Recursive decomposition nodes converge
			if node.Depth < maxDepth-1 && node.Type == WorkDecompose {
				output, _, dErr := ConvergeStep(execCtx, buildConvergeDeps(
					fmt.Sprintf("decompose:%s", node.ID),
					node.Scope,
					func(dCtx context.Context, feedback string) (string, error) {
						prompt := prompts.BuildDecompositionPrompt(mCtx, string(node.Type), node.Scope, node.Depth, maxDepth)
						if feedback != "" {
							prompt += "\n\n" + feedback
						}
						return deps.DecomposeFn(dCtx, m, prompt)
					},
					func(dCtx context.Context, scope, dOutput string) ([]string, error) {
						if deps.ValidateStepFn == nil {
							return nil, nil
						}
						items, direct := parseDecomposition(dOutput)
						if direct || len(items) == 0 {
							return nil, nil
						}
						valPrompt := prompts.BuildDecompositionValidationPrompt(scope, formatDecompositionForValidation(items))
						valResp, err := deps.ValidateStepFn(dCtx, m, valPrompt)
						if err != nil {
							return nil, err
						}
						return ParseValidationGaps(valResp), nil
					},
				))
				if dErr != nil {
					return nil, fmt.Errorf("convergent decompose at depth %d: %w", node.Depth, dErr)
				}
				children, direct := parseDecomposition(output)
				if !direct && len(children) > 0 {
					return &WorkResult{
						Summary:  fmt.Sprintf("Decomposed into %d sub-tasks (converged)", len(children)),
						Children: children,
						Duration: time.Since(nodeStart),
						Agent:    "convergent-decompose",
					}, nil
				}
			}

			// Work node execution — converges via ConvergeStep (multi-model or single-model)
			var allFiles []string
			output, converged, execErr := ConvergeStep(execCtx, buildConvergeDeps(
				fmt.Sprintf("node:%s:%s", node.Type, node.ID),
				node.Scope,
				func(nCtx context.Context, feedback string) (string, error) {
					nodePrompt := prompts.BuildWorkNodePrompt(mCtx, string(node.Type), node.Scope, rootScope, "")
					if feedback != "" {
						nodePrompt += "\n\n" + feedback
					}
					filesChanged, err := deps.WorkNodeFn(nCtx, m, nodePrompt, node.Scope)
					if err != nil {
						return "", err
					}
					allFiles = append(allFiles, filesChanged...)
					return fmt.Sprintf("Files changed: %s", strings.Join(filesChanged, ", ")), nil
				},
				func(nCtx context.Context, scope, nOutput string) ([]string, error) {
					if deps.ValidateStepFn == nil {
						return nil, nil
					}
					valPrompt := prompts.BuildNodeValidationPrompt(string(node.Type), scope, nOutput)
					valResp, err := deps.ValidateStepFn(nCtx, m, valPrompt)
					if err != nil {
						return nil, err
					}
					return ParseValidationGaps(valResp), nil
				},
			))
			if execErr != nil {
				return nil, execErr
			}

			agent := "convergent-work-node"
			summary := fmt.Sprintf("Converged: %s", truncateOutput(node.Scope, 80))
			var resultGaps []string
			if !converged {
				agent = "work-node-unconverged"
				summary = fmt.Sprintf("Unconverged: %s", truncateOutput(node.Scope, 80))
				resultGaps = []string{fmt.Sprintf("node %s did not fully converge: %s", node.ID, truncateOutput(output, 200))}
			}

			return &WorkResult{
				Summary:      summary,
				FilesChanged: dedupeStrings(allFiles),
				Gaps:         resultGaps,
				Duration:     time.Since(nodeStart),
				Agent:        agent,
			}, nil
		}

		dag := NewWorkDAG(executor, maxWorkers)
		dag.SetMaxDepth(maxDepth)

		for _, item := range workItems {
			if err := dag.AddNode(item); err != nil {
				log.Printf("[mission] %s: failed to add work node %s: %v", m.ID, item.ID, err)
			}
		}

		dagResult, err := dag.Run(ctx, mc)
		if err != nil {
			return nil, fmt.Errorf("dag execution: %w", err)
		}

		// Record any gaps discovered during work
		for _, gapDesc := range dagResult.Gaps {
			gapID := fmt.Sprintf("dag-%s-%d", m.ID, time.Now().UnixNano())
			deps.Store.AddGap(&Gap{
				ID:          gapID,
				MissionID:   m.ID,
				Category:    "completeness",
				Severity:    "blocking",
				Description: gapDesc,
			})
		}

		if deps.Metrics != nil {
			deps.Metrics.RecordPhaseTransition("executing", time.Since(start))
		}

		return &PhaseResult{
			Phase: PhaseExecuting,
			Summary: fmt.Sprintf("DAG execution: %d/%d nodes complete, %d failed, %d files changed",
				dagResult.NodesComplete, dagResult.NodesTotal, dagResult.NodesFailed, len(dagResult.FilesChanged)),
			FilesChanged: dagResult.FilesChanged,
			Artifacts: map[string]string{
				"dag_stats": fmt.Sprintf("total=%d complete=%d failed=%d blocked=%d",
					dagResult.NodesTotal, dagResult.NodesComplete, dagResult.NodesFailed, dagResult.NodesBlocked),
			},
			Duration: time.Since(start),
			Agent:    "dag-execute-handler",
		}, nil
	}
}

// parseDecomposition parses the JSON response from DecomposeFn.
// Returns work items and whether the scope should be executed directly.
func parseDecomposition(response string) ([]WorkNode, bool) {
	type decompItem struct {
		ID        string   `json:"id"`
		Type      string   `json:"type"`
		Scope     string   `json:"scope"`
		DependsOn []string `json:"depends_on"`
		Files     []string `json:"files"`
	}
	type decompResponse struct {
		Action string       `json:"action"`
		Items  []decompItem `json:"items"`
	}

	var resp decompResponse
	if err := json.Unmarshal([]byte(response), &resp); err != nil {
		// Try to find JSON in the response
		start := strings.Index(response, "{")
		end := strings.LastIndex(response, "}")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(response[start:end+1]), &resp); err2 != nil {
				return nil, true // can't parse, execute directly
			}
		} else {
			return nil, true
		}
	}

	if resp.Action == "execute" || len(resp.Items) == 0 {
		return nil, true
	}

	now := time.Now()
	nodes := make([]WorkNode, 0, len(resp.Items))
	for _, item := range resp.Items {
		wt := WorkType(item.Type)
		if wt == "" {
			wt = WorkImplement
		}
		nodes = append(nodes, WorkNode{
			ID:        item.ID,
			Type:      wt,
			Scope:     item.Scope,
			DependsOn: item.DependsOn,
			Files:     item.Files,
			Status:    WorkPending,
			MaxDepth:  4,
			CreatedAt: now,
		})
	}
	return nodes, false
}

// NewValidateHandler creates a handler for the Validating phase.
//
// Validation has three layers, all of which produce blocking gaps:
//
//  1. Live verification: Runs actual build/test/lint commands against the repo.
//     ANY failure is a blocking gap — pre-existing or introduced. The harness
//     does not distinguish between "was already broken" and "we broke it."
//     If the test suite is red, the work is not done.
//
//  2. Static analysis: Runs the convergence rule engine against source files
//     for code quality, security, and completeness checks.
//
//  3. Adversarial LLM validation: Sends the full mission-aware validation
//     prompt (with criteria, gaps, research, and the 5 convergence gates)
//     to a model via ValidateFn. The model is instructed to disprove
//     completeness, not confirm it.
//
// All three layers produce gaps that must be resolved before convergence.
func NewValidateHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()
		emitMissionEvent(ctx, deps.EventBus, &hub.Event{
			Type: hub.EventMissionValidateStart, TaskID: m.ID, Phase: "validate",
		})

		if deps.RepoRoot == "" {
			return &PhaseResult{
				Phase:   PhaseValidating,
				Summary: "Validation skipped (no repo root)",
			}, nil
		}

		var allGapCount int
		var blockingCount int
		var summaryParts []string

		// --- Layer 1: Live verification (build/test/lint) ---
		if deps.VerifyCommands != nil {
			snap, err := baseline.Verify(ctx, deps.RepoRoot, *deps.VerifyCommands)
			if err != nil {
				return nil, fmt.Errorf("live verification: %w", err)
			}

			for _, failure := range snap.Failures() {
				gapID := fmt.Sprintf("verify-%s-%s-%d", m.ID, failure.Name, time.Now().UnixNano())

				category := "verification"
				description := fmt.Sprintf("%s failed (exit %d): %s",
					failure.Name, failure.ExitCode, truncateOutput(failure.Output, 500))
				suggestion := fmt.Sprintf("Fix the %s failure. Run: %s", failure.Name, failure.Command)

				if deps.Baseline != nil {
					diff := baseline.Compare(deps.Baseline, snap)
					for _, pe := range diff.PreExisting {
						if pe.Name == failure.Name {
							category = "pre-existing-failure"
							description = fmt.Sprintf("PRE-EXISTING %s failure (was broken before mission started, must still be fixed): %s",
								failure.Name, truncateOutput(failure.Output, 500))
							suggestion = fmt.Sprintf("This %s failure existed before the mission. Fix it — the harness requires a green suite, not just 'no regressions'.",
								failure.Name)
							break
						}
					}
				}

				deps.Store.AddGap(&Gap{
					ID:          gapID,
					MissionID:   m.ID,
					Category:    category,
					Severity:    "blocking",
					Description: description,
					Suggestion:  suggestion,
				})
				allGapCount++
				blockingCount++

				if deps.Metrics != nil {
					deps.Metrics.RecordGapFound(true)
				}
			}

			if snap.AllPass {
				summaryParts = append(summaryParts, fmt.Sprintf("verification: %d commands all pass", len(snap.Results)))
			} else {
				summaryParts = append(summaryParts, fmt.Sprintf("verification: %d/%d FAILED",
					len(snap.Failures()), len(snap.Results)))
			}
		}

		// --- Layer 2: Static analysis (convergence rules) ---
		// When Layer 4 (agentic discovery validation) is available, we only
		// run security-critical rules here. The model handles completeness,
		// test quality, code quality, and UX analysis far better than regex.
		// When Layer 4 is NOT available, we run the full rule set.
		if deps.Validator != nil {
			var files []convergence.FileInput
			_ = filepath.WalkDir(deps.RepoRoot, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					// Best-effort validation scan: log unreadable
					// entries and keep walking so a single bad path
					// can't prevent validation of the rest of the repo.
					log.Printf("mission: validator walk error at %s: %v", path, walkErr)
					return nil
				}
				if d.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(deps.RepoRoot, path)
				if strings.HasPrefix(rel, ".") || strings.Contains(rel, "vendor/") ||
					strings.Contains(rel, "node_modules/") {
					return nil
				}
				ext := filepath.Ext(path)
				if ext == ".go" || ext == ".ts" || ext == ".js" || ext == ".py" || ext == ".rs" {
					content, err := os.ReadFile(path)
					if err == nil {
						files = append(files, convergence.FileInput{Path: rel, Content: content})
					}
				}
				return nil
			})

			var report *convergence.Report
			if deps.ValidateDiscoveryFn != nil {
				// Agentic validation is available — only run security rules.
				// The model handles everything else with far more accuracy.
				report = deps.Validator.ValidateSecurityOnly(m.ID, files)
			} else {
				// No agentic validation — run full rule set including criteria.
				var criteriaDescs []string
				for _, c := range m.Criteria {
					if !c.Satisfied {
						criteriaDescs = append(criteriaDescs, c.Description)
					}
				}
				if len(criteriaDescs) > 0 {
					report = deps.Validator.ValidateWithCriteria(m.ID, files, criteriaDescs)
				} else {
					report = deps.Validator.Validate(m.ID, files)
				}
			}

			for i, f := range report.Findings {
				gapID := fmt.Sprintf("val-%s-%d-%d", m.ID, time.Now().Unix(), i)
				deps.Store.AddGap(&Gap{
					ID:          gapID,
					MissionID:   m.ID,
					Category:    string(f.Category),
					Severity:    string(f.Severity),
					Description: f.Description,
					File:        f.File,
					Line:        f.Line,
					Suggestion:  f.Suggestion,
				})
				allGapCount++
				if f.Severity == convergence.SevBlocking {
					blockingCount++
				}

				if deps.Metrics != nil {
					deps.Metrics.RecordGapFound(f.Severity == convergence.SevBlocking)
				}
			}

			mode := "full"
			if deps.ValidateDiscoveryFn != nil {
				mode = "security-only"
			}
			summaryParts = append(summaryParts, fmt.Sprintf("static(%s): score=%.2f, %d findings (%d blocking)",
				mode, report.Score, len(report.Findings), report.BlockingCount()))
		}

		// --- Layer 3: Convergent adversarial LLM validation ---
		// A fresh invocation validates the validator — catches gaps the first pass missed.
		if deps.ValidateFn != nil {
			mc := buildMissionContext(deps, m)
			validatePrompt := prompts.BuildMissionValidatePrompt(mc)

			validationScope := fmt.Sprintf("Adversarial validation of mission: %s\nIntent: %s", m.Title, m.Intent)
			findings, _, err := ConvergeStep(ctx, convergeStepDeps{
				ModelAskFn:    deps.ModelAskFn,
				Models:        deps.ConvergenceModels,
				ArbiterModel:  deps.ArbiterModel,
				MaxDepth:      deps.MaxConvergenceDepth,
				MaxIterations: deps.MaxMicroIterations,
				BiggerMission: validationScope,
				Mission:       "Find ALL gaps in the implementation. Miss nothing.",
				StepName:      fmt.Sprintf("validate-l3:%s", m.ID),
				ExecuteFn: func(vCtx context.Context, feedback string) (string, error) {
					prompt := validatePrompt
					if feedback != "" {
						prompt += "\n\n" + feedback
					}
					return deps.ValidateFn(vCtx, m, prompt)
				},
				ValidateFn: func(vCtx context.Context, scope, output string) ([]string, error) {
					if deps.ValidateStepFn == nil {
						return nil, nil
					}
					// Ask a fresh invocation: did the validator miss anything?
					reviewPrompt := fmt.Sprintf(`A validator produced these findings for scope:
%s

Findings:
%s

Did the validator miss anything? Are all findings accurate and specific?
Return {"gaps": [...]} with any missed items or {"gaps": []} if complete.`, scope, output)
					resp, err := deps.ValidateStepFn(vCtx, m, reviewPrompt)
					if err != nil {
						return nil, err
					}
					return ParseValidationGaps(resp), nil
				},
			})
			if err != nil {
				summaryParts = append(summaryParts, fmt.Sprintf("adversarial: error (%v)", err))
			} else if findings != "" {
				layer3GapCount := parseValidationFindings(findings, m.ID, deps.Store)
				allGapCount += layer3GapCount
				blockingCount += layer3GapCount
				if layer3GapCount > 0 {
					summaryParts = append(summaryParts, fmt.Sprintf("adversarial: %d gaps", layer3GapCount))
				} else {
					summaryParts = append(summaryParts, "adversarial: findings reported (unstructured)")
				}
			} else {
				summaryParts = append(summaryParts, "adversarial: no findings")
			}
		}

		// --- Layer 4: Agentic multi-turn discovery validation ---
		// This is the highest-quality validation: the model iteratively
		// traces code paths, checks consumer/producer contracts, verifies
		// cross-surface reachability, and reasons about intent satisfaction.
		//
		// Unlike Layer 3 (single-shot), this runs a multi-turn loop where
		// the model can read files, search symbols, check dependencies,
		// and build a complete picture before declaring findings.
		if deps.ValidateDiscoveryFn != nil {
			mc := buildMissionContext(deps, m)
			discoveryPrompt := prompts.BuildMissionValidateDiscoveryPrompt(mc)

			discValScope := fmt.Sprintf("Multi-turn discovery validation of mission: %s\nIntent: %s", m.Title, m.Intent)
			findings, _, err := ConvergeStep(ctx, convergeStepDeps{
				ModelAskFn:    deps.ModelAskFn,
				Models:        deps.ConvergenceModels,
				ArbiterModel:  deps.ArbiterModel,
				MaxDepth:      deps.MaxConvergenceDepth,
				MaxIterations: deps.MaxMicroIterations,
				BiggerMission: discValScope,
				Mission:       "Trace all code paths, verify consumer/producer contracts, check cross-surface reachability. Miss nothing.",
				StepName:      fmt.Sprintf("validate-l4:%s", m.ID),
				ExecuteFn: func(vCtx context.Context, feedback string) (string, error) {
					prompt := discoveryPrompt
					if feedback != "" {
						prompt += "\n\n" + feedback
					}
					return deps.ValidateDiscoveryFn(vCtx, m, prompt)
				},
				ValidateFn: func(vCtx context.Context, scope, output string) ([]string, error) {
					if deps.ValidateStepFn == nil {
						return nil, nil
					}
					reviewPrompt := fmt.Sprintf(`A discovery validator traced code paths and produced these findings:
%s

Did the validator miss any code paths, consumer/producer relationships,
cross-surface issues, or security implications?
Return {"gaps": [...]} with any missed items or {"gaps": []} if thorough.`, output)
					resp, err := deps.ValidateStepFn(vCtx, m, reviewPrompt)
					if err != nil {
						return nil, err
					}
					return ParseValidationGaps(resp), nil
				},
			})
			if err != nil {
				summaryParts = append(summaryParts, fmt.Sprintf("discovery-validation: error (%v)", err))
			} else if findings != "" {
				// Parse structured findings from the model's discovery.
				// Try JSON first (model may return structured response),
				// then fall back to line-based GAP:/FIXED: parsing.
				var newGapCount, fixedCount int

				type discoveryGap struct {
					Category    string `json:"category"`
					Severity    string `json:"severity"`
					File        string `json:"file"`
					Line        int    `json:"line"`
					Description string `json:"description"`
					Suggestion  string `json:"suggestion"`
					Fixed       bool   `json:"fixed"`
				}
				type discoveryResponse struct {
					Gaps  []discoveryGap `json:"gaps"`
					Fixed []string       `json:"fixed"`
				}

				jsonParsed := false
				if idx := strings.Index(findings, "{"); idx >= 0 {
					if end := strings.LastIndex(findings, "}"); end > idx {
						var parsed discoveryResponse
						if err := json.Unmarshal([]byte(findings[idx:end+1]), &parsed); err == nil && (len(parsed.Gaps) > 0 || len(parsed.Fixed) > 0) {
							jsonParsed = true
							for _, fix := range parsed.Fixed {
								openGaps, _ := deps.Store.OpenGaps(m.ID)
								for _, g := range openGaps {
									if strings.Contains(g.Description, fix) || strings.Contains(fix, g.Description) {
										deps.Store.ResolveGap(m.ID, g.ID)
										fixedCount++
										break
									}
								}
							}
							for i, gap := range parsed.Gaps {
								if gap.Fixed {
									continue
								}
								severity := gap.Severity
								if severity == "" {
									severity = "blocking"
								}
								category := gap.Category
								if category == "" {
									category = "discovery-validation"
								}
								gapID := fmt.Sprintf("disc-val-%s-%d-%d", m.ID, time.Now().UnixNano(), i)
								deps.Store.AddGap(&Gap{
									ID:          gapID,
									MissionID:   m.ID,
									Category:    category,
									Severity:    severity,
									Description: truncateOutput(gap.Description, 1000),
									File:        gap.File,
									Line:        gap.Line,
									Suggestion:  gap.Suggestion,
								})
								allGapCount++
								if severity == "blocking" {
									blockingCount++
								}
								newGapCount++
							}
						}
					}
				}

				if !jsonParsed {
				for i, line := range strings.Split(findings, "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}

					// Handle FIXED: lines — resolve matching open gaps
					if strings.HasPrefix(line, "FIXED:") {
						fixedDesc := strings.TrimSpace(strings.TrimPrefix(line, "FIXED:"))
						if fixedDesc == "" {
							continue
						}
						// Try to match against open gaps by substring
						openGaps, _ := deps.Store.OpenGaps(m.ID)
						for _, g := range openGaps {
							if strings.Contains(g.Description, fixedDesc) || strings.Contains(fixedDesc, g.Description) {
								deps.Store.ResolveGap(m.ID, g.ID)
								fixedCount++
								break
							}
						}
						continue
					}

					// Handle GAP: and GAP:MAJOR: lines
					// Check more specific prefix first
					if strings.HasPrefix(line, "GAP:MAJOR:") {
						gapDesc := strings.TrimSpace(strings.TrimPrefix(line, "GAP:MAJOR:"))
						if gapDesc == "" {
							continue
						}
						gapID := fmt.Sprintf("disc-val-%s-%d-%d", m.ID, time.Now().UnixNano(), i)
						deps.Store.AddGap(&Gap{
							ID:          gapID,
							MissionID:   m.ID,
							Category:    "discovery-validation",
							Severity:    "major",
							Description: truncateOutput(gapDesc, 1000),
							Suggestion:  "Address the gap found by multi-turn discovery validation",
						})
						allGapCount++
						newGapCount++
					} else if strings.HasPrefix(line, "GAP:") {
						gapDesc := strings.TrimSpace(strings.TrimPrefix(line, "GAP:"))
						if gapDesc == "" {
							continue
						}
						gapID := fmt.Sprintf("disc-val-%s-%d-%d", m.ID, time.Now().UnixNano(), i)
						deps.Store.AddGap(&Gap{
							ID:          gapID,
							MissionID:   m.ID,
							Category:    "discovery-validation",
							Severity:    "blocking",
							Description: truncateOutput(gapDesc, 1000),
							Suggestion:  "Address the gap found by multi-turn discovery validation",
						})
						allGapCount++
						blockingCount++
						newGapCount++
					}
				}
				} // end !jsonParsed
				if newGapCount > 0 || fixedCount > 0 {
					summaryParts = append(summaryParts, fmt.Sprintf("discovery-validation: %d new gaps, %d fixed", newGapCount, fixedCount))
				} else {
					summaryParts = append(summaryParts, "discovery-validation: no structured gaps")
				}
			// Persist validation findings to research store for future phases
			if deps.RecordResearchFn != nil && findings != "" {
				deps.RecordResearchFn(m.ID, "Validation Discovery", findings)
			}
			} else {
				summaryParts = append(summaryParts, "discovery-validation: clean")
			}
		}

		summary := strings.Join(summaryParts, " | ")
		if len(summaryParts) == 0 {
			summary = "Validation skipped (no commands, validator, or validate function configured)"
		}

		if deps.Metrics != nil {
			deps.Metrics.RecordPhaseTransition("validating", time.Since(start))
		}

		return &PhaseResult{
			Phase:   PhaseValidating,
			Summary: summary,
			Artifacts: map[string]string{
				"total_gaps":    fmt.Sprintf("%d", allGapCount),
				"blocking_gaps": fmt.Sprintf("%d", blockingCount),
			},
			Duration: time.Since(start),
			Agent:    "validate-handler",
		}, nil
	}
}

// truncateOutput returns the last N bytes of output for gap descriptions.
func truncateOutput(output string, maxLen int) string {
	output = strings.TrimSpace(output)
	if len(output) <= maxLen {
		return output
	}
	return "..." + output[len(output)-maxLen:]
}

// vagueAffirmationPatterns lists phrases that indicate the model is rubber-stamping
// rather than providing evidence-based reasoning.
var vagueAffirmationPatterns = []string{
	"looks good",
	"appears complete",
	"should work",
	"seems fine",
	"no issues found",
	"everything is in order",
	"all looks correct",
	"implementation is solid",
}

// isVagueAffirmation returns true if the reasoning contains vague affirmation
// phrases without specific evidence, or is too terse to contain real analysis.
func isVagueAffirmation(reasoning string) bool {
	if len(reasoning) < 100 {
		return true
	}
	lower := strings.ToLower(reasoning)
	for _, pat := range vagueAffirmationPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// evidenceCitationRe matches file:line references like "auth.go:42" or path references.
var evidenceCitationRe = regexp.MustCompile(`\w+\.\w+:\d+`)

// fileExtensionRe matches common source file extensions in reasoning text.
var fileExtensionRe = regexp.MustCompile(`\.\b(go|ts|tsx|js|jsx|py|rs|java|rb|c|cpp|h|hpp|css|html|sql|yaml|yml|json|toml)\b`)

// hasEvidenceCitations returns true if the reasoning contains at least one
// file path reference (file:line or file extension mention).
func hasEvidenceCitations(reasoning string) bool {
	if evidenceCitationRe.MatchString(reasoning) {
		return true
	}
	if fileExtensionRe.MatchString(reasoning) {
		return true
	}
	return false
}

// scopeQualifierRe matches "pre-existing" or "out of scope" qualifiers in gap descriptions.
var scopeQualifierRe = regexp.MustCompile(`(?i)\b(pre-existing|out of scope)\b`)

// NewConsensusHandler creates a handler for the Converged phase.
// It builds the full adversarial consensus prompt (with the validation report,
// anti-rationalization protocol, and challenge questions) and passes it to
// each consensus model. Models must try to DISPROVE completeness.
func NewConsensusHandler(deps HandlerDeps, models []string) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()
		emitMissionEvent(ctx, deps.EventBus, &hub.Event{
			Type: hub.EventMissionConsensusStart, TaskID: m.ID, Phase: "consensus",
		})

		// Build mission context for the consensus prompt
		mc := buildMissionContext(deps, m)

		// Build a validation report summary from the latest gaps and convergence status
		var reportParts []string
		if deps.Store != nil {
			status, err := deps.Store.GetConvergenceStatus(m.ID, len(models))
			if err == nil {
				reportParts = append(reportParts, fmt.Sprintf(
					"Convergence: %d/%d criteria satisfied, %d open gaps (%d blocking), consensus=%v",
					status.SatisfiedCriteria, status.TotalCriteria,
					status.OpenGapCount, status.BlockingGapCount, status.HasConsensus))
			}

			gaps, _ := deps.Store.OpenGaps(m.ID)
			if len(gaps) > 0 {
				reportParts = append(reportParts, fmt.Sprintf("Open gaps (%d):", len(gaps)))
				for _, g := range gaps {
					reportParts = append(reportParts, fmt.Sprintf("  - [%s] %s (%s)", g.Severity, g.Description, g.Category))
				}
			} else {
				reportParts = append(reportParts, "No open gaps found by static analysis and live verification.")
			}
		}
		validationReport := strings.Join(reportParts, "\n")

		if deps.ConsensusModelFn == nil {
			// Never auto-approve — consensus requires real model verification
			return nil, fmt.Errorf("consensus requires at least one model function configured")
		}

		// All models vote in PARALLEL — independent adversarial reviewers
		type consensusVote struct {
			model     string
			verdict   string
			reasoning string
			gapsFound []string
			err       error
		}
		voteCh := make(chan consensusVote, len(models))
		consensusPrompt := prompts.BuildMissionConsensusPrompt(mc, validationReport)

		for _, model := range models {
			go func(mdl string) {
				verdict, reasoning, gapsFound, err := deps.ConsensusModelFn(ctx, m.ID, mdl, consensusPrompt)
				voteCh <- consensusVote{model: mdl, verdict: verdict, reasoning: reasoning, gapsFound: gapsFound, err: err}
			}(model)
		}

		var verdicts []string
		for range models {
			vote := <-voteCh
			if vote.err != nil {
				return nil, fmt.Errorf("consensus from %s: %w", vote.model, vote.err)
			}

			// Anti-hallucination: reject vague "complete" verdicts that lack evidence
			if vote.verdict == "complete" {
				if isVagueAffirmation(vote.reasoning) || !hasEvidenceCitations(vote.reasoning) {
					vote.verdict = "incomplete"
					vote.gapsFound = append(vote.gapsFound, "Consensus rejected: reasoning lacks specific evidence (file:line citations required)")
				}
			}

			// Scope expansion: nothing is "out of scope" or "pre-existing" — all issues block
			for i, gap := range vote.gapsFound {
				vote.gapsFound[i] = strings.TrimSpace(scopeQualifierRe.ReplaceAllString(gap, ""))
			}

			deps.Store.RecordConsensus(&ConsensusRecord{
				MissionID: m.ID,
				Model:     vote.model,
				Verdict:   vote.verdict,
				Reasoning: vote.reasoning,
				GapsFound: vote.gapsFound,
			})

			for i, gapDesc := range vote.gapsFound {
				gapID := fmt.Sprintf("consensus-%s-%s-%d-%d", m.ID, vote.model, time.Now().UnixNano(), i)
				deps.Store.AddGap(&Gap{
					ID:          gapID,
					MissionID:   m.ID,
					Category:    "consensus",
					Severity:    "blocking",
					Description: truncateOutput(gapDesc, 1000),
					Suggestion:  fmt.Sprintf("Identified by %s during consensus review", vote.model),
				})
			}

			if deps.Metrics != nil {
				deps.Metrics.RecordConsensusVote(vote.verdict == "complete")
			}

			verdicts = append(verdicts, fmt.Sprintf("%s: %s", vote.model, vote.verdict))
		}

		return &PhaseResult{
			Phase:   PhaseConverged,
			Summary: strings.Join(verdicts, ", "),
			Artifacts: map[string]string{
				"validation_report": validationReport,
			},
			Duration: time.Since(start),
			Agent:    "consensus-handler",
		}, nil
	}
}

// parseValidationFindings extracts structured gaps from Layer 3 model output.
// The prompt asks for JSON with a gaps array, but we gracefully handle raw text too.
func parseValidationFindings(findings, missionID string, store *Store) int {
	// Try to parse JSON response
	type validateGap struct {
		Category    string `json:"category"`
		Severity    string `json:"severity"`
		File        string `json:"file"`
		Line        int    `json:"line"`
		Description string `json:"description"`
		Suggestion  string `json:"suggestion"`
	}
	type validateResponse struct {
		Verdict string        `json:"verdict"`
		Gaps    []validateGap `json:"gaps"`
	}

	// Extract JSON from response (may be wrapped in code fences)
	jsonText := findings
	if idx := strings.Index(jsonText, "{"); idx >= 0 {
		if end := strings.LastIndex(jsonText, "}"); end > idx {
			jsonText = jsonText[idx : end+1]
		}
	}

	var parsed validateResponse
	if err := json.Unmarshal([]byte(jsonText), &parsed); err == nil && len(parsed.Gaps) > 0 {
		for i, g := range parsed.Gaps {
			sev := g.Severity
			if sev == "" {
				sev = "blocking"
			}
			cat := g.Category
			if cat == "" {
				cat = "adversarial-validation"
			}
			gapID := fmt.Sprintf("llm-val-%s-%d-%d", missionID, time.Now().UnixNano(), i)
			store.AddGap(&Gap{
				ID:          gapID,
				MissionID:   missionID,
				Category:    cat,
				Severity:    sev,
				Description: truncateOutput(g.Description, 1000),
				File:        g.File,
				Line:        g.Line,
				Suggestion:  g.Suggestion,
			})
		}
		return len(parsed.Gaps)
	}

	// Fallback: store the raw findings as a single gap
	if strings.TrimSpace(findings) != "" {
		gapID := fmt.Sprintf("llm-val-%s-%d", missionID, time.Now().UnixNano())
		store.AddGap(&Gap{
			ID:          gapID,
			MissionID:   missionID,
			Category:    "adversarial-validation",
			Severity:    "blocking",
			Description: truncateOutput(findings, 1000),
			Suggestion:  "Address the findings from adversarial LLM validation",
		})
		return 1
	}

	return 0
}

// extractMissionKeywords extracts searchable keywords from intent text.
func extractMissionKeywords(intent string) []string {
	words := strings.Fields(strings.ToLower(intent))
	seen := make(map[string]bool)
	keywords := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()[]{}") // strip punctuation
		if len(w) < 3 || seen[w] {
			continue
		}
		switch w {
		case "the", "and", "for", "with", "that", "this", "from", "are",
			"was", "been", "have", "has", "will", "can", "should", "would",
			"not", "all", "but", "they", "each", "which", "their", "into":
			continue
		}
		seen[w] = true
		keywords = append(keywords, w)
	}
	return keywords
}

// dedupeStrings returns a deduplicated copy of the input slice, preserving order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// emitMissionEvent sends an event to the hub bus if configured. Nil-safe.
func emitMissionEvent(ctx context.Context, bus *hub.Bus, ev *hub.Event) {
	if bus == nil {
		return
	}
	bus.Emit(ctx, ev)
}

// formatDecompositionForValidation formats work items as a human-readable
// summary for the decomposition validation prompt.
func formatDecompositionForValidation(items []WorkNode) string {
	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "%d. [%s] %s (id=%s", i+1, item.Type, item.Scope, item.ID)
		if len(item.DependsOn) > 0 {
			fmt.Fprintf(&b, ", depends_on=%s", strings.Join(item.DependsOn, ","))
		}
		if len(item.Files) > 0 {
			fmt.Fprintf(&b, ", files=%s", strings.Join(item.Files, ","))
		}
		fmt.Fprintf(&b, ")\n")
	}
	return b.String()
}
