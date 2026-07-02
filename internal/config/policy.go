package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/RelayOne/r1/internal/mcp"
	"github.com/RelayOne/r1/internal/promptguard"
)

// PromptGuardConfig is the operator-facing per-phase action knob for
// the promptguard package. See specs/promptguard-hardening.md §T1. Each
// phase carries one Action ("warn"|"strip"|"reject"); missing entries
// fall through to the spec defaults (plan=strip, execute=warn,
// verify=strip, convergence=strip).
//
// ToolInput is the per-tool input-validation block added in §T2 item 10.
// Carries an action knob, an optional list of operator-defined custom
// rules, and an optional path to a sidecar YAML carrying additional
// rules. Bundled defaults (three rules for deploy.execute, browse.fetch,
// file.write) ship via //go:embed inside the promptguard package and
// load unconditionally at startup; operator overrides merge on top.
type PromptGuardConfig struct {
	Plan        PromptGuardPhaseConfig     `json:"plan,omitempty" yaml:"plan,omitempty"`
	Execute     PromptGuardPhaseConfig     `json:"execute,omitempty" yaml:"execute,omitempty"`
	Verify      PromptGuardPhaseConfig     `json:"verify,omitempty" yaml:"verify,omitempty"`
	Convergence PromptGuardPhaseConfig     `json:"convergence,omitempty" yaml:"convergence,omitempty"`
	ToolInput   PromptGuardToolInputConfig `json:"tool_input,omitempty" yaml:"tool_input,omitempty"`
}

// PromptGuardToolInputConfig is the per-tool input-validation block.
// See specs/promptguard-hardening.md §T2 item 10. Schema:
//
//	tool_input:
//	  action: warn|strip|reject (default reject)
//	  rules:
//	    - tool: <name>
//	      require_struct: true|false
//	      struct_fields: [a, b]
//	      max_length_kb: <int>
//	      deny_patterns:
//	        - '<regex>'
//	  additional_rules_path: /path/to/extra.yaml
type PromptGuardToolInputConfig struct {
	// Action is "warn" | "strip" | "reject"; empty means "use the spec
	// default" (reject). Validated via promptguard.ParseAction at apply
	// time so an unrecognised string falls back to the default rather
	// than erroring out the policy load.
	Action string `json:"action,omitempty" yaml:"action,omitempty"`
	// Rules carries operator-supplied custom rules. Each merges with
	// the bundled defaults at apply time. An empty slice leaves the
	// bundled rules untouched.
	Rules []PromptGuardToolInputRule `json:"rules,omitempty" yaml:"rules,omitempty"`
	// AdditionalRulesPath is an optional sidecar YAML file with the
	// same schema as configs/promptguard-toolinput-defaults.yaml. If
	// set, loaded at apply time and merged after Rules.
	AdditionalRulesPath string `json:"additional_rules_path,omitempty" yaml:"additional_rules_path,omitempty"`
}

// PromptGuardToolInputRule is the YAML-on-disk shape of one custom
// rule. The fields mirror promptguard.ToolInputRule but carry the
// deny-pattern list as raw strings (compiled at apply time).
type PromptGuardToolInputRule struct {
	Tool          string   `json:"tool" yaml:"tool"`
	RequireStruct bool     `json:"require_struct,omitempty" yaml:"require_struct,omitempty"`
	StructFields  []string `json:"struct_fields,omitempty" yaml:"struct_fields,omitempty"`
	MaxLengthKB   int      `json:"max_length_kb,omitempty" yaml:"max_length_kb,omitempty"`
	DenyPatterns  []string `json:"deny_patterns,omitempty" yaml:"deny_patterns,omitempty"`
}

// PromptGuardPhaseConfig carries one phase's promptguard knobs.
// Action is "warn" | "strip" | "reject"; an empty string means "use the
// spec default for this phase".
type PromptGuardPhaseConfig struct {
	Action string `json:"action,omitempty" yaml:"action,omitempty"`
}

// ResolveAction returns the promptguard.Action to apply at the named
// phase. Honors the operator's per-phase override when set; falls
// through to the spec default (plan=strip, execute=warn, verify=strip,
// convergence=strip) when missing or unrecognized.
func (c *PromptGuardConfig) ResolveAction(phase string) promptguard.Action {
	if c == nil {
		return promptguard.PhaseAction(phase)
	}
	var raw string
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "plan":
		raw = c.Plan.Action
	case "execute":
		raw = c.Execute.Action
	case "verify":
		raw = c.Verify.Action
	case "convergence":
		raw = c.Convergence.Action
	default:
		return promptguard.PhaseAction(phase)
	}
	if a, ok := promptguard.ParseAction(raw); ok {
		return a
	}
	return promptguard.PhaseAction(phase)
}

// Apply installs the operator's per-phase overrides on the
// process-wide promptguard phase-action map so wire-sites that call
// promptguard.PhaseAction(...) see the operator's settings without
// having to thread the policy through every call signature.
//
// Also applies the per-tool input-validation overrides from c.ToolInput
// (specs/promptguard-hardening.md §T2 item 10): the embedded defaults
// are restored, then operator-supplied rules are merged on top, then
// any additional_rules_path sidecar YAML is loaded and merged.
func (c *PromptGuardConfig) Apply() {
	if c == nil {
		promptguard.ResetPhaseActions()
		promptguard.ResetToolInputRules()
		return
	}
	promptguard.ResetPhaseActions()
	for phase, raw := range map[string]string{
		"plan":        c.Plan.Action,
		"execute":     c.Execute.Action,
		"verify":      c.Verify.Action,
		"convergence": c.Convergence.Action,
	} {
		if a, ok := promptguard.ParseAction(raw); ok {
			promptguard.SetPhaseAction(phase, a)
		}
	}
	c.applyToolInput()
}

// applyToolInput installs the per-tool input-validation overrides on
// the package-wide promptguard rule registry. Steps:
//
//  1. Restore embedded defaults (so a subsequent reload starts clean).
//  2. If c.ToolInput.Action is a valid action string, override the
//     default action.
//  3. For each rule in c.ToolInput.Rules: compile its deny patterns
//     and register via promptguard.AddToolInputRule. A bad regex is
//     skipped with a log line so a single malformed entry does not
//     fail the entire policy load.
//  4. If c.ToolInput.AdditionalRulesPath is set, read the file and
//     merge via promptguard.LoadToolInputYAML.
func (c *PromptGuardConfig) applyToolInput() {
	promptguard.ResetToolInputRules()
	if a, ok := promptguard.ParseAction(c.ToolInput.Action); ok {
		promptguard.SetToolInputAction(a)
	}
	for _, r := range c.ToolInput.Rules {
		if strings.TrimSpace(r.Tool) == "" {
			continue
		}
		compiled := make([]*regexp.Regexp, 0, len(r.DenyPatterns))
		skip := false
		for _, p := range r.DenyPatterns {
			re, err := regexp.Compile(p)
			if err != nil {
				skip = true
				break
			}
			compiled = append(compiled, re)
		}
		if skip {
			continue
		}
		promptguard.AddToolInputRule(promptguard.ToolInputRule{
			Tool:          r.Tool,
			DenyPatterns:  compiled,
			MaxLengthKB:   r.MaxLengthKB,
			RequireStruct: r.RequireStruct,
			StructFields:  append([]string(nil), r.StructFields...),
			Source:        "policy",
		})
	}
	if path := strings.TrimSpace(c.ToolInput.AdditionalRulesPath); path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			// best-effort load: a missing or unreadable sidecar file
			// does not fail the policy load — it just means no extra
			// rules are merged. The error path is documented in the
			// spec's error-handling table.
			_ = promptguard.LoadToolInputYAML(data, "policy:"+path)
		}
	}
}

// Policy defines the security and tool restrictions for each workflow phase.
type Policy struct {
	Phases       map[string]PhasePolicy `json:"phases"`
	Files        FilesPolicy            `json:"files"`
	Verification VerificationPolicy     `json:"verification"`
	Skills       SkillsConfig           `json:"skills"`
	Honesty      HonestyConfig          `json:"honesty"`
	// MCPServers is the list of MCP servers the registry should
	// construct from stoke.policy.yaml. Parsed by the yaml.v3-backed
	// loader in mcp_servers.go; validated by ValidateMCPServers.
	MCPServers []mcp.ServerConfig `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`

	// Cortex carries per-Lobe enable / privacy flags loaded from the
	// top-level `cortex:` block in r1.policy.yaml. Parsed by the
	// yaml.v3-backed loader in schema.go; the custom line-scanner in
	// parsePolicyYAML skips the `cortex:` section the same way it skips
	// `mcp_servers:`. See specs/cortex-concerns.md item 3.
	Cortex CortexConfig `json:"cortex,omitempty" yaml:"cortex,omitempty"`

	// Throttling carries per-tool rate-limit policy loaded from the
	// top-level `throttling:` block in r1.policy.yaml. Parsed by the
	// yaml.v3-backed loader in throttling.go; the custom line-scanner
	// skips the section like `mcp_servers:` / `cortex:`. See
	// specs/per-tool-throttling.md item T1.
	Throttling ThrottlingConfig `json:"throttling,omitempty" yaml:"throttling,omitempty"`
	// PromptGuard carries per-phase action knobs for the promptguard
	// package. See specs/promptguard-hardening.md §T1. Parsed inline by
	// the custom line-scanner in parsePolicyYAML (section nesting:
	// promptguard.<phase>.action).
	PromptGuard PromptGuardConfig `json:"promptguard,omitempty" yaml:"promptguard,omitempty"`

	// Governance carries the V2 governance-layer enable switch loaded from
	// the top-level `governance:` block in r1.policy.yaml. Parsed by the
	// yaml.v3-backed loader in governance_config.go; the custom line-scanner
	// in parsePolicyYAML skips the `governance:` section the same way it
	// skips `mcp_servers:` / `cortex:`. Default OFF — when disabled the
	// governance Governor is never constructed and has zero runtime impact.
	Governance GovernanceConfig `json:"governance,omitempty" yaml:"governance,omitempty"`

	// verificationExplicit is true when the YAML/JSON had a verification section.
	// This distinguishes "all gates intentionally disabled" from "section omitted."
	verificationExplicit bool

	// governanceExplicit is true when the YAML/JSON had a governance section.
	// Mirrors verificationExplicit: it distinguishes "operator explicitly set
	// governance.enabled (possibly to false)" from "section omitted." When the
	// section is omitted, normalizePolicy defaults Governance.Enabled to true.
	governanceExplicit bool
}

// HonestyConfig controls the 7-layer Honesty Judge.
type HonestyConfig struct {
	Enabled            bool   `json:"enabled" yaml:"enabled"`
	CheckImports       bool   `json:"check_imports" yaml:"check_imports"`
	HiddenTestDir      string `json:"hidden_test_dir,omitempty" yaml:"hidden_test_dir"`
	HiddenTestCommand  string `json:"hidden_test_command,omitempty" yaml:"hidden_test_command"`
	ClaimDecomposition bool   `json:"claim_decomposition" yaml:"claim_decomposition"`
	CoTMonitoring      bool   `json:"cot_monitoring" yaml:"cot_monitoring"`
	Confession         bool   `json:"confession" yaml:"confession"`
	JudgeModel         string `json:"judge_model,omitempty" yaml:"judge_model"`
}

// DefaultHonestyConfig returns the default honesty configuration.
func DefaultHonestyConfig() HonestyConfig {
	return HonestyConfig{
		Enabled:       true,
		CheckImports:  true,
		CoTMonitoring: true,
	}
}

// GovernanceConfig controls the V2 governance layer (bus + ledger +
// supervisor). Default OFF: when Enabled is false the runtime constructs
// no Governor and the change is zero-impact.
type GovernanceConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// DefaultGovernanceConfig returns the bare governance configuration value
// with Enabled:false. NOTE: this is the explicit-off constructor for callers
// that want the governance layer disabled. It is NOT the parse-path default.
// When a policy is loaded via LoadPolicy/AutoLoadPolicy and the `governance:`
// section is omitted, normalizePolicy flips Governance.Enabled to true (the
// V2 governance layer is default-ON), mirroring the verificationExplicit
// "absent vs explicit-false" trick. Setting `governance:\n  enabled: false`
// in the policy honors the off intent (governanceExplicit becomes true).
func DefaultGovernanceConfig() GovernanceConfig {
	return GovernanceConfig{Enabled: false}
}

// SkillsConfig controls skill injection behavior.
type SkillsConfig struct {
	Enabled      bool     `json:"enabled"`       // master switch (default true)
	AlwaysOn     []string `json:"always_on"`     // skill names always injected
	AutoDetect   bool     `json:"auto_detect"`   // run skillselect to detect repo stack
	TokenBudget  int      `json:"token_budget"`  // max tokens for injection (default 3000)
	ResearchFeed bool     `json:"research_feed"` // auto-update skills from research store
	Excluded     []string `json:"excluded"`      // skill names to never load
}

// DefaultSkillsConfig returns the recommended skills configuration.
func DefaultSkillsConfig() SkillsConfig {
	return SkillsConfig{
		Enabled:      true,
		AutoDetect:   true,
		TokenBudget:  3000,
		ResearchFeed: true,
		AlwaysOn:     []string{"agent-discipline"},
	}
}

// PhasePolicy specifies the builtin tools, allow/deny rules, and MCP access for a single workflow phase.
type PhasePolicy struct {
	BuiltinTools []string `json:"builtin_tools"`
	DeniedRules  []string `json:"denied_rules"`
	AllowedRules []string `json:"allowed_rules"`
	MCPEnabled   bool     `json:"mcp_enabled"`
}

// FilesPolicy lists file patterns that agents are not allowed to modify.
type FilesPolicy struct {
	Protected []string `json:"protected"`
}

// VerificationPolicy controls which verification steps (build, test, lint, review, scope) are required.
type VerificationPolicy struct {
	Build            bool `json:"build"`
	Tests            bool `json:"tests"`
	Lint             bool `json:"lint"`
	CrossModelReview bool `json:"cross_model_review"`
	ScopeCheck       bool `json:"scope_check"`
}

// DefaultPolicy returns the built-in policy with plan/execute/verify phases and standard protections.
func DefaultPolicy() Policy {
	return Policy{
		Phases: map[string]PhasePolicy{
			"plan": {
				BuiltinTools: []string{"Read", "Glob", "Grep"},
				AllowedRules: []string{"Read", "Glob", "Grep"},
				DeniedRules:  []string{},
				MCPEnabled:   false,
			},
			"execute": {
				BuiltinTools: []string{"Read", "Edit", "Write", "Bash", "Glob", "Grep"},
				AllowedRules: []string{"Read", "Edit", "Bash(npm test:*)", "Bash(npm run lint:*)", "Bash(npm run build:*)", "Bash(cat *)", "Bash(grep *)", "Bash(git status)", "Bash(git diff *)"},
				DeniedRules:  []string{"Bash(rm -rf *)", "Bash(git push *)", "Bash(git reset --hard *)", "Bash(git rebase *)", "Bash(sudo *)", "Bash(curl *)", "Bash(wget *)"},
				MCPEnabled:   true,
			},
			"verify": {
				BuiltinTools: []string{"Read", "Glob", "Grep", "Bash"},
				AllowedRules: []string{"Read", "Bash(npm test:*)", "Bash(npm run lint:*)"},
				DeniedRules:  []string{"Edit", "Write", "Bash(rm *)", "Bash(git *)"},
				MCPEnabled:   false,
			},
		},
		Files:        FilesPolicy{Protected: []string{".claude/", ".stoke/", "CLAUDE.md", ".env*", "r1.policy.yaml"}},
		Verification: VerificationPolicy{Build: true, Tests: true, Lint: true, CrossModelReview: true, ScopeCheck: true},
		Skills:       DefaultSkillsConfig(),
	}
}

// DefaultPolicyYAML returns the default policy serialized as a YAML string for use as a starter template.
func DefaultPolicyYAML() string {
	return `phases:
  plan:
    builtin_tools: [Read, Glob, Grep]
    denied_rules: []
    allowed_rules: [Read, Glob, Grep]
    mcp_enabled: false

  execute:
    builtin_tools: [Read, Edit, Write, Bash, Glob, Grep]
    denied_rules:
      - "Bash(rm -rf *)"
      - "Bash(git push *)"
      - "Bash(git reset --hard *)"
      - "Bash(git rebase *)"
      - "Bash(sudo *)"
      - "Bash(curl *)"
      - "Bash(wget *)"
    allowed_rules:
      - "Read"
      - "Edit"
      - "Bash(npm test:*)"
      - "Bash(npm run lint:*)"
      - "Bash(npm run build:*)"
      - "Bash(cat *)"
      - "Bash(grep *)"
      - "Bash(git status)"
      - "Bash(git diff *)"
    mcp_enabled: true

  verify:
    builtin_tools: [Read, Glob, Grep, Bash]
    denied_rules:
      - "Edit"
      - "Write"
      - "Bash(rm *)"
      - "Bash(git *)"
    allowed_rules:
      - "Read"
      - "Bash(npm test:*)"
      - "Bash(npm run lint:*)"
    mcp_enabled: false

files:
  protected: [".claude/", ".stoke/", "CLAUDE.md", ".env*", "r1.policy.yaml"]

verification:
  build: required
  tests: required
  lint: required
  cross_model_review: required
  scope_check: required
`
}

// LoadPolicy reads a policy from the given path (JSON or YAML), returning DefaultPolicy if path is empty.
func LoadPolicy(path string) (Policy, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultPolicy(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var p Policy
		if err = json.Unmarshal(raw, &p); err != nil {
			return Policy{}, err
		}
		// Detect whether the JSON had a verification / governance section.
		var probe struct {
			Verification *json.RawMessage `json:"verification"`
			Governance   *json.RawMessage `json:"governance"`
		}
		json.Unmarshal(raw, &probe)
		p.verificationExplicit = probe.Verification != nil
		p.governanceExplicit = probe.Governance != nil
		return normalizePolicy(p), nil
	}
	p, err := parsePolicyYAML(trimmed)
	if err != nil {
		return Policy{}, err
	}
	// Parse the mcp_servers top-level block (if any) via yaml.v3; the
	// custom line-scanner above skips its contents.
	servers, err := parseMCPServersBlock(raw)
	if err != nil {
		return Policy{}, err
	}
	if len(servers) > 0 {
		if verr := ValidateMCPServers(servers); verr != nil {
			return Policy{}, verr
		}
		p.MCPServers = servers
	}
	// Parse the cortex top-level block (if any) via yaml.v3; the
	// custom line-scanner above skips its contents.
	cortex, err := parseCortexBlock(raw)
	if err != nil {
		return Policy{}, err
	}
	p.Cortex = cortex
	// Parse the throttling top-level block (if any) via yaml.v3 and
	// validate immediately so malformed rate strings / globs surface
	// at LoadPolicy time, not at first Allow call. See
	// specs/per-tool-throttling.md T1.
	throttling, err := parseThrottlingBlock(raw)
	if err != nil {
		return Policy{}, err
	}
	if !throttling.IsZero() {
		normalized, verr := ValidateThrottling(throttling)
		if verr != nil {
			return Policy{}, verr
		}
		p.Throttling = normalized
	}
	// Parse the promptguard.tool_input block (if any) via yaml.v3;
	// the custom line-scanner above skips its contents. See
	// specs/promptguard-hardening.md §T2 item 10.
	toolInput, err := parsePromptGuardToolInputBlock(raw)
	if err != nil {
		return Policy{}, err
	}
	p.PromptGuard.ToolInput = toolInput
	// Parse the governance top-level block (if any) via yaml.v3; the
	// custom line-scanner above skips its contents. Absent block yields
	// the zero GovernanceConfig (Enabled:false) — the safe default.
	governance, governancePresent, err := parseGovernanceBlock(raw)
	if err != nil {
		return Policy{}, err
	}
	p.Governance = governance
	p.governanceExplicit = governancePresent
	return normalizePolicy(p), nil
}

// policySearchNames are the filenames checked (in order) when auto-discovering
// a policy file from the repo root.
var policySearchNames = []string{
	"stoke.yaml",
	"stoke.yml",
	".stoke.yaml",
	".stoke.yml",
	"r1.policy.yaml",
	"stoke.policy.yaml",
	"stoke.policy.yml",
}

// AutoLoadPolicy discovers and loads a policy file from the repo root.
// If explicitPath is non-empty, it is used directly (same as LoadPolicy).
// Otherwise, searches for well-known filenames in repoRoot.
// Returns DefaultPolicy if no file is found.
func AutoLoadPolicy(repoRoot, explicitPath string) (Policy, error) {
	if strings.TrimSpace(explicitPath) != "" {
		return LoadPolicy(explicitPath)
	}
	for _, name := range policySearchNames {
		candidate := filepath.Join(repoRoot, name)
		if _, err := os.Stat(candidate); err == nil {
			return LoadPolicy(candidate)
		}
	}
	return DefaultPolicy(), nil
}

func normalizePolicy(p Policy) Policy {
	d := DefaultPolicy()
	if p.Phases == nil {
		p.Phases = d.Phases
	}
	for name, phase := range d.Phases {
		current, ok := p.Phases[name]
		if !ok {
			p.Phases[name] = phase
			continue
		}
		if current.BuiltinTools == nil {
			current.BuiltinTools = phase.BuiltinTools
		}
		if current.DeniedRules == nil {
			current.DeniedRules = phase.DeniedRules
		}
		if current.AllowedRules == nil {
			current.AllowedRules = phase.AllowedRules
		}
		p.Phases[name] = current
	}
	if p.Files.Protected == nil {
		p.Files = d.Files
	}
	// Only restore default verification if the section was never explicitly provided.
	// If a user wrote verification: with all fields false, honor that intent.
	if !p.verificationExplicit && !p.Verification.Build && !p.Verification.Tests && !p.Verification.Lint && !p.Verification.CrossModelReview && !p.Verification.ScopeCheck {
		p.Verification = d.Verification
	}
	// Governance defaults ON when the section was never explicitly provided.
	// If a user wrote governance: with enabled: false, honor that intent
	// (governanceExplicit is true and this branch is skipped). Mirrors the
	// verificationExplicit "absent vs explicit-false" trick above.
	if !p.governanceExplicit {
		p.Governance.Enabled = true
	}
	// Apply skill defaults if not explicitly configured
	if p.Skills.TokenBudget == 0 {
		p.Skills = d.Skills
	}
	return p
}

func parsePolicyYAML(input string) (Policy, error) {
	p := Policy{Phases: map[string]PhasePolicy{}}
	scanner := bufio.NewScanner(strings.NewReader(input))
	section := ""
	currentPhase := ""
	currentListField := ""
	// promptguard sub-section state — tracks which phase block
	// (plan|execute|verify|convergence) the scanner is currently
	// inside so a 4-space-indented `action: <val>` line can be routed
	// to the right PromptGuardPhaseConfig field.
	pgPhase := ""

	for scanner.Scan() {
		raw := scanner.Text()
		line := stripComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		text := strings.TrimSpace(line)

		switch {
		case indent == 0 && strings.HasSuffix(text, ":"):
			section = strings.TrimSuffix(text, ":")
			currentPhase = ""
			currentListField = ""
			pgPhase = ""
		case section == "mcp_servers":
			// mcp_servers is a yaml-sequence block; the custom
			// scanner skips all its contents (any indent >0) and
			// leaves parsing to parseMCPServersBlock (yaml.v3).
			continue
		case section == "cortex":
			// cortex is a nested-map block (cortex.lobes.<name>.<flags>);
			// the custom scanner skips all its contents (any indent >0)
			// and leaves parsing to parseCortexBlock (yaml.v3).
			// See specs/cortex-concerns.md item 3.
			continue
		case section == "throttling":
			// throttling is a nested-map block
			// (throttling.{defaults,tools,overrides}); the custom scanner
			// skips its contents and leaves parsing to
			// parseThrottlingBlock (yaml.v3). See
			// specs/per-tool-throttling.md T1.
			continue
		case section == "governance":
			// governance is a small nested-map block (governance.enabled);
			// the custom scanner skips its contents and leaves parsing to
			// parseGovernanceBlock (yaml.v3). Mirrors the cortex / throttling
			// skip pattern above.
			continue
		case section == "phases" && indent == 2 && strings.HasSuffix(text, ":"):
			currentPhase = strings.TrimSuffix(text, ":")
			currentListField = ""
			if _, ok := p.Phases[currentPhase]; !ok {
				p.Phases[currentPhase] = PhasePolicy{}
			}
		case section == "phases" && indent == 4:
			key, val, ok := splitKV(text)
			if !ok {
				return Policy{}, fmt.Errorf("invalid phases line: %q", raw)
			}
			currentListField = ""
			phase := p.Phases[currentPhase]
			switch key {
			case "builtin_tools":
				phase.BuiltinTools = parseListValue(val)
			case "denied_rules":
				if val == "" {
					currentListField = key
				} else {
					phase.DeniedRules = parseListValue(val)
				}
			case "allowed_rules":
				if val == "" {
					currentListField = key
				} else {
					phase.AllowedRules = parseListValue(val)
				}
			case "mcp_enabled":
				phase.MCPEnabled = parseBoolLike(val)
			default:
				return Policy{}, fmt.Errorf("unknown phase key %q", key)
			}
			p.Phases[currentPhase] = phase
		case section == "phases" && indent == 6 && strings.HasPrefix(text, "- "):
			phase := p.Phases[currentPhase]
			item := unquote(strings.TrimSpace(strings.TrimPrefix(text, "- ")))
			switch currentListField {
			case "denied_rules":
				phase.DeniedRules = append(phase.DeniedRules, item)
			case "allowed_rules":
				phase.AllowedRules = append(phase.AllowedRules, item)
			default:
				return Policy{}, fmt.Errorf("list item without list field: %q", raw)
			}
			p.Phases[currentPhase] = phase
		case section == "files" && indent == 2:
			key, val, ok := splitKV(text)
			if !ok {
				return Policy{}, fmt.Errorf("invalid files line: %q", raw)
			}
			if key != "protected" {
				return Policy{}, fmt.Errorf("unknown files key %q", key)
			}
			if val == "" {
				// Block-style list follows (what the wizard emits);
				// items are consumed by the case below.
				currentListField = "protected"
			} else {
				currentListField = ""
				p.Files.Protected = parseListValue(val)
			}
		case section == "files" && indent >= 4 && strings.HasPrefix(text, "- "):
			if currentListField != "protected" {
				return Policy{}, fmt.Errorf("list item without list field: %q", raw)
			}
			item := unquote(strings.TrimSpace(strings.TrimPrefix(text, "- ")))
			p.Files.Protected = append(p.Files.Protected, item)
		case section == "promptguard" && indent == 2 && strings.HasSuffix(text, ":"):
			pgPhase = strings.TrimSuffix(text, ":")
		case section == "promptguard" && pgPhase == "tool_input":
			// tool_input is a nested-map block (action + rules + path).
			// The custom scanner skips every line below `tool_input:`
			// (any indent >2) and leaves parsing to
			// parsePromptGuardToolInputBlock (yaml.v3). Mirrors the
			// mcp_servers and cortex skip patterns above. See
			// specs/promptguard-hardening.md §T2 item 10.
			continue
		case section == "promptguard" && indent == 4:
			key, val, ok := splitKV(text)
			if !ok {
				return Policy{}, fmt.Errorf("invalid promptguard line: %q", raw)
			}
			if key != "action" {
				return Policy{}, fmt.Errorf("unknown promptguard key %q (only 'action' is supported)", key)
			}
			cfg := PromptGuardPhaseConfig{Action: unquote(val)}
			switch pgPhase {
			case "plan":
				p.PromptGuard.Plan = cfg
			case "execute":
				p.PromptGuard.Execute = cfg
			case "verify":
				p.PromptGuard.Verify = cfg
			case "convergence":
				p.PromptGuard.Convergence = cfg
			default:
				return Policy{}, fmt.Errorf("unknown promptguard phase %q (want plan|execute|verify|convergence)", pgPhase)
			}
		case section == "verification" && indent == 2:
			key, val, ok := splitKV(text)
			if !ok {
				return Policy{}, fmt.Errorf("invalid verification line: %q", raw)
			}
			p.verificationExplicit = true
			parsed := parseBoolLike(val)
			switch key {
			case "build":
				p.Verification.Build = parsed
			case "tests":
				p.Verification.Tests = parsed
			case "lint":
				p.Verification.Lint = parsed
			case "cross_model_review":
				p.Verification.CrossModelReview = parsed
			case "scope_check":
				p.Verification.ScopeCheck = parsed
			default:
				return Policy{}, fmt.Errorf("unknown verification key %q", key)
			}
		default:
			return Policy{}, fmt.Errorf("unsupported policy structure at line %q", raw)
		}
	}
	if err := scanner.Err(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func stripComment(line string) string {
	inQuote := false
	var quote rune
	var out strings.Builder
	escaped := false
	for _, r := range line {
		if escaped {
			out.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && inQuote {
			escaped = true
			out.WriteRune(r)
			continue
		}
		if (r == '\'' || r == '"') && (!inQuote || r == quote) {
			if inQuote && r == quote {
				inQuote = false
			} else if !inQuote {
				inQuote = true
				quote = r
			}
		}
		if r == '#' && !inQuote {
			break
		}
		out.WriteRune(r)
	}
	return strings.TrimRight(out.String(), " ")
}

func leadingSpaces(s string) int {
	count := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

func splitKV(s string) (string, string, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func parseListValue(v string) []string {
	v = strings.TrimSpace(v)
	if v == "[]" || v == "" {
		return []string{}
	}
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]"))
		if inner == "" {
			return []string{}
		}
		parts := strings.Split(inner, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			out = append(out, unquote(strings.TrimSpace(part)))
		}
		return out
	}
	return []string{unquote(v)}
}

func parseBoolLike(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "true", "yes", "required", "on":
		return true
	case "false", "no", "off", "optional":
		return false
	default:
		b, _ := strconv.ParseBool(v)
		return b
	}
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
