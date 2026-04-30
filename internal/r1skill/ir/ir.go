// Package ir defines the canonical Intermediate Representation for R1Skill
// DSL. This is the data the parser produces, the analyzer consumes, the
// interpreter executes, and the ledger persists.
//
// The IR is intentionally pure data: no methods that mutate, no hidden
// state, no embedded interfaces. This makes it trivially serializable
// to JSON, hashable for proof IDs, and safe to ship across processes
// (e.g. from a worker that authors a draft to a separate analyzer
// service that validates it).
//
// Wire format: stable JSON with explicit `schema_version` field.
// Forward compatibility: unknown fields are preserved on round-trip via
// json.RawMessage in `Extras`. Backward compatibility: missing fields
// default per the field-level documentation.
package ir

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the current IR wire version. Analyzer + interpreter
// require this to match exactly; mismatched versions go through the
// migration package (when it exists). For now: bump → break.
const SchemaVersion = 1

// Skill is the top-level IR object. One skill = one Skill struct.
type Skill struct {
	SchemaVersion int             `json:"schema_version"`
	SkillID       string          `json:"skill_id"`
	SkillVersion  int             `json:"skill_version"`
	Description   string          `json:"description,omitempty"`
	Authors       []string        `json:"authors,omitempty"`
	Lineage       Lineage         `json:"lineage"`
	Schemas       Schemas         `json:"schemas"`
	Capabilities  Capabilities    `json:"capabilities"`
	Contracts     []Contract      `json:"contracts,omitempty"`
	Graph         Graph           `json:"graph"`
	Tests         []Test          `json:"tests,omitempty"`
	Extras        json.RawMessage `json:"extras,omitempty"`
}

// Lineage records who/what authored this skill and the chain of
// authorship (for amendments).
type Lineage struct {
	Kind             string    `json:"kind"` // "human" | "llm-authored" | "llm-amended" | "imported"
	MissionID        string    `json:"mission_id,omitempty"`
	AuthoringStance  string    `json:"authoring_stance,omitempty"`
	ParentSkillID    string    `json:"parent_skill_id,omitempty"`
	AuthoredAt       time.Time `json:"authored_at"`
	ConstitutionHash string    `json:"constitution_hash,omitempty"`
	AnalyzerVersion  string    `json:"analyzer_version,omitempty"`
}

// Schemas declares input, output, and named user-defined types.
type Schemas struct {
	Inputs     TypeSpec            `json:"inputs"`
	Outputs    TypeSpec            `json:"outputs"`
	NamedTypes map[string]TypeSpec `json:"named_types,omitempty"`
}

// TypeSpec is a recursive type description. The discriminator is `Type`.
type TypeSpec struct {
	Type        string              `json:"type"`                   // "bool"|"int"|"int64"|"string"|"bytes"|"time"|"duration"|"record"|"list"|"map"|"enum"|"optional"|"named"
	Fields      map[string]TypeSpec `json:"fields,omitempty"`       // for record
	ElementType *TypeSpec           `json:"element_type,omitempty"` // for list, optional
	KeyType     *TypeSpec           `json:"key_type,omitempty"`     // for map
	ValueType   *TypeSpec           `json:"value_type,omitempty"`   // for map
	Values      []string            `json:"values,omitempty"`       // for enum
	NamedRef    string              `json:"named_ref,omitempty"`    // for named (e.g. "Todo")
	Validators  []Validator         `json:"validators,omitempty"`   // refinement validators
}

// Validator is a refinement constraint on a typed value.
type Validator struct {
	Kind    string `json:"kind"` // "regex" | "range" | "min_len" | "max_len" | "non_empty"
	Pattern string `json:"pattern,omitempty"`
	Min     *int64 `json:"min,omitempty"`
	Max     *int64 `json:"max,omitempty"`
}

// Capabilities is the contract surface declaring what effects the skill
// is permitted to produce. Empty / zero values mean "this capability is
// not available at all"; the analyzer rejects nodes that need a
// capability the skill didn't declare.
type Capabilities struct {
	Network NetworkCap `json:"network"`
	FS      FSCap      `json:"fs"`
	Shell   ShellCap   `json:"shell"`
	LLM     LLMCap     `json:"llm"`
	Ledger  LedgerCap  `json:"ledger"`
	Skill   SkillCap   `json:"skill"`
}

type NetworkCap struct {
	AllowDomains []string `json:"allow_domains,omitempty"`
	AllowMethods []string `json:"allow_methods,omitempty"`
}

type FSCap struct {
	ReadPaths  []string `json:"read_paths,omitempty"`
	WritePaths []string `json:"write_paths,omitempty"`
}

type ShellCap struct {
	AllowCommands []string `json:"allow_commands,omitempty"` // command patterns, e.g. "go test *", "git status"
}

type LLMCap struct {
	BudgetUSD     float64  `json:"budget_usd"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	MaxCalls      int      `json:"max_calls"`
}

type LedgerCap struct {
	ReadNodeTypes  []string `json:"read_node_types,omitempty"`
	WriteNodeTypes []string `json:"write_node_types,omitempty"`
}

type SkillCap struct {
	AllowedCallees []string `json:"allowed_callees,omitempty"`
}

// Contract is a declared invariant. The analyzer checks decidable
// subsets statically and emits runtime assertions for the rest.
type Contract struct {
	Kind      string          `json:"kind"`             // "forall" | "exists" | "wall_time_lt" | "actual_cost_lt" | "predicate"
	Binder    string          `json:"binder,omitempty"` // for forall/exists
	Iter      string          `json:"iter,omitempty"`   // for forall/exists; expression
	Predicate json.RawMessage `json:"predicate,omitempty"`
	Seconds   int             `json:"seconds,omitempty"` // for wall_time_lt
	USD       float64         `json:"usd,omitempty"`     // for actual_cost_lt
}

// Graph is the dataflow DAG. Nodes are keyed by name (the source-level
// identifier), edges are implicit in the input expressions of each node
// (an `Expr` referencing `othernode.field` creates an edge).
type Graph struct {
	Nodes  map[string]Node `json:"nodes"`
	Return Expr            `json:"return"` // expression yielding the skill's outputs
}

// Node is one step in the dataflow graph.
type Node struct {
	Kind    string              `json:"kind"`    // see primitive table in spec
	Config  json.RawMessage     `json:"config"`  // kind-specific config; the per-kind packages parse it
	Outputs map[string]TypeSpec `json:"outputs"` // declared output schema; analyzer verifies against config
}

// Expr is a typed expression. The discriminator is `Kind`.
type Expr struct {
	Kind  string          `json:"kind"`            // "literal" | "ref" | "interp" | "sha256" | "field"
	Value json.RawMessage `json:"value,omitempty"` // for literal
	Ref   string          `json:"ref,omitempty"`   // for ref/field, e.g. "fetch.body"
	Parts []Expr          `json:"parts,omitempty"` // for interp (string interpolation)
	Input *Expr           `json:"input,omitempty"` // for sha256
}

// Test is a property-based, golden, or fixture test the skill must pass
// during T1-T8 verification.
type Test struct {
	Kind               string          `json:"kind"` // "property" | "golden" | "fixture"
	Name               string          `json:"name"`
	FixtureInput       json.RawMessage `json:"fixture_input,omitempty"`
	Generators         json.RawMessage `json:"generators,omitempty"` // for property tests
	ExpectPredicate    json.RawMessage `json:"expect_predicate,omitempty"`
	ReplayRecordingRef string          `json:"replay_recording_ref,omitempty"`
	ExpectMatchRef     string          `json:"expect_match_ref,omitempty"`
}

// Validate is a shallow structural check. The full analyzer (in package
// analyze) does the deep work; Validate catches obvious malformations
// at parse time so we don't run analysis on garbage.
func (s *Skill) Validate() error {
	if s.SchemaVersion != SchemaVersion {
		return wrapErr("schema_version mismatch: got %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if s.SkillID == "" {
		return wrapErr("skill_id required")
	}
	if s.SkillVersion < 1 {
		return wrapErr("skill_version must be >= 1")
	}
	if s.Lineage.Kind == "" {
		return wrapErr("lineage.kind required")
	}
	if !validLineageKinds[s.Lineage.Kind] {
		return wrapErr("lineage.kind %q invalid (valid: human, llm-authored, llm-amended, imported)", s.Lineage.Kind)
	}
	if len(s.Graph.Nodes) == 0 {
		return wrapErr("graph must have at least one node")
	}
	return nil
}

var validLineageKinds = map[string]bool{
	"human":        true,
	"llm-authored": true,
	"llm-amended":  true,
	"imported":     true,
}

// IRError is the structural error type used throughout the IR package.
// Returned by Validate and helpers.
type IRError struct {
	Msg string
}

func (e *IRError) Error() string { return "r1skill/ir: " + e.Msg }

func wrapErr(format string, args ...any) error {
	return &IRError{Msg: sprintf(format, args...)}
}

// sprintf is a tiny fmt.Sprintf that doesn't pull the fmt package's
// reflection cost into the IR's hot path. (The parser produces many IR
// objects; minimizing import cost matters for analyzer pipeline latency.)
func sprintf(format string, args ...any) string {
	// Defer to fmt.Sprintf; the optimization is theoretical until profiling
	// shows it matters. Keeping the function pointer indirection so we can
	// swap implementations later without changing callers.
	return fmtSprintf(format, args...)
}
