package context

import (
	"fmt"
	"strings"
)

// Tier defines context priority levels.
type Tier int

const (
	TierActive  Tier = iota // in the API call: prompt, task, retry brief, recent tools
	TierSession             // on disk, promoted on demand: plan state, error history
	TierProject             // persistent: CLAUDE.md, project map, learned patterns
)

// Budget controls context window utilization.
type Budget struct {
	MaxTokens        int     // total context window (e.g. 200000)
	TargetUtil       float64 // target utilization (spec says <40%)
	GentleThreshold  float64 // truncate long outputs (50%)
	ModerateThresh   float64 // compress file reads (65%)
	AggressiveThresh float64 // summarize everything (80%)
}

// DefaultBudget returns the spec-recommended budget.
func DefaultBudget() Budget {
	return Budget{
		MaxTokens:        200000,
		TargetUtil:       0.40,
		GentleThreshold:  0.50,
		ModerateThresh:   0.65,
		AggressiveThresh: 0.80,
	}
}

// ContextBlock is one piece of context with estimated token count.
type ContextBlock struct {
	Label    string
	Content  string
	Tier     Tier
	Priority int // higher = keep longer during compaction
	Tokens   int // estimated tokens (chars / 4)
}

// Manager assembles and compacts context for each phase.
type Manager struct {
	budget Budget
	blocks []ContextBlock
}

// NewManager creates a context manager with the given budget.
func NewManager(budget Budget) *Manager {
	return &Manager{budget: budget}
}

// Add adds a context block.
func (m *Manager) Add(block ContextBlock) {
	if block.Tokens == 0 {
		block.Tokens = len(block.Content) / 4
	}
	m.blocks = append(m.blocks, block)
}

// TotalTokens returns the estimated total token count.
func (m *Manager) TotalTokens() int {
	total := 0
	for _, b := range m.blocks {
		total += b.Tokens
	}
	return total
}

// Utilization returns current utilization as a fraction.
func (m *Manager) Utilization() float64 {
	if m.budget.MaxTokens == 0 {
		return 0
	}
	return float64(m.TotalTokens()) / float64(m.budget.MaxTokens)
}

// Compact progressively reduces context to fit within budget.
// Returns the compaction level applied: "none", "gentle", "moderate", "aggressive".
func (m *Manager) Compact() string {
	util := m.Utilization()

	if util <= m.budget.TargetUtil {
		return "none"
	}

	// Gentle: truncate tool outputs over 500 lines to summaries
	if util > m.budget.GentleThreshold {
		for i := range m.blocks {
			if m.blocks[i].Label == "tool_output" || m.blocks[i].Priority < 3 {
				m.blocks[i].Content = truncateLines(m.blocks[i].Content, 500)
				m.blocks[i].Tokens = len(m.blocks[i].Content) / 4
			}
		}
		if m.Utilization() <= m.budget.TargetUtil {
			return "gentle"
		}
	}

	// Moderate: compress file reads to "read X, found Y"
	if util > m.budget.ModerateThresh {
		for i := range m.blocks {
			if m.blocks[i].Tier == TierSession && m.blocks[i].Priority < 5 {
				m.blocks[i].Content = summarizeBlock(m.blocks[i])
				m.blocks[i].Tokens = len(m.blocks[i].Content) / 4
			}
		}
		if m.Utilization() <= m.budget.TargetUtil {
			return "moderate"
		}
	}

	// Aggressive: drop low-priority blocks entirely
	if util > m.budget.AggressiveThresh {
		var kept []ContextBlock
		for _, b := range m.blocks {
			if b.Tier == TierActive || b.Priority >= 7 {
				kept = append(kept, b)
			}
		}
		m.blocks = kept
		return "aggressive"
	}

	return "gentle"
}

// Assemble returns the full context string for a phase prompt.
func (m *Manager) Assemble() string {
	var parts []string
	for _, b := range m.blocks {
		if b.Content != "" {
			parts = append(parts, b.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Reset clears all blocks (called between phases for fresh context).
func (m *Manager) Reset() {
	m.blocks = nil
}

// --- Compaction helpers ---

func truncateLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	head := strings.Join(lines[:maxLines/2], "\n")
	tail := strings.Join(lines[len(lines)-maxLines/4:], "\n")
	return head + fmt.Sprintf("\n\n... (%d lines truncated) ...\n\n", len(lines)-maxLines*3/4) + tail
}

func summarizeBlock(b ContextBlock) string {
	lines := strings.Count(b.Content, "\n") + 1
	return fmt.Sprintf("[%s: %d lines, ~%d tokens]", b.Label, lines, b.Tokens)
}

// --- Event-Driven Reminders (from OPENDEV §8) ---

// ReminderTrigger identifies when a reminder should fire.
type ReminderTrigger int

const (
	TriggerFileWriteToTest ReminderTrigger = iota
	TriggerContextAbove60Pct
	TriggerErrorRepeated3x
	TriggerTaskRunning20Min
	TriggerPolicyViolationSeen
	TriggerScopeViolationSeen
)

// Reminder is injected into context when its trigger fires.
type Reminder struct {
	Trigger ReminderTrigger
	Message string
}

// DefaultReminders returns the spec-defined reminders.
func DefaultReminders() []Reminder {
	return []Reminder{
		{TriggerFileWriteToTest, "TEST RULES: never weaken assertions, never delete tests, never use .skip or .only. If a test fails, fix the code, not the test."},
		{TriggerContextAbove60Pct, "CONTEXT FILLING: focus on the current task. Do not explore unrelated files. Finish or report blockers."},
		{TriggerErrorRepeated3x, "STUCK: you hit this error 3 times. Try a fundamentally different approach instead of iterating on the same fix."},
		{TriggerTaskRunning20Min, "TIMEOUT WARNING: this task is taking too long. Finish what you have and commit, or report what's blocking you."},
		{TriggerPolicyViolationSeen, "POLICY: do NOT use @ts-ignore, as any, eslint-disable, or any other bypass. Fix the actual error."},
		{TriggerScopeViolationSeen, "SCOPE: only modify files listed in the task specification. Do not touch other files."},
	}
}

// CheckReminders evaluates triggers and returns applicable reminders.
func CheckReminders(reminders []Reminder, state ReminderState) []string {
	var fired []string
	for _, r := range reminders {
		switch r.Trigger {
		case TriggerContextAbove60Pct:
			if state.ContextUtil > 0.60 {
				fired = append(fired, r.Message)
			}
		case TriggerErrorRepeated3x:
			if state.SameErrorCount >= 3 {
				fired = append(fired, r.Message)
			}
		case TriggerTaskRunning20Min:
			if state.TaskMinutes >= 20 {
				fired = append(fired, r.Message)
			}
		case TriggerFileWriteToTest:
			if state.WritingTestFile {
				fired = append(fired, r.Message)
			}
		case TriggerPolicyViolationSeen:
			if state.PolicyViolation {
				fired = append(fired, r.Message)
			}
		case TriggerScopeViolationSeen:
			if state.ScopeViolation {
				fired = append(fired, r.Message)
			}
		}
	}
	return fired
}

// ReminderState captures current execution state for reminder evaluation.
type ReminderState struct {
	ContextUtil     float64
	SameErrorCount  int
	TaskMinutes     float64
	WritingTestFile bool
	PolicyViolation bool
	ScopeViolation  bool
}
