package context

import (
	"strings"
	"testing"
)

func TestBudgetDefault(t *testing.T) {
	b := DefaultBudget()
	if b.MaxTokens != 200000 { t.Errorf("max=%d", b.MaxTokens) }
	if b.TargetUtil != 0.40 { t.Errorf("target=%f", b.TargetUtil) }
}

func TestAddAndAssemble(t *testing.T) {
	m := NewManager(DefaultBudget())
	m.Add(ContextBlock{Label: "system", Content: "You are a coding agent.", Tier: TierActive, Priority: 10})
	m.Add(ContextBlock{Label: "task", Content: "Add auth middleware.", Tier: TierActive, Priority: 9})

	assembled := m.Assemble()
	if !strings.Contains(assembled, "coding agent") { t.Error("missing system prompt") }
	if !strings.Contains(assembled, "auth middleware") { t.Error("missing task") }
}

func TestCompactNone(t *testing.T) {
	m := NewManager(Budget{MaxTokens: 100000, TargetUtil: 0.40, GentleThreshold: 0.50, ModerateThresh: 0.65, AggressiveThresh: 0.80})
	m.Add(ContextBlock{Label: "small", Content: "hello", Tier: TierActive, Priority: 10, Tokens: 2})
	level := m.Compact()
	if level != "none" { t.Errorf("level=%q, want none", level) }
}

func TestCompactGentle(t *testing.T) {
	m := NewManager(Budget{MaxTokens: 100, TargetUtil: 0.40, GentleThreshold: 0.50, ModerateThresh: 0.65, AggressiveThresh: 0.80})
	// Add a big low-priority tool output that pushes past 50%
	m.Add(ContextBlock{Label: "tool_output", Content: strings.Repeat("line\n", 600), Tier: TierSession, Priority: 1, Tokens: 60})
	level := m.Compact()
	if level != "gentle" && level != "moderate" && level != "aggressive" {
		t.Errorf("should compact, got %q", level)
	}
}

func TestCompactAggressive(t *testing.T) {
	m := NewManager(Budget{MaxTokens: 100, TargetUtil: 0.10, GentleThreshold: 0.20, ModerateThresh: 0.30, AggressiveThresh: 0.40})
	m.Add(ContextBlock{Label: "important", Content: "keep", Tier: TierActive, Priority: 10, Tokens: 30})
	m.Add(ContextBlock{Label: "junk", Content: strings.Repeat("x", 400), Tier: TierSession, Priority: 1, Tokens: 100})
	m.Compact()
	// Junk should be dropped
	assembled := m.Assemble()
	if !strings.Contains(assembled, "keep") { t.Error("should keep active tier") }
}

func TestReset(t *testing.T) {
	m := NewManager(DefaultBudget())
	m.Add(ContextBlock{Label: "test", Content: "data", Tier: TierActive})
	m.Reset()
	if m.TotalTokens() != 0 { t.Error("should be empty after reset") }
}

func TestCheckRemindersContextHigh(t *testing.T) {
	reminders := DefaultReminders()
	state := ReminderState{ContextUtil: 0.75}
	fired := CheckReminders(reminders, state)
	found := false
	for _, r := range fired {
		if strings.Contains(r, "CONTEXT FILLING") { found = true }
	}
	if !found { t.Error("should fire context filling reminder") }
}

func TestCheckRemindersErrorRepeated(t *testing.T) {
	fired := CheckReminders(DefaultReminders(), ReminderState{SameErrorCount: 3})
	found := false
	for _, r := range fired { if strings.Contains(r, "STUCK") { found = true } }
	if !found { t.Error("should fire stuck reminder") }
}

func TestCheckRemindersNone(t *testing.T) {
	fired := CheckReminders(DefaultReminders(), ReminderState{})
	if len(fired) != 0 { t.Errorf("should fire nothing, got %d", len(fired)) }
}
