package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

type skillExecutionAuditLogInput struct {
	LedgerDir         string `json:"ledger_dir"`
	Capability        string `json:"capability"`
	MissionID         string `json:"mission_id"`
	CreatedBy         string `json:"created_by"`
	Since             string `json:"since"`
	Until             string `json:"until"`
	Limit             int    `json:"limit"`
	OnlyDeterministic bool   `json:"only_deterministic"`
}

type skillExecutionAuditLogOutput struct {
	QuerySlug    string                            `json:"query_slug"`
	Mode         string                            `json:"mode"`
	Summary      string                            `json:"summary"`
	LedgerDir    string                            `json:"ledger_dir"`
	MatchedCount int                               `json:"matched_count"`
	Filters      skillExecutionAuditLogFilters     `json:"filters"`
	Capabilities []skillExecutionAuditLogCount     `json:"capabilities"`
	Executions   []skillExecutionAuditLogExecution `json:"executions"`
	Followups    []string                          `json:"followups"`
}

type skillExecutionAuditLogFilters struct {
	Capability        string `json:"capability,omitempty"`
	MissionID         string `json:"mission_id,omitempty"`
	CreatedBy         string `json:"created_by,omitempty"`
	Since             string `json:"since,omitempty"`
	Until             string `json:"until,omitempty"`
	Limit             int    `json:"limit"`
	OnlyDeterministic bool   `json:"only_deterministic"`
}

type skillExecutionAuditLogCount struct {
	Capability string `json:"capability"`
	Count      int    `json:"count"`
}

type skillExecutionAuditLogExecution struct {
	NodeID             string `json:"node_id"`
	Capability         string `json:"capability"`
	CreatedAt          string `json:"created_at"`
	CreatedBy          string `json:"created_by"`
	MissionID          string `json:"mission_id,omitempty"`
	ManifestHash       string `json:"manifest_hash,omitempty"`
	ManifestName       string `json:"manifest_name,omitempty"`
	ManifestVersion    string `json:"manifest_version,omitempty"`
	ManifestRegistered bool   `json:"manifest_registered"`
	Deterministic      bool   `json:"deterministic"`
	DelegationID       string `json:"delegation_id,omitempty"`
	InputBytes         int    `json:"input_bytes"`
}

type capabilityInvocationAuditRecord struct {
	Kind               string `json:"kind"`
	Capability         string `json:"capability"`
	ManifestHash       string `json:"manifest_hash"`
	ManifestName       string `json:"manifest_name"`
	ManifestVersion    string `json:"manifest_version"`
	ManifestRegistered bool   `json:"manifest_registered"`
	DelegationID       string `json:"delegation_id"`
	InputBytes         int    `json:"input_bytes"`
	Deterministic      bool   `json:"deterministic"`
}

func skillExecutionAuditLogRuntime(input json.RawMessage) (json.RawMessage, error) {
	var req skillExecutionAuditLogInput
	if len(input) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &req); err != nil {
			return nil, fmt.Errorf("decode input: %w", err)
		}
	}

	req.LedgerDir = strings.TrimSpace(req.LedgerDir)
	req.Capability = strings.TrimSpace(req.Capability)
	req.MissionID = strings.TrimSpace(req.MissionID)
	req.CreatedBy = strings.TrimSpace(req.CreatedBy)
	if req.LedgerDir == "" {
		return nil, fmt.Errorf("ledger_dir must be provided")
	}
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

	nodes, err := lg.Query(nil, ledger.QueryFilter{
		MissionID: req.MissionID,
		CreatedBy: req.CreatedBy,
		Since:     since,
		Until:     until,
	})
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}

	executions := make([]skillExecutionAuditLogExecution, 0, len(nodes))
	for _, node := range nodes {
		if node.Type != "decision_internal" {
			continue
		}
		var record capabilityInvocationAuditRecord
		if err := json.Unmarshal(node.Content, &record); err != nil {
			return nil, fmt.Errorf("decode node %s content: %w", node.ID, err)
		}
		if record.Kind != "capability_invocation" {
			continue
		}
		if req.Capability != "" && record.Capability != req.Capability {
			continue
		}
		if req.OnlyDeterministic && !record.Deterministic {
			continue
		}
		executions = append(executions, skillExecutionAuditLogExecution{
			NodeID:             node.ID,
			Capability:         record.Capability,
			CreatedAt:          node.CreatedAt.UTC().Format(time.RFC3339Nano),
			CreatedBy:          node.CreatedBy,
			MissionID:          node.MissionID,
			ManifestHash:       record.ManifestHash,
			ManifestName:       record.ManifestName,
			ManifestVersion:    record.ManifestVersion,
			ManifestRegistered: record.ManifestRegistered,
			Deterministic:      record.Deterministic,
			DelegationID:       record.DelegationID,
			InputBytes:         record.InputBytes,
		})
		if len(executions) == req.Limit {
			break
		}
	}

	counts := makeSkillExecutionCounts(executions)
	out := skillExecutionAuditLogOutput{
		QuerySlug:    "skill-execution-audit-log",
		Mode:         "read-only",
		Summary:      buildSkillExecutionAuditSummary(req, executions, counts),
		LedgerDir:    req.LedgerDir,
		MatchedCount: len(executions),
		Filters: skillExecutionAuditLogFilters{
			Capability:        req.Capability,
			MissionID:         req.MissionID,
			CreatedBy:         req.CreatedBy,
			Since:             formatOptionalTime(since),
			Until:             formatOptionalTime(until),
			Limit:             req.Limit,
			OnlyDeterministic: req.OnlyDeterministic,
		},
		Capabilities: counts,
		Executions:   executions,
		Followups:    buildSkillExecutionAuditFollowups(req, executions),
	}
	return json.Marshal(out)
}

func makeSkillExecutionCounts(executions []skillExecutionAuditLogExecution) []skillExecutionAuditLogCount {
	counts := make(map[string]int, len(executions))
	for _, item := range executions {
		counts[item.Capability]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]skillExecutionAuditLogCount, 0, len(keys))
	for _, key := range keys {
		out = append(out, skillExecutionAuditLogCount{Capability: key, Count: counts[key]})
	}
	return out
}

func buildSkillExecutionAuditSummary(req skillExecutionAuditLogInput, executions []skillExecutionAuditLogExecution, counts []skillExecutionAuditLogCount) string {
	parts := []string{fmt.Sprintf("Read %d skill execution audit entries", len(executions))}
	if req.Capability != "" {
		parts = append(parts, fmt.Sprintf("for capability %q", req.Capability))
	}
	if req.MissionID != "" {
		parts = append(parts, fmt.Sprintf("within mission %q", req.MissionID))
	}
	if req.OnlyDeterministic {
		parts = append(parts, "filtered to deterministic runtime executions")
	}
	if len(counts) > 0 {
		chunks := make([]string, 0, len(counts))
		for _, item := range counts {
			chunks = append(chunks, fmt.Sprintf("%s=%d", item.Capability, item.Count))
		}
		parts = append(parts, "capability counts: "+strings.Join(chunks, ", "))
	}
	return strings.Join(parts, "; ") + "."
}

func buildSkillExecutionAuditFollowups(req skillExecutionAuditLogInput, executions []skillExecutionAuditLogExecution) []string {
	followups := []string{
		"Join these execution entries with ledger_audit_query_runtime when you need adjacent honesty, approval, or verification nodes from the same mission.",
		"Filter by capability to isolate one deterministic runtime before exporting evidence for operator review.",
	}
	var sawUnregistered bool
	var sawExternal bool
	for _, item := range executions {
		if !item.ManifestRegistered {
			sawUnregistered = true
		}
		if !item.Deterministic {
			sawExternal = true
		}
	}
	if sawUnregistered {
		followups = append(followups, "Investigate entries with manifest_registered=false before treating them as shipped bundled-runtime executions.")
	}
	if sawExternal {
		followups = append(followups, "Re-run with only_deterministic=true when the audit should exclude non-runtime or externally handled capabilities.")
	}
	if req.Capability == "" {
		followups = append(followups, "Set capability to focus the audit on one skill when the ledger mixes multiple runtime families.")
	}
	return dedupeStrings(followups)
}
