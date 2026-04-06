package hub

import (
	"context"
)

// WisdomSubscriber creates a hub subscriber that records task outcomes as wisdom learnings.
// This replaces the wisdom-specific AfterTask hook in the workflow TaskHook interface.
func WisdomSubscriber(recordFn func(task string, success bool, attempt int)) Subscriber {
	return Subscriber{
		ID:     "hub.wisdom",
		Events: []EventType{EventTaskCompleted, EventTaskFailed},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			if ev.Lifecycle == nil {
				return nil
			}
			success := ev.Type == EventTaskCompleted
			recordFn(ev.TaskID, success, ev.Lifecycle.Attempt)
			return nil
		},
	}
}

// CostAlertSubscriber creates a hub subscriber that fires on cost threshold events.
func CostAlertSubscriber(alertFn func(threshold string, spent, budget float64)) Subscriber {
	return Subscriber{
		ID:     "hub.cost-alert",
		Events: []EventType{EventCostBudget50, EventCostBudget80, EventCostBudget90, EventCostBudgetExceeded},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			if ev.Cost == nil {
				return nil
			}
			alertFn(ev.Cost.Threshold, ev.Cost.TotalSpent, ev.Cost.BudgetLimit)
			return nil
		},
	}
}

// FileProtectionGate creates a hub gate subscriber that blocks writes to protected files.
// This is the hub equivalent of lifecycle.FileProtectionHook.
func FileProtectionGate(protectedFiles []string) Subscriber {
	protected := make(map[string]bool, len(protectedFiles))
	for _, f := range protectedFiles {
		protected[f] = true
	}
	return Subscriber{
		ID:       "hub.file-protection",
		Events:   []EventType{EventToolFileWrite},
		Mode:     ModeGate,
		Priority: 10,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			if ev.File != nil && protected[ev.File.Path] {
				return &HookResponse{
					Decision: Deny,
					Reason:   "file is protected: " + ev.File.Path,
				}
			}
			return &HookResponse{Decision: Allow}
		},
	}
}

// ScopeEnforcementGate creates a hub gate subscriber that blocks writes outside the allowed file set.
// This is the hub equivalent of lifecycle.ScopeEnforcementHook.
func ScopeEnforcementGate(allowedFiles []string) Subscriber {
	allowed := make(map[string]bool, len(allowedFiles))
	for _, f := range allowedFiles {
		allowed[f] = true
	}
	return Subscriber{
		ID:       "hub.scope-enforcement",
		Events:   []EventType{EventToolFileWrite},
		Mode:     ModeGate,
		Priority: 20,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			if len(allowed) == 0 {
				return &HookResponse{Decision: Allow}
			}
			if ev.File != nil && !allowed[ev.File.Path] {
				return &HookResponse{
					Decision: Deny,
					Reason:   "file outside task scope: " + ev.File.Path,
				}
			}
			return &HookResponse{Decision: Allow}
		},
	}
}

// SecurityScanObserver creates a hub observer that logs security scan findings.
func SecurityScanObserver(logFn func(category, severity, details string)) Subscriber {
	return Subscriber{
		ID:     "hub.security-observer",
		Events: []EventType{"security.*"},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			if ev.Security != nil {
				logFn(ev.Security.Category, ev.Security.Severity, ev.Security.Details)
			}
			return nil
		},
	}
}

// PromptInjectionTransformer creates a hub transform subscriber that injects
// content into prompts. This is the hub equivalent of lifecycle.ContextInjectionHook.
func PromptInjectionTransformer(label string, contentFn func() string) Subscriber {
	return Subscriber{
		ID:       "hub.prompt-inject." + label,
		Events:   []EventType{EventPromptBuilding},
		Mode:     ModeTransform,
		Priority: 500,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			content := contentFn()
			if content == "" {
				return &HookResponse{Decision: Allow}
			}
			return &HookResponse{
				Decision: Allow,
				Injections: []Injection{{
					Position: "system",
					Content:  content,
					Label:    label,
					Priority: 500,
				}},
			}
		},
	}
}
