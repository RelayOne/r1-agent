// Package mcp memory tools expose the ledger, wisdom, research, replay,
// and skill stores as MCP tools for external editors (Claude Code, Cursor).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/research"
	"github.com/ericmacdougall/stoke/internal/wisdom"
)

// MemoryServer exposes Stoke's persistent stores as MCP tools.
type MemoryServer struct {
	Ledger   *ledger.Ledger
	Wisdom   *wisdom.Store
	Research *research.Store
}

// MemoryToolDefinition is a simplified MCP tool definition.
type MemoryToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// MemoryToolDefinitions returns the 12+ MCP tool definitions for the memory surface.
func (s *MemoryServer) MemoryToolDefinitions() []MemoryToolDefinition {
	return []MemoryToolDefinition{
		{
			Name:        "stoke_status",
			Description: "Get current Stoke session status including active missions, pool state, and cost",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "stoke_ledger_query",
			Description: "Query the append-only reasoning ledger by node type, mission ID, or time range",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":       map[string]interface{}{"type": "string", "description": "Node type filter (e.g., decision, task, draft)"},
					"mission_id": map[string]interface{}{"type": "string", "description": "Mission ID filter"},
					"since":      map[string]interface{}{"type": "string", "description": "ISO 8601 timestamp for lower bound"},
					"limit":      map[string]interface{}{"type": "integer", "description": "Max results (default 20)"},
				},
			},
		},
		{
			Name:        "stoke_ledger_walk",
			Description: "Walk the ledger graph from a node, following edges of specified types",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"node_id"},
				"properties": map[string]interface{}{
					"node_id":   map[string]interface{}{"type": "string", "description": "Starting node ID"},
					"direction": map[string]interface{}{"type": "string", "enum": []string{"outgoing", "incoming", "both"}, "description": "Edge direction"},
					"depth":     map[string]interface{}{"type": "integer", "description": "Max traversal depth (default 3)"},
				},
			},
		},
		{
			Name:        "stoke_wisdom_find",
			Description: "Search cross-task learnings (gotchas, decisions, patterns) by keyword or pattern hash",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":   map[string]interface{}{"type": "string", "description": "Search query"},
					"pattern": map[string]interface{}{"type": "string", "description": "Failure pattern hash for dedup"},
				},
			},
		},
		{
			Name:        "stoke_wisdom_record",
			Description: "Record a new learning (gotcha, decision, or pattern) into the wisdom store",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"category", "description"},
				"properties": map[string]interface{}{
					"category":    map[string]interface{}{"type": "string", "enum": []string{"gotcha", "decision", "pattern"}},
					"description": map[string]interface{}{"type": "string"},
					"task_id":     map[string]interface{}{"type": "string"},
					"file":        map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        "stoke_research_search",
			Description: "Search the research store (FTS5 + semantic) for previously gathered information",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string"},
					"limit": map[string]interface{}{"type": "integer", "description": "Max results (default 10)"},
				},
			},
		},
		{
			Name:        "stoke_research_add",
			Description: "Add a research entry to the persistent store",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"topic", "content"},
				"properties": map[string]interface{}{
					"topic":   map[string]interface{}{"type": "string"},
					"query":   map[string]interface{}{"type": "string"},
					"content": map[string]interface{}{"type": "string"},
					"source":  map[string]interface{}{"type": "string"},
					"tags":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
			},
		},
		{
			Name:        "stoke_session_status",
			Description: "Get the current session state including tasks, attempts, and learned patterns",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "stoke_check_duplicate",
			Description: "Check if a topic/query has already been researched to avoid redundant work",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"topic"},
				"properties": map[string]interface{}{
					"topic": map[string]interface{}{"type": "string"},
					"query": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        "stoke_skill_list",
			Description: "List available skills (reusable workflow patterns) with confidence levels",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "stoke_replay_search",
			Description: "Search session replay recordings for post-mortem debugging",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":      map[string]interface{}{"type": "string"},
					"session_id": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        "stoke_wisdom_as_of",
			Description: "Query wisdom store at a specific point in time (temporal validity)",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"as_of"},
				"properties": map[string]interface{}{
					"as_of": map[string]interface{}{"type": "string", "description": "ISO 8601 timestamp"},
				},
			},
		},
	}
}

// HandleMemoryToolCall dispatches a memory tool call to the appropriate handler.
func (s *MemoryServer) HandleMemoryToolCall(ctx context.Context, toolName string, args json.RawMessage) (string, error) {
	switch toolName {
	case "stoke_wisdom_find":
		return s.handleWisdomFind(args)
	case "stoke_wisdom_record":
		return s.handleWisdomRecord(args)
	case "stoke_research_search":
		return s.handleResearchSearch(args)
	case "stoke_research_add":
		return s.handleResearchAdd(args)
	case "stoke_check_duplicate":
		return s.handleCheckDuplicate(args)
	case "stoke_ledger_query":
		return s.handleLedgerQuery(ctx, args)
	case "stoke_status":
		return s.handleStatus()
	default:
		return "", fmt.Errorf("unknown memory tool: %s", toolName)
	}
}

func (s *MemoryServer) handleWisdomFind(args json.RawMessage) (string, error) {
	var params struct {
		Query   string `json:"query"`
		Pattern string `json:"pattern"`
	}
	json.Unmarshal(args, &params)

	if s.Wisdom == nil {
		return "wisdom store not initialized", nil
	}

	if params.Pattern != "" {
		if existing := s.Wisdom.FindByPattern(params.Pattern); existing != nil {
			data, _ := json.MarshalIndent(existing, "", "  ")
			return string(data), nil
		}
		return "no matching pattern found", nil
	}

	learnings := s.Wisdom.Learnings()
	if params.Query != "" {
		var filtered []wisdom.Learning
		q := strings.ToLower(params.Query)
		for _, l := range learnings {
			if strings.Contains(strings.ToLower(l.Description), q) ||
				strings.Contains(strings.ToLower(l.File), q) {
				filtered = append(filtered, l)
			}
		}
		learnings = filtered
	}

	data, _ := json.MarshalIndent(learnings, "", "  ")
	return string(data), nil
}

func (s *MemoryServer) handleWisdomRecord(args json.RawMessage) (string, error) {
	var params struct {
		Category    string `json:"category"`
		Description string `json:"description"`
		TaskID      string `json:"task_id"`
		File        string `json:"file"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	if s.Wisdom == nil {
		return "wisdom store not initialized", nil
	}

	s.Wisdom.Record(params.TaskID, wisdom.Learning{
		Category:    wisdom.ParseCategory(params.Category),
		Description: params.Description,
		File:        params.File,
	})
	return "recorded", nil
}

func (s *MemoryServer) handleResearchSearch(args json.RawMessage) (string, error) {
	var params struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	json.Unmarshal(args, &params)
	if params.Limit <= 0 {
		params.Limit = 10
	}

	if s.Research == nil {
		return "research store not initialized", nil
	}

	results, err := s.Research.Search(params.Query, params.Limit)
	if err != nil {
		return "", err
	}

	data, _ := json.MarshalIndent(results, "", "  ")
	return string(data), nil
}

func (s *MemoryServer) handleResearchAdd(args json.RawMessage) (string, error) {
	var params struct {
		Topic   string   `json:"topic"`
		Query   string   `json:"query"`
		Content string   `json:"content"`
		Source  string   `json:"source"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}

	if s.Research == nil {
		return "research store not initialized", nil
	}

	entry := research.Entry{
		Topic:     params.Topic,
		Query:     params.Query,
		Content:   params.Content,
		Source:    params.Source,
		Tags:      params.Tags,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.Research.Add(&entry); err != nil {
		return "", err
	}
	return "added", nil
}

func (s *MemoryServer) handleCheckDuplicate(args json.RawMessage) (string, error) {
	var params struct {
		Topic string `json:"topic"`
		Query string `json:"query"`
	}
	json.Unmarshal(args, &params)

	if s.Research == nil {
		return `{"duplicate": false, "reason": "research store not initialized"}`, nil
	}

	exists, err := s.Research.HasResearch(params.Topic, params.Query)
	if err != nil {
		return "", err
	}

	result := map[string]interface{}{"duplicate": exists}
	data, _ := json.Marshal(result)
	return string(data), nil
}

func (s *MemoryServer) handleLedgerQuery(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Type      string `json:"type"`
		MissionID string `json:"mission_id"`
		Since     string `json:"since"`
		Limit     int    `json:"limit"`
	}
	json.Unmarshal(args, &params)
	if params.Limit <= 0 {
		params.Limit = 20
	}

	if s.Ledger == nil {
		return "ledger not initialized", nil
	}

	filter := ledger.QueryFilter{
		Type:      params.Type,
		MissionID: params.MissionID,
		Limit:     params.Limit,
	}
	if params.Since != "" {
		if t, err := time.Parse(time.RFC3339, params.Since); err == nil {
			filter.Since = &t
		}
	}

	nodes, err := s.Ledger.Query(ctx, filter)
	if err != nil {
		return "", err
	}

	data, _ := json.MarshalIndent(nodes, "", "  ")
	return string(data), nil
}

func (s *MemoryServer) handleStatus() (string, error) {
	status := map[string]interface{}{
		"version": "0.1.0",
		"stores":  map[string]interface{}{},
	}

	stores := status["stores"].(map[string]interface{})
	if s.Wisdom != nil {
		stores["wisdom"] = map[string]interface{}{
			"learnings_count": len(s.Wisdom.Learnings()),
		}
	}
	if s.Research != nil {
		count, _ := s.Research.Count()
		stores["research"] = map[string]interface{}{
			"entries_count": count,
		}
	}

	data, _ := json.MarshalIndent(status, "", "  ")
	return string(data), nil
}
