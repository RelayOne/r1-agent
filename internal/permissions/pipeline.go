// Package permissions implements a composable authorization pipeline for tool use.
// Inspired by claw-code-parity's permission system: a clear priority chain where
// each step can short-circuit: deny rules → context overrides → ask rules → allow rules → mode check.
//
// This replaces ad-hoc permission checking scattered across hooks and settings
// with a single, testable pipeline.
package permissions

import (
	"fmt"
	"strings"
)

// Decision is the outcome of an authorization check.
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
	Ask   Decision = "ask" // prompt user for confirmation
)

// Result is the full authorization result with reason.
type Result struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
	Rule     string   `json:"rule,omitempty"` // which rule matched
}

// Mode defines the permission level for a session.
type Mode string

const (
	ModeReadOnly       Mode = "read_only"       // only read tools
	ModeWorkspaceWrite Mode = "workspace_write"  // read + write in workspace
	ModeFull           Mode = "full"             // all tools allowed
	ModeDangerous      Mode = "dangerous"        // skip all checks (--dangerously-skip-permissions)
)

// Rule is a single permission rule with a pattern and decision.
type Rule struct {
	Pattern  string   `json:"pattern"`  // e.g., "Bash(git:*)", "Write", "Edit(.env*)"
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
}

// Policy is a complete permission policy with ordered rules.
type Policy struct {
	Mode       Mode   `json:"mode"`
	DenyRules  []Rule `json:"deny_rules"`
	AskRules   []Rule `json:"ask_rules"`
	AllowRules []Rule `json:"allow_rules"`
}

// Step is one stage in the authorization pipeline.
// Each step can return a decision or pass to the next step.
type Step func(tool, input string, ctx *Context) *Result

// Context carries contextual information for authorization decisions.
type Context struct {
	Mode        Mode
	HookOverride *Result // from PreToolUse hook
	SessionTool  bool    // is this a session-management tool?
	FilePath     string  // for file operations
	Command      string  // for Bash operations
}

// Pipeline runs authorization steps in order until one decides.
type Pipeline struct {
	steps []Step
}

// NewPipeline creates the standard authorization pipeline:
// deny rules → hook overrides → ask rules → allow rules → mode check
func NewPipeline(policy Policy) *Pipeline {
	return &Pipeline{
		steps: []Step{
			denyStep(policy.DenyRules),
			hookOverrideStep(),
			askStep(policy.AskRules),
			allowStep(policy.AllowRules),
			modeStep(policy.Mode),
		},
	}
}

// Authorize runs the pipeline for a tool invocation.
func (p *Pipeline) Authorize(tool, input string, ctx *Context) Result {
	if ctx == nil {
		ctx = &Context{Mode: ModeWorkspaceWrite}
	}

	for _, step := range p.steps {
		if result := step(tool, input, ctx); result != nil {
			return *result
		}
	}

	// Default: deny (fail-closed)
	return Result{Decision: Deny, Reason: "no rule matched (fail-closed)"}
}

// --- Pipeline steps ---

// denyStep blocks if any deny rule matches (highest priority).
func denyStep(rules []Rule) Step {
	return func(tool, input string, _ *Context) *Result {
		for _, r := range rules {
			if matchRule(r.Pattern, tool, input) {
				reason := r.Reason
				if reason == "" {
					reason = fmt.Sprintf("denied by rule: %s", r.Pattern)
				}
				return &Result{Decision: Deny, Reason: reason, Rule: r.Pattern}
			}
		}
		return nil // pass to next step
	}
}

// hookOverrideStep applies hook-provided overrides.
func hookOverrideStep() Step {
	return func(_, _ string, ctx *Context) *Result {
		if ctx.HookOverride != nil {
			return ctx.HookOverride
		}
		return nil
	}
}

// askStep forces user confirmation for matching tools.
func askStep(rules []Rule) Step {
	return func(tool, input string, _ *Context) *Result {
		for _, r := range rules {
			if matchRule(r.Pattern, tool, input) {
				return &Result{Decision: Ask, Reason: r.Reason, Rule: r.Pattern}
			}
		}
		return nil
	}
}

// allowStep permits matching tools.
func allowStep(rules []Rule) Step {
	return func(tool, input string, _ *Context) *Result {
		for _, r := range rules {
			if matchRule(r.Pattern, tool, input) {
				return &Result{Decision: Allow, Reason: "allowed by rule", Rule: r.Pattern}
			}
		}
		return nil
	}
}

// modeStep applies mode-based defaults.
func modeStep(mode Mode) Step {
	return func(tool, _ string, _ *Context) *Result {
		switch mode {
		case ModeDangerous:
			return &Result{Decision: Allow, Reason: "dangerous mode: all allowed"}
		case ModeFull:
			return &Result{Decision: Allow, Reason: "full mode: tool allowed"}
		case ModeWorkspaceWrite:
			if isReadTool(tool) || isWriteTool(tool) {
				return &Result{Decision: Allow, Reason: "workspace-write mode"}
			}
			if isDangerousTool(tool) {
				return &Result{Decision: Ask, Reason: "workspace-write mode: dangerous tool requires confirmation"}
			}
			return &Result{Decision: Allow, Reason: "workspace-write mode default"}
		case ModeReadOnly:
			if isReadTool(tool) {
				return &Result{Decision: Allow, Reason: "read-only mode"}
			}
			return &Result{Decision: Deny, Reason: "read-only mode: write operations denied"}
		default:
			return &Result{Decision: Deny, Reason: "unknown mode"}
		}
	}
}

// matchRule checks if a tool+input matches a rule pattern.
// Pattern syntax (from claw-code-parity):
//   - "Bash" matches Bash tool with any input
//   - "Bash(git:*)" matches Bash where input starts with "git"
//   - "Write(.env*)" matches Write where file path starts with ".env"
//   - "*" matches everything
func matchRule(pattern, tool, input string) bool {
	if pattern == "*" {
		return true
	}

	// Check for parenthesized argument pattern: Tool(arg)
	parenIdx := strings.IndexByte(pattern, '(')
	if parenIdx < 0 {
		// Simple tool name match
		return tool == pattern
	}

	toolPattern := pattern[:parenIdx]
	if tool != toolPattern {
		return false
	}

	// Extract argument pattern
	if !strings.HasSuffix(pattern, ")") {
		return tool == pattern // malformed, treat as exact match
	}
	argPattern := pattern[parenIdx+1 : len(pattern)-1]

	// Wildcard suffix: "git:*" matches anything starting with "git"
	// The colon is part of the separator syntax, not literal
	if strings.HasSuffix(argPattern, "*") {
		prefix := strings.TrimSuffix(argPattern, "*")
		prefix = strings.TrimSuffix(prefix, ":") // "git:*" → match "git" prefix
		return strings.HasPrefix(input, prefix)
	}

	// Exact match
	return input == argPattern
}

func isReadTool(tool string) bool {
	switch tool {
	case "Read", "Glob", "Grep", "WebSearch", "WebFetch", "ToolSearch":
		return true
	}
	return false
}

func isWriteTool(tool string) bool {
	switch tool {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

func isDangerousTool(tool string) bool {
	switch tool {
	case "Bash":
		return true
	}
	return false
}

// DefaultPolicy returns the standard workspace-write policy with Stoke's deny rules.
func DefaultPolicy() Policy {
	return Policy{
		Mode: ModeWorkspaceWrite,
		DenyRules: []Rule{
			{Pattern: "Bash(git push*)", Reason: "git push blocked — Stoke controls push"},
			{Pattern: "Bash(git reset --hard*)", Reason: "git reset --hard destroys evidence"},
			{Pattern: "Bash(git stash*)", Reason: "git stash hides work from verification"},
			{Pattern: "Bash(rm -rf /*)", Reason: "destructive: rm -rf / blocked"},
			{Pattern: "Write(.env*)", Reason: "cannot modify .env files"},
			{Pattern: "Write(.claude*)", Reason: "cannot modify .claude/ files"},
			{Pattern: "Write(.stoke*)", Reason: "cannot modify .stoke/ files"},
			{Pattern: "Edit(.env*)", Reason: "cannot modify .env files"},
		},
		AllowRules: []Rule{
			{Pattern: "Read"},
			{Pattern: "Glob"},
			{Pattern: "Grep"},
			{Pattern: "Write"},
			{Pattern: "Edit"},
			{Pattern: "Bash"},
		},
	}
}
