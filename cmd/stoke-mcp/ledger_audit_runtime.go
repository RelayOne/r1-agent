package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

type ledgerAuditQueryRuntimeInput struct {
	LedgerDir      string   `json:"ledger_dir"`
	MissionID      string   `json:"mission_id"`
	NodeTypes      []string `json:"node_types"`
	CreatedBy      string   `json:"created_by"`
	Since          string   `json:"since"`
	Until          string   `json:"until"`
	Limit          int      `json:"limit"`
	IncludeContent bool     `json:"include_content"`
}

type ledgerAuditQueryRuntimeOutput struct {
	QuerySlug    string                         `json:"query_slug"`
	Mode         string                         `json:"mode"`
	Summary      string                         `json:"summary"`
	LedgerDir    string                         `json:"ledger_dir"`
	MatchedCount int                            `json:"matched_count"`
	Filters      ledgerAuditQueryRuntimeFilters `json:"filters"`
	TypeCounts   []ledgerAuditQueryRuntimeCount `json:"type_counts"`
	Nodes        []ledgerAuditQueryRuntimeNode  `json:"nodes"`
	Followups    []string                       `json:"followups"`
}

type ledgerAuditQueryRuntimeFilters struct {
	MissionID      string   `json:"mission_id,omitempty"`
	NodeTypes      []string `json:"node_types,omitempty"`
	CreatedBy      string   `json:"created_by,omitempty"`
	Since          string   `json:"since,omitempty"`
	Until          string   `json:"until,omitempty"`
	Limit          int      `json:"limit"`
	IncludeContent bool     `json:"include_content"`
}

type ledgerAuditQueryRuntimeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type ledgerAuditQueryRuntimeNode struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	CreatedAt         string `json:"created_at"`
	CreatedBy         string `json:"created_by"`
	MissionID         string `json:"mission_id,omitempty"`
	ParentHash        string `json:"parent_hash,omitempty"`
	ContentCommitment string `json:"content_commitment,omitempty"`
	Content           any    `json:"content,omitempty"`
}

func ledgerAuditQueryRuntime(input json.RawMessage) (json.RawMessage, error) {
	var req ledgerAuditQueryRuntimeInput
	if len(input) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &req); err != nil {
			return nil, fmt.Errorf("decode input: %w", err)
		}
	}

	req.LedgerDir = strings.TrimSpace(req.LedgerDir)
	if req.LedgerDir == "" {
		return nil, fmt.Errorf("ledger_dir must be provided")
	}
	req.MissionID = strings.TrimSpace(req.MissionID)
	req.CreatedBy = strings.TrimSpace(req.CreatedBy)
	req.NodeTypes = normalizeNodeTypes(req.NodeTypes)
	if req.Limit <= 0 {
		req.Limit = 50
	}
	if req.Limit > 200 {
		return nil, fmt.Errorf("limit must be between 1 and 200")
	}

	since, err := parseRFC3339Time(req.Since, "since")
	if err != nil {
		return nil, err
	}
	until, err := parseRFC3339Time(req.Until, "until")
	if err != nil {
		return nil, err
	}
	if since != nil && until != nil && since.After(*until) {
		return nil, fmt.Errorf("since must be before until")
	}

	lg, err := ledger.New(req.LedgerDir)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	defer lg.Close()

	baseFilter := ledger.QueryFilter{
		MissionID: req.MissionID,
		CreatedBy: req.CreatedBy,
		Since:     since,
		Until:     until,
	}
	nodes, err := lg.Query(nil, baseFilter)
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}
	nodes = filterLedgerNodesByType(nodes, req.NodeTypes)
	if len(nodes) > req.Limit {
		nodes = nodes[:req.Limit]
	}

	typeCounts := makeTypeCounts(nodes)
	outNodes := make([]ledgerAuditQueryRuntimeNode, 0, len(nodes))
	for _, node := range nodes {
		item := ledgerAuditQueryRuntimeNode{
			ID:                node.ID,
			Type:              node.Type,
			CreatedAt:         node.CreatedAt.UTC().Format(time.RFC3339Nano),
			CreatedBy:         node.CreatedBy,
			MissionID:         node.MissionID,
			ParentHash:        node.ParentHash,
			ContentCommitment: node.ContentCommitment,
		}
		if req.IncludeContent {
			var content any
			if err := json.Unmarshal(node.Content, &content); err != nil {
				return nil, fmt.Errorf("decode node %s content: %w", node.ID, err)
			}
			item.Content = content
		}
		outNodes = append(outNodes, item)
	}

	out := ledgerAuditQueryRuntimeOutput{
		QuerySlug:    "ledger-audit-query",
		Mode:         "read-only",
		Summary:      buildLedgerAuditSummary(req, nodes, typeCounts),
		LedgerDir:    req.LedgerDir,
		MatchedCount: len(nodes),
		Filters: ledgerAuditQueryRuntimeFilters{
			MissionID:      req.MissionID,
			NodeTypes:      req.NodeTypes,
			CreatedBy:      req.CreatedBy,
			Since:          formatOptionalTime(since),
			Until:          formatOptionalTime(until),
			Limit:          req.Limit,
			IncludeContent: req.IncludeContent,
		},
		TypeCounts: typeCounts,
		Nodes:      outNodes,
		Followups:  buildLedgerAuditFollowups(req, typeCounts),
	}
	return json.Marshal(out)
}

func normalizeNodeTypes(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func parseRFC3339Time(raw, field string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be RFC3339: %w", field, err)
	}
	return &parsed, nil
}

func filterLedgerNodesByType(nodes []ledger.Node, allowed []string) []ledger.Node {
	if len(allowed) == 0 {
		return nodes
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, item := range allowed {
		allowedSet[item] = struct{}{}
	}
	filtered := make([]ledger.Node, 0, len(nodes))
	for _, node := range nodes {
		if _, ok := allowedSet[node.Type]; ok {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func makeTypeCounts(nodes []ledger.Node) []ledgerAuditQueryRuntimeCount {
	counts := make(map[string]int, len(nodes))
	for _, node := range nodes {
		counts[node.Type]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ledgerAuditQueryRuntimeCount, 0, len(keys))
	for _, key := range keys {
		out = append(out, ledgerAuditQueryRuntimeCount{Type: key, Count: counts[key]})
	}
	return out
}

func buildLedgerAuditSummary(req ledgerAuditQueryRuntimeInput, nodes []ledger.Node, counts []ledgerAuditQueryRuntimeCount) string {
	parts := []string{fmt.Sprintf("Read %d ledger nodes", len(nodes))}
	if req.MissionID != "" {
		parts = append(parts, fmt.Sprintf("for mission %q", req.MissionID))
	}
	if len(req.NodeTypes) > 0 {
		parts = append(parts, fmt.Sprintf("filtered to node types %s", strings.Join(req.NodeTypes, ", ")))
	}
	if req.CreatedBy != "" {
		parts = append(parts, fmt.Sprintf("created by %q", req.CreatedBy))
	}
	if len(counts) > 0 {
		chunks := make([]string, 0, len(counts))
		for _, item := range counts {
			chunks = append(chunks, fmt.Sprintf("%s=%d", item.Type, item.Count))
		}
		parts = append(parts, "type counts: "+strings.Join(chunks, ", "))
	}
	return strings.Join(parts, "; ") + "."
}

func buildLedgerAuditFollowups(req ledgerAuditQueryRuntimeInput, counts []ledgerAuditQueryRuntimeCount) []string {
	followups := []string{
		"Re-run with include_content=true when you need the raw node payloads for offline audit.",
		"Add mission_id or created_by filters to tighten the audit slice before exporting evidence.",
	}
	for _, item := range counts {
		switch item.Type {
		case "honesty_decision":
			followups = append(followups, "Review honesty_decision nodes alongside receipt evidence before claiming live verification.")
		case "verification_evidence":
			followups = append(followups, "Cross-check verification_evidence nodes with the merged PR or deploy probe to confirm the fix path.")
		case "artifact":
			followups = append(followups, "Walk artifact nodes backward through references when the audit needs the exact plan or proof lineage.")
		}
	}
	if req.MissionID == "" {
		followups = append(followups, "Set mission_id to isolate one task trail instead of scanning cross-mission governance data.")
	}
	return dedupeStrings(followups)
}

func formatOptionalTime(ts *time.Time) string {
	if ts == nil {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func dedupeStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}