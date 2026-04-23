// Package eventlog — resume decision logic.
//
// DecideResume is a pure, IO-free classifier that inspects a sequence of
// bus.Events (already filtered to a single session by the caller) and
// decides what a resume-from-crash run should do next:
//
//   - No events → ResumeFreshStart (nothing to resume from).
//   - Last task.dispatch had no matching task.complete / task.fail →
//     ResumeRetryTask (re-dispatch the same task).
//   - Last task.complete / task.fail → ResumeNextTask (the crash happened
//     after the task settled but before the next one was dispatched).
//   - A session.end (or session.complete) is present as the most recent
//     session marker → ResumeAlreadyDone (no-op; the session finished).
//
// This is the minimal viable subset of spec item 25. Orphan tool-call
// synthesis and plan.SOW lookahead are intentionally out of scope here
// (spec items 26, 35) and will be layered on top once the core decision
// logic is stable.
package eventlog

import (
	"strings"

	"github.com/ericmacdougall/stoke/internal/bus"
)

// ResumeMode enumerates the four possible outcomes of DecideResume.
type ResumeMode int

const (
	// ResumeFreshStart means the event slice is empty: there is nothing
	// to resume from, so the caller should start a brand-new session.
	ResumeFreshStart ResumeMode = iota

	// ResumeRetryTask means the last observed task marker was a
	// task.dispatch with no matching task.complete or task.fail. The
	// caller should re-dispatch the same task (nextTaskID holds its
	// Scope.TaskID, falling back to the dispatch event's ID).
	ResumeRetryTask

	// ResumeNextTask means the last observed task marker was a
	// task.complete or task.fail. The caller should advance to the next
	// task in the plan. nextTaskID is the ID of the just-completed task
	// (callers use it as an anchor when walking the plan).
	ResumeNextTask

	// ResumeAlreadyDone means the session has already run to completion
	// (session.end / session.complete observed). No dispatch needed.
	ResumeAlreadyDone
)

// String renders a ResumeMode as a short, lowercase identifier suitable
// for logs and test failure messages.
func (m ResumeMode) String() string {
	switch m {
	case ResumeFreshStart:
		return "fresh_start"
	case ResumeRetryTask:
		return "retry_task"
	case ResumeNextTask:
		return "next_task"
	case ResumeAlreadyDone:
		return "already_done"
	default:
		return "unknown"
	}
}

// DecideResume walks events backwards and returns the resume mode plus
// (when applicable) the task ID that anchors the decision. The function
// is pure — no IO, no mutation of the input slice, no time dependency.
//
// Decision table (in order of precedence, most-recent event wins):
//
//	len(events) == 0                → (""      , ResumeFreshStart)
//	last session.* is session.end   → (""      , ResumeAlreadyDone)
//	last task.* is task.dispatch    → (taskID  , ResumeRetryTask)
//	last task.* is task.complete    → (taskID  , ResumeNextTask)
//	last task.* is task.fail        → (taskID  , ResumeNextTask)
//	no task.* or session.* markers  → (""      , ResumeFreshStart)
//
// "Session end" matches both `session.end` and `session.complete` for
// compatibility with the two naming conventions in use across the
// codebase (specs/cloudswarm-protocol.md uses session.start/complete;
// operator-ux-memory.md uses session.started/completed). The matcher is
// suffix-based so optional prefixes (e.g. "stoke.session.end") also
// resolve correctly.
func DecideResume(events []bus.Event) (nextTaskID string, mode ResumeMode) {
	if len(events) == 0 {
		return "", ResumeFreshStart
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		t := string(ev.Type)
		switch {
		case isSessionEnd(t):
			return "", ResumeAlreadyDone
		case isTaskDispatch(t):
			return taskAnchor(ev), ResumeRetryTask
		case isTaskComplete(t), isTaskFail(t):
			return taskAnchor(ev), ResumeNextTask
		}
	}
	// No task.* or session.* markers found.
	return "", ResumeFreshStart
}

// taskAnchor returns the best-effort identifier for the task the event
// refers to. Prefers Scope.TaskID (populated by the SOW runner); falls
// back to the event's own ID so the caller at least has a correlation
// anchor rather than an empty string.
func taskAnchor(ev bus.Event) string {
	if ev.Scope.TaskID != "" {
		return ev.Scope.TaskID
	}
	return ev.ID
}

// isTaskDispatch matches "task.dispatch" or "task.dispatched" with any
// dotted prefix (e.g. "stoke.task.dispatch").
func isTaskDispatch(t string) bool {
	return hasTaskSuffix(t, "dispatch") || hasTaskSuffix(t, "dispatched") ||
		hasTaskSuffix(t, "start") || hasTaskSuffix(t, "started")
}

// isTaskComplete matches task.complete / task.completed.
func isTaskComplete(t string) bool {
	return hasTaskSuffix(t, "complete") || hasTaskSuffix(t, "completed")
}

// isTaskFail matches task.fail / task.failed.
func isTaskFail(t string) bool {
	return hasTaskSuffix(t, "fail") || hasTaskSuffix(t, "failed")
}

// isSessionEnd matches session.end / session.complete / session.completed
// / session.aborted — anything that signals the session is terminal.
func isSessionEnd(t string) bool {
	return hasSessionSuffix(t, "end") || hasSessionSuffix(t, "ended") ||
		hasSessionSuffix(t, "complete") || hasSessionSuffix(t, "completed") ||
		hasSessionSuffix(t, "aborted") || hasSessionSuffix(t, "failed")
}

// hasTaskSuffix reports whether t is "task.<sfx>" or ends in
// ".task.<sfx>" (permitting an optional dotted prefix such as "stoke.").
func hasTaskSuffix(t, sfx string) bool {
	needle := "task." + sfx
	return t == needle || strings.HasSuffix(t, "."+needle)
}

// hasSessionSuffix mirrors hasTaskSuffix for session.* events.
func hasSessionSuffix(t, sfx string) bool {
	needle := "session." + sfx
	return t == needle || strings.HasSuffix(t, "."+needle)
}
