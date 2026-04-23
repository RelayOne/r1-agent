package executor

import (
	"fmt"
)

// This file holds scaffolding entries for Tasks 20-24 — each
// satisfies Executor so the router can register them today, while
// real per-type logic lands in the follow-up commits noted per
// type. Execute returns a clearly labelled
// ErrExecutorNotWired error; callers (CLI, router) detect this
// sentinel and print a helpful message. BuildCriteria /
// BuildRepairFunc / BuildEnvFixFunc return nil — the descent engine
// treats nil as "executor has no verification/repair primitive yet"
// and short-circuits.

// ErrExecutorNotWired is the sentinel returned by Execute on an
// executor whose real pipeline has not been wired yet. Callers
// check for it via errors.Is so they can print an operator-friendly
// message instead of a stack trace.
type ErrExecutorNotWired struct {
	// Type is the executor type that was invoked.
	Type TaskType
	// FollowUp names the task(s) that will land the real pipeline.
	FollowUp string
}

// Error implements the error interface.
func (e *ErrExecutorNotWired) Error() string {
	return fmt.Sprintf("%s executor not wired yet; lands in %s", e.Type.String(), e.FollowUp)
}

// ResearchExecutor is now wired — see internal/executor/research.go
// (Track B Task 20). The scaffolding stub that previously lived here
// has been replaced by a real claim-gated research implementation
// backed by the internal/research package.

// BrowserExecutor is now wired — see internal/executor/browser.go
// (Track B Task 21 part 1). Interactive actions (click, type,
// screenshot) land in part 2 with a go-rod backend.

// DeployExecutor is now wired — see internal/executor/deploy.go
// (Track B Task 22). The scaffolding entry that previously lived
// here has been replaced by a real fly.io-backed implementation.

// DelegateExecutor is now wired — see internal/executor/delegate.go
// (work-stoke TASK 2). The scaffolding stub that previously lived
// here has been replaced by a real implementation composing Hirer,
// Delegator, TrustPlane, and the a2a task-dispatch seam.
