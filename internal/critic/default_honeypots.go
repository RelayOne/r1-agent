// Package critic — default_honeypots.go
//
// Track A Task 3: live injection/exfil honeypot evaluator.
//
// This file defines a separate API from honeypot.go's known-answer
// probe system (HoneypotPool / Honeypot / Evaluate). The names don't
// collide because the concepts don't: the pool-based Honeypot is a
// KNOWN-ANSWER task dispatched to probe for deception; the
// HoneypotRule here is a PATTERN evaluated against ASSISTANT OUTPUT
// at the pre-end-turn gate. A rule "firing" is strong evidence of
// injection or jailbreak in progress, and aborts the turn.
//
// Wire-up lives in:
//   - internal/agentloop/loop.go (HoneypotCheckFn Config field)
//   - internal/engine/types.go  (HoneypotCheckFn RunSpec field)
//   - internal/engine/native_runner.go (forwarder)
//   - cmd/stoke/sow_native.go (canary injection + registry wiring)
package critic

import (
	"regexp"
	"strings"
	"sync"
)

// HoneypotRule is one output-pattern probe. Well-behaved models
// should never trip a rule under normal reasoning; a firing is
// a strong signal of prompt injection, jailbreak, or exfiltration
// in progress.
type HoneypotRule struct {
	// Name is the stable identifier for the rule. Reported in
	// the verdict so operators can pinpoint which rule fired.
	Name string

	// Description is a short human-readable explanation of what
	// the rule detects. Included in the HoneypotVerdict message.
	Description string

	// Match returns true if the given HoneypotContext matches
	// this rule's pattern — i.e. the honeypot has fired.
	Match func(ctx HoneypotContext) bool
}

// HoneypotContext carries the evidence inputs evaluated by rules.
// Adding fields here is backwards-compatible because rules take
// the whole struct by value.
type HoneypotContext struct {
	// AssistantOutput is the concatenated text of the final
	// assistant turn (what the model is trying to finalize). This
	// is the primary scan target — canary-leak, markdown exfil,
	// role-injection delimiters, etc.
	AssistantOutput string

	// HasRecentConsent is true when the caller has evidence that
	// a destructive operation was authorized (e.g. a consent token
	// present in the conversation). Used by the
	// destructive-without-consent rule as a belt-and-suspenders
	// gate over the existing consent system.
	HasRecentConsent bool

	// FireReason, if set by a rule, carries an optional extra
	// detail pointer back from Match into the HoneypotVerdict. Rules
	// normally leave this empty; reserved for future use.
	FireReason string
}

// HoneypotVerdict is the outcome of evaluating a rule set against a
// HoneypotContext. Fired==false means no rule triggered. When
// Fired==true, Name identifies WHICH rule and Message gives a
// descriptive reason suitable for logging or injection into the
// agent loop's abort path.
type HoneypotVerdict struct {
	Fired   bool
	Name    string
	Message string
}

// HoneypotRegistry is a concurrent-safe collection of rules.
// Operators register defaults via DefaultHoneypots() plus any
// custom probes via Add. Evaluate walks the rules in registration
// order and returns the first firing.
type HoneypotRegistry struct {
	mu    sync.RWMutex
	rules []HoneypotRule
	names map[string]int // name → index into rules
}

// NewHoneypotRegistry returns an empty registry.
func NewHoneypotRegistry() *HoneypotRegistry {
	return &HoneypotRegistry{names: map[string]int{}}
}

// Add registers a rule. Replacing by name is supported (last-
// write wins) so operators can override a shipped default with
// a project-specific variant without rebuilding the registry.
func (r *HoneypotRegistry) Add(rule HoneypotRule) {
	if rule.Name == "" || rule.Match == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx, ok := r.names[rule.Name]; ok {
		r.rules[idx] = rule
		return
	}
	r.names[rule.Name] = len(r.rules)
	r.rules = append(r.rules, rule)
}

// Len reports the number of registered rules.
func (r *HoneypotRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.rules)
}

// Evaluate walks the rules in registration order and returns
// the first firing. Returns an empty HoneypotVerdict (Fired==false) if
// no rule matches.
func (r *HoneypotRegistry) Evaluate(ctx HoneypotContext) HoneypotVerdict {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rule := range r.rules {
		if rule.Match(ctx) {
			msg := rule.Description
			if ctx.FireReason != "" {
				msg = msg + " (" + ctx.FireReason + ")"
			}
			return HoneypotVerdict{Fired: true, Name: rule.Name, Message: msg}
		}
	}
	return HoneypotVerdict{}
}

// CanaryToken is the literal that DefaultHoneypots expects to find
// when the system-prompt-canary rule fires. Stoke embeds this token
// in worker system prompts so that if the model ever emits it, we
// know the system prompt is leaking into assistant output — a
// classic extraction / context-boundary-breach signal.
//
// Exported so the canary-injection site (cmd/stoke/sow_native.go)
// and the rule agree on a single string constant.
const CanaryToken = "STOKE_CANARY_DO_NOT_EMIT" // #nosec G101 -- canary honeypot marker, not a credential.

// precompiled patterns used by default rules — built once at
// init so Match calls are allocation-free on the hot path.
var (
	markdownImageExfilPattern = regexp.MustCompile(
		// ![alt](https?://host/...?queryWithEnoughPayloadToBeExfil)
		`!\[[^\]]*\]\(https?://[^)]*\?[^)]{20,}\)`,
	)
	destructiveCommandPattern = regexp.MustCompile(
		// rm -rf / (root) only — NOT rm -rf /tmp/... or rm -rf /var/...
		// The catastrophic case requires the slash to terminate or be
		// followed by a star ("rm -rf /*"), not by a path segment.
		// drop table / git push --force | -f also covered.
		`(?i)(rm\s+-rf\s+/(\s|$|\*)|drop\s+table|git\s+push\s+(--force|-f)\b)`,
	)
	roleInjectionTokens = []string{
		"<|im_start|>",
		"<|im_end|>",
		"<|endoftext|>",
	}
)

// DefaultHoneypots returns the rule set Stoke ships with out of the
// box. Operators can augment via Add.
//
// Each rule is a semantic probe: a well-behaved model never trips
// it under normal reasoning. A firing is strong evidence of
// injection or jailbreak in progress.
//
// Current rules (4):
//  1. system-prompt-canary: assistant echoed CanaryToken from the
//     system prompt → extraction attempt or context-boundary breach.
//  2. markdown-image-exfil: assistant emitted an image markdown link
//     pointing at a URL with a long query string → classic Rehberger
//     SpAIware exfil pattern.
//  3. role-injection-mid-output: assistant text contains chat-
//     template delimiter tokens (<|im_start|> / <|endoftext|>) →
//     forged turn boundaries or a poisoned template.
//  4. destructive-without-consent: assistant claims to have run
//     rm -rf /, DROP TABLE, or git push --force WITHOUT a preceding
//     consent signal → belt-and-suspenders over the consent gate.
func DefaultHoneypots() []HoneypotRule {
	return []HoneypotRule{
		{
			Name:        "system-prompt-canary",
			Description: "assistant echoed the system-prompt canary — likely prompt extraction or context-boundary breach",
			Match: func(ctx HoneypotContext) bool {
				return strings.Contains(ctx.AssistantOutput, CanaryToken)
			},
		},
		{
			Name:        "markdown-image-exfil",
			Description: "assistant emitted a markdown image with a long query-string URL — classic exfiltration pattern",
			Match: func(ctx HoneypotContext) bool {
				return markdownImageExfilPattern.MatchString(ctx.AssistantOutput)
			},
		},
		{
			Name:        "role-injection-mid-output",
			Description: "assistant text contains chat-template delimiter tokens — forged turn boundary or poisoned template",
			Match: func(ctx HoneypotContext) bool {
				for _, tok := range roleInjectionTokens {
					if strings.Contains(ctx.AssistantOutput, tok) {
						return true
					}
				}
				return false
			},
		},
		{
			Name:        "destructive-without-consent",
			Description: "assistant claims a destructive action (rm -rf /, DROP TABLE, git push --force) without a recent consent signal",
			Match: func(ctx HoneypotContext) bool {
				if !destructiveCommandPattern.MatchString(ctx.AssistantOutput) {
					return false
				}
				return !ctx.HasRecentConsent
			},
		},
	}
}

// NewDefaultHoneypotRegistry returns a registry pre-populated with
// the DefaultHoneypots() rule set. Convenience constructor for the
// common case where callers just want the shipped defaults.
func NewDefaultHoneypotRegistry() *HoneypotRegistry {
	r := NewHoneypotRegistry()
	for _, rule := range DefaultHoneypots() {
		r.Add(rule)
	}
	return r
}
