// Context builder generates enriched prompt context for agent execution.
//
// When an agent starts working on a mission phase, it needs structured context
// about the mission state, prior work, research findings, open gaps, and
// handoff history. The ContextBuilder aggregates information from multiple
// stores and produces a compact, token-budget-aware context block that can
// be injected into the agent's system prompt.
//
// The builder is designed for composability — each section is independently
// generated and budget-aware, allowing graceful degradation when context
// windows are tight.
package mission

import (
	"fmt"
	"strings"
)

// ContextSource provides access to research and handoff data
// for context enrichment. This interface decouples the context builder
// from concrete store implementations.
type ContextSource interface {
	// SearchResearch returns research entries matching the query, up to limit.
	SearchResearch(query string, limit int) ([]ResearchEntry, error)

	// GetResearchByMission returns all research for a mission.
	GetResearchByMission(missionID string) ([]ResearchEntry, error)

	// GetHandoffContext returns formatted handoff history for a mission,
	// truncated to fit within maxTokens.
	GetHandoffContext(missionID string, maxTokens int) (string, error)
}

// ResearchEntry is a simplified view of a research finding for context injection.
type ResearchEntry struct {
	Topic   string `json:"topic"`
	Query   string `json:"query"`
	Content string `json:"content"`
	Source  string `json:"source"`
}

// ContextBuilder generates enriched prompt context for mission execution.
type ContextBuilder struct {
	store  *Store
	source ContextSource // optional — may be nil if research/handoff not available
}

// NewContextBuilder creates a context builder backed by a mission store.
// The source parameter is optional — pass nil to build context without
// research enrichment.
func NewContextBuilder(store *Store, source ContextSource) (*ContextBuilder, error) {
	if store == nil {
		return nil, fmt.Errorf("mission.NewContextBuilder: store must not be nil")
	}
	return &ContextBuilder{store: store, source: source}, nil
}

// ContextConfig controls what sections are included and their budget allocation.
type ContextConfig struct {
	// MaxTokens is the total token budget (estimated at ~4 chars/token).
	// Default: 4000.
	MaxTokens int `json:"max_tokens"`

	// IncludeMissionInfo includes title, intent, phase. Always on by default.
	IncludeMissionInfo bool `json:"include_mission_info"`

	// IncludeCriteria includes the acceptance criteria checklist.
	IncludeCriteria bool `json:"include_criteria"`

	// IncludeGaps includes open gaps from convergence validation.
	IncludeGaps bool `json:"include_gaps"`

	// IncludeResearch includes relevant research findings.
	IncludeResearch bool `json:"include_research"`

	// IncludeHandoffs includes handoff history from prior agents.
	IncludeHandoffs bool `json:"include_handoffs"`

	// IncludeConvergenceStatus includes convergence metrics.
	IncludeConvergenceStatus bool `json:"include_convergence_status"`

	// ResearchQuery is used to find relevant research. If empty,
	// uses the mission intent as the query.
	ResearchQuery string `json:"research_query"`

	// MaxResearchEntries limits how many research findings to include.
	// Default: 5.
	MaxResearchEntries int `json:"max_research_entries"`
}

// DefaultContextConfig returns a config that includes all sections.
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		MaxTokens:                4000,
		IncludeMissionInfo:       true,
		IncludeCriteria:          true,
		IncludeGaps:              true,
		IncludeResearch:          true,
		IncludeHandoffs:          true,
		IncludeConvergenceStatus: true,
		MaxResearchEntries:       5,
	}
}

// BuildContext generates the full enriched context for a mission.
// Returns a structured markdown string suitable for system prompt injection.
func (cb *ContextBuilder) BuildContext(missionID string, config ContextConfig) (string, error) {
	if config.MaxTokens <= 0 {
		config.MaxTokens = 4000
	}
	if config.MaxResearchEntries <= 0 {
		config.MaxResearchEntries = 5
	}

	m, err := cb.store.Get(missionID)
	if err != nil {
		return "", fmt.Errorf("get mission: %w", err)
	}
	if m == nil {
		return "", fmt.Errorf("mission %q not found", missionID)
	}

	charBudget := config.MaxTokens * 4
	var sections []string

	// Section 1: Mission Info (always included, ~200 chars)
	if config.IncludeMissionInfo {
		section := cb.buildMissionSection(m)
		sections = append(sections, section)
	}

	// Section 2: Convergence Status (~150 chars)
	if config.IncludeConvergenceStatus {
		section, err := cb.buildConvergenceSection(m.ID)
		if err == nil && section != "" {
			sections = append(sections, section)
		}
	}

	// Section 3: Acceptance Criteria (~50 chars per criterion)
	if config.IncludeCriteria && len(m.Criteria) > 0 {
		section := cb.buildCriteriaSection(m)
		sections = append(sections, section)
	}

	// Section 4: Open Gaps (~80 chars per gap)
	if config.IncludeGaps {
		section, err := cb.buildGapsSection(m.ID)
		if err == nil && section != "" {
			sections = append(sections, section)
		}
	}

	// Section 5: Research Findings (~200 chars per entry)
	if config.IncludeResearch && cb.source != nil {
		query := config.ResearchQuery
		if query == "" {
			query = m.Intent
		}
		section, err := cb.buildResearchSection(m.ID, query, config.MaxResearchEntries)
		if err == nil && section != "" {
			sections = append(sections, section)
		}
	}

	// Section 6: Handoff History (variable, budget-aware)
	if config.IncludeHandoffs && cb.source != nil {
		// Allocate remaining budget to handoffs
		currentLen := 0
		for _, s := range sections {
			currentLen += len(s)
		}
		remaining := charBudget - currentLen
		if remaining > 200 {
			handoffTokens := remaining / 4
			section, err := cb.source.GetHandoffContext(m.ID, handoffTokens)
			if err == nil && section != "" {
				sections = append(sections, section)
			}
		}
	}

	result := strings.Join(sections, "\n")

	// Final truncation if over budget
	if len(result) > charBudget {
		result = result[:charBudget-30] + "\n\n[context truncated for budget]\n"
	}

	return result, nil
}

// buildMissionSection generates the mission header.
func (cb *ContextBuilder) buildMissionSection(m *Mission) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Mission: %s\n\n", m.Title)
	fmt.Fprintf(&b, "**Intent:** %s\n\n", m.Intent)
	fmt.Fprintf(&b, "**Phase:** %s\n", m.Phase)
	if len(m.Tags) > 0 {
		fmt.Fprintf(&b, "**Tags:** %s\n", strings.Join(m.Tags, ", "))
	}
	return b.String()
}

// buildConvergenceSection generates convergence metrics.
func (cb *ContextBuilder) buildConvergenceSection(missionID string) (string, error) {
	status, err := cb.store.GetConvergenceStatus(missionID, 2)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n## Convergence Status\n")
	fmt.Fprintf(&b, "- Criteria: %d/%d satisfied\n", status.SatisfiedCriteria, status.TotalCriteria)
	if status.OpenGapCount > 0 {
		fmt.Fprintf(&b, "- Open gaps: %d (%d blocking)\n", status.OpenGapCount, status.BlockingGapCount)
	} else {
		fmt.Fprintf(&b, "- Open gaps: 0\n")
	}
	if status.HandoffCount > 0 {
		fmt.Fprintf(&b, "- Handoffs: %d\n", status.HandoffCount)
	}
	if status.ConsensusCount > 0 {
		fmt.Fprintf(&b, "- Consensus votes: %d (%d complete)\n", status.ConsensusCount, status.CompleteVotes)
	}
	if status.IsConverged {
		fmt.Fprintf(&b, "- **Status: CONVERGED** ✓\n")
	}
	return b.String(), nil
}

// buildCriteriaSection generates the acceptance criteria checklist.
func (cb *ContextBuilder) buildCriteriaSection(m *Mission) string {
	var b strings.Builder
	satisfied := 0
	for _, c := range m.Criteria {
		if c.Satisfied {
			satisfied++
		}
	}
	fmt.Fprintf(&b, "\n## Acceptance Criteria (%d/%d)\n", satisfied, len(m.Criteria))
	for _, c := range m.Criteria {
		mark := "[ ]"
		if c.Satisfied {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "- %s %s\n", mark, c.Description)
		if c.Satisfied && c.Evidence != "" {
			fmt.Fprintf(&b, "  Evidence: %s\n", truncateStr(c.Evidence, 120))
		}
	}
	return b.String()
}

// buildGapsSection generates the open gaps list.
func (cb *ContextBuilder) buildGapsSection(missionID string) (string, error) {
	gaps, err := cb.store.OpenGaps(missionID)
	if err != nil {
		return "", err
	}
	if len(gaps) == 0 {
		return "", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n## Open Gaps (%d)\n", len(gaps))
	for _, g := range gaps {
		prefix := ""
		if g.File != "" {
			prefix = g.File
			if g.Line > 0 {
				prefix = fmt.Sprintf("%s:%d", g.File, g.Line)
			}
			prefix += ": "
		}
		fmt.Fprintf(&b, "- [%s] %s%s\n", g.Severity, prefix, g.Description)
		if g.Suggestion != "" {
			fmt.Fprintf(&b, "  Suggestion: %s\n", truncateStr(g.Suggestion, 100))
		}
	}
	return b.String(), nil
}

// buildResearchSection generates relevant research findings.
func (cb *ContextBuilder) buildResearchSection(missionID, query string, maxEntries int) (string, error) {
	// Try mission-specific research first
	entries, err := cb.source.GetResearchByMission(missionID)
	if err != nil {
		return "", err
	}

	// If no mission-specific research, search by query
	if len(entries) == 0 && query != "" {
		entries, err = cb.source.SearchResearch(query, maxEntries)
		if err != nil {
			return "", err
		}
	}

	if len(entries) == 0 {
		return "", nil
	}

	// Limit entries
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n## Research Context (%d entries)\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&b, "\n### %s\n", e.Topic)
		if e.Query != "" {
			fmt.Fprintf(&b, "**Query:** %s\n", e.Query)
		}
		fmt.Fprintf(&b, "%s\n", truncateStr(e.Content, 300))
		if e.Source != "" {
			fmt.Fprintf(&b, "Source: %s\n", e.Source)
		}
	}
	return b.String(), nil
}

// truncateStr truncates a string to maxLen, adding ellipsis if needed.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
