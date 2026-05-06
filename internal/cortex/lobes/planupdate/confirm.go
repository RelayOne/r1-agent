// User-confirmation event handling for PlanUpdateLobe (spec item 20).
//
// On EventCortexUserConfirmedPlanChange the Lobe pops the queued
// planChange under the supplied queue_id and applies the additions +
// removals via plan.Save. The event payload's queue_id is the same
// string the Lobe stamped on the user-confirm Note's Meta in TASK-19.
package planupdate

import (
	"context"
	"log/slog"

	"github.com/RelayOne/r1/internal/hub"
)

// confirmSubscriberID is the stable subscriber identifier for the
// user-confirmation handler. Used by hub.Register's dedup so a
// daemon-restart re-Register call is a no-op.
const confirmSubscriberID = "plan-update-confirm"

// registerConfirmSubscriber registers a hub.Subscriber that reacts to
// EventCortexUserConfirmedPlanChange. Safe with a nil hubBus (skipped).
//
// The subscriber Mode is ModeObserve — application-level logic, not a
// gating hook. The Handler runs in the bus's per-event goroutine; we
// keep work small (a queued-map pop + plan.Save) to avoid backing up
// the hub.
func (l *PlanUpdateLobe) registerConfirmSubscriber() {
	if l.hubBus == nil {
		return
	}
	l.hubBus.Register(hub.Subscriber{
		ID:     confirmSubscriberID,
		Events: []hub.EventType{hub.EventCortexUserConfirmedPlanChange},
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			l.handleConfirmEvent(ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
}

// handleConfirmEvent applies the queued additions/removals identified
// by ev.Custom["queue_id"]. An unknown queue_id is logged and dropped
// (the user might have confirmed twice; we apply once).
func (l *PlanUpdateLobe) handleConfirmEvent(ev *hub.Event) {
	if ev == nil {
		return
	}
	queueID, _ := ev.Custom["queue_id"].(string)
	if queueID == "" {
		slog.Warn("plan-update: confirm event missing queue_id", "event", ev.Type)
		return
	}

	l.queuedMu.Lock()
	change, ok := l.queued[queueID]
	if ok {
		delete(l.queued, queueID)
	}
	l.queuedMu.Unlock()

	if !ok {
		slog.Warn("plan-update: unknown queue_id in confirm event",
			"queue_id", queueID, "event", ev.Type)
		return
	}

	adds := addsFromAnySlice(change.additions)
	removes := removesFromAnySlice(change.removals)
	applied, err := applyAddsRemoves(l.planPath, adds, removes)
	if err != nil {
		slog.Warn("plan-update: apply on confirm failed",
			"err", err, "queue_id", queueID)
		return
	}
	slog.Debug("plan-update: applied confirmed change",
		"queue_id", queueID, "applied", applied)
}

// QueuedCount reports the number of currently-queued planChange
// records. Test-facing accessor; production callers do not need this.
func (l *PlanUpdateLobe) QueuedCount() int {
	l.queuedMu.Lock()
	defer l.queuedMu.Unlock()
	return len(l.queued)
}
