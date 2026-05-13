// Package critic — injection_aware.go
//
// InjectionAwareCritic is the adversarial reviewer for prompt-injection
// signatures. Spec: specs/promptguard-hardening.md §T4 items 15-18.
//
// The critic consumes file content + (optionally) serialized tool-call
// arg blobs and produces a SeverityBlock finding for each promptguard
// signature match. A reviewed file that contains "ignore previous
// instructions" or an injected role marker counts as PRIMA-FACIE
// evidence of injection per the spec, so reviews of code touched by
// such content fail.
//
// SPEC DEVIATION (real-code reality check):
//
//   - The spec named a `critic.Hook` interface with `Name()` and
//     `OnToolCall(ctx, tc)`. The real critic package has no `Hook`
//     interface; its extension point is `Rule` (a regex or Check
//     function that produces []Finding). The InjectionAwareCritic
//     reaches the same operator-visible outcome by surfacing as a
//     critic.Rule (function-based) registered in DefaultRules. The
//     reviewer's system prompt still receives the injection-corpus
//     signature briefing via BuildVerifyPrompt (see prompts.go).

package critic

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/RelayOne/r1/internal/promptguard"
)

// InjectionAwareCriticID is the stable Rule ID surfaced in Finding.Rule
// so operator-side log parsers can filter on it.
const InjectionAwareCriticID = "promptguard-injection-aware"

// InjectionAwareNote mirrors the spec's `critic.Note` shape (which
// does not exist in the real critic package). It is exported so
// callers that consume tool-call blobs out-of-band (e.g. an
// agentloop hook) can surface notes with the same severity/body
// shape they would see if a Finding had fired.
type InjectionAwareNote struct {
	Severity string
	Body     string
}

// Name returns the stable identifier the reviewer/log surface uses.
// Mirrors the `Name() string` shape the spec requested for the
// non-existent Hook interface so future refactors that add a real
// Hook interface have zero rename churn.
func (i InjectionAwareCritic) Name() string { return InjectionAwareCriticID }

// InjectionAwareCritic is a stateless struct that produces the
// promptguard injection-aware Rule and tool-call notes. Use the
// package-level Rule() to obtain the critic.Rule instance for
// registration.
type InjectionAwareCritic struct{}

// Rule returns a critic.Rule whose Check function scans the supplied
// file content for promptguard injection signatures and produces a
// SeverityBlock Finding for each detected threat.
func (i InjectionAwareCritic) Rule() Rule {
	return Rule{
		ID:       InjectionAwareCriticID,
		Name:     "Prompt-injection signature in reviewed content",
		Severity: SeverityBlock,
		Check: func(file, content string) []Finding {
			threats := promptguard.Scan(content)
			if len(threats) == 0 {
				return nil
			}
			out := make([]Finding, 0, len(threats))
			for _, t := range threats {
				out = append(out, Finding{
					Severity:   SeverityBlock,
					Category:   "security",
					File:       file,
					Line:       lineOfOffset(content, t.Start),
					Message:    "Reviewed content contains injection signature: " + t.PatternName + " — excerpt: " + t.Excerpt,
					Suggestion: "Strip the injection-shaped payload from the source file before re-running the reviewer.",
					Rule:       InjectionAwareCriticID,
				})
			}
			return out
		},
	}
}

// OnToolCall scans a serialized tool-call argument blob for injection
// signatures. Returns one InjectionAwareNote per detected threat.
// This is the surface external callers (mid-stream tool-call
// inspection in the agentloop, hub subscribers) use to flag
// post-hoc injection-aware tool calls per spec §T4 behavioral-contract
// item 1.
func (i InjectionAwareCritic) OnToolCall(_ context.Context, toolName string, rawArgs []byte) []InjectionAwareNote {
	if len(rawArgs) == 0 {
		return nil
	}
	// Marshal+remarshal so a tool call passed in as a string-typed
	// arg ends up as the same bytes a JSON tool-call surface would
	// see. Bare bytes are treated as the verbatim arg blob.
	body := string(rawArgs)
	// If rawArgs decodes as JSON, scan a flattened string form so
	// pattern matches still hit embedded fields. Best-effort.
	var any interface{}
	if err := json.Unmarshal(rawArgs, &any); err == nil {
		if reBytes, mErr := json.Marshal(any); mErr == nil {
			body = string(reBytes)
		}
	}
	threats := promptguard.Scan(body)
	if len(threats) == 0 {
		return nil
	}
	out := make([]InjectionAwareNote, 0, len(threats))
	for _, t := range threats {
		out = append(out, InjectionAwareNote{
			Severity: "high",
			Body: "Tool call contains injection signature: " + t.PatternName +
				" (tool=" + toolName + ", excerpt=" + t.Excerpt + ")",
		})
	}
	return out
}

// lineOfOffset returns the 1-indexed line number that byte offset
// `off` falls on. Used to populate Finding.Line.
func lineOfOffset(content string, off int) int {
	if off <= 0 {
		return 1
	}
	if off > len(content) {
		off = len(content)
	}
	return 1 + strings.Count(content[:off], "\n")
}

// InjectionAwareRule is the convenience accessor used by registry.go
// to ensure the critic is on the default chain. It wraps
// InjectionAwareCritic{}.Rule() so callers don't need to instantiate
// the struct themselves.
func InjectionAwareRule() Rule {
	return InjectionAwareCritic{}.Rule()
}
