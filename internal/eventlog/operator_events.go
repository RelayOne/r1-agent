package eventlog

// OperatorEventKinds enumerates the event types emitted by sessionctl
// verb handlers. New kinds must be added here AND to the handlers in
// internal/sessionctl/handlers.go.
var OperatorEventKinds = []string{
	"operator.approve",
	"operator.override",
	"operator.budget_change",
	"operator.pause",
	"operator.resume",
	"operator.inject",
	"operator.takeover_start",
	"operator.takeover_end",
}

// IsOperatorEvent returns true if kind is a known operator event type.
func IsOperatorEvent(kind string) bool {
	for _, k := range OperatorEventKinds {
		if k == kind {
			return true
		}
	}
	return false
}
