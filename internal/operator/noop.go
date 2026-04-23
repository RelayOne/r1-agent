package operator

import "context"

// NoOp auto-answers Ask with a configurable default and silently drops Notify.
// Useful for tests and non-interactive runs (e.g. CI).
type NoOp struct {
	Default string // returned verbatim from Ask; "" if caller doesn't care
}

func (n *NoOp) Ask(_ context.Context, _ string, _ []Option) (string, error) {
	return n.Default, nil
}

func (n *NoOp) Notify(NotifyKind, string) {}
