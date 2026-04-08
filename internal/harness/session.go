package harness

import (
	"context"
	"sync"
	"time"
)

// StanceSession is the internal mutable state of an active stance.
type StanceSession struct {
	ID              string
	Role            string
	Status          StanceStatus
	Model           string
	SystemPrompt    string
	ConcernField    string // rendered concern field text
	AuthorizedTools []string
	SpawnRequest    SpawnRequest
	TokensUsed      int64
	CostUSD         float64
	CreatedAt       time.Time
	PauseReason     string
	AdditionalCtx   string

	// Cooperative pause signaling channels.
	pauseCh    chan struct{} // closed when pause is requested
	resumeCh   chan struct{} // closed when resume is requested
	pauseAckCh chan struct{} // closed by stance when it reaches a safe checkpoint
	pauseMu    sync.Mutex   // serializes channel lifecycle operations
}

// CheckpointCheck should be called by the stance runner at safe points.
// If a pause has been requested, it acknowledges the pause and blocks until
// resume or context cancellation.
func (s *StanceSession) CheckpointCheck(ctx context.Context) error {
	// Fast path: bail out if context is already done.
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case <-s.pauseCh:
		// Acknowledge the pause.
		s.pauseMu.Lock()
		select {
		case <-s.pauseAckCh:
			// Already acknowledged (shouldn't happen, but be safe).
		default:
			close(s.pauseAckCh)
		}
		s.pauseMu.Unlock()

		// Wait for resume or context cancellation.
		select {
		case <-s.resumeCh:
			// Reset channels for next pause cycle.
			s.pauseMu.Lock()
			s.pauseCh = make(chan struct{})
			s.pauseAckCh = make(chan struct{})
			s.pauseMu.Unlock()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		return nil
	}
}

// StanceState is the public read-only snapshot returned by InspectStance.
type StanceState struct {
	StanceHandle
	Model       string
	TokensUsed  int64
	CostUSD     float64
	CreatedAt   time.Time
	PauseReason string
}
