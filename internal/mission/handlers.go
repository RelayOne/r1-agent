// Phase handlers bridge the mission lifecycle to concrete execution.
//
// Each handler implements PhaseHandler and delegates to appropriate
// subsystems. Handlers are designed as composable building blocks:
//
//   - ResearchHandler: Gathers information needed for planning
//   - PlanHandler: Creates a structured implementation plan
//   - ExecuteHandler: Runs the implementation via the workflow engine
//   - ValidateHandler: Runs adversarial convergence validation
//   - ConsensusHandler: Gathers multi-model completion votes
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

	"github.com/ericmacdougall/stoke/internal/convergence"
)

// HandlerDeps bundles the dependencies that phase handlers need.
// This avoids circular imports by letting the orchestration layer
// inject concrete implementations.
type HandlerDeps struct {
	// Store is the mission persistence layer.
	Store *Store

	// Validator is the convergence rule engine.
	Validator *convergence.Validator

	// RepoRoot is the git repository root for file scanning.
	RepoRoot string

	// Metrics tracks operational statistics.
	Metrics *Metrics

	// ExecuteFn is called by the execute handler to run a task through
	// the workflow engine. It receives the mission and task description,
	// and returns the files changed and any error.
	// This is the integration point with the existing workflow.Engine.
	ExecuteFn func(ctx context.Context, m *Mission, taskDesc string) (filesChanged []string, err error)

	// ConsensusModelFn is called to get a model's verdict on mission completion.
	// It receives the mission ID and returns the verdict and reasoning.
	ConsensusModelFn func(ctx context.Context, missionID, model string) (verdict, reasoning string, gapsFound []string, err error)
}

// NewResearchHandler creates a handler for the Researching phase.
// It searches the codebase for relevant files and records research entries.
func NewResearchHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

		// Research by scanning the repo for files related to the mission intent
		var findings []string
		keywords := extractMissionKeywords(m.Intent)

		if deps.RepoRoot != "" {
			filepath.WalkDir(deps.RepoRoot, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				// Skip hidden dirs, vendor, node_modules
				rel, _ := filepath.Rel(deps.RepoRoot, path)
				if strings.HasPrefix(rel, ".") || strings.Contains(rel, "vendor/") ||
					strings.Contains(rel, "node_modules/") {
					return nil
				}
				// Check if filename contains any keyword
				name := strings.ToLower(d.Name())
				for _, kw := range keywords {
					if strings.Contains(name, kw) {
						findings = append(findings, rel)
						break
					}
				}
				return nil
			})
		}

		if deps.Metrics != nil {
			deps.Metrics.RecordResearchQuery()
		}

		summary := fmt.Sprintf("Found %d relevant files for mission intent", len(findings))
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
			Duration:     time.Since(start),
			Agent:        "research-handler",
		}, nil
	}
}

// NewPlanHandler creates a handler for the Planning phase.
// It generates a structured plan based on mission criteria.
func NewPlanHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

		// Build a plan from criteria
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
				"plan": strings.Join(planItems, "\n"),
			},
			Duration: time.Since(start),
			Agent:    "plan-handler",
		}, nil
	}
}

// NewExecuteHandler creates a handler for the Executing phase.
// It delegates to the ExecuteFn to run tasks through the workflow engine.
func NewExecuteHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

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

		var filesChanged []string
		if deps.ExecuteFn != nil {
			var err error
			filesChanged, err = deps.ExecuteFn(ctx, m, taskDesc)
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
			Duration:     time.Since(start),
			Agent:        "execute-handler",
		}, nil
	}
}

// NewValidateHandler creates a handler for the Validating phase.
// It runs the convergence validator against modified files and maps
// findings to mission gaps.
func NewValidateHandler(deps HandlerDeps) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

		if deps.Validator == nil || deps.RepoRoot == "" {
			return &PhaseResult{
				Phase:   PhaseValidating,
				Summary: "Validation skipped (no validator or repo)",
			}, nil
		}

		// Collect files to validate — all .go, .ts, .py, etc. in repo
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

		// Run validation with criteria
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

		// Map findings to gaps
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

			if deps.Metrics != nil {
				deps.Metrics.RecordGapFound(f.Severity == convergence.SevBlocking)
			}
		}

		summary := fmt.Sprintf("Validated %d files: score=%.2f, %d findings (%d blocking)",
			len(files), report.Score, len(report.Findings), report.BlockingCount())

		if deps.Metrics != nil {
			deps.Metrics.RecordPhaseTransition("validating", time.Since(start))
		}

		return &PhaseResult{
			Phase:   PhaseValidating,
			Summary: summary,
			Artifacts: map[string]string{
				"score":    fmt.Sprintf("%.2f", report.Score),
				"findings": fmt.Sprintf("%d", len(report.Findings)),
			},
			Duration: time.Since(start),
			Agent:    "validate-handler",
		}, nil
	}
}

// NewConsensusHandler creates a handler for the Converged phase.
// It gathers completion votes from multiple models.
func NewConsensusHandler(deps HandlerDeps, models []string) PhaseHandler {
	return func(ctx context.Context, m *Mission) (*PhaseResult, error) {
		start := time.Now()

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
			verdict, reasoning, gapsFound, err := deps.ConsensusModelFn(ctx, m.ID, model)
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
			Duration: time.Since(start),
			Agent:    "consensus-handler",
		}, nil
	}
}

// extractMissionKeywords extracts searchable keywords from intent text.
func extractMissionKeywords(intent string) []string {
	// Simple keyword extraction: split on spaces, filter short words,
	// lowercase, deduplicate
	words := strings.Fields(strings.ToLower(intent))
	seen := make(map[string]bool)
	var keywords []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()[]{}") // strip punctuation
		if len(w) < 3 || seen[w] {
			continue
		}
		// Skip common stop words
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
