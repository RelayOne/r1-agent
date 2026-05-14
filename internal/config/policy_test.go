package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/promptguard"
)

func TestVerificationAllDisabledHonored(t *testing.T) {
	dir := t.TempDir()
	yaml := `phases:
  plan:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
  execute:
    builtin_tools: [Read, Edit]
    denied_rules: []
    allowed_rules: [Read, Edit]
  verify:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
files:
  protected: []
verification:
  build: false
  tests: false
  lint: false
  cross_model_review: false
  scope_check: false
`
	path := filepath.Join(dir, "stoke.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	// All gates should be false — not restored to defaults.
	if p.Verification.Build {
		t.Error("Build should be false when explicitly disabled")
	}
	if p.Verification.Tests {
		t.Error("Tests should be false when explicitly disabled")
	}
	if p.Verification.Lint {
		t.Error("Lint should be false when explicitly disabled")
	}
	if p.Verification.CrossModelReview {
		t.Error("CrossModelReview should be false when explicitly disabled")
	}
	if p.Verification.ScopeCheck {
		t.Error("ScopeCheck should be false when explicitly disabled")
	}
}

func TestVerificationOmittedGetsDefaults(t *testing.T) {
	dir := t.TempDir()
	// YAML with no verification section at all
	yaml := `phases:
  plan:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
  execute:
    builtin_tools: [Read, Edit]
    denied_rules: []
    allowed_rules: [Read, Edit]
  verify:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
files:
  protected: []
`
	path := filepath.Join(dir, "stoke.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	// When verification section is omitted, defaults should apply.
	def := DefaultPolicy()
	if p.Verification != def.Verification {
		t.Errorf("omitted verification should get defaults: got %+v, want %+v", p.Verification, def.Verification)
	}
}

func TestAutoLoadPolicyDiscovers(t *testing.T) {
	dir := t.TempDir()
	// Write a policy file with a well-known name
	path := filepath.Join(dir, "stoke.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	// AutoLoadPolicy with empty explicit path should discover it
	p, err := AutoLoadPolicy(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Phases["execute"].BuiltinTools) == 0 {
		t.Fatal("expected auto-discovered policy to have execute builtin tools")
	}
}

func TestAutoLoadPolicyFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	// No policy file exists
	p, err := AutoLoadPolicy(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	// Should get default policy
	def := DefaultPolicy()
	if len(p.Phases) != len(def.Phases) {
		t.Errorf("expected default policy phases, got %d vs %d", len(p.Phases), len(def.Phases))
	}
}

func TestAutoLoadPolicyExplicitOverrides(t *testing.T) {
	dir := t.TempDir()
	// Write a policy at a non-standard name
	explicit := filepath.Join(dir, "custom-policy.yaml")
	if err := os.WriteFile(explicit, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Also write stoke.yaml (should be ignored when explicit is given)
	if err := os.WriteFile(filepath.Join(dir, "stoke.yaml"), []byte("invalid yaml {{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Explicit path should take precedence
	p, err := AutoLoadPolicy(dir, explicit)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Phases["execute"].BuiltinTools) == 0 {
		t.Fatal("expected explicit policy to load correctly")
	}
}

func TestLoadPolicyYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Phases["execute"].BuiltinTools) == 0 {
		t.Fatalf("expected execute builtin tools to be loaded")
	}
	if !p.Verification.Build || !p.Verification.ScopeCheck {
		t.Fatalf("expected verification flags to parse as required=true")
	}
}

// TestPolicyParse_PromptGuardBlock covers the operator-facing per-phase
// promptguard knobs (specs/promptguard-hardening.md §T1 item 1) — both
// the omitted-block fallthrough and an explicit override.
func TestPolicyParse_PromptGuardBlock(t *testing.T) {
	// Case 1: no promptguard block — every phase falls through to its
	// spec default (plan=strip, execute=warn, verify=strip,
	// convergence=strip) via ResolveAction.
	emptyDir := t.TempDir()
	emptyPath := filepath.Join(emptyDir, "r1.policy.yaml")
	if err := os.WriteFile(emptyPath, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(emptyPath)
	if err != nil {
		t.Fatalf("default policy load: %v", err)
	}
	if p.PromptGuard.Plan.Action != "" {
		t.Errorf("omitted plan action should be empty; got %q", p.PromptGuard.Plan.Action)
	}
	// Default fallthrough surfaces as the spec defaults via ResolveAction.
	gotPlan := p.PromptGuard.ResolveAction("plan").String()
	if gotPlan != "strip" {
		t.Errorf("omitted plan -> ResolveAction = %s, want strip", gotPlan)
	}
	gotExec := p.PromptGuard.ResolveAction("execute").String()
	if gotExec != "warn" {
		t.Errorf("omitted execute -> ResolveAction = %s, want warn", gotExec)
	}

	// Case 2: explicit override.
	yaml := DefaultPolicyYAML() + `
promptguard:
  plan:
    action: reject
  execute:
    action: strip
  verify:
    action: warn
  convergence:
    action: reject
`
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	p2, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("override policy load: %v", err)
	}
	if got := p2.PromptGuard.ResolveAction("plan").String(); got != "reject" {
		t.Errorf("plan override -> %s, want reject", got)
	}
	if got := p2.PromptGuard.ResolveAction("execute").String(); got != "strip" {
		t.Errorf("execute override -> %s, want strip", got)
	}
	if got := p2.PromptGuard.ResolveAction("verify").String(); got != "warn" {
		t.Errorf("verify override -> %s, want warn", got)
	}
	if got := p2.PromptGuard.ResolveAction("convergence").String(); got != "reject" {
		t.Errorf("convergence override -> %s, want reject", got)
	}
}

// TestPolicyParse_ToolInputBlock covers the operator-facing per-tool
// input-validation block (specs/promptguard-hardening.md §T2 item 10).
// Fixture YAML carries one custom rule for `custom.tool`; we assert
// the rule round-trips through the parser and merges with the bundled
// defaults at Apply time.
func TestPolicyParse_ToolInputBlock(t *testing.T) {
	yaml := DefaultPolicyYAML() + `
promptguard:
  tool_input:
    action: warn
    rules:
      - tool: custom.tool
        require_struct: true
        struct_fields: [op]
        max_length_kb: 8
        deny_patterns:
          - '(?i)danger'
`
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	ti := p.PromptGuard.ToolInput
	if ti.Action != "warn" {
		t.Errorf("ToolInput.Action = %q, want warn", ti.Action)
	}
	if len(ti.Rules) != 1 {
		t.Fatalf("ToolInput.Rules len = %d, want 1; rules=%+v", len(ti.Rules), ti.Rules)
	}
	r := ti.Rules[0]
	if r.Tool != "custom.tool" {
		t.Errorf("rule tool = %q, want custom.tool", r.Tool)
	}
	if !r.RequireStruct {
		t.Errorf("rule RequireStruct = false, want true")
	}
	if len(r.StructFields) != 1 || r.StructFields[0] != "op" {
		t.Errorf("rule StructFields = %v, want [op]", r.StructFields)
	}
	if r.MaxLengthKB != 8 {
		t.Errorf("rule MaxLengthKB = %d, want 8", r.MaxLengthKB)
	}
	if len(r.DenyPatterns) != 1 || r.DenyPatterns[0] != "(?i)danger" {
		t.Errorf("rule DenyPatterns = %v, want [(?i)danger]", r.DenyPatterns)
	}
}

// TestPolicyParse_ToolInputBlock_OmittedFallthrough asserts an absent
// tool_input block leaves PromptGuard.ToolInput zero-valued so the
// promptguard package's bundled defaults remain effective.
func TestPolicyParse_ToolInputBlock_OmittedFallthrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if p.PromptGuard.ToolInput.Action != "" {
		t.Errorf("omitted block ToolInput.Action = %q, want empty", p.PromptGuard.ToolInput.Action)
	}
	if len(p.PromptGuard.ToolInput.Rules) != 0 {
		t.Errorf("omitted block ToolInput.Rules len = %d, want 0", len(p.PromptGuard.ToolInput.Rules))
	}
}

// TestPolicyParse_ToolInputBlock_Apply asserts Apply merges the
// operator-supplied rule into the promptguard package's live rule
// registry alongside the three bundled defaults.
func TestPolicyParse_ToolInputBlock_Apply(t *testing.T) {
	yaml := DefaultPolicyYAML() + `
promptguard:
  tool_input:
    action: warn
    rules:
      - tool: custom.tool
        require_struct: true
        struct_fields: [op]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	defer promptguard.ResetToolInputRules()
	p.PromptGuard.Apply()
	if got := promptguard.ToolInputAction(); got != promptguard.ActionWarn {
		t.Errorf("after Apply, ToolInputAction = %v, want warn", got)
	}
	tools, rules := promptguard.ToolInputRuleCount()
	// 3 bundled + 1 operator-added = 4 tools total. Rules count is the
	// total across all tools (bundled = 3, operator = 1) = 4.
	if tools != 4 {
		t.Errorf("after Apply, tool count = %d, want 4 (3 bundled + 1 operator)", tools)
	}
	if rules != 4 {
		t.Errorf("after Apply, total rules = %d, want 4", rules)
	}
}
