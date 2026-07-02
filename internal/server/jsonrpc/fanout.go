// jsonrpc/fanout.go — live subscription fanout + journal replay seams
// (audit A008/A069).
//
// Before this file, DaemonSessionSubscribe minted Subscriptions whose
// sink wrote to /dev/null ("writing to /dev/null today" — the discard
// stub the audit flagged at daemonapi_impl.go:322) and no component
// ever published live events into them. This file supplies the three
// missing pieces:
//
//  1. A per-HubHandler fanout registry (sessionID → SubID →
//     *Subscription) with PublishSessionEvent / PublishHubEvent as
//     the daemon-side publish entry points. Any code path that
//     dispatches a session event (Session.DispatchEvent → the
//     journal-first hook installed by attachSessionEventPipe) now
//     reaches every live subscriber.
//
//  2. FileJournalReplayer — the JournalReplayer implementation over
//     internal/journal that Subscription.Replay drives for the
//     replay-before-live contract (spec §11.32). The daemon's serve
//     glue points HubHandler.JournalPathFn at the per-session journal
//     file; subscribe calls replay records with seq > since_seq.
//
//  3. SubscribeSessionWithSink — the shared subscribe engine used by
//     the SSE bridge (internal/server/sse, spec item 33) and the web
//     typed-frame bridge (internal/server/ws.WebHandler). It mints a
//     Subscription, registers it in the fanout, runs the synchronous
//     journal replay, and returns a cancel func that tears everything
//     down.
//
// The WS JSON-RPC path (DaemonSessionSubscribe) uses the same fanout
// but replays asynchronously so the subscribe RPC response reaches
// the wire before the replay frames.
package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/journal"
	"github.com/RelayOne/r1/internal/server/sessionhub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// NotificationWriter is the structural slice of the WS connection the
// subscribe path needs to deliver `$/event` frames. *ws.Conn satisfies
// it via WriteNotification; declaring it here (like
// SubscriptionRegistry) keeps jsonrpc free of an import cycle into ws.
type NotificationWriter interface {
	WriteNotification(ctx context.Context, n *Notification) error
}

// fanoutSubscription is the closer stored in the per-connection
// SubscriptionRegistry. Close tears down the Subscription AND removes
// it from the HubHandler fanout so connection teardown
// (Conn.CloseAllSubscriptions) and session.unsubscribe both leave no
// orphaned fanout entries.
type fanoutSubscription struct {
	sub     *Subscription
	onClose func()
	once    sync.Once
}

// Close is idempotent; safe from unsubscribe and conn-teardown paths.
func (f *fanoutSubscription) Close() {
	f.once.Do(func() {
		f.sub.Close()
		if f.onClose != nil {
			f.onClose()
		}
	})
}

// registerFanout adds sub to the sessionID bucket.
func (h *HubHandler) registerFanout(sessionID string, sub *Subscription) {
	h.fanoutMu.Lock()
	defer h.fanoutMu.Unlock()
	if h.fanout == nil {
		h.fanout = make(map[string]map[string]*Subscription)
	}
	bucket := h.fanout[sessionID]
	if bucket == nil {
		bucket = make(map[string]*Subscription)
		h.fanout[sessionID] = bucket
	}
	bucket[sub.SubID] = sub
}

// unregisterFanout removes one subscription; empty buckets are pruned.
func (h *HubHandler) unregisterFanout(sessionID, subID string) {
	h.fanoutMu.Lock()
	defer h.fanoutMu.Unlock()
	bucket := h.fanout[sessionID]
	if bucket == nil {
		return
	}
	delete(bucket, subID)
	if len(bucket) == 0 {
		delete(h.fanout, sessionID)
	}
}

// SubscriberCount reports the number of live subscriptions for a
// session. Test + diagnostics helper.
func (h *HubHandler) SubscriberCount(sessionID string) int {
	h.fanoutMu.Lock()
	defer h.fanoutMu.Unlock()
	return len(h.fanout[sessionID])
}

// PublishSessionEvent fans one live event out to every subscription
// registered for sessionID. This is the real publish path that
// replaces the discard sink (audit A069):
//
//   - closed subscriptions are pruned;
//   - subscriptions whose replay has not started yet (pending) are
//     skipped — the event is already journaled by the journal-first
//     hook, so their replay covers it;
//   - a Publish error (sink failure / live-buffer overflow) closes
//     and prunes the subscription; the client reconnects with its
//     last seq per the crash_recovery contract.
func (h *HubHandler) PublishSessionEvent(ctx context.Context, sessionID, eventType string, data any) {
	h.fanoutMu.Lock()
	bucket := h.fanout[sessionID]
	subs := make([]*Subscription, 0, len(bucket))
	for _, sub := range bucket {
		subs = append(subs, sub)
	}
	h.fanoutMu.Unlock()

	for _, sub := range subs {
		if sub.IsClosed() {
			h.unregisterFanout(sessionID, sub.SubID)
			continue
		}
		if sub.state.Load() == subStatePending {
			// Replay not started yet; the journal-first hook already
			// persisted this event so replay will deliver it.
			continue
		}
		if err := sub.Publish(ctx, eventType, data); err != nil {
			h.unregisterFanout(sessionID, sub.SubID)
		}
	}
}

// PublishHubEvent is the hub.Event-typed convenience the session
// event pipe calls: event type on the wire is the canonical hub
// EventType string ("lane.delta", "tool.post_use", ...).
func (h *HubHandler) PublishHubEvent(ctx context.Context, sessionID string, ev *hub.Event) {
	if ev == nil {
		return
	}
	h.PublishSessionEvent(ctx, sessionID, string(ev.Type), ev)
}

// SubscribeSessionWithSink is the shared subscribe engine for
// non-JSON-RPC transports (SSE bridge, web typed-frame bridge). It:
//
//  1. validates the session via the hub;
//  2. mints a *Subscription bound to sink;
//  3. registers it in the live fanout (events arriving during replay
//     buffer per the Subscription state machine);
//  4. drives the synchronous journal replay from sinceSeq;
//  5. returns a cancel func (close + fanout unregister).
//
// The sink is invoked for replayed AND live events, in
// replay-before-live order, with the per-subscription monotonic seq.
func (h *HubHandler) SubscribeSessionWithSink(ctx context.Context, sessionID string, sinceSeq uint64, filter []string, sink EventSink) (func(), error) {
	if sink == nil {
		return nil, stokerr.New(stokerr.ErrValidation, "subscribe: nil sink")
	}
	if _, err := h.lookupSession(sessionID); err != nil {
		return nil, err
	}
	subID := mintSubID()
	sub := NewSubscription(subID, sessionID, sink, filter)
	h.registerFanout(sessionID, sub)
	if err := sub.Replay(ctx, sinceSeq, h.replayerFor(sessionID)); err != nil {
		sub.Close()
		h.unregisterFanout(sessionID, subID)
		return nil, err
	}
	cancel := func() {
		sub.Close()
		h.unregisterFanout(sessionID, subID)
	}
	return cancel, nil
}

// replayerFor resolves the per-session JournalReplayer, or nil when
// no journal path is configured (subscription flips straight to live).
func (h *HubHandler) replayerFor(sessionID string) JournalReplayer {
	h.mu.Lock()
	fn := h.journalPathFn
	h.mu.Unlock()
	if fn == nil {
		return nil
	}
	path := fn(sessionID)
	if path == "" {
		return nil
	}
	return FileJournalReplayer{Path: path}
}

// SetJournalPathFn installs the per-session journal path resolver.
// The serve glue calls this once at startup (before any session is
// created) pointing at `<journalDir>/<sessionID>.jsonl`. nil disables
// journaling + replay (subscriptions are live-only).
func (h *HubHandler) SetJournalPathFn(fn func(sessionID string) string) {
	h.mu.Lock()
	h.journalPathFn = fn
	h.mu.Unlock()
}

// attachSessionEventPipe wires a freshly created session's event path:
//
//   - opens the per-session journal writer (when a path resolver is
//     configured) and installs it via Session.SetJournal;
//   - installs the journal-first OnEvent hook: append to the journal
//     (kind = the hub EventType so replay emits the same event types
//     the live fanout does), flush so concurrent replay readers see
//     the record, then fan out to live subscribers.
//
// Called by DaemonSessionStart. The journal-first invariant from spec
// §11.24 holds: a journal append failure aborts the dispatch and no
// subscriber observes the event.
func (h *HubHandler) attachSessionEventPipe(s *sessionhub.Session) {
	var jw *journal.Writer
	h.mu.Lock()
	fn := h.journalPathFn
	h.mu.Unlock()
	if fn != nil {
		if path := fn(s.ID); path != "" {
			w, err := journal.OpenWriter(path, journal.WriterOptions{})
			if err == nil {
				jw = w
				s.SetJournal(jw)
				h.journalsMu.Lock()
				if h.journals == nil {
					h.journals = make(map[string]*journal.Writer)
				}
				h.journals[s.ID] = jw
				h.journalsMu.Unlock()
			}
			// An open failure degrades to live-only fanout rather
			// than blocking session creation; replay simply finds an
			// absent journal (= empty).
		}
	}
	sessionID := s.ID
	s.SetOnEvent(func(ctx context.Context, ev *hub.Event) error {
		if jw != nil {
			if _, err := jw.Append(string(ev.Type), ev); err != nil {
				return errors.New("jsonrpc: journal append failed; event not fanned out: " + err.Error())
			}
			// Flush to the OS page cache so a concurrent replay
			// (subscribe racing a dispatch) observes the record.
			// Terminal kinds already fsynced inside Append.
			_ = jw.Flush()
		}
		h.PublishHubEvent(ctx, sessionID, ev)
		return nil
	})
}

// closeSessionJournal closes and forgets the per-session journal
// writer. Called by DaemonSessionCancel after hub.Delete.
func (h *HubHandler) closeSessionJournal(sessionID string) {
	h.journalsMu.Lock()
	jw := h.journals[sessionID]
	delete(h.journals, sessionID)
	h.journalsMu.Unlock()
	if jw != nil {
		_ = jw.Close()
	}
}

// FileJournalReplayer adapts internal/journal to the JournalReplayer
// interface Subscription.Replay consumes. Records with seq <= sinceSeq
// are skipped; the handler receives (seq, kind, data) where data is
// the raw JSON payload (json.RawMessage) of the journal record.
//
// A corrupt tail terminates the replay cleanly after the good prefix
// (the daemon's restart path owns Truncate; a read-only subscriber
// must not fail the whole stream because the last line is torn).
type FileJournalReplayer struct {
	Path string
}

// ReplaySince implements JournalReplayer.
func (r FileJournalReplayer) ReplaySince(ctx context.Context, sinceSeq uint64, handler JournalHandler) error {
	err := journal.OpenReader(r.Path).Replay(func(rec journal.Record) error {
		if rec.Seq <= sinceSeq {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return handler(rec.Seq, rec.Kind, json.RawMessage(rec.Data))
	})
	if err != nil && errors.Is(err, journal.ErrCorruptTail) {
		// Good prefix already delivered; the torn tail is the crash
		// recovery path's problem, not the reader's.
		return nil
	}
	return err
}

// Compile-time guarantee: FileJournalReplayer satisfies JournalReplayer.
var _ JournalReplayer = FileJournalReplayer{}
