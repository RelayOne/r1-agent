// antitrunc.go — wires the antitrunc.Gate into the agentloop's
// PreEndTurnCheckFn so the gate composes BEFORE any cortex /
// operator hook.
//
// Behaviour
//
// When Config.AntiTruncEnforce is true (default true once the
// rollout flag flips, false during initial integration), the loop
// constructs an antitrunc.Gate from Config.AntiTruncPlanPath and
// Config.AntiTruncSpecPaths and wraps the user-supplied
// PreEndTurnCheckFn:
//
//   1. The wrapper runs the gate first.
//   2. If the gate returns a non-empty refusal, that refusal is
//      returned IMMEDIATELY — the user-supplied hook is NOT called.
//      This is the load-bearing guarantee: a model that says "skip
//      the gate this once" cannot influence the wrapper.
//   3. If the gate is silent, the wrapper falls through to the
//      user-supplied hook.
//
// Why composition order matters
//
// The antitrunc spec §"Layer 5" requires that the gate runs FIRST,
// before the cortex hook, before the operator hook. Order is the
// substrate that prevents an LLM from convincing a downstream hook
// to mark its self-truncation acceptable. The user-supplied hook
// runs only when the gate is silent.
//
// Operator override
//
// AntiTruncAdvisory promotes the gate to advisory-only — findings
// are forwarded to AntiTruncAdvisoryFn but the gate returns "" so
// the loop is not blocked. This is the operator's escape hatch
// (`--no-antitrunc-enforce`) and has no LLM-visible toggle.

package agentloop

import (
	"github.com/RelayOne/r1/internal/antitrunc"
)

// installAntiTruncGate composes the antitrunc.Gate into the config's
// PreEndTurnCheckFn. Called from defaults() — see loop.go.
//
// The function is idempotent: calling it twice does NOT double-wrap
// (the wrapped function captures the original by reference; the
// second call wraps the wrapper, which is harmless but wastes one
// gate evaluation). defaults() guards with the AntiTruncEnforce
// flag so this is only called once per Config initialisation.
func (c *Config) installAntiTruncGate() {
	if !c.AntiTruncEnforce {
		return
	}
	gate := &antitrunc.Gate{
		PlanPath:         c.AntiTruncPlanPath,
		SpecPaths:        c.AntiTruncSpecPaths,
		CommitLookbackFn: c.AntiTruncCommitLookbackFn,
		Advisory:         c.AntiTruncAdvisory,
		AdvisoryFn:       c.AntiTruncAdvisoryFn,
	}

	original := c.PreEndTurnCheckFn
	c.PreEndTurnCheckFn = func(messages []Message) string {
		// Convert agentloop.Message to antitrunc.Message and run
		// the gate first. The gate is the load-bearing layer —
		// nothing downstream can short-circuit it.
		ms := messagesToAntiTrunc(messages)
		if refusal := gate.CheckOutput(ms); refusal != "" {
			return refusal
		}
		// Gate silent: fall through to whatever the user wired up
		// (build verifier, cortex hook, etc.).
		if original != nil {
			return original(messages)
		}
		return ""
	}
}

// messagesToAntiTrunc converts the agentloop's typed Message
// slice into the gate's plain {Role, Text} shape. Tool-use blocks
// and thinking blocks are skipped — only text content is scanned.
func messagesToAntiTrunc(messages []Message) []antitrunc.Message {
	out := make([]antitrunc.Message, 0, len(messages))
	for _, m := range messages {
		text := extractText(m.Content)
		if text == "" {
			continue
		}
		out = append(out, antitrunc.Message{
			Role: m.Role,
			Text: text,
		})
	}
	return out
}
