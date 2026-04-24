package eventlog

import (
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
)

// EmitBus is the single blessed bridge between the durable event log and
// the in-memory hub. It appends ev to l FIRST and only then publishes it to
// b. Rationale:
//
//   - If the SQLite append fails, no subscriber ever sees a phantom event
//     with no durable record behind it.
//   - If the publish fails or a subscriber panics, the durable record still
//     exists and can be replayed later.
//
// Append mutates ev.ID, ev.Sequence, and ev.Timestamp in place; the Publish
// that follows carries those mutated fields.
func EmitBus(b *bus.Bus, l *Log, ev bus.Event) error {
	if l == nil {
		return fmt.Errorf("eventlog: EmitBus: nil Log")
	}
	if b == nil {
		return fmt.Errorf("eventlog: EmitBus: nil Bus")
	}
	if err := l.Append(&ev); err != nil {
		return fmt.Errorf("eventlog: EmitBus append: %w", err)
	}
	if err := b.Publish(ev); err != nil {
		return fmt.Errorf("eventlog: EmitBus publish: %w", err)
	}
	return nil
}
