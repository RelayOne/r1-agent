// jsonrpc/subscribe.go — Phase E item 32: subscribe semantics with
// per-subscription monotonic seq and replay-before-live ordering.
//
// # The two-phase contract
//
// When a client calls `session.subscribe` with `since_seq=N`, the daemon
// MUST deliver:
//
//  1. journal records with seq > N, in seq order, BEFORE
//  2. any live event the bus is currently fanning out.
//
// "Before" means in wire-arrival order: the WebSocket peer sees every
// replay frame, then every live frame, with no interleaving. The
// monotonic seq carried on each event preserves causal order across
// the boundary so a reconnecting client can prove it didn't miss
// anything.
//
// # How the buffer works
//
// Subscription holds a small buffered channel for live events. While
// the replay goroutine is running, live events that fire are queued
// (up to a bounded capacity) and flushed AFTER replay completes. If
// the buffer overflows, the subscription closes with `crash_recovery`
// — the client must reconnect with the new `since_seq` it observed
// last.
//
// The bound is chosen to absorb a typical lobe burst (~256 events)
// without excessive memory; LiveBufferCap is the package constant.
//
// # Why a dedicated type
//
// The DaemonAPI surface (TASK-31) declared the verb signatures but
// left the publish path open. This file ships the publish primitive
// — Subscription.Publish — that the daemon's bus subscriber calls for
// each live event. The replay path is driven by Subscription.Replay
// which the daemon invokes once at subscribe time.
package jsonrpc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/RelayOne/r1/internal/stokerr"
)

// LiveBufferCap is the per-subscription channel capacity used while
// replay is in flight. Set to 1024 — enough to absorb a multi-lobe
// burst without back-pressuring the bus, small enough that the worst-
// case memory use of one runaway subscription stays bounded.
const LiveBufferCap = 1024

// EventSink is the wire-side delivery callback. The daemon wires this
// to a *ws.Conn's WriteNotification path; tests provide a slice
// recorder. A non-nil error from EventSink stops the subscription
// (the wire is gone — no point continuing to deliver).
type EventSink func(ctx context.Context, ev *SubscriptionEvent) error

// JournalReplayer is the interface used by Subscription.Replay to pull
// the records-since-N sequence. The daemon implements this around
// `*journal.Reader`; tests can stub it with a slice. We keep the
// interface in this package so the daemon's wiring stays clean.
//
// Implementations MUST yield records in monotonic seq order and stop
// on the first handler error.
type JournalReplayer interface {
	ReplaySince(ctx context.Context, sinceSeq uint64, handler JournalHandler) error
}

// JournalHandler is the per-record callback for ReplaySince. Returning
// a non-nil error stops the replay.
type JournalHandler func(seq uint64, kind string, data any) error

// Subscription is one client's per-session feed. It owns:
//
//   - SubID — the server-minted subscription handle returned in
//     session.subscribe's response;
//   - sink — the EventSink that writes to the client transport;
//   - liveBuf — the bounded channel that holds events arriving during
//     replay so they can be flushed in order afterward;
//   - seq — the per-subscription monotonic sequence (independent of
//     the journal's per-session seq; this counter is what the client
//     uses for resume — see SubscriptionEvent.Seq);
//   - state — replay vs live transition flag (atomic).
//
// One Subscription serves one wire connection. The daemon mints a new
// Subscription per `session.subscribe` call; closing the connection
// closes the Subscription via Close.
type Subscription struct {
	SubID string

	// SessionID names the session this subscription tracks. Surfaced
	// here so daemon listeners can filter live events by session.
	SessionID string

	// Filter, when non-empty, restricts which events are delivered.
	// Empty means "all events". The daemon's bus subscriber consults
	// this before pushing into Publish.
	Filter map[string]struct{}

	sink EventSink

	// liveBuf absorbs live events that fire during replay. After
	// replay completes (state -> live), Publish calls bypass the buf
	// and write directly to sink. Capacity is LiveBufferCap.
	liveBuf chan *SubscriptionEvent

	// state transitions: 0 = pending, 1 = replay-in-progress, 2 = live, 3 = closed.
	state atomic.Int32

	// seq is the per-subscription monotonic counter. Bumps on EVERY
	// outbound event (replay AND live), giving the client one number
	// to resume from regardless of which side of the boundary the
	// event came from.
	seqMu sync.Mutex
	seq   uint64

	// closeOnce guards Close.
	closeOnce sync.Once
}

// Subscription state constants. Exposed so daemon code can assert
// state without a string compare.
const (
	subStatePending int32 = 0
	subStateReplay  int32 = 1
	subStateLive    int32 = 2
	subStateClosed  int32 = 3
)

// NewSubscription mints a fresh subscription for the given session +
// sink. The returned object is ready to receive Publish calls.
//
// subID is the caller-minted handle; production daemons use a ULID,
// tests can pass any unique string.
func NewSubscription(subID, sessionID string, sink EventSink, filter []string) *Subscription {
	s := &Subscription{
		SubID:     subID,
		SessionID: sessionID,
		sink:      sink,
		liveBuf:   make(chan *SubscriptionEvent, LiveBufferCap),
	}
	if len(filter) > 0 {
		s.Filter = make(map[string]struct{}, len(filter))
		for _, f := range filter {
			s.Filter[f] = struct{}{}
		}
	}
	return s
}

// Replay drains the journal (records with seq > sinceSeq) through the
// sink, then flushes any events that arrived live during the replay,
// then transitions state to "live" so subsequent Publish calls write
// directly to the sink.
//
// Replay-before-live ordering invariants (spec §11.32):
//
//   - Every journal record yielded BEFORE Replay returns is a replay
//     frame, in monotonic seq order.
//   - Every live event queued during Replay is flushed in arrival
//     order BEFORE the state flips to subStateLive.
//   - From subStateLive onward, Publish writes directly to the sink
//     without queuing.
//
// # Seq assignment
//
// Per-subscription seq is assigned at DELIVERY time, not at publish
// time. That's load-bearing: events queued during replay would
// otherwise interleave seqs with replay records (which are delivered
// later), breaking the monotonic-by-arrival contract clients depend
// on for resume.
//
// Returns:
//
//   - nil on a clean replay+flush.
//   - stokerr.ErrCrashRecovery wrapping ErrLiveBufferOverflow if the
//     liveBuf overflowed during replay (the client MUST reconnect).
//   - any error returned by the sink (caller decides to close).
func (s *Subscription) Replay(ctx context.Context, sinceSeq uint64, src JournalReplayer) error {
	if !s.state.CompareAndSwap(subStatePending, subStateReplay) {
		return errors.New("jsonrpc: subscription not in pending state")
	}
	if src != nil {
		err := src.ReplaySince(ctx, sinceSeq, func(seq uint64, kind string, data any) error {
			ev := &SubscriptionEvent{
				SubID: s.SubID,
				Seq:   s.nextSeq(),
				Type:  kind,
				Data:  data,
			}
			return s.sink(ctx, ev)
		})
		if err != nil {
			s.markClosed()
			return err
		}
	}
	// Flush any live events that arrived during replay. Each queued
	// event has Seq=0 (filled in here, at delivery time, so the
	// monotonic ordering matches arrival-on-the-wire — see TASK-32
	// commit message + the load-bearing comment above).
	for {
		select {
		case ev := <-s.liveBuf:
			ev.Seq = s.nextSeq()
			if err := s.sink(ctx, ev); err != nil {
				s.markClosed()
				return err
			}
		default:
			// Buffer is empty — flip to live BEFORE dropping the lock.
			// New Publish calls will see subStateLive and bypass the
			// channel; the small window where Publish raced into the
			// channel right before the flip is handled by the loop
			// below.
			s.state.Store(subStateLive)
			// Drain any stragglers that landed in the race window.
			for {
				select {
				case ev := <-s.liveBuf:
					ev.Seq = s.nextSeq()
					if err := s.sink(ctx, ev); err != nil {
						s.markClosed()
						return err
					}
				default:
					return nil
				}
			}
		}
	}
}

// Publish delivers one live event. Behaviour depends on state:
//
//   - subStatePending: rejected (caller must Replay first). This is a
//     programmer error — the daemon's wiring drives state transitions.
//   - subStateReplay: queued in liveBuf. If the buffer is full,
//     ErrLiveBufferOverflow surfaces and the subscription is closed
//     (the client missed events; reconnect with last seq).
//   - subStateLive: delivered immediately via the sink.
//   - subStateClosed: dropped silently (the wire is gone).
//
// The seq on the outbound event is assigned in Publish, not in the
// caller, so there's exactly one source of monotonicity per
// subscription.
func (s *Subscription) Publish(ctx context.Context, eventType string, data any) error {
	// Filter check first — gate before allocating an event struct.
	if !s.matchesFilter(eventType) {
		return nil
	}

	state := s.state.Load()
	switch state {
	case subStatePending:
		return errors.New("jsonrpc: publish before replay")
	case subStateClosed:
		return nil
	}

	// Build the event WITHOUT a seq during the replay phase — Replay
	// fills in the seq when it dequeues so monotonicity matches
	// delivery order, not enqueue order. In live state we assign the
	// seq immediately because there's no queue to reorder against.
	ev := &SubscriptionEvent{
		SubID: s.SubID,
		Type:  eventType,
		Data:  data,
	}

	if state == subStateReplay {
		select {
		case s.liveBuf <- ev:
			return nil
		default:
			s.markClosed()
			return ErrLiveBufferOverflow
		}
	}
	// Live state: assign seq + deliver. Sink errors close the
	// subscription.
	ev.Seq = s.nextSeq()
	if err := s.sink(ctx, ev); err != nil {
		s.markClosed()
		return err
	}
	return nil
}

// matchesFilter reports whether eventType passes the filter. Empty
// filter passes everything (the common case).
func (s *Subscription) matchesFilter(eventType string) bool {
	if len(s.Filter) == 0 {
		return true
	}
	_, ok := s.Filter[eventType]
	return ok
}

// nextSeq bumps the per-subscription seq atomically.
func (s *Subscription) nextSeq() uint64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.seq++
	return s.seq
}

// LastSeq returns the most recently issued seq. Tests use this to
// assert the boundary count between replay and live frames.
func (s *Subscription) LastSeq() uint64 {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return s.seq
}

// IsClosed reports whether Close has been called or the subscription
// auto-closed (overflow / sink error). Bus subscribers consult this
// before publishing to short-circuit the close-then-publish race.
func (s *Subscription) IsClosed() bool {
	return s.state.Load() == subStateClosed
}

// Close winds down the subscription. Idempotent. After Close, Publish
// drops events silently and Replay returns an error.
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		s.state.Store(subStateClosed)
	})
}

// markClosed is the internal close path used when the subscription
// must self-close due to overflow or sink error. Same idempotent
// semantics as Close; kept distinct so the call site in Publish/Replay
// reads as "the publish path detected an unrecoverable condition" vs
// the explicit Close from the daemon's teardown.
func (s *Subscription) markClosed() {
	s.closeOnce.Do(func() {
		s.state.Store(subStateClosed)
	})
}

// ErrLiveBufferOverflow is returned by Publish when the liveBuf is
// full during the replay phase. The subscription is closed; the
// client must reconnect with the last seq it observed and re-replay
// from there.
//
// Mapped to stokerr.ErrCrashRecovery on the wire (the caller can see
// "events were lost; please re-fetch state").
var ErrLiveBufferOverflow = stokerr.New(stokerr.ErrCrashRecovery,
	"jsonrpc: live buffer overflowed during replay; reconnect with last seq")
