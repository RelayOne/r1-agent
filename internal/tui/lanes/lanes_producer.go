package lanes

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

// runProducer is the single coalescer goroutine that fans upstream
// transport events into the model's bounded sub channel. It runs from
// Init() (item 11) and terminates on ctx.Done().
//
// Per specs/tui-lanes.md §"Subscription Wiring" final paragraph:
//
//	The producer goroutine (`runProducer`) wraps `Transport.Subscribe`
//	with a 200–300 ms coalesce window: it holds a `map[laneID]
//	laneTickMsg`, overwriting on each upstream event; on the timer
//	tick, it flushes all queued lanes into `m.sub` and resets the
//	map. Status changes (Pending→Running→Done) bypass the coalescer
//	(sent immediately) so transitions feel snappy.
//
// And §"Component model" sentinel rule:
//
//	`m.sub` is never closed by Update; only by `runProducer` on
//	context cancel, in which case the final receive returns the zero
//	value and the model treats `LaneID == ""` as a no-op.
//
// runProducer signals shutdown by sending a zero-value laneTickMsg
// (LaneID == "") on m.sub before returning. We do NOT close(m.sub)
// because Update may still be holding a re-armed waitForLaneTick cmd
// that would race with the close otherwise — the sentinel zero-value
// path is the one Bubble-Tea-safe way to terminate.
//
// The channel element type is tea.Msg so the producer can push every
// streaming variant (laneTickMsg, laneStartMsg, laneEndMsg,
// laneListMsg, killAckMsg, budgetMsg) through one channel, matching
// the spec's "every Update branch that consumes m.sub re-arms
// waitForLaneTick" rule.
//
// Concurrency:
//   - Reads exclusively from `in` (the transport's Subscribe channel).
//     Writes exclusively to m.sub.
//   - Holds no model locks. Status-change bypass uses LaneEvent fields
//     only.
func (m *Model) runProducer(ctx context.Context) {
	// in is the channel the Transport pushes events onto. Owned by
	// runProducer (this goroutine creates it, the transport writes to
	// it via Subscribe, this goroutine drains it). Buffer matches
	// m.sub for symmetry — a brief upstream burst is absorbed without
	// blocking the transport's hub handler.
	in := make(chan LaneEvent, 32)

	// Subscribe runs in its own goroutine because the transport
	// contract is "Subscribe blocks until ctx cancels". Errors are
	// silently dropped here; surfacing transport errors via a
	// transportErrMsg routed through Update is a follow-up that
	// doesn't gate this commit.
	go func() {
		defer close(in)
		if m.transport == nil {
			<-ctx.Done()
			return
		}
		_ = m.transport.Subscribe(ctx, m.sessionID, in)
	}()

	// coalesce holds the most-recent tick per lane within one tick
	// window plus separate queues for non-coalesced events.
	type coalesceState struct {
		ticks      map[string]LaneEvent // keyed by lane id; overwrite-on-write
		listSnap   *LaneEvent           // single most-recent list (rare)
		hasBudget  bool
		budget     LaneEvent
		ackQueue   []LaneEvent // every kill-ack flushes (no merge)
		startQueue []LaneEvent // bypass: every start fires its own message
		endQueue   []LaneEvent // bypass: terminal transitions are snappy
	}
	state := coalesceState{ticks: make(map[string]LaneEvent)}

	// flush drains all queued events into m.sub in a deterministic
	// order: list first, then starts (so the model has the lane
	// before any tick), then ends, then ticks, then acks, then
	// budget. Every send is select-guarded by ctx so a hung Update
	// loop doesn't pin the goroutine.
	flush := func() {
		if state.listSnap != nil {
			lanes := make([]LaneSnapshot, len(state.listSnap.List))
			copy(lanes, state.listSnap.List)
			send(ctx, m.sub, laneListMsg{Lanes: lanes})
			state.listSnap = nil
		}
		for _, e := range state.startQueue {
			s := e.Snapshot
			send(ctx, m.sub, laneStartMsg{
				LaneID:    s.ID,
				Title:     s.Title,
				Role:      s.Role,
				StartedAt: s.StartedAt,
			})
		}
		state.startQueue = state.startQueue[:0]

		for _, e := range state.endQueue {
			s := e.Snapshot
			send(ctx, m.sub, laneEndMsg{
				LaneID:  s.ID,
				Final:   s.Status,
				CostUSD: s.CostUSD,
				Tokens:  s.Tokens,
			})
		}
		state.endQueue = state.endQueue[:0]

		for id, e := range state.ticks {
			s := e.Snapshot
			send(ctx, m.sub, laneTickMsg{
				LaneID:   id,
				Activity: s.Activity,
				Tokens:   s.Tokens,
				CostUSD:  s.CostUSD,
				Status:   s.Status,
				Model:    s.Model,
				Elapsed:  elapsed(s),
				Err:      s.Err,
			})
			delete(state.ticks, id)
		}

		for _, e := range state.ackQueue {
			send(ctx, m.sub, killAckMsg{LaneID: e.LaneID, Err: e.Err})
		}
		state.ackQueue = state.ackQueue[:0]

		if state.hasBudget {
			send(ctx, m.sub, budgetMsg{SpentUSD: state.budget.SpentUSD, LimitUSD: state.budget.LimitUSD})
			state.hasBudget = false
		}
	}

	tick := time.NewTicker(time.Duration(PRODUCER_TICK_MS) * time.Millisecond)
	defer tick.Stop()

	defer func() {
		// Sentinel zero-value flush: signals to Update that the
		// producer has shut down. Update treats LaneID == "" as a
		// no-op terminal — see lanes_update.go (item 12).
		select {
		case m.sub <- laneTickMsg{}:
		default:
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case ev, ok := <-in:
			if !ok {
				flush()
				return
			}
			switch ev.Kind {
			case "list":
				e := ev
				state.listSnap = &e
				flush()
			case "start":
				state.startQueue = append(state.startQueue, ev)
				// Bypass: a start IS a status transition into
				// Pending/Running and the spec requires snappy
				// transitions.
				flush()
			case "end":
				state.endQueue = append(state.endQueue, ev)
				flush()
			case "tick":
				prev, had := state.ticks[ev.Snapshot.ID]
				state.ticks[ev.Snapshot.ID] = ev
				if had && prev.Snapshot.Status != ev.Snapshot.Status {
					// Status-change bypass.
					flush()
				}
			case "kill_ack":
				state.ackQueue = append(state.ackQueue, ev)
				flush()
			case "budget":
				state.hasBudget = true
				state.budget = ev
				// Budget changes piggyback on the next tick.
			default:
				// Unknown kind — drop. Don't panic on a
				// malformed remote-transport event.
			}

		case <-tick.C:
			flush()
		}
	}
}

// send pushes msg onto out, respecting ctx cancellation. If ctx is
// cancelled mid-send the message is dropped (the model is shutting
// down; no consumer will ever read it).
func send(ctx context.Context, out chan<- tea.Msg, msg tea.Msg) {
	select {
	case out <- msg:
	case <-ctx.Done():
	}
}

// elapsed returns the duration since the lane's start time, or zero if
// StartedAt is the zero value (defensive for transports that don't
// stamp StartedAt on tick events).
func elapsed(s LaneSnapshot) time.Duration {
	if s.StartedAt.IsZero() {
		return 0
	}
	return time.Since(s.StartedAt)
}
