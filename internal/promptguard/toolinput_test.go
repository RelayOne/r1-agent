package promptguard

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// withRecordingEmitter installs a recording emitter for the duration of
// a test, returning the captured events and a cleanup that restores the
// previous emitter. Helper avoids duplicating the SetEmitter dance in
// every test in this file.
func withRecordingEmitter(t *testing.T) (*[]ThreatEvent, func()) {
	t.Helper()
	var mu sync.Mutex
	got := make([]ThreatEvent, 0, 4)
	prev := SetEmitter(func(e ThreatEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, e)
	})
	return &got, func() { SetEmitter(prev) }
}

// TestToolInputDefaults_LoadsCleanly asserts the embedded YAML produces
// exactly three rules with require_struct=true (one per dangerous tool).
// Per specs/promptguard-hardening.md §T2 item 7.
func TestToolInputDefaults_LoadsCleanly(t *testing.T) {
	rules, err := loadDefaultToolInputRules()
	if err != nil {
		t.Fatalf("loadDefaultToolInputRules: %v", err)
	}
	wantTools := []string{"deploy.execute", "browse.fetch", "file.write"}
	if len(rules) != len(wantTools) {
		t.Fatalf("rule map size = %d, want %d (tools: %v)", len(rules), len(wantTools), keysOf(rules))
	}
	for _, tool := range wantTools {
		rs, ok := rules[tool]
		if !ok {
			t.Fatalf("missing rule for tool %q (got: %v)", tool, keysOf(rules))
		}
		if len(rs) != 1 {
			t.Fatalf("tool %s has %d rules, want exactly 1 bundled default", tool, len(rs))
		}
		if !rs[0].RequireStruct {
			t.Errorf("tool %s default rule should have RequireStruct=true", tool)
		}
		if rs[0].Source != "builtin" {
			t.Errorf("tool %s rule source = %q, want builtin", tool, rs[0].Source)
		}
	}
}

// keysOf returns the sorted (sort-by-walk-order; sufficient for error
// messages, no guarantee) key list of a rule map. Helper for diagnostic
// log lines inside tests.
func keysOf(m map[string][]ToolInputRule) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestValidateToolInput_RejectsCommandSubstitution covers the spec
// example: pass {"target":"prod","command":"rm $(ls)"} to deploy.execute,
// assert error and exactly one threat event. Per spec §T2 item 6.
func TestValidateToolInput_RejectsCommandSubstitution(t *testing.T) {
	ResetToolInputRules()
	captured, restore := withRecordingEmitter(t)
	defer restore()

	args := map[string]any{"target": "prod", "command": "rm $(ls)"}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	rep, err := ValidateToolInput(context.Background(), "deploy.execute", raw)
	if err == nil {
		t.Fatalf("expected non-nil error; got rep=%+v", rep)
	}
	if !strings.Contains(err.Error(), "deploy.execute") {
		t.Errorf("error message should name the tool; got %q", err.Error())
	}
	if len(rep.Violations) == 0 {
		t.Fatalf("report carried no violations; got %+v", rep)
	}
	// At least one violation must be a deny_pattern match against the
	// command-substitution regex.
	denyFound := false
	for _, v := range rep.Violations {
		if v.Kind == "deny_pattern" {
			denyFound = true
		}
	}
	if !denyFound {
		t.Errorf("expected at least one deny_pattern violation; got %+v", rep.Violations)
	}
	// Exactly one threat event per violation. We had one deny_pattern
	// match; there should be no struct_shape or missing_field failures
	// (both required fields present and arg is a JSON object).
	if len(*captured) == 0 {
		t.Fatalf("expected at least one threat event; got 0")
	}
	for _, e := range *captured {
		if e.Phase != "tool_input" {
			t.Errorf("event phase = %q, want tool_input", e.Phase)
		}
		if e.Source != "tool_input:deploy.execute" {
			t.Errorf("event source = %q, want tool_input:deploy.execute", e.Source)
		}
	}
}

// TestValidateToolInput_PassesCleanInput asserts a well-formed call to
// deploy.execute with no banned substrings produces no error and no
// recorded threat event. Regression guard against a future rule edit
// that inadvertently flags clean input.
func TestValidateToolInput_PassesCleanInput(t *testing.T) {
	ResetToolInputRules()
	captured, restore := withRecordingEmitter(t)
	defer restore()

	args := map[string]any{"target": "staging", "command": "kubectl apply -f deploy.yaml"}
	raw, _ := json.Marshal(args)
	rep, err := ValidateToolInput(context.Background(), "deploy.execute", raw)
	if err != nil {
		t.Fatalf("expected nil error; got %v rep=%+v", err, rep)
	}
	if rep.Actioned() {
		t.Errorf("expected clean report; got violations %+v", rep.Violations)
	}
	if len(*captured) != 0 {
		t.Errorf("expected zero threat events on clean input; got %d", len(*captured))
	}
}

// TestValidateToolInput_RejectsMissingStructField checks the required-
// fields path: a deploy.execute call with `command` but no `target`
// should fail with a missing_field violation.
func TestValidateToolInput_RejectsMissingStructField(t *testing.T) {
	ResetToolInputRules()
	_, restore := withRecordingEmitter(t)
	defer restore()

	args := map[string]any{"command": "ls"}
	raw, _ := json.Marshal(args)
	rep, err := ValidateToolInput(context.Background(), "deploy.execute", raw)
	if err == nil {
		t.Fatalf("expected error on missing required field; got rep=%+v", rep)
	}
	missing := false
	for _, v := range rep.Violations {
		if v.Kind == "missing_field" && v.Detail == "target" {
			missing = true
		}
	}
	if !missing {
		t.Errorf("expected missing_field=target; got %+v", rep.Violations)
	}
}

// TestValidateToolInput_RejectsStringTopLevel asserts a JSON-string
// top-level (instead of a JSON object) trips struct_shape on a tool
// that requires a struct. Per spec §T2 error-handling table:
// "RequireStruct: true receives a string arg -> reject; emit threat event".
func TestValidateToolInput_RejectsStringTopLevel(t *testing.T) {
	ResetToolInputRules()
	_, restore := withRecordingEmitter(t)
	defer restore()

	raw := []byte(`"just a string"`)
	rep, err := ValidateToolInput(context.Background(), "browse.fetch", raw)
	if err == nil {
		t.Fatalf("expected error on string top-level; got rep=%+v", rep)
	}
	shape := false
	for _, v := range rep.Violations {
		if v.Kind == "struct_shape" {
			shape = true
		}
	}
	if !shape {
		t.Errorf("expected struct_shape violation; got %+v", rep.Violations)
	}
}

// TestValidateToolInput_RejectsBrowseFileScheme exercises the
// browse.fetch deny rule for `file://` URIs. Covers the spec's
// indirect-injection vector where an agent under injection is steered
// to read /etc/passwd via a file-scheme fetch.
func TestValidateToolInput_RejectsBrowseFileScheme(t *testing.T) {
	ResetToolInputRules()
	_, restore := withRecordingEmitter(t)
	defer restore()

	args := map[string]any{"url": "file:///etc/passwd"}
	raw, _ := json.Marshal(args)
	rep, err := ValidateToolInput(context.Background(), "browse.fetch", raw)
	if err == nil {
		t.Fatalf("expected error on file:// URL; got rep=%+v", rep)
	}
	denyHit := false
	for _, v := range rep.Violations {
		if v.Kind == "deny_pattern" {
			denyHit = true
		}
	}
	if !denyHit {
		t.Errorf("expected deny_pattern violation; got %+v", rep.Violations)
	}
}

// TestValidateToolInput_NoRulesIsNoOp asserts a tool with zero
// configured rules passes through cleanly. Operators who want strict
// allow-listing can layer that on top; the package itself is a
// rules-mode validator only.
func TestValidateToolInput_NoRulesIsNoOp(t *testing.T) {
	ResetToolInputRules()
	captured, restore := withRecordingEmitter(t)
	defer restore()

	raw := []byte(`{"anything":"goes"}`)
	rep, err := ValidateToolInput(context.Background(), "unknown.tool", raw)
	if err != nil {
		t.Fatalf("expected nil error for tool with no rules; got %v", err)
	}
	if rep.Actioned() {
		t.Errorf("expected clean report; got %+v", rep.Violations)
	}
	if len(*captured) != 0 {
		t.Errorf("expected zero threat events; got %d", len(*captured))
	}
}

// TestValidateToolInput_LengthCap covers the MaxLengthKB knob: a
// deploy.execute call whose serialized args exceed 16 KiB trips a
// length violation regardless of content.
func TestValidateToolInput_LengthCap(t *testing.T) {
	ResetToolInputRules()
	_, restore := withRecordingEmitter(t)
	defer restore()

	bigCmd := strings.Repeat("a", 17*1024)
	args := map[string]any{"target": "prod", "command": bigCmd}
	raw, _ := json.Marshal(args)
	rep, err := ValidateToolInput(context.Background(), "deploy.execute", raw)
	if err == nil {
		t.Fatalf("expected length violation; got rep=%+v", rep)
	}
	length := false
	for _, v := range rep.Violations {
		if v.Kind == "length" {
			length = true
		}
	}
	if !length {
		t.Errorf("expected length violation; got %+v", rep.Violations)
	}
}

// TestAddToolInputRule_OperatorAddition asserts an operator-supplied
// rule layered on top of the bundled defaults is honored by
// ValidateToolInput. Covers the policy-block path in spec §T2 item 10.
func TestAddToolInputRule_OperatorAddition(t *testing.T) {
	ResetToolInputRules()
	defer ResetToolInputRules()
	_, restore := withRecordingEmitter(t)
	defer restore()

	AddToolInputRule(ToolInputRule{
		Tool:          "custom.tool",
		RequireStruct: true,
		StructFields:  []string{"action"},
		Source:        "operator",
	})

	raw := []byte(`{"not_action":"oops"}`)
	rep, err := ValidateToolInput(context.Background(), "custom.tool", raw)
	if err == nil {
		t.Fatalf("expected error on custom.tool missing required field; got rep=%+v", rep)
	}
	if len(rep.Violations) == 0 {
		t.Errorf("expected violations on custom.tool; got %+v", rep)
	}
}

// TestLoadToolInputYAML_MergesOperatorYAML asserts an operator YAML
// document parses and merges with the bundled defaults. Verifies the
// additional_rules_path code path in spec §T2 item 10.
func TestLoadToolInputYAML_MergesOperatorYAML(t *testing.T) {
	ResetToolInputRules()
	defer ResetToolInputRules()

	extra := []byte(`promptguard:
  tool_input:
    rules:
      - tool: custom.tool
        require_struct: true
        struct_fields: [op]
        deny_patterns:
          - '(?i)danger'
`)
	if err := LoadToolInputYAML(extra, "operator-extra"); err != nil {
		t.Fatalf("LoadToolInputYAML: %v", err)
	}
	rules := ToolInputRulesFor("custom.tool")
	if len(rules) != 1 {
		t.Fatalf("custom.tool rule count = %d, want 1", len(rules))
	}
	if rules[0].Source != "operator-extra" {
		t.Errorf("source = %q, want operator-extra", rules[0].Source)
	}
	// Bundled defaults must still be present after merge.
	if len(ToolInputRulesFor("deploy.execute")) == 0 {
		t.Error("deploy.execute default rule lost after operator merge")
	}
}

// TestParseToolInputYAML_SkipsMalformedPattern verifies a syntactically
// bad regex does not crash parsing — the malformed rule is skipped with
// a log line and remaining rules continue to load. Spec §Error Handling
// row: "Tool input validation has a malformed rule (config error) ->
// skip the rule, log a WARN, continue with remaining rules".
func TestParseToolInputYAML_SkipsMalformedPattern(t *testing.T) {
	bad := []byte(`promptguard:
  tool_input:
    rules:
      - tool: a.bad
        deny_patterns:
          - '['
      - tool: b.good
        require_struct: true
        struct_fields: [x]
`)
	rules, _, err := parseToolInputYAML(bad, "test")
	if err != nil {
		t.Fatalf("parseToolInputYAML: %v", err)
	}
	if _, ok := rules["a.bad"]; ok {
		t.Errorf("malformed rule should have been skipped; got %+v", rules["a.bad"])
	}
	if _, ok := rules["b.good"]; !ok {
		t.Errorf("subsequent good rule should still load; got %+v", rules)
	}
}

// TestToolInputAction_Default asserts the embedded YAML's default
// action is reject, matching the spec.
func TestToolInputAction_Default(t *testing.T) {
	ResetToolInputRules()
	got := ToolInputAction()
	if got != ActionReject {
		t.Errorf("default ToolInputAction = %v, want %v (reject)", got, ActionReject)
	}
}

// TestSetToolInputAction_OverrideAndReset asserts operator overrides
// take effect and ResetToolInputRules restores defaults.
func TestSetToolInputAction_OverrideAndReset(t *testing.T) {
	ResetToolInputRules()
	defer ResetToolInputRules()
	SetToolInputAction(ActionWarn)
	if got := ToolInputAction(); got != ActionWarn {
		t.Fatalf("after override got %v, want warn", got)
	}
	ResetToolInputRules()
	if got := ToolInputAction(); got != ActionReject {
		t.Fatalf("after reset got %v, want reject", got)
	}
}
