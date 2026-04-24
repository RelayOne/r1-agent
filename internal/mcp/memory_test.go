package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestMemoryToolDefinitions_DualRegistersR1Aliases proves S1-4 invariant:
// every legacy stoke_* memory tool is published under its canonical r1_*
// alias with identical description + schema. Both names stay live until
// v2.0.0 per S6-6.
func TestMemoryToolDefinitions_DualRegistersR1Aliases(t *testing.T) {
	s := &MemoryServer{}
	defs := s.MemoryToolDefinitions()
	byName := map[string]MemoryToolDefinition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	// The 12 legacy primitives; each must have an r1_* twin.
	legacy := []string{
		"stoke_status",
		"stoke_ledger_query",
		"stoke_ledger_walk",
		"stoke_wisdom_find",
		"stoke_wisdom_record",
		"stoke_research_search",
		"stoke_research_add",
		"stoke_session_status",
		"stoke_check_duplicate",
		"stoke_skill_list",
		"stoke_replay_search",
		"stoke_wisdom_as_of",
	}
	for _, name := range legacy {
		canonical := "r1_" + strings.TrimPrefix(name, "stoke_")
		lDef, ok := byName[name]
		if !ok {
			t.Fatalf("missing legacy tool: %s", name)
		}
		cDef, ok := byName[canonical]
		if !ok {
			t.Fatalf("missing canonical r1_* alias for %s: expected %s", name, canonical)
		}
		if lDef.Description != cDef.Description {
			t.Errorf("%s/%s description mismatch", name, canonical)
		}
		// InputSchema is a map; compare via JSON encoding (stable key order).
		lJSON, _ := json.Marshal(lDef.InputSchema)
		cJSON, _ := json.Marshal(cDef.InputSchema)
		if string(lJSON) != string(cJSON) {
			t.Errorf("%s/%s inputSchema mismatch:\n  legacy=%s\n  canonical=%s",
				name, canonical, lJSON, cJSON)
		}
	}
}

// TestMemoryToolDefinitions_TotalCount pins the length of the returned
// definitions to catch drift: 12 base tools × 2 (legacy + canonical) = 24.
func TestMemoryToolDefinitions_TotalCount(t *testing.T) {
	s := &MemoryServer{}
	defs := s.MemoryToolDefinitions()
	const want = 24
	if len(defs) != want {
		t.Errorf("MemoryToolDefinitions length=%d want %d (12 stoke_* + 12 r1_*)", len(defs), want)
	}
}

// TestHandleMemoryToolCall_R1AliasDispatches proves a r1_* alias reaches the
// same handler as the legacy stoke_* name. Uses stoke_status / r1_status
// which requires no injected stores (the nil-store branch returns a
// deterministic payload) so the test stays hermetic.
func TestHandleMemoryToolCall_R1AliasDispatches(t *testing.T) {
	s := &MemoryServer{}
	ctx := context.Background()
	for _, name := range []string{"stoke_status", "r1_status"} {
		out, err := s.HandleMemoryToolCall(ctx, name, nil)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("%s: invalid JSON: %v (raw=%q)", name, err, out)
		}
		if _, ok := resp["version"]; !ok {
			t.Errorf("%s: response missing \"version\" key: %+v", name, resp)
		}
	}
}

// TestHandleMemoryToolCall_UnknownToolErrors proves the dual-accept switch
// still rejects unknown names (neither stoke_* nor r1_* prefixed).
func TestHandleMemoryToolCall_UnknownToolErrors(t *testing.T) {
	s := &MemoryServer{}
	ctx := context.Background()
	_, err := s.HandleMemoryToolCall(ctx, "not_a_tool", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool; got nil")
	}
	if !strings.Contains(err.Error(), "unknown memory tool") {
		t.Errorf("error message should mention \"unknown memory tool\", got: %v", err)
	}
}
