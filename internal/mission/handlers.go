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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/baseline"
	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/depgraph"
	"github.com/ericmacdougall/stoke/internal/prompts"
	"github.com/ericmacdougall/stoke/internal/symindex"
	"github.com/ericmacdougall/stoke/internal/tfidf"
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
// It uses multi-signal semantic search to map the mission intent against
// the codebase: TF-IDF content search, symbol index, and dependency graph.
//
// Instead of matching keywords against filenames, it:
//  1. Searches file content semantically (TF-IDF) for intent-related concepts
//  2. Finds symbols (functions, types, classes) related to the intent
//  3. Traces dependency chains to find consumers and producers
//  4. Expands the result set via impact analysis (dependents of relevant files)
func NewResearchHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

		mc := buildMissionContext(deps, m)
		researchPrompt := prompts.BuildMissionResearchPrompt(mc)

		relevantFiles := make(map[string]float64) // file -> relevance score
		var artifacts = map[string]string{"prompt": researchPrompt}

		if deps.RepoRoot != "" {
			exts := []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java",
				".css", ".scss", ".html", ".vue", ".svelte", ".yaml", ".yml", ".json"}

			// Signal 1: TF-IDF content search — find files whose content relates to the intent
			tfidfIdx := deps.TFIDFIndex
			if tfidfIdx == nil {
				tfidfIdx, _ = tfidf.Build(deps.RepoRoot, exts)
			}
			if tfidfIdx != nil {
				// Search with the full intent + each criterion as separate queries
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
				// Also look up exact symbol names from criteria
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
			// Files that import or are imported by relevant files are also relevant
			graph := deps.DepGraph
			if graph == nil {
				graph, _ = depgraph.Build(deps.RepoRoot, exts)
			}
			if graph != nil {
				// Get the top files from signals 1+2
				topFiles := topN(relevantFiles, 15)
				for _, f := range topFiles {
					// Add consumers (dependents) — what uses this file
					for _, dep := range graph.Dependents(f) {
						relevantFiles[dep] += 0.2
					}
					// Add producers (dependencies) — what this file uses
					for _, dep := range graph.Dependencies(f) {
						relevantFiles[dep] += 0.15
					}
				}

				// Build a graph summary artifact for downstream phases
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
	var items []scored
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
	var result []string
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
	count := 0
	if deps.RepoRoot != "" {
		count++ // TF-IDF always available with RepoRoot
		count++ // Symbol index always available
		count++ // Dep graph always available
	}
	return count
}

// NewPlanHandler creates a handler for the Planning phase.
// It generates a structured plan based on mission criteria and builds
// the planning prompt that includes research context and gap history.
func NewPlanHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

		mc := buildMissionContext(deps, m)
		planPrompt := prompts.BuildMissionPlanPrompt(mc)

		var planItems []string
		for _, c := range m.Criteria {
			if !c.Satisfied {
				planItems = append(planItems, fmt.Sprintf("Implement: %s", c.Description))
			}
		}

		summary := fmt.Sprintf("Plan: %d tasks for %d unsatisfied criteria",
			len(planItems), len(planItems))

		return &PhaseResult{
			Phase:   PhasePlanning,
			Summary: summary,
			Artifacts: map[string]string{
				"plan":   strings.Join(planItems, "\n"),
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

		// Build the full mission-aware execute prompt
		executePrompt := prompts.BuildMissionExecutePrompt(mc, taskDesc, verification)

		var filesChanged []string
		if deps.ExecuteFn != nil {
			var err error
			filesChanged, err = deps.ExecuteFn(ctx, m, executePrompt, taskDesc)
			if err != nil {
				return nil, fmt.Errorf("execute: %w", err)
			}
		}

		if deps.Metrics != nil {
			deps.Metrics.RecordPhaseTransition("executing", time.Since(start))
		}

		return &PhaseResult{
			Phase:        PhaseExecuting,
			Summary:      fmt.Sprintf("Executed %d work items, %d files changed", len(taskParts), len(filesChanged)),
			FilesChanged: filesChanged,
			Artifacts: map[string]string{
				"prompt": executePrompt,
			},
			Duration: time.Since(start),
			Agent:    "execute-handler",
		}, nil
	}
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
		if deps.Validator != nil {
			var files []convergence.FileInput
			filepath.WalkDir(deps.RepoRoot, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
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

			var criteriaDescs []string
			for _, c := range m.Criteria {
				if !c.Satisfied {
					criteriaDescs = append(criteriaDescs, c.Description)
				}
			}

			var report *convergence.Report
			if len(criteriaDescs) > 0 {
				report = deps.Validator.ValidateWithCriteria(m.ID, files, criteriaDescs)
			} else {
				report = deps.Validator.Validate(m.ID, files)
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

			summaryParts = append(summaryParts, fmt.Sprintf("static: score=%.2f, %d findings (%d blocking)",
				report.Score, len(report.Findings), report.BlockingCount()))
		}

		// --- Layer 3: Adversarial LLM validation ---
		if deps.ValidateFn != nil {
			mc := buildMissionContext(deps, m)
			validatePrompt := prompts.BuildMissionValidatePrompt(mc)

			findings, err := deps.ValidateFn(ctx, m, validatePrompt)
			if err != nil {
				summaryParts = append(summaryParts, fmt.Sprintf("adversarial: error (%v)", err))
			} else if findings != "" {
				// Store the raw LLM findings as a gap for the convergence loop to act on
				gapID := fmt.Sprintf("llm-val-%s-%d", m.ID, time.Now().UnixNano())
				deps.Store.AddGap(&Gap{
					ID:          gapID,
					MissionID:   m.ID,
					Category:    "adversarial-validation",
					Severity:    "blocking",
					Description: truncateOutput(findings, 1000),
					Suggestion:  "Address the findings from adversarial LLM validation",
				})
				allGapCount++
				blockingCount++
				summaryParts = append(summaryParts, "adversarial: findings reported")
			} else {
				summaryParts = append(summaryParts, "adversarial: no findings")
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

// NewConsensusHandler creates a handler for the Converged phase.
// It builds the full adversarial consensus prompt (with the validation report,
// anti-rationalization protocol, and challenge questions) and passes it to
// each consensus model. Models must try to DISPROVE completeness.
func NewConsensusHandler(deps HandlerDeps, models []string) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

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
			// No consensus function — auto-approve
			for _, model := range models {
				deps.Store.RecordConsensus(&ConsensusRecord{
					MissionID: m.ID,
					Model:     model,
					Verdict:   "complete",
					Reasoning: "auto-approved (no consensus function)",
				})
			}
			return &PhaseResult{
				Phase:   PhaseConverged,
				Summary: fmt.Sprintf("Auto-approved by %d models", len(models)),
			}, nil
		}

		var verdicts []string
		for _, model := range models {
			// Build the adversarial consensus prompt with the validation report
			consensusPrompt := prompts.BuildMissionConsensusPrompt(mc, validationReport)

			verdict, reasoning, gapsFound, err := deps.ConsensusModelFn(ctx, m.ID, model, consensusPrompt)
			if err != nil {
				return nil, fmt.Errorf("consensus from %s: %w", model, err)
			}

			deps.Store.RecordConsensus(&ConsensusRecord{
				MissionID: m.ID,
				Model:     model,
				Verdict:   verdict,
				Reasoning: reasoning,
				GapsFound: gapsFound,
			})

			if deps.Metrics != nil {
				deps.Metrics.RecordConsensusVote(verdict == "complete")
			}

			verdicts = append(verdicts, fmt.Sprintf("%s: %s", model, verdict))
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

// extractMissionKeywords extracts searchable keywords from intent text.
func extractMissionKeywords(intent string) []string {
	words := strings.Fields(strings.ToLower(intent))
	seen := make(map[string]bool)
	var keywords []string
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
