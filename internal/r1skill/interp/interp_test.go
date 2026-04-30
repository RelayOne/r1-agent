package interp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/r1skill/analyze"
	"github.com/RelayOne/r1/internal/r1skill/ir"
)

func testSkill() *ir.Skill {
	return &ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       "echo",
		SkillVersion:  1,
		Lineage:       ir.Lineage{Kind: "human", AuthoredAt: time.Now().UTC()},
		Schemas: ir.Schemas{
			Inputs:  ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"msg": {Type: "string"}}},
			Outputs: ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"msg": {Type: "string"}}},
		},
		Graph: ir.Graph{
			Nodes: map[string]ir.Node{
				"echo": {
					Kind: "pure_fn",
					Config: json.RawMessage(`{
						"registry_ref":"stdlib:echo",
						"input":{"kind":"ref","ref":"inputs.msg"}
					}`),
				},
			},
			Return: ir.Expr{Kind: "ref", Ref: "echo"},
		},
	}
}

func TestRunPureFn(t *testing.T) {
	skill := testSkill()
	proof, err := analyze.Analyze(skill, analyze.Constitution{Hash: "sha256:test"}, analyze.DefaultOptions())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	rt := &Runtime{
		PureFuncs: map[string]PureFunc{
			"stdlib:echo": func(input json.RawMessage) (json.RawMessage, error) {
				out, err := json.Marshal(map[string]string{"msg": string(input[1 : len(input)-1])})
				return out, err
			},
		},
		Cache: NewMemoryCache(),
	}
	res, err := Run(context.Background(), rt, skill, proof, json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(res.Output) != `{"msg":"hello"}` {
		t.Fatalf("output = %s", string(res.Output))
	}
}

func TestRunLLMReplay(t *testing.T) {
	skill := &ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       "llm",
		SkillVersion:  1,
		Lineage:       ir.Lineage{Kind: "human", AuthoredAt: time.Now().UTC()},
		Schemas: ir.Schemas{
			Inputs:  ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"msg": {Type: "string"}}},
			Outputs: ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"result": {Type: "string"}}},
		},
		Capabilities: ir.Capabilities{
			LLM: ir.LLMCap{BudgetUSD: 1, MaxCalls: 1},
		},
		Graph: ir.Graph{
			Nodes: map[string]ir.Node{
				"ask": {
					Kind: "llm_call",
					Config: json.RawMessage(`{
						"model":"test",
						"input":{"kind":"ref","ref":"inputs.msg"},
						"cache_key":{"kind":"literal","value":"fixed"}
					}`),
				},
			},
			Return: ir.Expr{Kind: "ref", Ref: "ask"},
		},
	}
	proof, err := analyze.Analyze(skill, analyze.Constitution{Hash: "sha256:test"}, analyze.DefaultOptions())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	calls := 0
	rt := &Runtime{
		PureFuncs: map[string]PureFunc{},
		LLM: func(_ context.Context, _ LLMCallConfig) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"result":"ok"}`), nil
		},
		Cache: NewMemoryCache(),
	}
	_, err = Run(context.Background(), rt, skill, proof, json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	res, err := Run(context.Background(), rt, skill, proof, json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("llm calls = %d, want 1", calls)
	}
	if len(res.Effects) != 1 || !res.Effects[0].Replay {
		t.Fatalf("expected replay effect, got %+v", res.Effects)
	}
}
