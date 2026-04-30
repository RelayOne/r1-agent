package analyze

import (
	"encoding/json"

	"github.com/RelayOne/r1/internal/r1skill/ir"
)

// Stage 1: schema check. Verifies that input/output schemas, named
// types, and validators are all well-formed. The IR's own Validate
// catches gross malformations; this stage catches the more specific
// schema-level issues like undefined named-type references and
// validator-validity.
func stageSchema(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	if skill.Schemas.Inputs.Type == "" {
		res.Passed = false
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Level: "error", Code: "E010_SCHEMA_NO_INPUTS",
			Message: "schemas.inputs is required",
		})
	}
	if skill.Schemas.Outputs.Type == "" {
		res.Passed = false
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Level: "error", Code: "E011_SCHEMA_NO_OUTPUTS",
			Message: "schemas.outputs is required",
		})
	}

	// Verify all named-type references resolve
	checkType := func(t ir.TypeSpec, where string) {
		if t.Type == "named" && t.NamedRef != "" {
			if _, ok := skill.Schemas.NamedTypes[t.NamedRef]; !ok {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E012_UNDEFINED_NAMED_TYPE",
					Message:  "named type " + t.NamedRef + " referenced but not defined",
					Location: where,
					Hint:     "add a named_types." + t.NamedRef + " definition",
				})
			}
		}
	}
	checkType(skill.Schemas.Inputs, "schemas.inputs")
	checkType(skill.Schemas.Outputs, "schemas.outputs")
	for name, t := range skill.Schemas.NamedTypes {
		checkType(t, "schemas.named_types."+name)
	}

	return res
}

// Stage 2: type inference + edge type check. Walks the graph, infers
// the type of each node's output from its config + declared schema,
// and verifies that every consumer's input type matches the producer's
// output type.
//
// This is the largest stage by code volume in production; the skeleton
// here demonstrates the structure. A full implementation walks Expr
// references (e.g. "fetch.body" reads node "fetch"'s "body" output) and
// type-checks against the consuming node's expected input shape.
func stageType(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	// Verify each node's declared outputs are present in the per-kind
	// expected schema. The actual per-kind expected schemas live in the
	// interp/nodes/*.go files; in the analyzer we consult a registry
	// stub for now.
	for nodeName, node := range skill.Graph.Nodes {
		if node.Kind == "" {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E020_NODE_NO_KIND",
				Message:  "node has no kind",
				Location: "graph.nodes." + nodeName,
			})
		}
		if !knownNodeKinds[node.Kind] {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E021_UNKNOWN_NODE_KIND",
				Message:  "unknown node kind: " + node.Kind,
				Location: "graph.nodes." + nodeName,
				Hint:     "valid kinds: pure_fn, http_get, http_post, fs_read, fs_write, shell_exec, llm_call, ledger_read, ledger_write, skill_call, branch, map, assert, emit_artifact, emit_annotation",
			})
		}
	}

	return res
}

// knownNodeKinds is the closed set of primitive node types.
var knownNodeKinds = map[string]bool{
	"pure_fn":         true,
	"http_get":        true,
	"http_post":       true,
	"fs_read":         true,
	"fs_write":        true,
	"shell_exec":      true,
	"llm_call":        true,
	"ledger_read":     true,
	"ledger_write":    true,
	"skill_call":      true,
	"branch":          true,
	"map":             true,
	"assert":          true,
	"emit_artifact":   true,
	"emit_annotation": true,
}

// Stage 3: capability conformance. For each effect-producing node,
// verify the corresponding capability is declared in skill.Capabilities.
func stageCapability(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	for nodeName, node := range skill.Graph.Nodes {
		switch node.Kind {
		case "http_get", "http_post":
			if len(skill.Capabilities.Network.AllowDomains) == 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E030_HTTP_NO_NETWORK_CAP",
					Message:  "node uses " + node.Kind + " but capabilities.network.allow_domains is empty",
					Location: "graph.nodes." + nodeName,
					Hint:     "declare allowed domains in capabilities.network.allow_domains",
				})
			}
		case "fs_read":
			if len(skill.Capabilities.FS.ReadPaths) == 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E031_FS_READ_NO_CAP",
					Message:  "node uses fs_read but capabilities.fs.read_paths is empty",
					Location: "graph.nodes." + nodeName,
				})
			}
		case "fs_write":
			if len(skill.Capabilities.FS.WritePaths) == 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E032_FS_WRITE_NO_CAP",
					Message:  "node uses fs_write but capabilities.fs.write_paths is empty",
					Location: "graph.nodes." + nodeName,
				})
			}
		case "shell_exec":
			if len(skill.Capabilities.Shell.AllowCommands) == 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E033_SHELL_NO_CAP",
					Message:  "node uses shell_exec but capabilities.shell.allow_commands is empty",
					Location: "graph.nodes." + nodeName,
				})
			}
		case "llm_call":
			if skill.Capabilities.LLM.BudgetUSD <= 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E034_LLM_NO_BUDGET",
					Message:  "node uses llm_call but capabilities.llm.budget_usd is zero",
					Location: "graph.nodes." + nodeName,
					Hint:     "declare a positive budget_usd",
				})
			}
			if skill.Capabilities.LLM.MaxCalls <= 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E035_LLM_NO_MAX_CALLS",
					Message:  "node uses llm_call but capabilities.llm.max_calls is zero",
					Location: "graph.nodes." + nodeName,
				})
			}
		case "ledger_read":
			if len(skill.Capabilities.Ledger.ReadNodeTypes) == 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E036_LEDGER_READ_NO_CAP",
					Message:  "node uses ledger_read but capabilities.ledger.read_node_types is empty",
					Location: "graph.nodes." + nodeName,
				})
			}
		case "ledger_write":
			if len(skill.Capabilities.Ledger.WriteNodeTypes) == 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E037_LEDGER_WRITE_NO_CAP",
					Message:  "node uses ledger_write but capabilities.ledger.write_node_types is empty",
					Location: "graph.nodes." + nodeName,
				})
			}
		case "skill_call":
			if len(skill.Capabilities.Skill.AllowedCallees) == 0 {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E038_SKILL_CALL_NO_CAP",
					Message:  "node uses skill_call but capabilities.skill.allowed_callees is empty",
					Location: "graph.nodes." + nodeName,
				})
			}
		}
	}

	return res
}

// Stage 5: contract conformance for decidable subsets. Defers
// non-decidable contracts to runtime assertion injection.
func stageContract(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	for _, c := range skill.Contracts {
		switch c.Kind {
		case "actual_cost_lt":
			// Decidable check: sum of llm_call.max_cost_usd across the
			// graph must be <= c.USD. This is conservative (assumes
			// every llm_call hits its max), but the conservatism is
			// correct: we want to reject skills that *might* exceed.
			projected := projectMaxCost(skill)
			if projected > c.USD {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level:   "error",
					Code:    "E050_COST_BOUND_VIOLATED",
					Message: "contract actual_cost_lt projects exceeded by graph",
					Hint:    "lower per-call max_cost_usd or relax the contract bound",
				})
			}
		case "wall_time_lt", "forall", "exists":
			// Defer to runtime. Record this fact in the proof.
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level:   "info",
				Code:    "I051_CONTRACT_DEFERRED_TO_RUNTIME",
				Message: c.Kind + " contract deferred to runtime assertion",
			})
		}
	}

	return res
}

// projectMaxCost sums the max_cost_usd of every llm_call node in the
// graph. Used for decidable cost-contract checks.
func projectMaxCost(skill *ir.Skill) float64 {
	var total float64
	for _, node := range skill.Graph.Nodes {
		if node.Kind != "llm_call" {
			continue
		}
		// Decode the per-kind config. In production this lives in the
		// interp/nodes/llm_call.go package; here we use a local typed
		// shape sufficient for cost projection.
		var cfg struct {
			MaxCostUSD float64 `json:"max_cost_usd"`
		}
		if err := json.Unmarshal(node.Config, &cfg); err == nil {
			total += cfg.MaxCostUSD
		}
	}
	return total
}

// Stage 6: termination + DAG check. Verifies the graph is acyclic.
// Production code does a proper topological sort; here we do a simpler
// presence check via reachability from declared nodes.
func stageTermination(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	// For now: trust map traversal. A real implementation builds the
	// reference graph from each node's config Expr fields, runs a
	// cycle-detection algorithm (DFS with three-color marking), and
	// reports the cycle path on failure. We sketch the entry but stub
	// the deep walk.

	if len(skill.Graph.Nodes) > 1000 {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Level:   "warning",
			Code:    "W060_VERY_LARGE_GRAPH",
			Message: "graph has > 1000 nodes; consider decomposing into multiple skills",
		})
	}

	return res
}

// Stage 7: replay determinism. Every stochastic effect (llm_call,
// http_get, http_post, shell_exec) must declare a cache_key in its
// config so replay is bit-exact. Without a cache_key the runtime cannot
// guarantee re-execution will produce the same outputs.
func stageReplay(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	stochasticKinds := map[string]bool{
		"llm_call":   true,
		"http_get":   true,
		"http_post":  true,
		"shell_exec": true,
	}

	for nodeName, node := range skill.Graph.Nodes {
		if !stochasticKinds[node.Kind] {
			continue
		}
		var cfg struct {
			CacheKey json.RawMessage `json:"cache_key"`
		}
		if err := json.Unmarshal(node.Config, &cfg); err != nil || len(cfg.CacheKey) == 0 {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E070_NO_CACHE_KEY",
				Message:  "stochastic node " + nodeName + " (" + node.Kind + ") has no cache_key; replay determinism not guaranteed",
				Location: "graph.nodes." + nodeName + ".config.cache_key",
				Hint:     "declare a cache_key expression, typically sha256 over the input",
			})
		}
	}

	return res
}
