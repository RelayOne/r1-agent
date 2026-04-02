// Package taskstate implements the V1 shipping bar anti-deception contract.
//
// Core rule: The model is allowed to propose. Only Stoke is allowed to decide.
//
// This package owns all task state transitions. No external code can
// set a task to "done" or "committed" directly. The only path to
// committed is through all gates passing.
package taskstate

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Phase is the lifecycle phase of a task. Transitions are enforced.
type Phase int

const (
	Pending    Phase = iota // not yet started
	Claimed                 // harness has dispatched to an agent
	Executed                // agent returned (model CLAIMS done)
	Verified                // harness verified (build+test+lint pass)
	Reviewed                // cross-model review passed
	Committed               // merged to main (terminal success)
	Failed                  // terminal failure (escalated to human)
	Blocked                 // dependency failed
	HumanNeeded             // requires operator decision
	UserSkipped             // operator explicitly skipped (terminal)
)

func (p Phase) String() string {
	switch p {
	case Pending:     return "pending"
	case Claimed:     return "claimed"
	case Executed:    return "executed"
	case Verified:    return "verified"
	case Reviewed:    return "reviewed"
	case Committed:   return "committed"
	case Failed:      return "failed"
	case Blocked:     return "blocked"
	case HumanNeeded: return "human_decision_needed"
	case UserSkipped: return "user_skipped"
	default:          return "unknown"
	}
}

// validTransitions defines the ONLY allowed state transitions.
// The model cannot skip from Claimed to Committed.
// Only the operator can trigger UserSkipped (via HumanNeeded).
var validTransitions = map[Phase][]Phase{
	Pending:     {Claimed, Blocked},
	Claimed:     {Executed, Failed},
	Executed:    {Verified, Failed},
	Verified:    {Reviewed, Failed},
	Reviewed:    {Committed},                            // the ONLY path to committed
	Failed:      {HumanNeeded},                          // escalate to operator
	HumanNeeded: {UserSkipped, Claimed, Blocked},        // operator decides: skip, retry, or block
	Blocked:     {HumanNeeded, Pending},               // operator can escalate or unblock
	// Committed, UserSkipped are terminal
}

// Evidence is the artifact proof for an attempt. Every attempt MUST have evidence.
// An attempt without evidence is invalid and cannot advance the state machine.
type Evidence struct {
	BuildOutput    string   `json:"build_output"`
	BuildPass      bool     `json:"build_pass"`
	TestOutput     string   `json:"test_output"`
	TestPass       bool     `json:"test_pass"`
	LintOutput     string   `json:"lint_output"`
	LintPass       bool     `json:"lint_pass"`
	DiffSummary    string   `json:"diff_summary"`
	ReviewEngine   string   `json:"review_engine"`
	ReviewOutput   string   `json:"review_output"`
	ReviewPass     bool     `json:"review_pass"`
	ScopeClean     bool     `json:"scope_clean"`
	ProtectedClean bool     `json:"protected_clean"`
	Warnings       []string `json:"warnings,omitempty"` // non-fatal issues (e.g. gitignored files)
}

// AllGatesPass returns true only if every verification gate passed
// and no warnings are present (warnings indicate verified != merged divergence).
func (e Evidence) AllGatesPass() bool {
	return e.BuildPass && e.TestPass && e.LintPass &&
		e.ScopeClean && e.ProtectedClean && e.ReviewPass &&
		len(e.Warnings) == 0
}

// FailedGates returns human-readable list of which gates failed.
func (e Evidence) FailedGates() []string {
	var failed []string
	if !e.BuildPass      { failed = append(failed, "build") }
	if !e.TestPass       { failed = append(failed, "tests") }
	if !e.LintPass       { failed = append(failed, "lint") }
	if !e.ScopeClean     { failed = append(failed, "scope") }
	if !e.ProtectedClean { failed = append(failed, "protected-files") }
	if !e.ReviewPass     { failed = append(failed, "cross-model-review") }
	if len(e.Warnings) > 0 { failed = append(failed, fmt.Sprintf("warnings(%d)", len(e.Warnings))) }
	return failed
}

// GateResult is the outcome of one verification gate.
type GateResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Output string `json:"output,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Attempt is one execution attempt with mandatory evidence.
type Attempt struct {
	Number    int           `json:"number"`
	StartedAt time.Time    `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	CostUSD   float64      `json:"cost_usd"`
	Engine    string       `json:"engine"`     // which model executed

	// UNTRUSTED: model-produced output. Stoke never trusts these.
	ProposedSummary  string   `json:"proposed_summary"`   // what the model CLAIMS it did
	ProposedBlockers []string `json:"proposed_blockers"`   // what the model CLAIMS is blocking

	// VERIFIED: harness-derived evidence. This is ground truth.
	Evidence         Evidence       `json:"evidence"`
	FailureCodes     []FailureCode  `json:"failure_codes,omitempty"`
	FailureDetails   []FailureDetail `json:"failure_details,omitempty"`
	Fingerprint      string         `json:"fingerprint,omitempty"` // for dedup
}

// TaskState is the harness-owned state of one task.
// External code cannot modify Phase directly.
type TaskState struct {
	mu       sync.Mutex
	TaskID   string    `json:"task_id"`
	phase    Phase
	Attempts []Attempt `json:"attempts"`
	Error    string    `json:"error,omitempty"`

	// Audit trail: every transition is logged
	Transitions []Transition `json:"transitions"`
}

// Transition is one state change in the audit trail.
type Transition struct {
	From      Phase     `json:"from"`
	To        Phase     `json:"to"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

// NewTaskState creates a task in Pending state.
func NewTaskState(taskID string) *TaskState {
	return &TaskState{
		TaskID: taskID,
		phase:  Pending,
	}
}

// Phase returns the current phase (read-only).
func (ts *TaskState) Phase() Phase {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.phase
}

// Advance transitions the task to a new phase.
// Returns an error if the transition is not valid.
// This is the ONLY way to change task state.
func (ts *TaskState) Advance(to Phase, reason string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	allowed := validTransitions[ts.phase]
	valid := false
	for _, a := range allowed {
		if a == to { valid = true; break }
	}
	if !valid {
		return fmt.Errorf("invalid transition: %s -> %s (allowed: %v)", ts.phase, to, allowed)
	}

	ts.Transitions = append(ts.Transitions, Transition{
		From: ts.phase, To: to, Timestamp: time.Now(), Reason: reason,
	})
	ts.phase = to
	return nil
}

// RecordAttempt adds an attempt with evidence. Evidence is mandatory.
// Fingerprint is computed automatically from failure codes.
func (ts *TaskState) RecordAttempt(a Attempt) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Evidence is mandatory -- an attempt without evidence is invalid
	if a.Evidence.DiffSummary == "" && !a.Evidence.BuildPass && !a.Evidence.TestPass && !a.Evidence.LintPass {
		return fmt.Errorf("attempt %d has no evidence (empty Evidence struct)", a.Number)
	}

	// Compute fingerprint for dedup
	if len(a.FailureCodes) > 0 {
		primaryFile := ""
		if len(a.FailureDetails) > 0 {
			primaryFile = a.FailureDetails[0].File
		}
		a.Fingerprint = Fingerprint(a.FailureCodes, primaryFile)
	}

	ts.Attempts = append(ts.Attempts, a)
	return nil
}

// LatestAttempt returns the most recent attempt, or nil.
func (ts *TaskState) LatestAttempt() *Attempt {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.Attempts) == 0 { return nil }
	a := ts.Attempts[len(ts.Attempts)-1]
	return &a
}

// CanCommit returns true only if the task has passed through all gates
// in the correct order: Claimed -> Executed -> Verified -> Reviewed.
// This is the V1 shipping bar: no task can become committed without passing gates.
// CanCommit returns true only if:
// 1. State machine is in Reviewed phase
// 2. At least one attempt exists
// 3. Latest attempt has zero failure codes
// 4. Latest attempt evidence passes all gates
// This is the V1 shipping bar: evidence-based, not phase-based.
func (ts *TaskState) CanCommit() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.phase != Reviewed {
		return false
	}
	if len(ts.Attempts) == 0 {
		return false
	}
	latest := ts.Attempts[len(ts.Attempts)-1]
	if len(latest.FailureCodes) > 0 {
		return false
	}
	return latest.Evidence.AllGatesPass()
}

// IsTerminal returns true if the task is in a terminal state.
func (ts *TaskState) IsTerminal() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.phase == Committed || ts.phase == Failed ||
		ts.phase == Blocked || ts.phase == UserSkipped
}

// ClaimedVsVerified returns a human-readable comparison of what the model
// claimed vs what the harness verified. This is the anti-deception display.
//
// "Show the lie."
func (ts *TaskState) ClaimedVsVerified() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if len(ts.Attempts) == 0 {
		return "No attempts recorded."
	}

	latest := ts.Attempts[len(ts.Attempts)-1]
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Task: %s\n", ts.TaskID))
	b.WriteString(fmt.Sprintf("State: %s\n\n", ts.phase))

	// What the model claimed (untrusted)
	b.WriteString("Agent claimed:\n")
	if latest.ProposedSummary != "" {
		b.WriteString(fmt.Sprintf("  \"%s\"\n", latest.ProposedSummary))
	} else {
		b.WriteString("  (no summary provided)\n")
	}
	b.WriteString("\n")

	// What the harness verified (ground truth)
	ev := latest.Evidence
	b.WriteString("Verified:\n")
	b.WriteString(fmt.Sprintf("  diff:              %s\n", boolStatus(ev.DiffSummary != "")))
	b.WriteString(fmt.Sprintf("  scope clean:       %s\n", boolStatus(ev.ScopeClean)))
	b.WriteString(fmt.Sprintf("  protected clean:   %s\n", boolStatus(ev.ProtectedClean)))
	b.WriteString(fmt.Sprintf("  build:             %s\n", passStatus(ev.BuildPass)))
	b.WriteString(fmt.Sprintf("  tests:             %s\n", passStatus(ev.TestPass)))
	b.WriteString(fmt.Sprintf("  lint:              %s\n", passStatus(ev.LintPass)))
	b.WriteString(fmt.Sprintf("  review (%s):    %s\n", ev.ReviewEngine, passStatus(ev.ReviewPass)))

	if len(latest.FailureCodes) > 0 {
		b.WriteString("\nFailure codes:\n")
		for _, fc := range latest.FailureCodes {
			b.WriteString(fmt.Sprintf("  - %s\n", fc))
		}
	}

	if len(latest.FailureDetails) > 0 {
		b.WriteString("\nDetails:\n")
		for _, fd := range latest.FailureDetails {
			b.WriteString(fmt.Sprintf("  [%s] %s", fd.Code, fd.Message))
			if fd.File != "" {
				b.WriteString(fmt.Sprintf(" (%s:%d)", fd.File, fd.Line))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

func boolStatus(v bool) string {
	if v { return "yes" }
	return "no"
}

func passStatus(v bool) string {
	if v { return "pass" }
	return "FAIL"
}

// --- Plan-level state tracking ---

// PlanState tracks all tasks in a plan. Harness-owned.
type PlanState struct {
	mu    sync.Mutex
	tasks map[string]*TaskState
}

// NewPlanState creates plan state for a set of task IDs.
func NewPlanState(taskIDs []string) *PlanState {
	ps := &PlanState{tasks: make(map[string]*TaskState)}
	for _, id := range taskIDs {
		ps.tasks[id] = NewTaskState(id)
	}
	return ps
}

// Get returns the state for a task.
func (ps *PlanState) Get(taskID string) *TaskState {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.tasks[taskID]
}

// Summary returns counts by phase.
func (ps *PlanState) Summary() map[Phase]int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	counts := map[Phase]int{}
	for _, ts := range ps.tasks {
		counts[ts.Phase()]++
	}
	return counts
}

// AllTerminal returns true if every task is in a terminal state.
func (ps *PlanState) AllTerminal() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, ts := range ps.tasks {
		if !ts.IsTerminal() { return false }
	}
	return true
}
