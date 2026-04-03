// drain.go implements graceful worker drain for pool scale-down.
// Inspired by OmX's dynamic scaling: when removing workers, set to 'draining'
// state for a grace period before force kill. This allows in-progress work to
// complete cleanly rather than being interrupted.
//
// OmX pattern: draining → 30s grace → force kill
// With monotonic worker indexing for stable naming.
package subscriptions

import (
	"context"
	"sync"
	"time"
)

// DrainState tracks a worker's drain lifecycle.
type DrainState int

const (
	DrainNone     DrainState = iota // not draining
	DrainStarted                    // drain initiated, grace period running
	DrainComplete                   // grace period expired, ready for removal
	DrainForced                     // force-killed
)

// DrainableWorker extends a pool with drain lifecycle management.
type DrainableWorker struct {
	PoolID     string     `json:"pool_id"`
	State      DrainState `json:"state"`
	DrainStart time.Time  `json:"drain_start,omitempty"`
	GracePeriod time.Duration `json:"grace_period"`
	Reason     string     `json:"reason,omitempty"`
}

// DrainManager coordinates graceful shutdown of workers.
type DrainManager struct {
	mu          sync.Mutex
	workers     map[string]*DrainableWorker
	gracePeriod time.Duration
	onDrain     DrainCallback // called when drain completes
}

// DrainCallback is called when a worker's drain completes.
type DrainCallback func(poolID string, forced bool)

// NewDrainManager creates a drain manager with the given grace period.
func NewDrainManager(gracePeriod time.Duration, onDrain DrainCallback) *DrainManager {
	if gracePeriod <= 0 {
		gracePeriod = 30 * time.Second
	}
	return &DrainManager{
		workers:     make(map[string]*DrainableWorker),
		gracePeriod: gracePeriod,
		onDrain:     onDrain,
	}
}

// InitiateDrain starts draining a worker. The worker continues its current task
// but accepts no new work. After the grace period, it's eligible for removal.
func (dm *DrainManager) InitiateDrain(poolID, reason string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.workers[poolID] = &DrainableWorker{
		PoolID:      poolID,
		State:       DrainStarted,
		DrainStart:  time.Now(),
		GracePeriod: dm.gracePeriod,
		Reason:      reason,
	}
}

// CheckDrains updates drain states based on elapsed time.
// Returns pool IDs that have completed draining.
func (dm *DrainManager) CheckDrains(now time.Time) []string {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	var completed []string
	for id, w := range dm.workers {
		if w.State == DrainStarted && now.Sub(w.DrainStart) >= w.GracePeriod {
			w.State = DrainComplete
			completed = append(completed, id)
			if dm.onDrain != nil {
				dm.onDrain(id, false)
			}
		}
	}
	return completed
}

// ForceDrain immediately completes the drain for a worker (no grace period).
func (dm *DrainManager) ForceDrain(poolID string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	w, ok := dm.workers[poolID]
	if !ok {
		w = &DrainableWorker{PoolID: poolID}
		dm.workers[poolID] = w
	}
	w.State = DrainForced
	if dm.onDrain != nil {
		dm.onDrain(poolID, true)
	}
}

// IsDraining returns true if the worker is in any drain state.
func (dm *DrainManager) IsDraining(poolID string) bool {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	w, ok := dm.workers[poolID]
	return ok && w.State != DrainNone
}

// IsComplete returns true if the drain has finished (graceful or forced).
func (dm *DrainManager) IsComplete(poolID string) bool {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	w, ok := dm.workers[poolID]
	return ok && (w.State == DrainComplete || w.State == DrainForced)
}

// ClearCompleted removes workers that have finished draining.
func (dm *DrainManager) ClearCompleted() int {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	cleared := 0
	for id, w := range dm.workers {
		if w.State == DrainComplete || w.State == DrainForced {
			delete(dm.workers, id)
			cleared++
		}
	}
	return cleared
}

// ActiveDrains returns the number of workers currently draining.
func (dm *DrainManager) ActiveDrains() int {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	count := 0
	for _, w := range dm.workers {
		if w.State == DrainStarted {
			count++
		}
	}
	return count
}

// DrainAll initiates drain for multiple workers and waits for completion.
// Blocks until all workers complete draining or context is cancelled.
func (dm *DrainManager) DrainAll(ctx context.Context, poolIDs []string, reason string) {
	for _, id := range poolIDs {
		dm.InitiateDrain(id, reason)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context cancelled — force remaining drains
			for _, id := range poolIDs {
				if !dm.IsComplete(id) {
					dm.ForceDrain(id)
				}
			}
			return
		case now := <-ticker.C:
			dm.CheckDrains(now)
			allDone := true
			for _, id := range poolIDs {
				if !dm.IsComplete(id) {
					allDone = false
					break
				}
			}
			if allDone {
				return
			}
		}
	}
}
