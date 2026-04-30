package interp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestRunLLMReplayUsesEvaluatedCacheKey(t *testing.T) {
	skill := &ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       "llm-sha",
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
						"cache_key":{"kind":"sha256","input":{"kind":"ref","ref":"inputs.msg"}}
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
	rt := &Runtime{
		LLM: func(_ context.Context, _ LLMCallConfig) (json.RawMessage, error) {
			return json.RawMessage(`{"result":"ok"}`), nil
		},
		Cache: NewMemoryCache(),
	}
	res, err := Run(context.Background(), rt, skill, proof, json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Effects) != 1 {
		t.Fatalf("effects = %d, want 1", len(res.Effects))
	}
	cacheValueHash := sha256.Sum256([]byte(`"hello"`))
	want := "sha256:" + sha256Hex(map[string]any{
		"ir_hash":   proof.IRHash,
		"node_name": "ask",
		"value":     "sha256:" + hex.EncodeToString(cacheValueHash[:]),
	})
	if res.Effects[0].CacheKey != want {
		t.Fatalf("cache key = %q, want %q", res.Effects[0].CacheKey, want)
	}
}

func sha256Hex(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
