// Package lifecycle implements a 5-tier hook system for agent orchestration.
// Inspired by OmO's 48 lifecycle hooks across 5 tiers:
// - Session (23 hooks): init, start, idle, resume, compact, complete, error
// - ToolGuard (12 hooks): pre-tool validation, file protection, scope enforcement
// - Transform (4 hooks): input/output transformation, prompt injection
// - Continuation (7 hooks): idle detection, todo enforcement, completion validation
// - Skill (2 hooks): skill enter/exit lifecycle
//
// Hooks are composable: multiple hooks can register for the same event,
// and they run in priority order. Any hook can abort the operation.
package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Tier classifies hooks by when they fire.
type Tier string

const (
	TierSession      Tier = "session"      // session lifecycle events
	TierToolGuard    Tier = "tool_guard"   // pre/post tool execution
	TierTransform    Tier = "transform"    // input/output transformation
	TierContinuation Tier = "continuation" // idle detection, completion enforcement
	TierSkill        Tier = "skill"        // skill lifecycle
)

// Event identifies a specific hook point.
type Event string

const (
	// Session events
	EventSessionInit     Event = "session.init"
	EventSessionStart    Event = "session.start"
	EventSessionIdle     Event = "session.idle"
	EventSessionResume   Event = "session.resume"
	EventSessionCompact  Event = "session.compact"
	EventSessionComplete Event = "session.complete"
	EventSessionError    Event = "session.error"
	EventSessionDispose  Event = "session.dispose"

	// Tool guard events
	EventPreToolUse      Event = "tool.pre_use"
	EventPostToolUse     Event = "tool.post_use"
	EventToolBlocked     Event = "tool.blocked"
	EventFileGuard       Event = "tool.file_guard"
	EventScopeCheck      Event = "tool.scope_check"
	EventDestructiveGuard Event = "tool.destructive_guard"

	// Transform events
	EventPromptTransform  Event = "transform.prompt"
	EventOutputTransform  Event = "transform.output"
	EventContextInject    Event = "transform.context_inject"
	EventResponseFilter   Event = "transform.response_filter"

	// Continuation events
	EventIdleDetected     Event = "continuation.idle"
	EventTodoCheck        Event = "continuation.todo_check"
	EventCompletionGate   Event = "continuation.completion_gate"
	EventRetryDecision    Event = "continuation.retry"
	EventEscalation       Event = "continuation.escalate"

	// Skill events
	EventSkillEnter Event = "skill.enter"
	EventSkillExit  Event = "skill.exit"
)

// HookContext carries data through the hook chain.
type HookContext struct {
	Event      Event
	Tier       Tier
	TaskID     string
	WorktreeID string
	ToolName   string
	FilePath   string
	Input      string
	Output     string
	Error      error
	Metadata   map[string]any
	Cancelled  bool
	Reason     string
}

// Decision is the hook's verdict.
type Decision string

const (
	DecisionAllow    Decision = "allow"    // proceed
	DecisionDeny     Decision = "deny"     // block the operation
	DecisionModify   Decision = "modify"   // allow with modifications (check Output)
	DecisionContinue Decision = "continue" // pass to next hook
)

// HookResult is what a hook returns.
type HookResult struct {
	Decision Decision
	Reason   string
	Output   string // modified output (for transform hooks)
	Metadata map[string]any
}

// HookFunc is the signature for a hook handler.
type HookFunc func(ctx context.Context, hctx *HookContext) HookResult

// Hook is a registered hook with metadata.
type Hook struct {
	Name     string   `json:"name"`
	Tier     Tier     `json:"tier"`
	Events   []Event  `json:"events"`
	Priority int      `json:"priority"` // lower = runs first
	Fn       HookFunc `json:"-"`
}

// Registry manages hook registration and dispatch.
type Registry struct {
	mu    sync.RWMutex
	hooks map[Event][]*Hook
	stats map[string]*HookStats
}

// HookStats tracks execution statistics for a hook.
type HookStats struct {
	Invocations int           `json:"invocations"`
	Denials     int           `json:"denials"`
	Errors      int           `json:"errors"`
	TotalTime   time.Duration `json:"total_time"`
}

// NewRegistry creates an empty hook registry.
func NewRegistry() *Registry {
	return &Registry{
		hooks: make(map[Event][]*Hook),
		stats: make(map[string]*HookStats),
	}
}

// Register adds a hook to the registry.
func (r *Registry) Register(hook Hook) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, event := range hook.Events {
		r.hooks[event] = append(r.hooks[event], &hook)
		sort.Slice(r.hooks[event], func(i, j int) bool {
			return r.hooks[event][i].Priority < r.hooks[event][j].Priority
		})
	}
	r.stats[hook.Name] = &HookStats{}
}

// Dispatch fires all hooks for an event in priority order.
// Returns the final result. Short-circuits on Deny.
func (r *Registry) Dispatch(ctx context.Context, hctx *HookContext) HookResult {
	r.mu.RLock()
	hooks := r.hooks[hctx.Event]
	r.mu.RUnlock()

	if len(hooks) == 0 {
		return HookResult{Decision: DecisionAllow}
	}

	lastResult := HookResult{Decision: DecisionAllow}

	for _, hook := range hooks {
		start := time.Now()
		result := hook.Fn(ctx, hctx)
		elapsed := time.Since(start)

		r.mu.Lock()
		if s, ok := r.stats[hook.Name]; ok {
			s.Invocations++
			s.TotalTime += elapsed
			if result.Decision == DecisionDeny {
				s.Denials++
			}
		}
		r.mu.Unlock()

		if result.Decision == DecisionDeny {
			hctx.Cancelled = true
			hctx.Reason = result.Reason
			return result
		}

		if result.Decision == DecisionModify {
			hctx.Output = result.Output
			if result.Metadata != nil {
				if hctx.Metadata == nil {
					hctx.Metadata = make(map[string]any)
				}
				for k, v := range result.Metadata {
					hctx.Metadata[k] = v
				}
			}
		}

		lastResult = result
	}

	return lastResult
}

// HooksFor returns all hooks registered for an event.
func (r *Registry) HooksFor(event Event) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for _, h := range r.hooks[event] {
		names = append(names, h.Name)
	}
	return names
}

// Stats returns execution statistics for a hook.
func (r *Registry) Stats(name string) *HookStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stats[name]
}

// AllStats returns all hook statistics.
func (r *Registry) AllStats() map[string]*HookStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make(map[string]*HookStats)
	for k, v := range r.stats {
		cp[k] = v
	}
	return cp
}

// Count returns the total number of registered hooks.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	for _, hooks := range r.hooks {
		for _, h := range hooks {
			seen[h.Name] = true
		}
	}
	return len(seen)
}

// --- Built-in hook constructors ---

// FileProtectionHook creates a hook that blocks writes to protected files.
func FileProtectionHook(protectedFiles []string) Hook {
	protected := make(map[string]bool, len(protectedFiles))
	for _, f := range protectedFiles {
		protected[f] = true
	}
	return Hook{
		Name:     "file-protection",
		Tier:     TierToolGuard,
		Events:   []Event{EventFileGuard, EventPreToolUse},
		Priority: 10,
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			if protected[hctx.FilePath] {
				return HookResult{
					Decision: DecisionDeny,
					Reason:   fmt.Sprintf("file %q is protected", hctx.FilePath),
				}
			}
			return HookResult{Decision: DecisionContinue}
		},
	}
}

// ScopeEnforcementHook creates a hook that blocks writes to files outside the allowed set.
func ScopeEnforcementHook(allowedFiles []string) Hook {
	allowed := make(map[string]bool, len(allowedFiles))
	for _, f := range allowedFiles {
		allowed[f] = true
	}
	return Hook{
		Name:     "scope-enforcement",
		Tier:     TierToolGuard,
		Events:   []Event{EventScopeCheck, EventPreToolUse},
		Priority: 20,
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			if len(allowed) == 0 {
				return HookResult{Decision: DecisionContinue}
			}
			if hctx.FilePath != "" && !allowed[hctx.FilePath] {
				return HookResult{
					Decision: DecisionDeny,
					Reason:   fmt.Sprintf("file %q is outside task scope", hctx.FilePath),
				}
			}
			return HookResult{Decision: DecisionContinue}
		},
	}
}

// IdleDetectionHook creates a hook that fires on session idle.
func IdleDetectionHook(onIdle func(taskID string)) Hook {
	return Hook{
		Name:     "idle-detection",
		Tier:     TierContinuation,
		Events:   []Event{EventIdleDetected},
		Priority: 50,
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			if onIdle != nil {
				onIdle(hctx.TaskID)
			}
			return HookResult{Decision: DecisionContinue}
		},
	}
}

// ContextInjectionHook creates a hook that injects content into prompts.
func ContextInjectionHook(name string, inject func() string) Hook {
	return Hook{
		Name:     name,
		Tier:     TierTransform,
		Events:   []Event{EventContextInject, EventPromptTransform},
		Priority: 30,
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			extra := inject()
			if extra != "" {
				return HookResult{
					Decision: DecisionModify,
					Output:   hctx.Output + "\n" + extra,
				}
			}
			return HookResult{Decision: DecisionContinue}
		},
	}
}
