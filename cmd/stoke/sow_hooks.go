package main

import (
	"strings"

	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/skill"
)

// hookCtx captures the (agent, phase, scenario-derived) tuple for a
// single LLM call site. The runtime fills Agent and Phase at the call
// site; scenario selectors are derived mechanically from session
// metadata via sessionScenarios so call sites don't need to remember
// the "-cont / -fix / -deep-" plumbing.
type hookCtx struct {
	// Agent is the per-role hook name, e.g. "worker-task-repair".
	Agent string
	// Phase is the per-phase hook name, e.g. "2-repair-loop". Empty
	// skips phase injection.
	Phase string
	// Session, when non-nil, feeds sessionScenarios to derive
	// scenario hooks (retry-attempt, fix-dag-session, etc).
	Session *plan.Session
	// Attempt is the retry attempt number (1 = first). Values >= 2
	// add the retry-attempt scenario hook.
	Attempt int
	// HasWisdom flips on the wisdom-present scenario hook.
	HasWisdom bool
	// HasPriorLearnings flips on the meta-prevention-rules scenario hook.
	HasPriorLearnings bool
}

// workerAgentFor returns the worker-agent hook name appropriate for
// the given session. Promoted sessions (fix-DAG, continuation, decomp
// overflow) route to their specialized worker hooks; everything else
// gets the normal worker hook.
func workerAgentFor(sess plan.Session) string {
	id := sess.ID
	switch {
	case strings.Contains(id, "-fix"):
		return "worker-task-promoted-fix-dag"
	case strings.Contains(id, "-cont"):
		return "worker-task-promoted-continuation"
	case strings.Contains(id, "-deep-"):
		return "worker-task-promoted-overflow"
	}
	return "worker-task-normal"
}

// sessionScenarios maps a session's ID pattern onto the scenarios/*
// hooks that should fire alongside it.
func sessionScenarios(sess *plan.Session) []skill.HookSelector {
	if sess == nil {
		return nil
	}
	var out []skill.HookSelector
	id := sess.ID
	switch {
	case strings.Contains(id, "-cont"):
		out = append(out, skill.HookSelector{Kind: "scenarios", Name: "continuation-session"})
	case strings.Contains(id, "-fix"):
		out = append(out, skill.HookSelector{Kind: "scenarios", Name: "fix-dag-session"})
	case strings.Contains(id, "-deep-"):
		out = append(out, skill.HookSelector{Kind: "scenarios", Name: "overflow-session"})
	}
	return out
}

// selectors expands a hookCtx into the full HookSelector slice that
// hookBlock passes to HookSet.PromptBlock.
func (c hookCtx) selectors() []skill.HookSelector {
	var sels []skill.HookSelector
	if c.Agent != "" {
		sels = append(sels, skill.HookSelector{Kind: "agents", Name: c.Agent})
	}
	if c.Attempt >= 2 {
		sels = append(sels, skill.HookSelector{Kind: "scenarios", Name: "retry-attempt"})
	} else if c.Attempt == 1 {
		sels = append(sels, skill.HookSelector{Kind: "scenarios", Name: "first-attempt"})
	}
	sels = append(sels, sessionScenarios(c.Session)...)
	if c.HasWisdom {
		sels = append(sels, skill.HookSelector{Kind: "scenarios", Name: "wisdom-present"})
	}
	if c.HasPriorLearnings {
		sels = append(sels, skill.HookSelector{Kind: "scenarios", Name: "meta-prevention-rules"})
	}
	if c.Phase != "" {
		sels = append(sels, skill.HookSelector{Kind: "phases", Name: c.Phase})
	}
	return sels
}

// combinedPromptBlock concatenates the universal block and the hooks
// block for the given hookCtx. Result can be passed into any existing
// UniversalPromptBlock field — no downstream API changes needed.
func (cfg sowNativeConfig) combinedPromptBlock(c hookCtx) string {
	return skill.ConcatPromptBlocks(
		cfg.UniversalContext.PromptBlock(),
		cfg.Hooks.PromptBlock(c.selectors()...),
	)
}

// agentContext convenience wrapper: builds a hookCtx for the given
// agent+phase using the session metadata on cfg-scope.
func (cfg sowNativeConfig) agentContext(agent, phase string, sess *plan.Session, attempt int) hookCtx {
	return hookCtx{
		Agent:             agent,
		Phase:             phase,
		Session:           sess,
		Attempt:           attempt,
		HasWisdom:         cfg.Wisdom != nil,
		HasPriorLearnings: strings.TrimSpace(cfg.PriorLearnings) != "",
	}
}
