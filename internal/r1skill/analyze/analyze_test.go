package analyze

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/r1skill/ir"
)

// validBaseSkill returns a skill that passes all stages. Subtests mutate
// it to test specific rejection conditions.
func validBaseSkill() ir.Skill {
	return ir.Skill{
		SchemaVersion: ir.SchemaVersion,
		SkillID:       "test",
		SkillVersion:  1,
		Lineage:       ir.Lineage{Kind: "human", AuthoredAt: time.Now().UTC()},
		Schemas: ir.Schemas{
			Inputs:  ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"x": {Type: "string"}}},
			Outputs: ir.TypeSpec{Type: "record", Fields: map[string]ir.TypeSpec{"y": {Type: "string"}}},
		},
		Graph: ir.Graph{
			Nodes: map[string]ir.Node{
				"identity": {
					Kind:   "pure_fn",
					Config: json.RawMessage(`{"registry_ref":"stdlib:identity"}`),
				},
			},
			Return: ir.Expr{Kind: "ref", Ref: "identity.output"},
		},
	}
}

// emptyConstitution returns a constitution that would never reject
// anything. Used as the baseline for tests that want to focus on a
// specific stage's logic.
func emptyConstitution() Constitution {
	return Constitution{Hash: "sha256:test_constitution"}
}

// strictConstitution returns the kind of constitution a regulated
// buyer would actually have. Used by the killer test below.
func strictConstitution() Constitution {
	return Constitution{
		ForbidShellPatterns: []string{
			"rm -rf /",
			"rm -rf $HOME",
			"sudo *",
		},
		ForbidFSWritePaths: []string{
			"r1.constitution.yaml",
			"policies/",
			".r1/teams/*/config.toml",
		},
		ForbidNetworkDomains: []string{
			"*.suspicious.tld",
			"data-exfil.example.com",
		},
		RequireLineageForLLMAuthored: true,
		DefaultCapsForLLMAuthored: ir.Capabilities{
			LLM: ir.LLMCap{BudgetUSD: 0.10, MaxCalls: 2},
		},
		Hash: "sha256:strict_constitution",
	}
}

// ─── happy path ──────────────────────────────────────────────────

func TestAnalyze_HappyPath(t *testing.T) {
	skill := validBaseSkill()
	proof, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if proof == nil {
		t.Fatal("nil proof on success")
	}
	if proof.IRHash == "" {
		t.Error("proof missing IR hash")
	}
	if proof.AnalyzerVersion != AnalyzerVersion {
		t.Errorf("analyzer version mismatch: %q", proof.AnalyzerVersion)
	}
	// Should have records for all 7 stages plus the proof emission stage
	if len(proof.Checks) < 7 {
		t.Errorf("expected at least 7 stage records, got %d", len(proof.Checks))
	}
}

func TestAnalyze_ProofIsHashStable(t *testing.T) {
	skill := validBaseSkill()
	proof1, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err != nil {
		t.Fatalf("first analysis: %v", err)
	}
	// Modify the timestamp and re-analyze; hash should be identical
	skill.Lineage.AuthoredAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	proof2, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err != nil {
		t.Fatalf("second analysis: %v", err)
	}
	if proof1.IRHash != proof2.IRHash {
		t.Errorf("IR hash should be timestamp-stable: %q vs %q", proof1.IRHash, proof2.IRHash)
	}
}

// ─── the killer demo: constitution-binding rejection ─────────────

// TestAnalyze_RejectsForbiddenShell is the demo that anchors the whole
// architecture. A skill that declares the right thing about itself
// (capabilities, lineage, schemas) but tries to use a constitution-
// forbidden shell pattern is rejected at compile time. The LLM that
// authored this skill never sees execution, never produces side effects,
// never reaches the runtime.
//
// This is the moment that no other agent system can match.
func TestAnalyze_RejectsForbiddenShell(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.Shell.AllowCommands = []string{"rm -rf /", "echo hello"}
	// Add a shell_exec node so the capability is actually used (otherwise
	// the analyzer's capability stage would also flag the unused cap)
	skill.Graph.Nodes["cleanup"] = ir.Node{
		Kind:   "shell_exec",
		Config: json.RawMessage(`{"cmd":"rm -rf /","cache_key":{"kind":"literal","value":"x"}}`),
	}

	proof, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject forbidden shell pattern")
	}
	ae, ok := err.(*AnalyzerError)
	if !ok {
		t.Fatalf("expected *AnalyzerError, got %T", err)
	}

	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E040_FORBIDDEN_SHELL_PATTERN" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected E040_FORBIDDEN_SHELL_PATTERN diagnostic, got: %v", ae.Diagnostics)
	}

	// Even on failure, proof should be partial-populated for diagnostic
	// surface.
	if proof == nil {
		t.Error("proof should be populated even on failure for surfacing diagnostics")
	}
}

func TestAnalyze_RejectsForbiddenFsWrite(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.FS.WritePaths = []string{"r1.constitution.yaml"}
	skill.Graph.Nodes["evil_write"] = ir.Node{
		Kind:   "fs_write",
		Config: json.RawMessage(`{"path":"r1.constitution.yaml","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject constitution-locked write")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E043_FORBIDDEN_FS_WRITE" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E043, got: %v", ae.Diagnostics)
	}
}

func TestAnalyze_RejectsForbiddenNetwork(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.Network.AllowDomains = []string{"data-exfil.example.com"}
	skill.Graph.Nodes["exfil"] = ir.Node{
		Kind:   "http_post",
		Config: json.RawMessage(`{"url":"https://data-exfil.example.com/upload","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject forbidden network domain")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E044_FORBIDDEN_NETWORK_DOMAIN" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E044, got: %v", ae.Diagnostics)
	}
}

// ─── LLM-authored discipline ─────────────────────────────────────

func TestAnalyze_LLMAuthored_RequiresMissionID(t *testing.T) {
	skill := validBaseSkill()
	skill.Lineage.Kind = "llm-authored"
	// missing mission_id and authoring_stance

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected rejection of llm-authored skill missing lineage details")
	}
	ae := err.(*AnalyzerError)
	codes := make(map[string]bool)
	for _, d := range ae.Diagnostics {
		codes[d.Code] = true
	}
	if !codes["E041_LINEAGE_MISSING_MISSION"] {
		t.Errorf("expected E041 (missing mission_id)")
	}
	if !codes["E042_LINEAGE_MISSING_STANCE"] {
		t.Errorf("expected E042 (missing authoring_stance)")
	}
}

func TestAnalyze_LLMAuthored_DefaultCapsWidening_Warns(t *testing.T) {
	skill := validBaseSkill()
	skill.Lineage.Kind = "llm-authored"
	skill.Lineage.MissionID = "MISSION-abc"
	skill.Lineage.AuthoringStance = "worker-1"
	// Try to declare a much higher LLM budget than the LLM-authored
	// default. Should produce a warning (HITL approval would be checked
	// at registry time).
	skill.Capabilities.LLM = ir.LLMCap{BudgetUSD: 5.00, MaxCalls: 50}

	proof, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err != nil {
		// Could pass or fail depending on whether other stages catch it.
		// The key thing is the warning is recorded.
		t.Logf("analysis produced error (may be expected from other stages): %v", err)
	}
	if proof == nil {
		t.Fatal("proof should be populated")
	}

	found := false
	for _, check := range proof.Checks {
		if check.Stage != "constitution" {
			continue
		}
		for _, d := range check.Diagnostics {
			if d.Code == "W046_LLM_AUTHORED_HIGH_BUDGET" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected W046 warning")
	}
}

// ─── replay determinism ──────────────────────────────────────────

func TestAnalyze_RejectsLLMCall_NoCacheKey(t *testing.T) {
	skill := validBaseSkill()
	skill.Capabilities.LLM = ir.LLMCap{BudgetUSD: 0.10, MaxCalls: 1}
	skill.Graph.Nodes["llm"] = ir.Node{
		Kind: "llm_call",
		// Note: NO cache_key
		Config: json.RawMessage(`{"model":"claude-haiku","system_prompt":"x"}`),
	}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected rejection of llm_call without cache_key")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E070_NO_CACHE_KEY" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E070, got: %v", ae.Diagnostics)
	}
}

// ─── capability conformance ──────────────────────────────────────

func TestAnalyze_RejectsHttpGet_NoNetworkCap(t *testing.T) {
	skill := validBaseSkill()
	// No network cap declared
	skill.Graph.Nodes["fetch"] = ir.Node{
		Kind:   "http_get",
		Config: json.RawMessage(`{"url":"https://example.com","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected rejection of http_get without network cap")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E030_HTTP_NO_NETWORK_CAP" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E030, got: %v", ae.Diagnostics)
	}
}

// ─── all-errors-collected mode ───────────────────────────────────

func TestAnalyze_CollectsAllErrors(t *testing.T) {
	skill := validBaseSkill()
	// Two distinct violations:
	skill.Lineage.Kind = "llm-authored"
	// (a) missing mission_id (constitution stage)
	// (b) http_get without network cap (capability stage)
	skill.Graph.Nodes["fetch"] = ir.Node{
		Kind:   "http_get",
		Config: json.RawMessage(`{"url":"https://example.com","cache_key":{"kind":"literal","value":"x"}}`),
	}

	_, err := Analyze(&skill, strictConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected error")
	}
	ae := err.(*AnalyzerError)

	codes := map[string]bool{}
	for _, d := range ae.Diagnostics {
		codes[d.Code] = true
	}

	if !codes["E041_LINEAGE_MISSING_MISSION"] {
		t.Errorf("expected E041 (lineage)")
	}
	if !codes["E030_HTTP_NO_NETWORK_CAP"] {
		t.Errorf("expected E030 (capability)")
	}
}

// ─── error rendering ─────────────────────────────────────────────

func TestAnalyzerError_Render(t *testing.T) {
	e := &AnalyzerError{
		Diagnostics: []Diagnostic{
			{Code: "E001_TEST", Message: "test message"},
		},
	}
	s := e.Error()
	if !strings.Contains(s, "E001_TEST") {
		t.Errorf("error should contain code: %q", s)
	}
	if !strings.Contains(s, "test message") {
		t.Errorf("error should contain message: %q", s)
	}
}

// ─── stage 2: type inference + edge type-check ───────────────────

// TestAnalyze_RejectsReturnTypeMismatch covers the HIGH-impact case
// from audit/scan-go-stubs.md item #3 (Stage 2): a skill whose return
// expression points at a producer's output of one type while
// schemas.outputs declares a different type. Before this fix, the
// analyzer let the skill through and the runtime produced corrupt
// outputs; after this fix the analyzer flags E027 with both type
// names and the offending edge.
func TestAnalyze_RejectsReturnTypeMismatch(t *testing.T) {
	skill := validBaseSkill()
	// schemas.outputs is record{y:string} (from validBaseSkill). Wire
	// up a producer whose declared output type is record{y:int} — a
	// clear mismatch the analyzer must surface.
	skill.Graph.Nodes = map[string]ir.Node{
		"compute": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity"}`),
			Outputs: map[string]ir.TypeSpec{
				"value": {
					Type: "record",
					Fields: map[string]ir.TypeSpec{
						"y": {Type: "int"},
					},
				},
			},
		},
	}
	skill.Graph.Return = ir.Expr{Kind: "ref", Ref: "compute.value"}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject return-type mismatch")
	}
	ae, ok := err.(*AnalyzerError)
	if !ok {
		t.Fatalf("expected *AnalyzerError, got %T", err)
	}
	found := false
	var msg string
	for _, d := range ae.Diagnostics {
		if d.Code == "E027_RETURN_TYPE_MISMATCH" {
			found = true
			msg = d.Message
			break
		}
	}
	if !found {
		t.Fatalf("expected E027_RETURN_TYPE_MISMATCH, got: %v", ae.Diagnostics)
	}
	// The error message must name both type names so the LLM-author
	// can fix the skill in one revision.
	if !strings.Contains(msg, "int") {
		t.Errorf("E027 message should name producer type (int): %q", msg)
	}
	if !strings.Contains(msg, "string") {
		t.Errorf("E027 message should name consumer type (string): %q", msg)
	}
	// And it must name the offending edge.
	if !strings.Contains(msg, "compute.value") {
		t.Errorf("E027 message should name the offending edge: %q", msg)
	}
}

// TestAnalyze_RejectsRefToUnknownNode covers the adjacent case: a
// return ref to a node that doesn't exist. Before this fix the
// analyzer accepted dangling refs.
func TestAnalyze_RejectsRefToUnknownNode(t *testing.T) {
	skill := validBaseSkill()
	skill.Graph.Return = ir.Expr{Kind: "ref", Ref: "ghost.value"}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject ref to unknown node")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E023_REF_TO_UNKNOWN_NODE" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E023, got: %v", ae.Diagnostics)
	}
}

// ─── stage 6: DAG / termination cycle detection ──────────────────

// TestAnalyze_RejectsThreeStepCycle covers the HIGH-impact case from
// audit/scan-go-stubs.md item #3 (Stage 6): a skill whose dependency
// graph contains a cycle. Before this fix the analyzer accepted the
// skill and the interpreter deadlocked at runtime; after, the
// analyzer rejects with E061 and lists the steps in the cycle.
func TestAnalyze_RejectsThreeStepCycle(t *testing.T) {
	skill := validBaseSkill()
	// Wire a -> b -> c -> a by referencing each other's outputs in
	// configs. The exact config schema isn't important; the analyzer's
	// ref-walker only looks for {"kind":"ref","ref":"..."} subtrees.
	skill.Graph.Nodes = map[string]ir.Node{
		"a": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity","arg":{"kind":"ref","ref":"c.out"}}`),
			Outputs: map[string]ir.TypeSpec{
				"out": {Type: "string"},
			},
		},
		"b": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity","arg":{"kind":"ref","ref":"a.out"}}`),
			Outputs: map[string]ir.TypeSpec{
				"out": {Type: "string"},
			},
		},
		"c": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity","arg":{"kind":"ref","ref":"b.out"}}`),
			Outputs: map[string]ir.TypeSpec{
				"out": {Type: "string"},
			},
		},
	}
	skill.Graph.Return = ir.Expr{Kind: "ref", Ref: "a.out"}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject a cyclic skill graph")
	}
	ae, ok := err.(*AnalyzerError)
	if !ok {
		t.Fatalf("expected *AnalyzerError, got %T", err)
	}

	var msg string
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E061_GRAPH_CYCLE" {
			found = true
			msg = d.Message
			break
		}
	}
	if !found {
		t.Fatalf("expected E061_GRAPH_CYCLE, got: %v", ae.Diagnostics)
	}
	// The cycle message must list every node in the loop so the
	// LLM-author can fix the dependency in one revision.
	for _, name := range []string{"a", "b", "c"} {
		if !strings.Contains(msg, name) {
			t.Errorf("E061 message should name node %q in cycle: %q", name, msg)
		}
	}
}

// TestAnalyze_RejectsSelfLoop is the degenerate cycle case: a single
// node whose config refs itself. The three-color DFS treats this as
// a back-edge to a GRAY ancestor and reports it like any other
// cycle.
func TestAnalyze_RejectsSelfLoop(t *testing.T) {
	skill := validBaseSkill()
	skill.Graph.Nodes = map[string]ir.Node{
		"loop": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity","arg":{"kind":"ref","ref":"loop.out"}}`),
			Outputs: map[string]ir.TypeSpec{
				"out": {Type: "string"},
			},
		},
	}
	skill.Schemas.Outputs = ir.TypeSpec{Type: "string"}
	skill.Graph.Return = ir.Expr{Kind: "ref", Ref: "loop.out"}

	_, err := Analyze(&skill, emptyConstitution(), DefaultOptions())
	if err == nil {
		t.Fatal("expected analyzer to reject self-looping node")
	}
	ae := err.(*AnalyzerError)
	found := false
	for _, d := range ae.Diagnostics {
		if d.Code == "E061_GRAPH_CYCLE" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E061 for self-loop, got: %v", ae.Diagnostics)
	}
}

// TestAnalyze_AcceptsAcyclicGraph is the negative companion: a
// linear A -> B -> C dependency must NOT be flagged.
func TestAnalyze_AcceptsAcyclicGraph(t *testing.T) {
	skill := validBaseSkill()
	skill.Graph.Nodes = map[string]ir.Node{
		"a": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity"}`),
			Outputs: map[string]ir.TypeSpec{
				"out": {Type: "string"},
			},
		},
		"b": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity","arg":{"kind":"ref","ref":"a.out"}}`),
			Outputs: map[string]ir.TypeSpec{
				"out": {Type: "string"},
			},
		},
		"c": {
			Kind:   "pure_fn",
			Config: json.RawMessage(`{"registry_ref":"stdlib:identity","arg":{"kind":"ref","ref":"b.out"}}`),
			Outputs: map[string]ir.TypeSpec{
				"out": {Type: "string"},
			},
		},
	}
	// schemas.outputs is record{y:string}; "c.out" is plain string.
	// Loosen schemas.outputs to string so this test focuses on the
	// cycle detector rather than re-tripping E027.
	skill.Schemas.Outputs = ir.TypeSpec{Type: "string"}
	skill.Graph.Return = ir.Expr{Kind: "ref", Ref: "c.out"}

	if _, err := Analyze(&skill, emptyConstitution(), DefaultOptions()); err != nil {
		// Cycle detector must not fire on a DAG. Other stages may emit
		// info diagnostics but no error from termination.
		ae, ok := err.(*AnalyzerError)
		if !ok {
			t.Fatalf("unexpected error type: %T", err)
		}
		for _, d := range ae.Diagnostics {
			if d.Code == "E061_GRAPH_CYCLE" {
				t.Errorf("acyclic graph wrongly flagged as cyclic: %v", d)
			}
		}
	}
}

// ─── stage 5: runtime-assertion injection ────────────────────────

// TestStageContract_EmitsRuntimeAssertions verifies that contracts
// the analyzer cannot decide statically — wall_time_lt, forall,
// exists — are recorded on StageResult.RuntimeAssertions so the
// runtime layer can install matching guards. Before this fix
// (audit/scan-go-stubs.md) stageContract emitted only an info
// diagnostic and dropped the clause on the floor; after this fix
// each non-decidable clause produces a structured RuntimeAssertion
// with kind, bound (for wall_time_lt), and predicate text (for
// forall/exists) preserved.
func TestStageContract_EmitsRuntimeAssertions(t *testing.T) {
	skill := validBaseSkill()
	skill.Contracts = []ir.Contract{
		{Kind: "wall_time_lt", Seconds: 30},
		{Kind: "forall", Binder: "r", Iter: "results", Predicate: json.RawMessage(`"all results valid"`)},
		{Kind: "exists", Binder: "r", Iter: "results", Predicate: json.RawMessage(`"result.ok==true"`)},
	}

	res := stageContract(&skill, nil)

	if !res.Passed {
		t.Fatalf("expected stageContract to pass with deferred contracts, got diagnostics: %v", res.Diagnostics)
	}
	if len(res.RuntimeAssertions) != 3 {
		t.Fatalf("expected 3 runtime assertions, got %d: %+v", len(res.RuntimeAssertions), res.RuntimeAssertions)
	}

	// Index by Kind for assertion clarity.
	byKind := make(map[string]RuntimeAssertion)
	for _, ra := range res.RuntimeAssertions {
		byKind[ra.Kind] = ra
	}
	for _, want := range []string{"wall_time_lt", "forall", "exists"} {
		if _, ok := byKind[want]; !ok {
			t.Errorf("missing runtime assertion of kind %q; got %+v", want, res.RuntimeAssertions)
		}
	}

	if got := byKind["wall_time_lt"].Bound; got != 30 {
		t.Errorf("wall_time_lt Bound = %v, want 30", got)
	}
	if pred := byKind["forall"].Predicate; !strings.Contains(pred, "all results valid") {
		t.Errorf("forall Predicate = %q, want it to contain %q", pred, "all results valid")
	}
	if pred := byKind["exists"].Predicate; !strings.Contains(pred, "result.ok==true") {
		t.Errorf("exists Predicate = %q, want it to contain %q", pred, "result.ok==true")
	}

	// SourceLocation should pin the clause back to its IR position so
	// the runtime injector can attribute failures.
	for kind, ra := range byKind {
		if ra.SourceLocation == "" {
			t.Errorf("runtime assertion %q has empty SourceLocation", kind)
		}
	}

	// The legacy info diagnostic must still be emitted (with an
	// updated message pointing readers at the recorded record) so
	// existing consumers that key off I051 keep working.
	infoCount := 0
	for _, d := range res.Diagnostics {
		if d.Code == "I051_CONTRACT_DEFERRED_TO_RUNTIME" {
			infoCount++
			if !strings.Contains(d.Message, "RuntimeAssertions") {
				t.Errorf("info diagnostic message %q should reference RuntimeAssertions", d.Message)
			}
		}
	}
	if infoCount != 3 {
		t.Errorf("expected 3 I051 info diagnostics, got %d", infoCount)
	}
}
