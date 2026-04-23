// Package operator defines the human-in-the-loop interface for
// Stoke's interactive surfaces (chat, ship, run).
//
// The interface is intentionally narrow: two methods, Ask and Notify.
// Ask blocks on a response; Notify does not. Implementations:
//   - Terminal: reads from os.Stdin, writes to os.Stdout
//   - NDJSON: emits prompts on stdout, reads replies from stdin (for
//     CloudSwarm HITL) — deferred to spec-19 agent-serve-async.
//   - NoOp: auto-answers with a default, for tests.
package operator

import "context"

// Option is one selectable answer in an Ask prompt.
type Option struct {
	Label string // what the operator types / clicks
	Hint  string // optional human-readable description
}

// NotifyKind classifies the severity / channel of a Notify message.
type NotifyKind int

const (
	KindInfo NotifyKind = iota
	KindWarn
	KindError
	KindSuccess
)

// Operator is the human-in-the-loop interface.
type Operator interface {
	// Ask presents prompt and options; blocks for a response. Returns
	// the chosen Option.Label (or an empty string on ctx cancel / EOF).
	//
	// If options is empty, the method returns free-text input.
	Ask(ctx context.Context, prompt string, options []Option) (string, error)

	// Notify streams a single message to the operator. Non-blocking.
	Notify(kind NotifyKind, message string)
}
