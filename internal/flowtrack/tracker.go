// Package flowtrack implements flow-aware intent tracking.
// Inspired by Windsurf Cascade's shared timeline:
//
// Instead of just watching files, track the SEQUENCE of actions
// to infer user intent. Action patterns reveal intent better than
// any single event:
// - "open file → search → open test" = investigating a bug
// - "create file → edit → edit → run test" = implementing a feature
// - "read file → read file → read file" = exploring/understanding code
//
// The flow tracker maintains a sliding window of actions, detects
// patterns, and infers the current development phase.
package flowtrack

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ActionType classifies a developer action.
type ActionType string

const (
	ActionFileOpen     ActionType = "file_open"
	ActionFileEdit     ActionType = "file_edit"
	ActionFileCreate   ActionType = "file_create"
	ActionFileDelete   ActionType = "file_delete"
	ActionSearch       ActionType = "search"
	ActionRunTest      ActionType = "run_test"
	ActionRunBuild     ActionType = "run_build"
	ActionRunLint      ActionType = "run_lint"
	ActionGitCommit    ActionType = "git_commit"
	ActionGitBranch    ActionType = "git_branch"
	ActionGitDiff      ActionType = "git_diff"
	ActionTerminalCmd  ActionType = "terminal_cmd"
	ActionNavigate     ActionType = "navigate"     // jump-to-definition, go-to-line
	ActionClipboard    ActionType = "clipboard"     // copy/paste
	ActionToolCall     ActionType = "tool_call"
	ActionAgentMessage ActionType = "agent_message"
	ActionError        ActionType = "error"
)

// Phase represents the inferred development phase.
type Phase string

const (
	PhaseExploring     Phase = "exploring"      // reading/navigating code
	PhaseImplementing  Phase = "implementing"    // writing new code
	PhaseDebugging     Phase = "debugging"       // investigating issues
	PhaseTesting       Phase = "testing"         // running/writing tests
	PhaseRefactoring   Phase = "refactoring"     // restructuring code
	PhaseReviewing     Phase = "reviewing"       // reading diffs, reviewing changes
	PhaseIntegrating   Phase = "integrating"     // merging, resolving conflicts
	PhaseUnknown       Phase = "unknown"
)

// Action is a recorded developer action.
type Action struct {
	Type      ActionType `json:"type"`
	Target    string     `json:"target,omitempty"`   // file path, command, query
	Detail    string     `json:"detail,omitempty"`   // additional context
	Timestamp time.Time  `json:"timestamp"`
}

// FlowState is the current inferred state.
type FlowState struct {
	Phase        Phase           `json:"phase"`
	Confidence   float64         `json:"confidence"`
	ActiveFiles  []string        `json:"active_files"`  // recently touched files
	FocusFile    string          `json:"focus_file"`     // current primary file
	ActionCount  int             `json:"action_count"`
	SessionStart time.Time       `json:"session_start"`
	LastAction   time.Time       `json:"last_action"`
	Patterns     []string        `json:"patterns"`       // detected flow patterns
}

// Tracker maintains the action timeline and infers state.
type Tracker struct {
	mu        sync.RWMutex
	actions   []Action
	window    int           // sliding window size
	maxAge    time.Duration // max age for actions
	listeners []func(FlowState)
}

// Config for the tracker.
type Config struct {
	WindowSize int           // number of recent actions to consider (default 50)
	MaxAge     time.Duration // max age for actions (default 30 min)
}

// NewTracker creates a flow tracker.
func NewTracker(cfg Config) *Tracker {
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 50
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 30 * time.Minute
	}
	return &Tracker{
		window: cfg.WindowSize,
		maxAge: cfg.MaxAge,
	}
}

// Record adds an action to the timeline.
func (t *Tracker) Record(action Action) {
	t.mu.Lock()
	if action.Timestamp.IsZero() {
		action.Timestamp = time.Now()
	}
	t.actions = append(t.actions, action)

	// Trim to window size
	if len(t.actions) > t.window*2 {
		t.actions = t.actions[len(t.actions)-t.window:]
	}

	listeners := make([]func(FlowState), len(t.listeners))
	copy(listeners, t.listeners)
	t.mu.Unlock()

	// Notify listeners
	if len(listeners) > 0 {
		state := t.State()
		for _, l := range listeners {
			l(state)
		}
	}
}

// OnStateChange registers a callback for state changes.
func (t *Tracker) OnStateChange(fn func(FlowState)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.listeners = append(t.listeners, fn)
}

// State returns the current inferred flow state.
func (t *Tracker) State() FlowState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	recent := t.recentActions()
	if len(recent) == 0 {
		return FlowState{Phase: PhaseUnknown}
	}

	state := FlowState{
		ActionCount:  len(recent),
		SessionStart: recent[0].Timestamp,
		LastAction:   recent[len(recent)-1].Timestamp,
	}

	// Count action types
	counts := make(map[ActionType]int)
	fileActivity := make(map[string]int)
	for _, a := range recent {
		counts[a.Type]++
		if a.Target != "" {
			fileActivity[a.Target]++
		}
	}

	// Active files (sorted by activity)
	type fileCount struct {
		file  string
		count int
	}
	files := make([]fileCount, 0, len(fileActivity))
	for f, c := range fileActivity {
		files = append(files, fileCount{f, c})
	}
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[j].count > files[i].count {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
	for _, f := range files {
		state.ActiveFiles = append(state.ActiveFiles, f.file)
		if len(state.ActiveFiles) >= 5 {
			break
		}
	}
	if len(state.ActiveFiles) > 0 {
		state.FocusFile = state.ActiveFiles[0]
	}

	// Infer phase
	state.Phase, state.Confidence = inferPhase(counts, recent)

	// Detect patterns
	state.Patterns = detectPatterns(recent)

	return state
}

// RecentActions returns the last N actions within the time window.
func (t *Tracker) RecentActions(n int) []Action {
	t.mu.RLock()
	defer t.mu.RUnlock()

	recent := t.recentActions()
	if n > 0 && len(recent) > n {
		recent = recent[len(recent)-n:]
	}
	result := make([]Action, len(recent))
	copy(result, recent)
	return result
}

// ActionsSince returns actions after a given time.
func (t *Tracker) ActionsSince(since time.Time) []Action {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var result []Action
	for _, a := range t.actions {
		if a.Timestamp.After(since) {
			result = append(result, a)
		}
	}
	return result
}

// ForPrompt generates a context summary for LLM prompts.
func (t *Tracker) ForPrompt() string {
	state := t.State()
	if state.ActionCount == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Current Development Context\n\n")
	fmt.Fprintf(&b, "Phase: %s (%.0f%% confidence)\n", state.Phase, state.Confidence*100)
	if state.FocusFile != "" {
		fmt.Fprintf(&b, "Focus: %s\n", state.FocusFile)
	}
	if len(state.ActiveFiles) > 1 {
		fmt.Fprintf(&b, "Active files: %s\n", strings.Join(state.ActiveFiles, ", "))
	}
	if len(state.Patterns) > 0 {
		fmt.Fprintf(&b, "Patterns: %s\n", strings.Join(state.Patterns, "; "))
	}
	return b.String()
}

// Clear resets the tracker.
func (t *Tracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.actions = nil
}

func (t *Tracker) recentActions() []Action {
	cutoff := time.Now().Add(-t.maxAge)
	start := 0
	for i, a := range t.actions {
		if a.Timestamp.After(cutoff) {
			start = i
			break
		}
	}
	recent := t.actions[start:]
	if len(recent) > t.window {
		recent = recent[len(recent)-t.window:]
	}
	return recent
}

func inferPhase(counts map[ActionType]int, actions []Action) (Phase, float64) {
	total := 0
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return PhaseUnknown, 0
	}

	// Weighted scoring for each phase
	scores := map[Phase]float64{
		PhaseExploring:    float64(counts[ActionFileOpen]+counts[ActionSearch]+counts[ActionNavigate]) / float64(total),
		PhaseImplementing: float64(counts[ActionFileEdit]+counts[ActionFileCreate]) / float64(total),
		PhaseDebugging:    float64(counts[ActionError]+counts[ActionSearch]) / float64(total) * 0.8,
		PhaseTesting:      float64(counts[ActionRunTest]) / float64(total) * 2.0,
		PhaseReviewing:    float64(counts[ActionGitDiff]) / float64(total) * 2.0,
		PhaseIntegrating:  float64(counts[ActionGitCommit]+counts[ActionGitBranch]) / float64(total) * 1.5,
	}

	// Boost debugging if errors followed by searches
	if hasSequence(actions, ActionError, ActionSearch) {
		scores[PhaseDebugging] += 0.3
	}

	// Boost testing if edit-then-test pattern
	if hasSequence(actions, ActionFileEdit, ActionRunTest) {
		scores[PhaseTesting] += 0.2
	}

	// Boost refactoring if many edits across multiple files
	editFiles := make(map[string]bool)
	for _, a := range actions {
		if a.Type == ActionFileEdit {
			editFiles[a.Target] = true
		}
	}
	if len(editFiles) >= 3 {
		scores[PhaseRefactoring] += 0.2
	}

	best := PhaseUnknown
	bestScore := 0.0
	for phase, score := range scores {
		if score > bestScore {
			bestScore = score
			best = phase
		}
	}

	confidence := bestScore
	if confidence > 1.0 {
		confidence = 1.0
	}

	return best, confidence
}

func hasSequence(actions []Action, a, b ActionType) bool {
	for i := 0; i < len(actions)-1; i++ {
		if actions[i].Type == a && actions[i+1].Type == b {
			return true
		}
	}
	return false
}

func detectPatterns(actions []Action) []string {
	var patterns []string

	// Detect "read-read-read" exploring pattern
	consecutive := 0
	for _, a := range actions {
		if a.Type == ActionFileOpen || a.Type == ActionNavigate {
			consecutive++
		} else {
			consecutive = 0
		}
		if consecutive >= 3 {
			patterns = append(patterns, "code exploration sequence")
			break
		}
	}

	// Detect "edit-test-edit-test" TDD pattern
	tddCount := 0
	for i := 0; i < len(actions)-1; i++ {
		if actions[i].Type == ActionFileEdit && actions[i+1].Type == ActionRunTest {
			tddCount++
		}
	}
	if tddCount >= 2 {
		patterns = append(patterns, "TDD cycle detected")
	}

	// Detect "error-search-edit" debugging loop
	for i := 0; i < len(actions)-2; i++ {
		if actions[i].Type == ActionError && actions[i+1].Type == ActionSearch && actions[i+2].Type == ActionFileEdit {
			patterns = append(patterns, "debug-fix loop")
			break
		}
	}

	// Detect rapid multi-file edits (refactoring)
	editBurst := 0
	var lastEdit time.Time
	for _, a := range actions {
		if a.Type == ActionFileEdit {
			if !lastEdit.IsZero() && a.Timestamp.Sub(lastEdit) < 10*time.Second {
				editBurst++
			}
			lastEdit = a.Timestamp
		}
	}
	if editBurst >= 3 {
		patterns = append(patterns, "rapid multi-file editing")
	}

	return patterns
}
