package interp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/eventlog"
	"github.com/RelayOne/r1/internal/r1skill/analyze"
	interpNodes "github.com/RelayOne/r1/internal/r1skill/interp/nodes"
	"github.com/RelayOne/r1/internal/r1skill/ir"
)

// Cache stores deterministic results for replay.
type Cache interface {
	Get(key string) (json.RawMessage, bool)
	Put(key string, value json.RawMessage)
}

// MemoryCache is the default in-memory cache used by tests and local runs.
type MemoryCache struct {
	items map[string]json.RawMessage
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{items: map[string]json.RawMessage{}}
}

func (c *MemoryCache) Get(key string) (json.RawMessage, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.items[key]
	return v, ok
}

func (c *MemoryCache) Put(key string, value json.RawMessage) {
	if c == nil {
		return
	}
	c.items[key] = value
}

// Effect records each node execution for replay verification.
type Effect struct {
	NodeName string          `json:"node_name"`
	NodeKind string          `json:"node_kind"`
	CacheKey string          `json:"cache_key,omitempty"`
	Replay   bool            `json:"replay"`
	Outputs  json.RawMessage `json:"outputs"`
}

// PureFunc executes a deterministic function registered by name.
type PureFunc func(input json.RawMessage) (json.RawMessage, error)

// LLMFunc is the controlled stochastic boundary. Runtime replay forces
// cache reuse for repeated inputs.
type LLMFunc func(ctx context.Context, cfg LLMCallConfig) (json.RawMessage, error)

type Runtime struct {
	PureFuncs    map[string]PureFunc
	LLM          LLMFunc
	Cache        Cache
	Prompter     interpNodes.Prompter
	Reasoner     interpNodes.HeadlessReasoner
	WizardMode   string
	WizardPolicy *interpNodes.ConstitutionPolicy
}

type Result struct {
	Output  json.RawMessage
	Effects []Effect
}

type PureFnConfig struct {
	RegistryRef string   `json:"registry_ref"`
	Input       *ir.Expr `json:"input,omitempty"`
}

type LLMCallConfig struct {
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt,omitempty"`
	Input        *ir.Expr        `json:"input,omitempty"`
	CacheKey     json.RawMessage `json:"cache_key"`
}

func Run(ctx context.Context, rt *Runtime, skill *ir.Skill, proof *analyze.CompileProof, inputs json.RawMessage) (*Result, error) {
	if rt == nil {
		return nil, fmt.Errorf("r1skill/interp: runtime required")
	}
	if skill == nil {
		return nil, fmt.Errorf("r1skill/interp: skill required")
	}
	if proof == nil {
		return nil, fmt.Errorf("r1skill/interp: compile proof required")
	}
	if proof.IRHash == "" {
		return nil, fmt.Errorf("r1skill/interp: compile proof missing IR hash")
	}
	if rt.Cache == nil {
		rt.Cache = NewMemoryCache()
	}

	order := sortedNodeNames(skill.Graph.Nodes)
	state := map[string]json.RawMessage{
		"inputs": inputs,
	}
	effects := make([]Effect, 0, len(order))
	for _, name := range order {
		node := skill.Graph.Nodes[name]
		out, eff, err := runNode(ctx, rt, proof.IRHash, name, node, state)
		if err != nil {
			return nil, err
		}
		state[name] = out
		effects = append(effects, eff)
	}
	output, err := evalExpr(skill.Graph.Return, state)
	if err != nil {
		return nil, fmt.Errorf("r1skill/interp: evaluate return: %w", err)
	}
	return &Result{Output: output, Effects: effects}, nil
}

func runNode(ctx context.Context, rt *Runtime, irHash, name string, node ir.Node, state map[string]json.RawMessage) (json.RawMessage, Effect, error) {
	switch node.Kind {
	case "pure_fn":
		var cfg PureFnConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: decode pure_fn config for %s: %w", name, err)
		}
		fn := rt.PureFuncs[cfg.RegistryRef]
		if fn == nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: pure_fn %s not registered", cfg.RegistryRef)
		}
		input, err := evalOptionalExpr(cfg.Input, state)
		if err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: pure_fn %s input: %w", name, err)
		}
		out, err := fn(input)
		if err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: pure_fn %s: %w", name, err)
		}
		return out, Effect{NodeName: name, NodeKind: node.Kind, Outputs: out}, nil
	case "llm_call":
		var cfg LLMCallConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: decode llm_call config for %s: %w", name, err)
		}
		cacheKey, err := deterministicCacheKey(irHash, name, cfg.CacheKey, state)
		if err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: llm_call %s cache_key: %w", name, err)
		}
		if cached, ok := rt.Cache.Get(cacheKey); ok {
			return cached, Effect{NodeName: name, NodeKind: node.Kind, CacheKey: cacheKey, Replay: true, Outputs: cached}, nil
		}
		if rt.LLM == nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: llm runtime not configured")
		}
		out, err := rt.LLM(ctx, cfg)
		if err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: llm_call %s: %w", name, err)
		}
		rt.Cache.Put(cacheKey, out)
		return out, Effect{NodeName: name, NodeKind: node.Kind, CacheKey: cacheKey, Outputs: out}, nil
	case "ask_user":
		var cfg interpNodes.AskUserConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: decode ask_user config for %s: %w", name, err)
		}
		cacheKey, err := deterministicCacheKey(irHash, name, cfg.CacheKey, state)
		if err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: ask_user %s cache_key: %w", name, err)
		}
		var cached *interpNodes.AskUserOutputs
		if raw, ok := rt.Cache.Get(cacheKey); ok {
			var parsed interpNodes.AskUserOutputs
			if err := json.Unmarshal(raw, &parsed); err == nil {
				cached = &parsed
			}
		}
		out, err := interpNodes.Execute(ctx, cfg, interpNodes.ExecuteOpts{
			Mode:               rt.WizardMode,
			Prompter:           rt.Prompter,
			Reasoner:           rt.Reasoner,
			ConstitutionPolicy: rt.WizardPolicy,
			CachedAnswer:       cached,
		})
		if err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: ask_user %s: %w", name, err)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, Effect{}, fmt.Errorf("r1skill/interp: encode ask_user outputs for %s: %w", name, err)
		}
		if cached == nil {
			rt.Cache.Put(cacheKey, raw)
		}
		return raw, Effect{NodeName: name, NodeKind: node.Kind, CacheKey: cacheKey, Replay: cached != nil, Outputs: raw}, nil
	default:
		return nil, Effect{}, fmt.Errorf("r1skill/interp: unsupported node kind %q", node.Kind)
	}
}

func evalOptionalExpr(expr *ir.Expr, state map[string]json.RawMessage) (json.RawMessage, error) {
	if expr == nil {
		return json.RawMessage("null"), nil
	}
	return evalExpr(*expr, state)
}

func evalExpr(expr ir.Expr, state map[string]json.RawMessage) (json.RawMessage, error) {
	switch expr.Kind {
	case "", "ref":
		return lookupRef(expr.Ref, state)
	case "literal":
		return expr.Value, nil
	default:
		return nil, fmt.Errorf("unsupported expr kind %q", expr.Kind)
	}
}

func lookupRef(ref string, state map[string]json.RawMessage) (json.RawMessage, error) {
	parts := strings.Split(ref, ".")
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty ref")
	}
	current, ok := state[parts[0]]
	if !ok {
		return nil, fmt.Errorf("unknown ref root %q", parts[0])
	}
	if len(parts) == 1 {
		return current, nil
	}
	var value any
	if err := json.Unmarshal(current, &value); err != nil {
		return nil, fmt.Errorf("decode ref root %q: %w", parts[0], err)
	}
	for _, part := range parts[1:] {
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ref %q is not an object at %q", ref, part)
		}
		next, ok := obj[part]
		if !ok {
			return nil, fmt.Errorf("ref %q missing field %q", ref, part)
		}
		value = next
	}
	out, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func deterministicCacheKey(irHash, nodeName string, raw json.RawMessage, state map[string]json.RawMessage) (string, error) {
	var expr ir.Expr
	if len(raw) == 0 {
		expr = ir.Expr{Kind: "literal", Value: json.RawMessage("null")}
	} else if err := json.Unmarshal(raw, &expr); err != nil {
		return "", fmt.Errorf("decode expr: %w", err)
	}
	value, err := evalCacheKeyExpr(expr, state)
	if err != nil {
		return "", err
	}
	canonical, err := eventlog.Marshal(map[string]any{
		"ir_hash":   irHash,
		"node_name": nodeName,
		"value":     value,
	})
	if err != nil {
		return "", fmt.Errorf("canonicalize: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func evalCacheKeyExpr(expr ir.Expr, state map[string]json.RawMessage) (any, error) {
	switch expr.Kind {
	case "", "ref":
		raw, err := lookupRef(expr.Ref, state)
		if err != nil {
			return nil, err
		}
		return decodeCanonicalValue(raw)
	case "literal":
		return decodeCanonicalValue(expr.Value)
	case "sha256":
		if expr.Input == nil {
			return nil, fmt.Errorf("sha256 cache_key missing input")
		}
		raw, err := evalExpr(*expr.Input, state)
		if err != nil {
			return nil, err
		}
		canonical, err := eventlog.Marshal(json.RawMessage(raw))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(canonical)
		return "sha256:" + hex.EncodeToString(sum[:]), nil
	case "interp":
		var parts strings.Builder
		for _, part := range expr.Parts {
			raw, err := evalExpr(part, state)
			if err != nil {
				return nil, err
			}
			text, err := rawToString(raw)
			if err != nil {
				return nil, err
			}
			parts.WriteString(text)
		}
		return parts.String(), nil
	case "field":
		raw, err := lookupRef(expr.Ref, state)
		if err != nil {
			return nil, err
		}
		return decodeCanonicalValue(raw)
	default:
		return nil, fmt.Errorf("unsupported cache_key expr kind %q", expr.Kind)
	}
}

func decodeCanonicalValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func rawToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	canonical, err := eventlog.Marshal(json.RawMessage(raw))
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func sortedNodeNames(nodes map[string]ir.Node) []string {
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
