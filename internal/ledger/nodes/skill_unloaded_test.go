package nodes

import (
	"strings"
	"testing"
	"time"
)

// TestSkillUnloadedValidate covers the validation surface of the
// SkillUnloaded node type. Spec r1-server-ui-v2 §"Skill-loaded /
// skill-unloaded emission + rendering" requires this type to validate
// every required field and reject reason values outside the
// {compactor_evicted, scope_exit, explicit_unload} taxonomy so the
// downstream waterfall renderer can branch on a closed enum.
func TestSkillUnloadedValidate(t *testing.T) {
	now := time.Now()
	valid := &SkillUnloaded{
		SkillRef:          "skill-1",
		LoadRef:           "sk-load-1",
		StanceID:          "stance-1",
		StanceRole:        "dev",
		Reason:            "compactor_evicted",
		BudgetTokensFreed: 1024,
		CreatedAt:         now,
		Version:           1,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid SkillUnloaded.Validate() = %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(s *SkillUnloaded)
		wantErr string
	}{
		{"empty skill_ref", func(s *SkillUnloaded) { s.SkillRef = "" }, "skill_ref"},
		{"empty load_ref", func(s *SkillUnloaded) { s.LoadRef = "" }, "load_ref"},
		{"empty stance_id", func(s *SkillUnloaded) { s.StanceID = "" }, "stance_id"},
		{"empty stance_role", func(s *SkillUnloaded) { s.StanceRole = "" }, "stance_role"},
		{"empty reason", func(s *SkillUnloaded) { s.Reason = "" }, "reason"},
		{"invalid reason", func(s *SkillUnloaded) { s.Reason = "bogus" }, "invalid reason"},
		{"zero created_at", func(s *SkillUnloaded) { s.CreatedAt = time.Time{} }, "created_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := *valid
			tc.mutate(&bad)
			err := bad.Validate()
			if err == nil {
				t.Fatalf("%s: want error containing %q, got nil", tc.name, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: error %q does not contain %q", tc.name, err.Error(), tc.wantErr)
			}
		})
	}

	// Reason taxonomy: scope_exit and explicit_unload also valid.
	for _, reason := range []string{"scope_exit", "explicit_unload"} {
		s := *valid
		s.Reason = reason
		s.BudgetTokensFreed = 0 // optional for non-compactor reasons
		if err := s.Validate(); err != nil {
			t.Errorf("reason=%q should validate, got %v", reason, err)
		}
	}
}

// TestSkillUnloadedNodeType pins the node-registry contract: every
// node type must register itself with the canonical lowercase name
// so the ledger can reconstruct typed nodes from JSON on disk.
func TestSkillUnloadedNodeType(t *testing.T) {
	s := &SkillUnloaded{Version: 1}
	if got := s.NodeType(); got != "skill_unloaded" {
		t.Errorf("NodeType() = %q, want skill_unloaded", got)
	}
	if got := s.SchemaVersion(); got != 1 {
		t.Errorf("SchemaVersion() = %d, want 1", got)
	}
	n, err := New("skill_unloaded")
	if err != nil {
		t.Fatalf("registry lookup: %v", err)
	}
	if _, ok := n.(*SkillUnloaded); !ok {
		t.Errorf("New(skill_unloaded) returned %T, want *SkillUnloaded", n)
	}
}
