// Package handoff manages agent-to-agent context transfer.
//
// When an agent exhausts its context window or completes a phase of work,
// it hands off to the next agent. The handoff chain captures:
//   - What was accomplished (summary of changes)
//   - What remains (pending work items)
//   - Key decisions made (so they aren't revisited)
//   - Current state (files modified, tests passing, etc.)
//
// The chain is stored in the mission database and produces a compact
// context block that can be injected into the next agent's prompt.
// Auto-summarization compacts older handoffs to stay within token budgets.
//
// This package integrates with mission.Store for persistence and
// supports building the context injection for the next agent.
package handoff

import (
	"fmt"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/mission"
)

// Record captures a single context transfer between agents.
type Record struct {
	MissionID    string    `json:"mission_id"`
	FromAgent    string    `json:"from_agent"`
	ToAgent      string    `json:"to_agent"`
	Summary      string    `json:"summary"`
	PendingWork  []string  `json:"pending_work"`
	KeyDecisions []string  `json:"key_decisions"`
	FilesChanged []string  `json:"files_changed"`
	TestStatus   string    `json:"test_status"`   // "passing", "failing", "unknown"
	Phase        string    `json:"phase"`          // current mission phase
	Timestamp    time.Time `json:"timestamp"`
}

// Chain manages the sequence of handoffs for a mission.
type Chain struct {
	store *mission.Store
}

// NewChain creates a handoff chain backed by a mission store.
func NewChain(store *mission.Store) (*Chain, error) {
	if store == nil {
		return nil, fmt.Errorf("handoff.NewChain: store must not be nil")
	}
	return &Chain{store: store}, nil
}

// Handoff records a context transfer and returns the ID.
func (c *Chain) Handoff(r Record) error {
	if r.MissionID == "" {
		return fmt.Errorf("mission ID must not be empty")
	}
	if r.Summary == "" {
		return fmt.Errorf("summary must not be empty")
	}

	// Build the detailed context for storage
	pendingStr := strings.Join(r.PendingWork, "\n- ")
	if pendingStr != "" {
		pendingStr = "- " + pendingStr
	}
	decisionsStr := strings.Join(r.KeyDecisions, "\n- ")
	if decisionsStr != "" {
		decisionsStr = "- " + decisionsStr
	}

	return c.store.RecordHandoff(&mission.HandoffRecord{
		MissionID:    r.MissionID,
		FromAgent:    r.FromAgent,
		ToAgent:      r.ToAgent,
		Summary:      r.Summary,
		PendingWork:  pendingStr,
		KeyDecisions: decisionsStr,
	})
}

// Latest returns the most recent handoff for a mission.
func (c *Chain) Latest(missionID string) (*Record, error) {
	h, err := c.store.LatestHandoff(missionID)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, nil
	}
	return &Record{
		MissionID:    h.MissionID,
		FromAgent:    h.FromAgent,
		ToAgent:      h.ToAgent,
		Summary:      h.Summary,
		PendingWork:  parseList(h.PendingWork),
		KeyDecisions: parseList(h.KeyDecisions),
		Timestamp:    h.Timestamp,
	}, nil
}

// History returns all handoffs for a mission in chronological order.
func (c *Chain) History(missionID string) ([]Record, error) {
	handoffs, err := c.store.Handoffs(missionID)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, h := range handoffs {
		records = append(records, Record{
			MissionID:    h.MissionID,
			FromAgent:    h.FromAgent,
			ToAgent:      h.ToAgent,
			Summary:      h.Summary,
			PendingWork:  parseList(h.PendingWork),
			KeyDecisions: parseList(h.KeyDecisions),
			Timestamp:    h.Timestamp,
		})
	}
	return records, nil
}

// BuildContext generates a compact context block for the next agent.
// It includes the most recent handoff details plus a summary of older ones,
// staying within the given token budget (estimated at ~4 chars/token).
func (c *Chain) BuildContext(missionID string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 2000
	}

	// Get mission info
	m, err := c.store.Get(missionID)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", fmt.Errorf("mission %q not found", missionID)
	}

	// Get convergence status
	status, err := c.store.GetConvergenceStatus(missionID, 2)
	if err != nil {
		return "", err
	}

	// Get handoff history
	history, err := c.History(missionID)
	if err != nil {
		return "", err
	}

	var b strings.Builder

	// Mission header
	fmt.Fprintf(&b, "# Mission: %s\n\n", m.Title)
	fmt.Fprintf(&b, "**Intent:** %s\n\n", m.Intent)
	fmt.Fprintf(&b, "**Phase:** %s\n\n", m.Phase)

	// Convergence status
	fmt.Fprintf(&b, "## Status\n")
	fmt.Fprintf(&b, "- Criteria: %d/%d satisfied\n", status.SatisfiedCriteria, status.TotalCriteria)
	if status.OpenGapCount > 0 {
		fmt.Fprintf(&b, "- Open gaps: %d (%d blocking)\n", status.OpenGapCount, status.BlockingGapCount)
	}
	if status.HandoffCount > 0 {
		fmt.Fprintf(&b, "- Handoff #%d\n", status.HandoffCount+1)
	}
	b.WriteString("\n")

	// Acceptance criteria
	if len(m.Criteria) > 0 {
		fmt.Fprintf(&b, "## Acceptance Criteria\n")
		for _, cr := range m.Criteria {
			mark := "[ ]"
			if cr.Satisfied {
				mark = "[x]"
			}
			fmt.Fprintf(&b, "- %s %s\n", mark, cr.Description)
		}
		b.WriteString("\n")
	}

	// Open gaps
	gaps, _ := c.store.OpenGaps(missionID)
	if len(gaps) > 0 {
		fmt.Fprintf(&b, "## Open Gaps\n")
		for _, g := range gaps {
			prefix := ""
			if g.File != "" {
				prefix = g.File + ": "
			}
			fmt.Fprintf(&b, "- [%s] %s%s\n", g.Severity, prefix, g.Description)
		}
		b.WriteString("\n")
	}

	// Latest handoff (full detail)
	if len(history) > 0 {
		latest := history[len(history)-1]
		fmt.Fprintf(&b, "## Previous Agent (%s)\n", latest.FromAgent)
		fmt.Fprintf(&b, "%s\n\n", latest.Summary)
		if len(latest.PendingWork) > 0 {
			fmt.Fprintf(&b, "**Pending:**\n")
			for _, w := range latest.PendingWork {
				fmt.Fprintf(&b, "- %s\n", w)
			}
			b.WriteString("\n")
		}
		if len(latest.KeyDecisions) > 0 {
			fmt.Fprintf(&b, "**Decisions:**\n")
			for _, d := range latest.KeyDecisions {
				fmt.Fprintf(&b, "- %s\n", d)
			}
			b.WriteString("\n")
		}
	}

	result := b.String()

	// If within budget, include older handoff summaries
	charBudget := maxTokens * 4
	if len(result) < charBudget && len(history) > 1 {
		var older strings.Builder
		fmt.Fprintf(&older, "## Handoff History\n")
		// Summarize older handoffs (newest first, skip the latest which is already shown)
		for i := len(history) - 2; i >= 0; i-- {
			h := history[i]
			line := fmt.Sprintf("- [%s → %s] %s\n", h.FromAgent, h.ToAgent, truncate(h.Summary, 100))
			if len(result)+older.Len()+len(line) > charBudget {
				break
			}
			older.WriteString(line)
		}
		if older.Len() > len("## Handoff History\n") {
			result += older.String()
		}
	}

	// Truncate if still over budget
	if len(result) > charBudget {
		result = result[:charBudget-20] + "\n\n[context truncated]\n"
	}

	return result, nil
}

// Count returns the number of handoffs for a mission.
func (c *Chain) Count(missionID string) (int, error) {
	history, err := c.store.Handoffs(missionID)
	if err != nil {
		return 0, err
	}
	return len(history), nil
}

func parseList(s string) []string {
	if s == "" {
		return nil
	}
	var items []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimSpace(line)
		if line != "" {
			items = append(items, line)
		}
	}
	return items
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
