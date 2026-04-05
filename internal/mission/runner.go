// Package mission runner drives missions through the convergence loop.
//
// The Runner orchestrates the full mission lifecycle:
//
//	Created → Researching → Planning → Executing → Validating → Converged → Completed
//
// At each phase, it delegates to pluggable phase handlers. The convergence loop
// (Validating ↔ Executing) re-executes until all acceptance criteria are met,
// all gaps are resolved, and two-model consensus confirms completion.
//
// The runner is designed for agentic remote control — it can be driven by
// an external orchestrator, CLI command, or MCP endpoint. All state is
// persisted in the mission store, so the runner can crash and resume.
package mission

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// PhaseHandler is a pluggable function that executes a mission phase.
// Handlers receive the current mission, do work, and return:
//   - result: structured outcome of the phase (passed to the next phase)
//   - err: if non-nil, the mission transitions to Failed
//
// Handlers must be idempotent — they may be called multiple times if the
// runner resumes after a crash.
type PhaseHandler func(ctx context.Context, m *Mission) (*PhaseResult, error)

// PhaseResult captures the outcome of executing a single phase.
type PhaseResult struct {
	Phase        Phase             `json:"phase"`          // which phase ran
	Summary      string            `json:"summary"`        // human-readable outcome
	FilesChanged []string          `json:"files_changed"`  // files modified during this phase
	Artifacts    map[string]string `json:"artifacts"`      // key-value artifacts (plan text, test output, etc.)
	Duration     time.Duration     `json:"duration"`       // wall-clock time
	Agent        string            `json:"agent"`          // which model/agent executed
}

// RunnerConfig controls convergence loop behavior.
type RunnerConfig struct {
	// MaxConvergenceLoops limits how many times the Validating→Executing cycle
	// can repeat before the runner gives up and fails the mission.
	// Default: 5.
	MaxConvergenceLoops int `json:"max_convergence_loops"`

	// RequiredConsensus is the number of distinct models that must vote
	// "complete" for the mission to advance from Converged to Completed.
	// Default: 2.
	RequiredConsensus int `json:"required_consensus"`

	// MaxPhaseRetries controls how many times a single phase can fail
	// before the mission transitions to Failed.
	// Default: 3.
	MaxPhaseRetries int `json:"max_phase_retries"`

	// PhaseTimeout is the maximum duration for a single phase execution.
	// Zero means no timeout.
	PhaseTimeout time.Duration `json:"phase_timeout"`

	// OnPhaseComplete is called after each phase completes successfully.
	// Used for event streaming, TUI updates, metrics, etc.
	OnPhaseComplete func(missionID string, result *PhaseResult)

	// OnConvergenceLoop is called each time the Validating→Executing loop fires.
	// Receives the loop iteration (1-indexed) and the gaps that triggered it.
	OnConvergenceLoop func(missionID string, iteration int, gapCount int)

	// OnMissionComplete is called when a mission reaches Completed or Failed.
	OnMissionComplete func(missionID string, phase Phase, summary string)
}

// DefaultRunnerConfig returns sensible defaults for the convergence loop.
func DefaultRunnerConfig() RunnerConfig {
	return RunnerConfig{
		MaxConvergenceLoops: 10,
		RequiredConsensus:   2,
		MaxPhaseRetries:     3,
	}
}

// Runner drives missions through the convergence lifecycle.
// It persists all state via the mission Store, so it can resume after crashes.
type Runner struct {
	store    *Store
	config   RunnerConfig
	handlers map[Phase]PhaseHandler
}

// NewRunner creates a mission runner backed by the given store.
func NewRunner(store *Store, config RunnerConfig) *Runner {
	if store == nil {
		panic("mission.NewRunner: store must not be nil")
	}
	if config.MaxConvergenceLoops <= 0 {
		config.MaxConvergenceLoops = 10
	}
	if config.RequiredConsensus <= 0 {
		config.RequiredConsensus = 2
	}
	if config.MaxPhaseRetries <= 0 {
		config.MaxPhaseRetries = 3
	}
	return &Runner{
		store:    store,
		config:   config,
		handlers: make(map[Phase]PhaseHandler),
	}
}

// RegisterHandler sets the handler for a phase. Panics if handler is nil.
func (r *Runner) RegisterHandler(phase Phase, handler PhaseHandler) {
	if handler == nil {
		panic(fmt.Sprintf("mission.Runner: nil handler for phase %q", phase))
	}
	r.handlers[phase] = handler
}

// Run drives a mission to completion or failure. It reads the mission's
// current phase, executes the appropriate handler, advances the state
// machine, and loops until a terminal state is reached.
//
// The convergence loop (Validating ↔ Executing) re-executes until:
//   - All acceptance criteria are satisfied
//   - No blocking gaps remain
//   - Two-model consensus confirms completion
//
// Run is safe to call on a mission in any non-terminal phase. It resumes
// from wherever the mission left off.
func (r *Runner) Run(ctx context.Context, missionID string) (*RunSummary, error) {
	start := time.Now()
	summary := &RunSummary{
		MissionID: missionID,
		Phases:    make([]PhaseResult, 0),
	}

	m, err := r.store.Get(missionID)
	if err != nil {
		return nil, fmt.Errorf("get mission %q: %w", missionID, err)
	}
	if m == nil {
		return nil, fmt.Errorf("mission %q not found", missionID)
	}

	// Terminal check
	if m.Phase == PhaseCompleted || m.Phase == PhaseFailed {
		summary.FinalPhase = m.Phase
		summary.TotalDuration = time.Since(start)
		return summary, nil
	}

	convergenceLoops := 0

	for {
		select {
		case <-ctx.Done():
			return summary, ctx.Err()
		default:
		}

		// Re-read mission to get latest phase (may have been advanced by handler)
		m, err = r.store.Get(missionID)
		if err != nil {
			return summary, fmt.Errorf("get mission: %w", err)
		}

		// Terminal states end the run
		if m.Phase == PhaseCompleted || m.Phase == PhaseFailed {
			summary.FinalPhase = m.Phase
			summary.TotalDuration = time.Since(start)
			if r.config.OnMissionComplete != nil {
				r.config.OnMissionComplete(missionID, m.Phase, summary.Summary())
			}
			return summary, nil
		}

		// Paused missions return without error
		if m.Phase == PhasePaused {
			summary.FinalPhase = m.Phase
			summary.TotalDuration = time.Since(start)
			return summary, nil
		}

		// Determine next action based on current phase
		nextPhase, err := r.executePhase(ctx, m, summary)
		if err != nil {
			// Phase execution failed — transition to Failed
			log.Printf("[mission] phase %s failed for %s: %v", m.Phase, missionID, err)
			advErr := r.store.Advance(missionID, PhaseFailed,
				fmt.Sprintf("phase %s error: %s", m.Phase, err.Error()), "runner")
			if advErr != nil {
				log.Printf("[mission] failed to advance to failed: %v", advErr)
			}
			summary.FinalPhase = PhaseFailed
			summary.TotalDuration = time.Since(start)
			if r.config.OnMissionComplete != nil {
				r.config.OnMissionComplete(missionID, PhaseFailed, err.Error())
			}
			return summary, err
		}

		// Special handling for convergence loop
		if m.Phase == PhaseValidating && nextPhase == PhaseExecuting {
			convergenceLoops++
			if convergenceLoops > r.config.MaxConvergenceLoops {
				err := fmt.Errorf("convergence loop exhausted after %d iterations", convergenceLoops-1)
				r.store.Advance(missionID, PhaseFailed, err.Error(), "runner")
				summary.FinalPhase = PhaseFailed
				summary.TotalDuration = time.Since(start)
				return summary, err
			}

			if convergenceLoops == 5 {
				log.Printf("[mission] WARNING: %s has reached 5 convergence loops — work may be stuck, %d loops remaining before failure",
					missionID, r.config.MaxConvergenceLoops-5)
			}

			gaps, _ := r.store.OpenGaps(missionID)
			if r.config.OnConvergenceLoop != nil {
				r.config.OnConvergenceLoop(missionID, convergenceLoops, len(gaps))
			}
			log.Printf("[mission] convergence loop %d/%d for %s (%d open gaps)",
				convergenceLoops, r.config.MaxConvergenceLoops, missionID, len(gaps))
		}

		// Track convergence loop count in summary
		summary.ConvergenceLoops = convergenceLoops

		// Advance the state machine
		reason := fmt.Sprintf("phase %s completed", m.Phase)
		if err := r.store.Advance(missionID, nextPhase, reason, "runner"); err != nil {
			return summary, fmt.Errorf("advance %s → %s: %w", m.Phase, nextPhase, err)
		}
	}
}

// executePhase runs the handler for the current phase and determines the next phase.
func (r *Runner) executePhase(ctx context.Context, m *Mission, summary *RunSummary) (Phase, error) {
	switch m.Phase {
	case PhaseCreated:
		return r.runHandler(ctx, m, PhaseResearching, summary)

	case PhaseResearching:
		return r.runHandler(ctx, m, PhasePlanning, summary)

	case PhasePlanning:
		return r.runHandler(ctx, m, PhaseExecuting, summary)

	case PhaseExecuting:
		return r.runHandler(ctx, m, PhaseValidating, summary)

	case PhaseValidating:
		return r.runValidation(ctx, m, summary)

	case PhaseConverged:
		return r.runConsensus(ctx, m, summary)

	default:
		return "", fmt.Errorf("unexpected phase %q", m.Phase)
	}
}

// runHandler executes a registered handler for the current phase.
// If no handler is registered, it auto-advances (useful for phases that
// don't need external work, like skipping research).
func (r *Runner) runHandler(ctx context.Context, m *Mission, nextPhase Phase, summary *RunSummary) (Phase, error) {
	handler, ok := r.handlers[m.Phase]
	if !ok {
		// No handler registered — auto-advance
		log.Printf("[mission] no handler for phase %s, auto-advancing to %s", m.Phase, nextPhase)
		return nextPhase, nil
	}

	var phaseCtx context.Context
	var cancel context.CancelFunc
	if r.config.PhaseTimeout > 0 {
		phaseCtx, cancel = context.WithTimeout(ctx, r.config.PhaseTimeout)
	} else {
		phaseCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	start := time.Now()
	result, err := handler(phaseCtx, m)
	if err != nil {
		return "", fmt.Errorf("handler for %s: %w", m.Phase, err)
	}

	if result == nil {
		result = &PhaseResult{Phase: m.Phase}
	}
	result.Phase = m.Phase
	result.Duration = time.Since(start)

	summary.Phases = append(summary.Phases, *result)

	// Persist files changed in mission metadata for crash recovery
	// and for downstream phases (validation needs to know what changed)
	if len(result.FilesChanged) > 0 {
		m, err := r.store.Get(m.ID)
		if err == nil {
			if m.Metadata == nil {
				m.Metadata = make(map[string]string)
			}
			m.Metadata["files_changed"] = strings.Join(result.FilesChanged, "\n")
			r.store.Update(m)
		}
	}

	if r.config.OnPhaseComplete != nil {
		r.config.OnPhaseComplete(m.ID, result)
	}

	return nextPhase, nil
}

// runValidation checks convergence status and decides whether to loop back
// to Executing (gaps found) or advance to Converged (all clear).
func (r *Runner) runValidation(ctx context.Context, m *Mission, summary *RunSummary) (Phase, error) {
	start := time.Now()

	// Run the validation handler if registered
	handler, ok := r.handlers[PhaseValidating]
	if ok {
		var phaseCtx context.Context
		var cancel context.CancelFunc
		if r.config.PhaseTimeout > 0 {
			phaseCtx, cancel = context.WithTimeout(ctx, r.config.PhaseTimeout)
		} else {
			phaseCtx, cancel = context.WithCancel(ctx)
		}
		defer cancel()

		result, err := handler(phaseCtx, m)
		if err != nil {
			return "", fmt.Errorf("validation handler: %w", err)
		}
		if result != nil {
			result.Phase = PhaseValidating
			result.Duration = time.Since(start)
			summary.Phases = append(summary.Phases, *result)
		}
	}

	// Check convergence status
	status, err := r.store.GetConvergenceStatus(m.ID, r.config.RequiredConsensus)
	if err != nil {
		return "", fmt.Errorf("get convergence status: %w", err)
	}

	// If all criteria met and no blocking gaps → Converged
	if status.IsConverged {
		log.Printf("[mission] %s converged: %d/%d criteria, 0 blocking gaps",
			m.ID, status.SatisfiedCriteria, status.TotalCriteria)

		if r.config.OnPhaseComplete != nil {
			r.config.OnPhaseComplete(m.ID, &PhaseResult{
				Phase:   PhaseValidating,
				Summary: fmt.Sprintf("Converged: %d/%d criteria satisfied, 0 blocking gaps", status.SatisfiedCriteria, status.TotalCriteria),
			})
		}
		return PhaseConverged, nil
	}

	// Gaps remain — loop back to Executing
	log.Printf("[mission] %s not converged: %d/%d criteria, %d blocking gaps, %d total gaps",
		m.ID, status.SatisfiedCriteria, status.TotalCriteria,
		status.BlockingGapCount, status.OpenGapCount)

	return PhaseExecuting, nil
}

// runConsensus checks whether two-model consensus has been reached.
// If consensus exists, advance to Completed. If not, either run the
// consensus handler to gather votes or loop back to Executing if
// consensus was rejected.
func (r *Runner) runConsensus(ctx context.Context, m *Mission, summary *RunSummary) (Phase, error) {
	start := time.Now()

	// Run consensus handler if registered (gathers model votes)
	handler, ok := r.handlers[PhaseConverged]
	if ok {
		var phaseCtx context.Context
		var cancel context.CancelFunc
		if r.config.PhaseTimeout > 0 {
			phaseCtx, cancel = context.WithTimeout(ctx, r.config.PhaseTimeout)
		} else {
			phaseCtx, cancel = context.WithCancel(ctx)
		}
		defer cancel()

		result, err := handler(phaseCtx, m)
		if err != nil {
			return "", fmt.Errorf("consensus handler: %w", err)
		}
		if result != nil {
			result.Phase = PhaseConverged
			result.Duration = time.Since(start)
			summary.Phases = append(summary.Phases, *result)
		}
	}

	// Check consensus
	hasConsensus, err := r.store.HasConsensus(m.ID, r.config.RequiredConsensus)
	if err != nil {
		return "", fmt.Errorf("check consensus: %w", err)
	}

	if hasConsensus {
		log.Printf("[mission] %s has %d-model consensus — completing", m.ID, r.config.RequiredConsensus)
		return PhaseCompleted, nil
	}

	// Check if any model explicitly rejected
	records, err := r.store.ConsensusRecords(m.ID)
	if err != nil {
		return "", fmt.Errorf("get consensus records: %w", err)
	}

	// Look at the most recent records for rejection
	for _, rec := range records {
		if rec.Verdict == "reject" || rec.Verdict == "incomplete" {
			log.Printf("[mission] %s consensus rejected by %s: %s", m.ID, rec.Model, rec.Reasoning)
			// Create gaps from rejection — apply scope expansion
			// (nothing is "out of scope" or "pre-existing" — everything is in scope)
			for _, gapDesc := range rec.GapsFound {
				gapDesc = expandScope(gapDesc)
				gapID := fmt.Sprintf("consensus-reject-%s-%d", m.ID, time.Now().UnixNano())
				r.store.AddGap(&Gap{
					ID:          gapID,
					MissionID:   m.ID,
					Category:    "completeness",
					Severity:    "blocking",
					Description: fmt.Sprintf("Consensus rejection by %s: %s", rec.Model, gapDesc),
				})
			}
			return PhaseExecuting, nil
		}
	}

	// No consensus yet but no rejections — need more votes
	// If no handler was registered, we can't gather votes, so auto-complete
	if !ok {
		log.Printf("[mission] %s no consensus handler, auto-completing", m.ID)
		return PhaseCompleted, nil
	}

	// Handler ran but didn't produce consensus — this is an error
	return "", fmt.Errorf("consensus handler did not produce required %d votes", r.config.RequiredConsensus)
}

// Resume re-reads the mission from the store and continues from wherever
// it left off. This is the primary recovery mechanism after crashes.
func (r *Runner) Resume(ctx context.Context, missionID string) (*RunSummary, error) {
	return r.Run(ctx, missionID)
}

// RunSummary captures the overall outcome of driving a mission.
type RunSummary struct {
	MissionID        string        `json:"mission_id"`
	FinalPhase       Phase         `json:"final_phase"`
	Phases           []PhaseResult `json:"phases"`
	ConvergenceLoops int           `json:"convergence_loops"`
	TotalDuration    time.Duration `json:"total_duration"`
}

// Summary returns a human-readable summary of the run.
func (s *RunSummary) Summary() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Mission %s: %s", s.MissionID, s.FinalPhase))
	parts = append(parts, fmt.Sprintf("Phases: %d", len(s.Phases)))
	if s.ConvergenceLoops > 0 {
		parts = append(parts, fmt.Sprintf("Convergence loops: %d", s.ConvergenceLoops))
	}
	parts = append(parts, fmt.Sprintf("Duration: %s", s.TotalDuration.Round(time.Millisecond)))
	return strings.Join(parts, ", ")
}

// IsSuccess returns true if the mission reached Completed.
func (s *RunSummary) IsSuccess() bool {
	return s.FinalPhase == PhaseCompleted
}

// IsFailed returns true if the mission reached Failed.
func (s *RunSummary) IsFailed() bool {
	return s.FinalPhase == PhaseFailed
}

// expandScope removes scope-limiting qualifiers from gap descriptions.
// Per the convergence spec: nothing is "out of scope," pre-existing issues
// must be fixed, and effort is not a valid excuse.
func expandScope(desc string) string {
	lower := strings.ToLower(desc)
	replacements := []struct{ old, repl string }{
		{"out of scope", "in scope (per convergence policy)"},
		{"out-of-scope", "in scope (per convergence policy)"},
		{"pre-existing issue", "issue to fix"},
		{"pre-existing", "existing"},
		{"would require too much", "requires"},
		{"blocked by", "depends on (must resolve)"},
		{"minor issue", "issue"},
		{"nice to have", "required"},
		{"low priority", "blocking"},
	}
	for _, r := range replacements {
		if idx := strings.Index(lower, r.old); idx >= 0 {
			desc = desc[:idx] + r.repl + desc[idx+len(r.old):]
			lower = strings.ToLower(desc)
		}
	}
	return desc
}
