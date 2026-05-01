package failure

import "time"

// BackpressureSnapshot captures worker-pool saturation at a dispatch boundary.
type BackpressureSnapshot struct {
	Active   int
	Capacity int
	Queued   int
}

// BackpressureDecision is a deterministic dispatch throttle recommendation.
type BackpressureDecision struct {
	Saturated bool
	Delay     time.Duration
	Reason    string
}

// ComputeBackpressure slows dispatch when the queue materially outruns capacity.
func ComputeBackpressure(s BackpressureSnapshot) BackpressureDecision {
	if s.Capacity <= 0 || s.Queued <= 0 {
		return BackpressureDecision{}
	}
	if s.Active < 0 {
		s.Active = 0
	}

	loadPct := s.Active * 100 / s.Capacity
	switch {
	case loadPct >= 100 && s.Queued >= s.Capacity*4:
		return BackpressureDecision{Saturated: true, Delay: 250 * time.Millisecond, Reason: "queue backlog exceeds 4x pool capacity"}
	case loadPct >= 100 && s.Queued >= s.Capacity*2:
		return BackpressureDecision{Saturated: true, Delay: 150 * time.Millisecond, Reason: "pool saturated with 2x backlog"}
	case loadPct >= 75 && s.Queued >= s.Capacity:
		return BackpressureDecision{Saturated: true, Delay: 75 * time.Millisecond, Reason: "pool nearing saturation"}
	default:
		return BackpressureDecision{}
	}
}
