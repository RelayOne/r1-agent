// Package promptguard — toolinput.go
//
// Per-tool input validation. The existing Scan/Sanitize path scans free-form
// text BEFORE it is concatenated into a prompt. This file closes a different
// vector: input passed to tool calls. Tool calls have structured schemas, so
// we can do stricter validation than free-form scanning: length caps,
// require-struct-shape checks, required-field checks, and per-tool deny-list
// regexes.
//
// See specs/promptguard-hardening.md §T2 items 6-10.
//
// The bundled default rules ship via //go:embed at
// internal/promptguard/configs/promptguard-toolinput-defaults.yaml and cover
// three dangerous tools: deploy.execute (require {target, command} + reject
// rm -rf, curl|wget chaining, command substitution), browse.fetch (require
// {url} + reject file://, jar:, data:), and file.write (require
// {path, contents} + reject path traversal + reject writes to
// /etc/passwd|shadow|sudoers).
//
// Operators add or override rules via the PromptGuard.ToolInput block in
// r1.policy.yaml. The action on any violation is operator-configurable
// (action: warn | strip | reject) but defaults to reject because structured
// input is more clearly bounded than free text — a violation is more
// obviously malicious.
package promptguard

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ToolInputRule names a single per-tool input-validation rule. Fields
// mirror the YAML schema in
// internal/promptguard/configs/promptguard-toolinput-defaults.yaml.
type ToolInputRule struct {
	// Tool is the tool name this rule applies to, e.g. "browse.fetch".
	Tool string
	// DenyPatterns is the list of per-tool regex patterns. Any match in
	// the serialized arg blob trips the rule.
	DenyPatterns []*regexp.Regexp
	// MaxLengthKB is the max serialized-JSON arg length in KiB. 0 means
	// unlimited.
	MaxLengthKB int
	// RequireStruct, when true, requires the top-level arg to be a JSON
	// object (not a string, array, number, etc.). A string-shaped arg
	// fails the check.
	RequireStruct bool
	// StructFields lists fields that MUST be present when RequireStruct
	// is true. Missing fields trip the rule.
	StructFields []string
	// Source is a free-form provenance tag, e.g. "builtin" or an
	// operator-supplied registry name. Set automatically by the loader.
	Source string
}

// ToolInputReport summarises one ValidateToolInput call. Mirrors the
// shape of Report from promptguard.go so operator log parsing stays
// uniform across the two surfaces.
type ToolInputReport struct {
	// Tool is the tool name the validator was invoked against.
	Tool string
	// Violations is one entry per failed sub-check (length, struct shape,
	// required field, deny pattern). Empty slice means the input passed.
	Violations []ToolInputViolation
	// Action is the disposition the caller applied (warn|strip|reject).
	// Set by the caller for log lines; ValidateToolInput itself does not
	// apply the action (that is the call-site's responsibility — see
	// internal/mcp/r1_server.go and internal/agentloop/loop.go hooks).
	Action Action
}

// Actioned returns true if any violation was recorded. Mirrors the
// SanitizationReport.Actioned() helper from agentloop/sanitize.go so
// log-site callers can predicate on a single method.
func (r ToolInputReport) Actioned() bool {
	return len(r.Violations) > 0
}

// Summary returns a single-line description of the report, safe for log
// inclusion. Mirrors Report.Summary().
func (r ToolInputReport) Summary() string {
	if !r.Actioned() {
		return fmt.Sprintf("promptguard: tool_input %s clean", r.Tool)
	}
	names := make([]string, 0, len(r.Violations))
	for _, v := range r.Violations {
		names = append(names, v.Kind)
	}
	return fmt.Sprintf("promptguard: tool_input %s — %d violation(s) [%s] action=%s",
		r.Tool, len(r.Violations), strings.Join(names, ","), actionName(r.Action))
}

// ToolInputViolation is one detected failure inside ValidateToolInput.
type ToolInputViolation struct {
	// Kind names the failed sub-check:
	// "length" | "struct_shape" | "missing_field" | "deny_pattern".
	Kind string
	// Detail is a human-readable detail message, e.g. the offending
	// field name or pattern name.
	Detail string
	// Excerpt is the matched substring (for deny_pattern violations),
	// capped at 120 chars. Empty for length / struct_shape violations.
	Excerpt string
	// PatternName is the deny-pattern's index in the rule (for
	// deny_pattern violations only). Empty for other kinds.
	PatternName string
}

// String renders one violation as a single-line human-readable phrase.
// Used in error messages produced by ValidateToolInput.
func (v ToolInputViolation) String() string {
	switch v.Kind {
	case "length":
		return v.Detail
	case "struct_shape":
		return "top-level arg must be JSON object"
	case "missing_field":
		return "missing required field: " + v.Detail
	case "deny_pattern":
		if v.Excerpt == "" {
			return "deny pattern matched: " + v.Detail
		}
		return "deny pattern matched (" + v.Detail + "): " + v.Excerpt
	default:
		return v.Kind + ": " + v.Detail
	}
}

// toolInputState carries the package-wide configured rule set and its
// default-action policy. Built at init time from the embedded YAML; an
// operator override via SetToolInputAction or AddToolInputRule replaces
// the relevant fields atomically.
type toolInputState struct {
	// rules is keyed by tool name; each tool may have multiple rules
	// (typically one bundled-default + zero or more operator-added).
	rules map[string][]ToolInputRule
	// action is the disposition applied on any violation. Defaults to
	// ActionReject per spec; operators flip via SetToolInputAction.
	action Action
}

var (
	toolInputMu    sync.RWMutex
	toolInputCurr  *toolInputState
	toolInputInit  sync.Once
)

// defaultToolInputYAML is the bundled YAML rule set. Embedded so the
// binary ships self-contained — no filesystem lookup at startup.
//
//go:embed configs/promptguard-toolinput-defaults.yaml
var defaultToolInputYAML []byte

// toolInputYAMLSchema mirrors the on-disk YAML structure for unmarshal.
// Kept private so external callers go through ToolInputRule directly.
type toolInputYAMLSchema struct {
	PromptGuard struct {
		ToolInput struct {
			Action string                  `yaml:"action"`
			Rules  []toolInputRuleYAMLBody `yaml:"rules"`
		} `yaml:"tool_input"`
	} `yaml:"promptguard"`
}

// toolInputRuleYAMLBody is the on-disk shape of a single rule. Distinct
// from ToolInputRule because the YAML carries patterns as strings, which
// we compile at load time.
type toolInputRuleYAMLBody struct {
	Tool          string   `yaml:"tool"`
	RequireStruct bool     `yaml:"require_struct"`
	StructFields  []string `yaml:"struct_fields"`
	MaxLengthKB   int      `yaml:"max_length_kb"`
	DenyPatterns  []string `yaml:"deny_patterns"`
}

// ensureToolInputInit lazily loads the embedded defaults the first time
// ValidateToolInput (or one of the accessors) is called. Idempotent.
func ensureToolInputInit() {
	toolInputInit.Do(func() {
		rules, action, err := parseToolInputYAML(defaultToolInputYAML, "builtin")
		if err != nil {
			// Embedded YAML failed to parse — fall back to empty
			// ruleset rather than panicking. Tests can recover via
			// ResetToolInputRules. Surfaced via log so operators see
			// the misconfiguration on daemon startup.
			log.Printf("promptguard: embedded tool-input defaults failed to load: %v", err)
			toolInputCurr = &toolInputState{rules: map[string][]ToolInputRule{}, action: ActionReject}
			return
		}
		toolInputCurr = &toolInputState{rules: rules, action: action}
	})
}

// parseToolInputYAML parses one YAML document into the in-memory rule
// map plus its default action. The source tag is stamped onto every
// rule for operator-side audit views.
func parseToolInputYAML(data []byte, source string) (map[string][]ToolInputRule, Action, error) {
	if len(data) == 0 {
		return map[string][]ToolInputRule{}, ActionReject, nil
	}
	var doc toolInputYAMLSchema
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, ActionReject, fmt.Errorf("parse tool-input yaml: %w", err)
	}
	out := make(map[string][]ToolInputRule)
	for _, r := range doc.PromptGuard.ToolInput.Rules {
		tool := strings.TrimSpace(r.Tool)
		if tool == "" {
			// Skip malformed entries with empty tool name; per spec
			// error-handling table: "log a WARN, continue with remaining
			// rules". We log here for operator visibility.
			log.Printf("promptguard: skipping malformed tool-input rule with empty tool name (source=%s)", source)
			continue
		}
		compiled := make([]*regexp.Regexp, 0, len(r.DenyPatterns))
		skip := false
		for _, p := range r.DenyPatterns {
			re, err := regexp.Compile(p)
			if err != nil {
				log.Printf("promptguard: skipping malformed tool-input rule for %s (source=%s): bad pattern %q: %v", tool, source, p, err)
				skip = true
				break
			}
			compiled = append(compiled, re)
		}
		if skip {
			continue
		}
		out[tool] = append(out[tool], ToolInputRule{
			Tool:          tool,
			DenyPatterns:  compiled,
			MaxLengthKB:   r.MaxLengthKB,
			RequireStruct: r.RequireStruct,
			StructFields:  append([]string(nil), r.StructFields...),
			Source:        source,
		})
	}
	action := ActionReject
	if a, ok := ParseAction(doc.PromptGuard.ToolInput.Action); ok {
		action = a
	}
	return out, action, nil
}

// ValidateToolInput inspects rawArgs (serialized JSON the model emitted
// for this tool call) against every configured rule for toolName. Each
// rule runs four sub-checks in order:
//
//  1. Length: if MaxLengthKB > 0 and len(rawArgs) > MaxLengthKB*1024,
//     record a "length" violation.
//  2. Struct shape: if RequireStruct, the rawArgs MUST json.Unmarshal
//     into a map[string]any. A string, array, or number top-level fails.
//  3. Required fields: if RequireStruct, every StructFields[i] MUST be
//     present (json.Unmarshal exposed key).
//  4. Deny patterns: every compiled regex runs against the full
//     serialized arg blob. Any match records a "deny_pattern" violation
//     carrying the matched excerpt for the audit log.
//
// On any violation:
//   - A promptguard.threat_detected event is emitted via Emit with
//     Phase="tool_input", Source="tool_input:"+toolName, one event per
//     violation (so the per-session budget tracker sees per-rule weight).
//   - A non-nil error is returned carrying a one-line description.
//   - The returned ToolInputReport carries the full violation list for
//     caller-side logging.
//
// If no rules are configured for toolName, ValidateToolInput is a no-op
// and returns nil error — operators who want to require explicit rules
// for every tool can layer a separate registry on top.
//
// rawArgs MUST be the serialized JSON form of the tool args. Callers
// that hold a map[string]any should json.Marshal first.
func ValidateToolInput(ctx context.Context, toolName string, rawArgs []byte) (ToolInputReport, error) {
	ensureToolInputInit()
	toolInputMu.RLock()
	st := toolInputCurr
	rules := st.rules[toolName]
	action := st.action
	toolInputMu.RUnlock()

	rep := ToolInputReport{Tool: toolName, Action: action}
	if len(rules) == 0 {
		return rep, nil
	}

	// Decode once for struct-shape + required-field checks. Repeated
	// json.Unmarshal across rules would be wasted work.
	var decoded any
	var decodeErr error
	decodeOnce := func() (any, error) {
		if decoded != nil || decodeErr != nil {
			return decoded, decodeErr
		}
		decodeErr = json.Unmarshal(rawArgs, &decoded)
		return decoded, decodeErr
	}

	for _, rule := range rules {
		// 1. Length check.
		if rule.MaxLengthKB > 0 {
			limit := rule.MaxLengthKB * 1024
			if len(rawArgs) > limit {
				rep.Violations = append(rep.Violations, ToolInputViolation{
					Kind:   "length",
					Detail: fmt.Sprintf("serialized arg length %d exceeds limit %d (%d KiB)", len(rawArgs), limit, rule.MaxLengthKB),
				})
			}
		}
		// 2 + 3. Struct shape + required fields.
		if rule.RequireStruct {
			val, err := decodeOnce()
			if err != nil {
				rep.Violations = append(rep.Violations, ToolInputViolation{
					Kind:   "struct_shape",
					Detail: "rawArgs is not valid JSON: " + err.Error(),
				})
			} else if obj, ok := val.(map[string]any); !ok {
				rep.Violations = append(rep.Violations, ToolInputViolation{
					Kind:   "struct_shape",
					Detail: "top-level arg must be JSON object",
				})
			} else {
				for _, field := range rule.StructFields {
					if _, present := obj[field]; !present {
						rep.Violations = append(rep.Violations, ToolInputViolation{
							Kind:   "missing_field",
							Detail: field,
						})
					}
				}
			}
		}
		// 4. Deny patterns. Run against the full serialized blob so
		// patterns can match across field boundaries (e.g. a deny on
		// `$(...)` covers command substitution wherever it appears).
		argText := string(rawArgs)
		for _, re := range rule.DenyPatterns {
			if loc := re.FindStringIndex(argText); loc != nil {
				rep.Violations = append(rep.Violations, ToolInputViolation{
					Kind:        "deny_pattern",
					Detail:      re.String(),
					Excerpt:     excerpt(argText, loc[0], loc[1]),
					PatternName: re.String(),
				})
			}
		}
	}

	if len(rep.Violations) == 0 {
		return rep, nil
	}

	// Emit one ThreatEvent per violation so the per-session budget
	// tracker (T5) sees per-rule weight, not just one event per call.
	for _, v := range rep.Violations {
		sev := severityForViolation(v)
		Emit(ctx, ThreatEvent{
			Phase:       "tool_input",
			Source:      "tool_input:" + toolName,
			PatternName: violationPatternName(v),
			Severity:    sev,
			Action:      actionName(action),
			Excerpt:     v.Excerpt,
		})
	}

	// Build a single error carrying the first violation; callers that
	// want the full list have it on the report.
	first := rep.Violations[0]
	return rep, fmt.Errorf("promptguard: tool input rejected for %s: %s", toolName, first.String())
}

// severityForViolation maps a violation kind to one of the
// {low,medium,high,critical} severity strings consumed by the per-session
// budget tracker (T5). Length and missing-field are routine config-mode
// failures (medium); struct_shape is medium; deny_pattern is high because
// the pattern was deliberately authored to catch a known-dangerous shape.
func severityForViolation(v ToolInputViolation) string {
	switch v.Kind {
	case "deny_pattern":
		return "high"
	default:
		return "medium"
	}
}

// violationPatternName returns the audit-log pattern name for a
// violation: deny_pattern violations carry the regex; structural
// violations carry a stable "tool_input.<kind>" tag.
func violationPatternName(v ToolInputViolation) string {
	if v.Kind == "deny_pattern" && v.PatternName != "" {
		return "tool_input.deny:" + v.PatternName
	}
	return "tool_input." + v.Kind
}

// ToolInputAction returns the current default action (warn|strip|reject)
// applied when ValidateToolInput records a violation. Exposed so the
// MCP and agentloop call-sites can decide whether to actually reject
// the call or just warn-and-pass.
func ToolInputAction() Action {
	ensureToolInputInit()
	toolInputMu.RLock()
	defer toolInputMu.RUnlock()
	return toolInputCurr.action
}

// SetToolInputAction installs an operator-supplied default action. Pass
// ActionReject (the spec default) to restore strict mode.
func SetToolInputAction(a Action) {
	ensureToolInputInit()
	toolInputMu.Lock()
	defer toolInputMu.Unlock()
	toolInputCurr.action = a
}

// AddToolInputRule registers an operator-supplied rule alongside the
// bundled defaults. Safe to call concurrently with ValidateToolInput.
// Rules with empty Tool name are ignored with a log line.
func AddToolInputRule(r ToolInputRule) {
	ensureToolInputInit()
	if strings.TrimSpace(r.Tool) == "" {
		log.Printf("promptguard: AddToolInputRule called with empty Tool name (source=%s)", r.Source)
		return
	}
	if r.Source == "" {
		r.Source = "operator"
	}
	toolInputMu.Lock()
	defer toolInputMu.Unlock()
	toolInputCurr.rules[r.Tool] = append(toolInputCurr.rules[r.Tool], r)
}

// LoadToolInputYAML parses an operator-supplied YAML document
// (additional_rules_path on the policy block) and merges it into the
// current rule set. The source tag is stamped onto every rule for audit.
// Replaces the configured default action when the document specifies one.
func LoadToolInputYAML(data []byte, source string) error {
	ensureToolInputInit()
	rules, action, err := parseToolInputYAML(data, source)
	if err != nil {
		return err
	}
	toolInputMu.Lock()
	defer toolInputMu.Unlock()
	for tool, rs := range rules {
		toolInputCurr.rules[tool] = append(toolInputCurr.rules[tool], rs...)
	}
	// Operator YAML wins on action when explicitly set; an empty action
	// in the supplied doc leaves the existing action in place. ParseAction
	// returns (ActionWarn, false) on empty, so we use the ok-flag via a
	// second pass — easier to just keep parseToolInputYAML's action when
	// the YAML had one.
	if action != ActionReject || hasExplicitAction(data) {
		toolInputCurr.action = action
	}
	return nil
}

// hasExplicitAction returns true if the YAML document includes a
// non-empty `promptguard.tool_input.action` field. Used so an operator
// YAML without an action field doesn't clobber the existing setting.
func hasExplicitAction(data []byte) bool {
	var probe struct {
		PromptGuard struct {
			ToolInput struct {
				Action string `yaml:"action"`
			} `yaml:"tool_input"`
		} `yaml:"promptguard"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	return strings.TrimSpace(probe.PromptGuard.ToolInput.Action) != ""
}

// ResetToolInputRules restores the bundled defaults, discarding any
// operator-supplied additions. Exposed for tests; also used by
// internal/config Apply() when policy reload pulls a new operator block.
func ResetToolInputRules() {
	toolInputInit = sync.Once{}
	toolInputMu.Lock()
	toolInputCurr = nil
	toolInputMu.Unlock()
	ensureToolInputInit()
}

// ToolInputRulesFor returns a snapshot of every configured rule for the
// named tool. Returns an empty slice when no rule is registered. Safe
// for callers to mutate.
func ToolInputRulesFor(toolName string) []ToolInputRule {
	ensureToolInputInit()
	toolInputMu.RLock()
	defer toolInputMu.RUnlock()
	src := toolInputCurr.rules[toolName]
	if len(src) == 0 {
		return nil
	}
	out := make([]ToolInputRule, len(src))
	copy(out, src)
	return out
}

// ToolInputRuleCount returns the count of (tool, rule) pairs currently
// loaded. Used by tests to assert the embedded defaults loaded cleanly.
func ToolInputRuleCount() (tools int, rules int) {
	ensureToolInputInit()
	toolInputMu.RLock()
	defer toolInputMu.RUnlock()
	for _, rs := range toolInputCurr.rules {
		rules += len(rs)
	}
	return len(toolInputCurr.rules), rules
}

// loadDefaultToolInputRules is the package-internal entry used by tests
// to assert the embedded YAML round-trips into a non-empty rule map.
// Returns a deep-copy snapshot so callers can't mutate the live state.
func loadDefaultToolInputRules() (map[string][]ToolInputRule, error) {
	rules, _, err := parseToolInputYAML(defaultToolInputYAML, "builtin")
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// ErrToolInputRejected is the sentinel returned by ValidateToolInput-
// wrapping helpers when the configured action is Reject and a violation
// was recorded. Call-sites at MCP and agentloop layers compare with
// errors.Is to deliver the structured rejection result.
var ErrToolInputRejected = errors.New("promptguard: tool_input_rejected")
