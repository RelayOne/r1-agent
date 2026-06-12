package throttle

import (
	"context"
	"time"
)

// emitterState carries the optional bus + CodeRadar fan-out targets.
// Stored behind an atomic.Pointer so WireBus / WireCodeRadar can be
// called at daemon-startup time without racing the hot path. Both
// fields are independently optional.
type emitterState struct {
	emitBusEvent     func(tool string, scope Scope, principal string, retryAfter time.Duration)
	captureCodeRadar func(ctx context.Context, tool string, scope Scope, principal string, retryAfter time.Duration)
}

// WireBusEmitter installs a callback the throttle gate invokes on
// every denial. The callback is responsible for translating the
// denial into a hub.EventToolThrottled emission. Wiring lives outside
// the throttle package so internal/throttle does not import
// internal/hub (avoids the import cycle config → throttle → hub →
// agentloop → ...).
//
// Pass nil to clear a previously-installed emitter (test cleanup).
func WireBusEmitter(l Limiter, fn func(tool string, scope Scope, principal string, retryAfter time.Duration)) {
	impl, ok := l.(*limiter)
	if !ok || impl == nil {
		return
	}
	prev := impl.emitter.Load()
	next := emitterState{}
	if prev != nil {
		next = *prev
	}
	next.emitBusEvent = fn
	impl.emitter.Store(&next)
}

// WireCodeRadarEmitter installs a callback the throttle gate invokes
// on every denial alongside the bus emission. The callback fan-outs
// to coderadar.Client.CaptureError so operators get a structured
// signal in their B3 dashboard. Optional; nil disables the fan-out.
func WireCodeRadarEmitter(l Limiter, fn func(ctx context.Context, tool string, scope Scope, principal string, retryAfter time.Duration)) {
	impl, ok := l.(*limiter)
	if !ok || impl == nil {
		return
	}
	prev := impl.emitter.Load()
	next := emitterState{}
	if prev != nil {
		next = *prev
	}
	next.captureCodeRadar = fn
	impl.emitter.Store(&next)
}

// emitDenied is called from the deny path of every Allow* call. It
// fans the denial out to both the optional bus emitter and the
// optional CodeRadar emitter. Both are nil-safe.
func (l *limiter) emitDenied(ctx context.Context, dec Decision) {
	if l == nil {
		return
	}
	e := l.emitter.Load()
	if e == nil {
		return
	}
	if e.emitBusEvent != nil {
		e.emitBusEvent(dec.Tool, dec.Scope, dec.Principal, dec.RetryAfter)
	}
	if e.captureCodeRadar != nil {
		e.captureCodeRadar(ctx, dec.Tool, dec.Scope, dec.Principal, dec.RetryAfter)
	}
}
