package hub

import (
	"context"

	"github.com/RelayOne/r1-agent/internal/consent"
	"github.com/RelayOne/r1-agent/internal/flowtrack"
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

// TaskHookAdapter bridges the workflow.TaskHook interface to hub events.
// Register BeforeTask as a gate on task.started, AfterTask as an observer
// on task.completed/task.failed, and BeforeRetry as a transform on task.retrying.
type TaskHookAdapter struct {
	// BeforeTaskFn is called before task execution. Return error to abort.
	BeforeTaskFn func(ctx context.Context, taskID string) error
	// AfterTaskFn is called after task execution (success or failure).
	AfterTaskFn func(ctx context.Context, taskID string, success bool)
	// BeforeRetryFn is called before retry. Returns prompt augmentation.
	BeforeRetryFn func(ctx context.Context, taskID string, attempt int) string
}

// Subscribers returns the hub subscribers for this TaskHook adapter.
func (a TaskHookAdapter) Subscribers(id string) []Subscriber {
	var subs []Subscriber

	if a.BeforeTaskFn != nil {
		subs = append(subs, Subscriber{
			ID:       id + ".before-task",
			Events:   []EventType{EventTaskStarted},
			Mode:     ModeGate,
			Priority: 100,
			Handler: func(ctx context.Context, ev *Event) *HookResponse {
				if err := a.BeforeTaskFn(ctx, ev.TaskID); err != nil {
					return &HookResponse{Decision: Deny, Reason: err.Error()}
				}
				return &HookResponse{Decision: Allow}
			},
		})
	}

	if a.AfterTaskFn != nil {
		subs = append(subs, Subscriber{
			ID:     id + ".after-task",
			Events: []EventType{EventTaskCompleted, EventTaskFailed},
			Mode:   ModeObserve,
			Handler: func(ctx context.Context, ev *Event) *HookResponse {
				a.AfterTaskFn(ctx, ev.TaskID, ev.Type == EventTaskCompleted)
				return nil
			},
		})
	}

	if a.BeforeRetryFn != nil {
		subs = append(subs, Subscriber{
			ID:       id + ".before-retry",
			Events:   []EventType{EventTaskRetrying},
			Mode:     ModeTransform,
			Priority: 200,
			Handler: func(ctx context.Context, ev *Event) *HookResponse {
				attempt := 0
				if ev.Lifecycle != nil {
					attempt = ev.Lifecycle.Attempt
				}
				aug := a.BeforeRetryFn(ctx, ev.TaskID, attempt)
				if aug == "" {
					return &HookResponse{Decision: Allow}
				}
				return &HookResponse{
					Decision: Allow,
					Injections: []Injection{{
						Position: "retry_context",
						Content:  aug,
						Label:    id + "-retry",
						Priority: 200,
					}},
				}
			},
		})
	}

	return subs
}

// FlowTrackObserver creates a hub observer that feeds tool/file/git events
// into a flowtrack.Tracker for development phase inference.
func FlowTrackObserver(tracker *flowtrack.Tracker) Subscriber {
	return Subscriber{
		ID:     "hub.flowtrack",
		Events: []EventType{EventToolFileWrite, EventToolFileRead, EventGitPostCommit, EventGitPostMerge, "tool.*"},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			var action flowtrack.Action
			switch {
			case ev.File != nil:
				if ev.Type == EventToolFileWrite {
					action = flowtrack.Action{Type: flowtrack.ActionFileEdit, Target: ev.File.Path}
				} else {
					action = flowtrack.Action{Type: flowtrack.ActionFileOpen, Target: ev.File.Path}
				}
			case ev.Git != nil:
				if ev.Type == EventGitPostCommit {
					action = flowtrack.Action{Type: flowtrack.ActionGitCommit, Target: ev.Git.Branch}
				} else {
					action = flowtrack.Action{Type: flowtrack.ActionGitBranch, Target: ev.Git.Branch}
				}
			case ev.Tool != nil:
				action = flowtrack.Action{Type: flowtrack.ActionToolCall, Target: ev.Tool.Name}
			default:
				return nil
			}
			tracker.Record(action)
			return nil
		},
	}
}

// ConsentGate creates a hub gate subscriber that enforces human-in-the-loop
// approval for dangerous operations via a consent.Workflow.
func ConsentGate(workflow *consent.Workflow) Subscriber {
	return Subscriber{
		ID:       "hub.consent",
		Events:   []EventType{EventGitPreCommit, EventGitPreMerge, EventToolFileWrite, "tool.exec"},
		Mode:     ModeGate,
		Priority: 5, // run before other gates
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			var operation, category, description string
			switch {
			case ev.Git != nil:
				operation = string(ev.Type)
				category = "git"
				description = "Git operation: " + ev.Git.Branch
			case ev.File != nil:
				operation = "write " + ev.File.Path
				category = "file"
				description = "File write: " + ev.File.Path
			case ev.Tool != nil:
				operation = ev.Tool.Name
				category = "exec"
				description = "Tool execution: " + ev.Tool.Name
			default:
				return &HookResponse{Decision: Allow}
			}

			decision := workflow.Check(operation, category, description)
			switch decision {
			case consent.DecisionDenied, consent.DecisionBlocked:
				return &HookResponse{Decision: Deny, Reason: "consent: " + string(decision)}
			case consent.DecisionApproved, consent.DecisionPending, consent.DecisionAuto:
				return &HookResponse{Decision: Allow}
			default:
				return &HookResponse{Decision: Allow}
			}
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
