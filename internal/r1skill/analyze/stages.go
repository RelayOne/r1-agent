package analyze

import (
	"encoding/json"
	"strconv"

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
// the type of each node's output from its declared Outputs, and
// verifies that every consumer's reference resolves to a declared
// output AND that the producer/consumer types are compatible at the
// edge.
//
// Why this matters: type-mismatched skills used to pass `analyze` and
// fail at runtime with corrupt outputs. Catching the mismatch here
// turns a runtime corruption into a compile-time error that names the
// offending edge and the two type names involved.
//
// Available signal in the IR (no separate tool-signature registry yet):
//   - skill.Graph.Nodes[*].Outputs map[string]ir.TypeSpec  -- producer
//     side type declarations.
//   - skill.Graph.Return ir.Expr                           -- the skill's
//     return expression; must resolve to skill.Schemas.Outputs.
//   - ir.Expr.Ref of the form "<nodeName>.<outputName>"   -- the only
//     mechanism by which one node references another's output.
//
// What we check:
//  1. node-kind validity (was here in the skeleton; preserved).
//  2. every ref expression in every node's Config JSON resolves to
//     a declared output of an existing node.
//  3. the Graph.Return expression resolves to a declared output whose
//     type structurally matches skill.Schemas.Outputs.
//  4. when a non-return consumer references a producer whose output
//     type is declared, the type is propagated for downstream checks
//     (currently surfaced as info diagnostics; a future per-kind
//     registry will let us assert a hard expected-input type).
func stageType(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	// (1) Node-kind validity. Preserved from the skeleton — every later
	// check assumes a recognized kind, so we surface unknowns up front.
	for nodeName, node := range skill.Graph.Nodes {
		if node.Kind == "" {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E020_NODE_NO_KIND",
				Message:  "node has no kind",
				Location: "graph.nodes." + nodeName,
			})
			continue
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

	// (2) Resolve every ref in every node's Config. Configs are
	// kind-specific JSON; we don't know their shape, so we walk the
	// raw JSON tree and look for Expr-shaped sub-trees ("kind":"ref"
	// with a "ref" string). This is conservative — it never flags a
	// false positive — and catches the common case where an LLM-author
	// wires up a stale node name or a renamed output.
	for nodeName, node := range skill.Graph.Nodes {
		refs := collectRefsFromConfig(node.Config)
		for _, r := range refs {
			checkRefResolves(skill, r, "graph.nodes."+nodeName+".config", &res)
		}
	}

	// (3) Return expression must resolve and its type must match
	// skill.Schemas.Outputs. This is the type-check that catches the
	// runtime-corruption class: a return ref pointing at a node output
	// whose declared type disagrees with the skill's declared output
	// schema.
	checkReturnType(skill, &res)

	return res
}

// refRef is one ref expression we extracted from a config blob, with
// the path inside the config so we can locate it for diagnostics.
type refRef struct {
	target string // e.g. "fetch.body"
	path   string // e.g. ".url" inside the config
}

// collectRefsFromConfig walks an arbitrary JSON tree and returns every
// Expr-shaped subtree whose Kind is "ref" or "field". The walker is
// type-blind by design: per-kind packages parse the config for their
// own purposes, and the analyzer must work even when a brand-new kind
// is added to the IR.
func collectRefsFromConfig(cfg json.RawMessage) []refRef {
	if len(cfg) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(cfg, &v); err != nil {
		// Stage 1 (schema) already emits a malformed-JSON diagnostic; we
		// silently skip here so we don't double-report.
		return nil
	}
	var out []refRef
	walkJSONForRefs(v, "", &out)
	return out
}

// walkJSONForRefs is the recursive half of collectRefsFromConfig. A
// ref-shaped node looks like:
//
//	{"kind":"ref","ref":"node.field"}
//	{"kind":"field","ref":"node.field"}
//
// Anything else we recurse through.
func walkJSONForRefs(v any, path string, out *[]refRef) {
	switch t := v.(type) {
	case map[string]any:
		if kind, ok := t["kind"].(string); ok && (kind == "ref" || kind == "field") {
			if r, ok := t["ref"].(string); ok && r != "" {
				*out = append(*out, refRef{target: r, path: path})
				// fall through; nested refs in "input" / "parts" still walked
			}
		}
		for k, child := range t {
			walkJSONForRefs(child, path+"."+k, out)
		}
	case []any:
		for i, child := range t {
			walkJSONForRefs(child, path+"["+itoa(i)+"]", out)
		}
	}
}

// itoa is a tiny int->string helper (avoids pulling strconv into the
// hot walker for the common case of small array indices).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// checkRefResolves verifies a ref like "fetch.body" points at a real
// node and a declared output. Emits an error diagnostic on failure.
//
// Special-case: the pseudo-node "inputs" refers to the skill's
// Schemas.Inputs. The interpreter binds it in the eval state; the
// analyzer checks the requested field against Schemas.Inputs.Fields.
func checkRefResolves(skill *ir.Skill, r refRef, location string, res *StageResult) {
	nodeName, outputName := splitRef(r.target)
	if nodeName == "" {
		res.Passed = false
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Level: "error", Code: "E022_MALFORMED_REF",
			Message:  "ref expression has no target node: " + r.target,
			Location: location + r.path,
			Hint:     "use the form <node_name>.<output_name>",
		})
		return
	}
	if nodeName == "inputs" {
		// "inputs.<field>" refers to the skill's declared inputs schema.
		// Verify the field exists when the schema is a typed record.
		if outputName != "" && skill.Schemas.Inputs.Type == "record" && skill.Schemas.Inputs.Fields != nil {
			// Only the leading field component is on Schemas.Inputs.
			leading := outputName
			for i := 0; i < len(outputName); i++ {
				if outputName[i] == '.' {
					leading = outputName[:i]
					break
				}
			}
			if _, ok := skill.Schemas.Inputs.Fields[leading]; !ok {
				res.Passed = false
				res.Diagnostics = append(res.Diagnostics, Diagnostic{
					Level: "error", Code: "E025_REF_TO_UNDECLARED_INPUT_FIELD",
					Message:  "ref " + r.target + " reads input field " + leading + " which schemas.inputs does not declare",
					Location: location + r.path,
					Hint:     "declare " + leading + " under schemas.inputs.fields, or fix the ref",
				})
			}
		}
		return
	}
	producer, ok := skill.Graph.Nodes[nodeName]
	if !ok {
		res.Passed = false
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Level: "error", Code: "E023_REF_TO_UNKNOWN_NODE",
			Message:  "ref to unknown node: " + nodeName + " (in " + r.target + ")",
			Location: location + r.path,
			Hint:     "the producer node must exist in graph.nodes; check for a typo or a renamed step",
		})
		return
	}
	if outputName != "" && producer.Outputs != nil {
		if _, ok := producer.Outputs[outputName]; !ok {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E024_REF_TO_UNDECLARED_OUTPUT",
				Message:  "ref " + r.target + " points to output " + outputName + " which node " + nodeName + " does not declare",
				Location: location + r.path,
				Hint:     "declare the output in graph.nodes." + nodeName + ".outputs, or fix the ref to a declared output name",
			})
		}
	}
}

// splitRef splits "node.field.subfield" into ("node", "field.subfield").
// "node" alone returns ("node", ""). "" returns ("", "").
func splitRef(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// checkReturnType type-checks the skill's Return expression against
// skill.Schemas.Outputs. The HIGH-severity case: Return is a ref to a
// node output whose declared TypeSpec does not match Schemas.Outputs.
func checkReturnType(skill *ir.Skill, res *StageResult) {
	ret := skill.Graph.Return
	switch ret.Kind {
	case "":
		// Empty return is allowed; some skills only produce side effects.
		return
	case "ref", "field":
		nodeName, outputName := splitRef(ret.Ref)
		if nodeName == "" {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E022_MALFORMED_REF",
				Message:  "graph.return ref has no target: " + ret.Ref,
				Location: "graph.return.ref",
			})
			return
		}
		producer, ok := skill.Graph.Nodes[nodeName]
		if !ok {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E023_REF_TO_UNKNOWN_NODE",
				Message:  "graph.return references unknown node: " + nodeName,
				Location: "graph.return.ref",
			})
			return
		}
		if outputName == "" {
			// Whole-node ref. Without a per-kind registry we can't infer
			// the node's aggregate type, so we record info and stop.
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "info", Code: "I025_RETURN_WHOLE_NODE",
				Message: "graph.return is a whole-node ref; type-check deferred to runtime",
			})
			return
		}
		if producer.Outputs == nil {
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "info", Code: "I026_PRODUCER_UNTYPED",
				Message: "graph.return references " + ret.Ref + " but producer declares no Outputs; type-check deferred to runtime",
			})
			return
		}
		producerType, ok := producer.Outputs[outputName]
		if !ok {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E024_REF_TO_UNDECLARED_OUTPUT",
				Message:  "graph.return references " + ret.Ref + " but " + nodeName + " does not declare output " + outputName,
				Location: "graph.return.ref",
			})
			return
		}
		// HIGH-severity type-mismatch. We compare structurally. If the
		// declared types disagree we name both type strings and the
		// offending edge.
		if !typesCompatible(producerType, skill.Schemas.Outputs) {
			res.Passed = false
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level: "error", Code: "E027_RETURN_TYPE_MISMATCH",
				Message: "graph.return type mismatch: producer " + nodeName + "." + outputName +
					" declares " + describeType(producerType) +
					" but skill schemas.outputs is " + describeType(skill.Schemas.Outputs),
				Location: "graph.return.ref",
				Hint:     "either change the return ref, change the producer's declared output type, or change schemas.outputs to match",
			})
		}
	default:
		// literal / interp / sha256 — defer to runtime for now. Future
		// work: derive the literal's type and check against Schemas.Outputs.
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Level: "info", Code: "I028_RETURN_NON_REF",
			Message: "graph.return is a " + ret.Kind + " expression; analyzer defers type-check to runtime",
		})
	}
}

// typesCompatible reports whether a producer's declared TypeSpec is
// structurally compatible with a consumer's expected TypeSpec. This is
// a conservative check: when in doubt we say compatible (so we don't
// reject valid skills); but a clear scalar mismatch (string vs int) is
// flagged.
//
// Compatibility rules:
//   - If either side has Type == "" we cannot compare; return true.
//   - If both are scalars and Type strings differ, incompatible.
//   - record/list/map/optional/named compared structurally; missing
//     sub-fields on either side are skipped (not flagged here — that
//     is the schema stage's job).
func typesCompatible(producer, consumer ir.TypeSpec) bool {
	if producer.Type == "" || consumer.Type == "" {
		return true
	}
	if producer.Type != consumer.Type {
		return false
	}
	switch producer.Type {
	case "list":
		if producer.ElementType != nil && consumer.ElementType != nil {
			return typesCompatible(*producer.ElementType, *consumer.ElementType)
		}
	case "optional":
		if producer.ElementType != nil && consumer.ElementType != nil {
			return typesCompatible(*producer.ElementType, *consumer.ElementType)
		}
	case "map":
		if producer.KeyType != nil && consumer.KeyType != nil {
			if !typesCompatible(*producer.KeyType, *consumer.KeyType) {
				return false
			}
		}
		if producer.ValueType != nil && consumer.ValueType != nil {
			return typesCompatible(*producer.ValueType, *consumer.ValueType)
		}
	case "named":
		// Compare named refs by string. A producer's "Todo" is only
		// compatible with a consumer's "Todo".
		if producer.NamedRef != "" && consumer.NamedRef != "" &&
			producer.NamedRef != consumer.NamedRef {
			return false
		}
	case "record":
		// Field-by-field compare for fields present on BOTH sides. A
		// missing-field is structural divergence; the schema stage flags
		// undeclared field references separately.
		for name, pField := range producer.Fields {
			if cField, ok := consumer.Fields[name]; ok {
				if !typesCompatible(pField, cField) {
					return false
				}
			}
		}
	}
	return true
}

// describeType renders a TypeSpec as a short human-readable string for
// error messages. Not a round-trippable form — just enough for
// diagnostics ("record{x:string}", "list<int>", "named:Todo").
func describeType(t ir.TypeSpec) string {
	switch t.Type {
	case "":
		return "<unknown>"
	case "list":
		if t.ElementType != nil {
			return "list<" + describeType(*t.ElementType) + ">"
		}
		return "list"
	case "optional":
		if t.ElementType != nil {
			return "optional<" + describeType(*t.ElementType) + ">"
		}
		return "optional"
	case "map":
		k, v := "?", "?"
		if t.KeyType != nil {
			k = describeType(*t.KeyType)
		}
		if t.ValueType != nil {
			v = describeType(*t.ValueType)
		}
		return "map<" + k + "," + v + ">"
	case "named":
		if t.NamedRef != "" {
			return "named:" + t.NamedRef
		}
		return "named"
	case "record":
		// Render up to a few fields for readability; full schema is in
		// the IR if a caller wants it.
		var b []byte
		b = append(b, "record{"...)
		first := true
		for name, f := range t.Fields {
			if !first {
				b = append(b, ',')
			}
			first = false
			b = append(b, name...)
			b = append(b, ':')
			b = append(b, describeType(f)...)
		}
		b = append(b, '}')
		return string(b)
	}
	return t.Type
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
	"ask_user":        true,
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
// non-decidable contracts to runtime assertion injection by recording
// a RuntimeAssertion entry on the StageResult so downstream tooling
// (the runtime, the proof emitter) can install the matching guard.
func stageContract(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	for i, c := range skill.Contracts {
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
			// Non-decidable at compile time. Record the clause as a
			// runtime assertion so the runtime layer can install the
			// matching guard, and surface an info diagnostic that
			// points readers at the recorded record.
			ra := RuntimeAssertion{
				Kind:           c.Kind,
				SourceLocation: contractLocation(i),
			}
			switch c.Kind {
			case "wall_time_lt":
				ra.Bound = float64(c.Seconds)
			case "forall", "exists":
				ra.Predicate = string(c.Predicate)
			}
			res.RuntimeAssertions = append(res.RuntimeAssertions, ra)
			res.Diagnostics = append(res.Diagnostics, Diagnostic{
				Level:    "info",
				Code:     "I051_CONTRACT_DEFERRED_TO_RUNTIME",
				Message:  "deferred to runtime assertion (recorded on StageResult.RuntimeAssertions)",
				Location: ra.SourceLocation,
			})
		}
	}

	return res
}

// contractLocation renders a stable path-style location for a contract
// clause at index i. Used as the SourceLocation field of recorded
// RuntimeAssertion entries and as the Diagnostic.Location for the
// matching info diagnostic.
func contractLocation(i int) string {
	return "contracts[" + strconv.Itoa(i) + "]"
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
//
// Why this matters: a cyclic skill graph used to pass `analyze` and
// deadlock at runtime when the interpreter tried to evaluate a node
// whose inputs depend on its own (still-pending) output. Catching the
// cycle here gives the LLM-author a single clean error listing the
// nodes in the cycle.
//
// Algorithm: build the dependency graph from node-config refs (using
// the same Expr-walker as Stage 2), then run DFS with three-color
// marking. WHITE = unvisited; GRAY = on the current DFS stack;
// BLACK = fully explored. A WHITE -> GRAY edge means we hit a
// back-edge, which proves a cycle. We record the path of GRAY nodes
// from the cycle's entry to the offending edge so the diagnostic can
// list the exact loop.
func stageTermination(skill *ir.Skill, _ *Constitution) StageResult {
	res := StageResult{Passed: true}

	// (1) Coarse warning: very large graphs are a maintainability red
	// flag even if acyclic. Preserved from the skeleton.
	if len(skill.Graph.Nodes) > 1000 {
		res.Diagnostics = append(res.Diagnostics, Diagnostic{
			Level:   "warning",
			Code:    "W060_VERY_LARGE_GRAPH",
			Message: "graph has > 1000 nodes; consider decomposing into multiple skills",
		})
	}

	// (2) Build dependency edges. An edge A -> B means "B depends on
	// A's output" — i.e. B's config refs A. We extract refs from each
	// node's config; if a ref's target is a real node (not the
	// "inputs" pseudo-node and not unknown), that target is a
	// predecessor of the consumer.
	deps := buildDepGraph(skill)

	// (3) Three-color DFS. We iterate node names in deterministic
	// (sorted) order so a deterministic cycle is reported across runs.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(skill.Graph.Nodes))
	for name := range skill.Graph.Nodes {
		color[name] = white
	}
	names := sortedKeys(color)

	var (
		stack []string               // current DFS stack of GRAY nodes
		seen  = map[string]bool{}    // cycles already reported (avoid spam on shared back-edges)
		cycle func(name string) bool // returns true if a cycle was found rooted at name
	)
	cycle = func(name string) bool {
		color[name] = gray
		stack = append(stack, name)

		// deterministic edge order
		neighbors := append([]string(nil), deps[name]...)
		sortStrings(neighbors)

		for _, next := range neighbors {
			switch color[next] {
			case white:
				if cycle(next) {
					return true
				}
			case gray:
				// Back-edge: found a cycle. Slice the stack from
				// `next` to the end and report it.
				start := -1
				for i, s := range stack {
					if s == next {
						start = i
						break
					}
				}
				if start < 0 {
					// Shouldn't happen: a GRAY node must be on the stack.
					start = 0
				}
				path := append([]string(nil), stack[start:]...)
				path = append(path, next) // close the loop visually
				key := cycleKey(path)
				if !seen[key] {
					seen[key] = true
					res.Passed = false
					res.Diagnostics = append(res.Diagnostics, Diagnostic{
						Level:    "error",
						Code:     "E061_GRAPH_CYCLE",
						Message:  "graph contains a cycle: " + joinPath(path),
						Location: "graph.nodes." + path[0],
						Hint:     "remove one of the back-edges in the listed cycle; skill graphs must be DAGs",
					})
				}
				// Continue searching to surface independent cycles
				// elsewhere in the graph.
			case black:
				// Already fully explored — no new cycle through this edge.
			}
		}

		// Pop and mark BLACK.
		stack = stack[:len(stack)-1]
		color[name] = black
		return false
	}

	for _, name := range names {
		if color[name] == white {
			cycle(name)
		}
	}

	return res
}

// buildDepGraph returns adjacency list deps[A] = nodes that A points
// at via any config ref. We deliberately model edges as A -> deps[A]
// where deps[A] is the set of producers that A consumes. A cycle in
// this graph is a real evaluation cycle: A reads B reads A.
//
// Refs to the "inputs" pseudo-node and refs to unknown nodes are
// dropped here — Stage 2 already reports them. We only track edges
// between real nodes so cycle messages don't include pseudo-nodes.
func buildDepGraph(skill *ir.Skill) map[string][]string {
	deps := make(map[string][]string, len(skill.Graph.Nodes))
	for name := range skill.Graph.Nodes {
		deps[name] = nil
	}
	dedup := make(map[string]map[string]bool, len(skill.Graph.Nodes))
	for consumer, node := range skill.Graph.Nodes {
		refs := collectRefsFromConfig(node.Config)
		for _, r := range refs {
			producer, _ := splitRef(r.target)
			if producer == "" || producer == "inputs" {
				continue
			}
			if _, ok := skill.Graph.Nodes[producer]; !ok {
				continue
			}
			if dedup[consumer] == nil {
				dedup[consumer] = make(map[string]bool)
			}
			if dedup[consumer][producer] {
				continue
			}
			dedup[consumer][producer] = true
			deps[consumer] = append(deps[consumer], producer)
		}
	}
	return deps
}

// sortedKeys returns the keys of a string-keyed map in lexical order.
// Cycle detection uses this for deterministic output across runs.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// sortStrings is an in-place insertion sort. The IR limits skills to
// O(100) nodes in practice and the analyzer is sensitive to import
// surface; we avoid pulling sort/strings into the cycle-detector hot
// path and use a small hand-rolled sort instead.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// cycleKey collapses a path of node names to a canonical string so we
// can dedupe reports of the same cycle hit through different DFS
// starts. We rotate so the lexically smallest node is first.
func cycleKey(path []string) string {
	if len(path) == 0 {
		return ""
	}
	// Drop the trailing "close-the-loop" repeat for canonicalization.
	core := path
	if len(path) > 1 && path[0] == path[len(path)-1] {
		core = path[:len(path)-1]
	}
	min := 0
	for i := 1; i < len(core); i++ {
		if core[i] < core[min] {
			min = i
		}
	}
	rotated := make([]string, 0, len(core))
	rotated = append(rotated, core[min:]...)
	rotated = append(rotated, core[:min]...)
	return joinPath(rotated)
}

// joinPath renders ["a","b","c","a"] as "a -> b -> c -> a".
func joinPath(path []string) string {
	if len(path) == 0 {
		return ""
	}
	out := path[0]
	for i := 1; i < len(path); i++ {
		out += " -> " + path[i]
	}
	return out
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
