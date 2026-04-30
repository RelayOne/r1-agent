package ir

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// validSkill returns a minimal but well-formed Skill suitable for
// round-trip tests. Subtests mutate copies of this to test failure modes.
func validSkill() Skill {
	return Skill{
		SchemaVersion: SchemaVersion,
		SkillID:       "test-skill",
		SkillVersion:  1,
		Description:   "Test skill",
		Lineage: Lineage{
			Kind:       "human",
			AuthoredAt: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		},
		Schemas: Schemas{
			Inputs:  TypeSpec{Type: "record", Fields: map[string]TypeSpec{"x": {Type: "string"}}},
			Outputs: TypeSpec{Type: "record", Fields: map[string]TypeSpec{"y": {Type: "string"}}},
		},
		Capabilities: Capabilities{
			LLM: LLMCap{BudgetUSD: 0.10, MaxCalls: 1, AllowedModels: []string{"claude-haiku-3.5"}},
		},
		Graph: Graph{
			Nodes: map[string]Node{
				"identity": {
					Kind:    "pure_fn",
					Config:  json.RawMessage(`{"registry_ref": "stdlib:identity", "input": {"kind": "ref", "ref": "inputs.x"}}`),
					Outputs: map[string]TypeSpec{"output": {Type: "string"}},
				},
			},
			Return: Expr{Kind: "ref", Ref: "identity.output"},
		},
	}
}

func TestSkill_JSONRoundtrip(t *testing.T) {
	s := validSkill()
	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Skill
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.SkillID != s.SkillID {
		t.Errorf("SkillID round-trip lost")
	}
	if back.Lineage.Kind != s.Lineage.Kind {
		t.Errorf("Lineage round-trip lost")
	}
	if len(back.Graph.Nodes) != 1 {
		t.Errorf("Graph.Nodes round-trip lost")
	}
}

func TestSkill_Validate_Happy(t *testing.T) {
	s := validSkill()
	if err := s.Validate(); err != nil {
		t.Errorf("validSkill.Validate(): %v", err)
	}
}

func TestSkill_Validate_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Skill)
		wantSub string
	}{
		{"wrong schema version", func(s *Skill) { s.SchemaVersion = 999 }, "schema_version mismatch"},
		{"missing skill_id", func(s *Skill) { s.SkillID = "" }, "skill_id required"},
		{"zero skill version", func(s *Skill) { s.SkillVersion = 0 }, "skill_version"},
		{"missing lineage kind", func(s *Skill) { s.Lineage.Kind = "" }, "lineage.kind required"},
		{"unknown lineage kind", func(s *Skill) { s.Lineage.Kind = "ai-overlord" }, "lineage.kind"},
		{"empty graph", func(s *Skill) { s.Graph.Nodes = map[string]Node{} }, "at least one node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSkill()
			tc.mut(&s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestTypeSpec_Recursive(t *testing.T) {
	// list[Todo] where Todo is a record
	spec := TypeSpec{
		Type: "list",
		ElementType: &TypeSpec{
			Type: "record",
			Fields: map[string]TypeSpec{
				"name": {Type: "string"},
				"age":  {Type: "int"},
			},
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back TypeSpec
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Type != "list" {
		t.Errorf("type mismatch")
	}
	if back.ElementType == nil {
		t.Fatal("element_type lost")
	}
	if back.ElementType.Fields["age"].Type != "int" {
		t.Errorf("nested field type lost")
	}
}

func TestExpr_Variants(t *testing.T) {
	cases := []struct {
		name string
		e    Expr
	}{
		{"literal", Expr{Kind: "literal", Value: json.RawMessage(`"hello"`)}},
		{"ref", Expr{Kind: "ref", Ref: "fetch.body"}},
		{"sha256", Expr{Kind: "sha256", Input: &Expr{Kind: "ref", Ref: "parse.output"}}},
		{"interp", Expr{Kind: "interp", Parts: []Expr{
			{Kind: "literal", Value: json.RawMessage(`"prefix-"`)},
			{Kind: "ref", Ref: "inputs.suffix"},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(&tc.e)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back Expr
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if back.Kind != tc.e.Kind {
				t.Errorf("kind round-trip lost")
			}
		})
	}
}

func TestCapabilities_AllZeroIsValid(t *testing.T) {
	// A skill with all-zero capabilities is valid IR — it just can't do
	// anything that requires a capability. The analyzer will reject any
	// node that needs an undeclared capability; that's the analyzer's
	// job, not the IR's.
	s := validSkill()
	s.Capabilities = Capabilities{}
	// Replace LLM-using node with a no-cap pure_fn node
	s.Graph.Nodes = map[string]Node{
		"identity": s.Graph.Nodes["identity"],
	}
	if err := s.Validate(); err != nil {
		t.Errorf("zero caps should validate: %v", err)
	}
}
