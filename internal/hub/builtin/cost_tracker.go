package builtin

import (
	"context"
	"sync"

	"github.com/RelayOne/r1/internal/hub"
)

// CostTracker is an observe subscriber that records per-call cost from model
// API events. Uses April 2026 pricing for Anthropic models.
type CostTracker struct {
	mu       sync.Mutex
	totalUSD float64
	perModel map[string]float64
}

// NewCostTracker creates a new cost tracker.
func NewCostTracker() *CostTracker {
	return &CostTracker{perModel: make(map[string]float64)}
}

// Pricing per million tokens (April 2026)
var modelPricing = map[string]struct {
	Input, Output, CacheWrite, CacheRead float64
}{
	"claude-opus-4-6":   {5.00, 25.00, 6.25, 0.50},
	"claude-sonnet-4-6": {3.00, 15.00, 3.75, 0.30},
	"claude-haiku-4-5":  {1.00, 5.00, 1.25, 0.10},
	"claude-haiku-3-5":  {0.80, 4.00, 1.00, 0.08},
}

// Register adds the cost tracker to the bus.
func (c *CostTracker) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID:       "builtin.cost_tracker",
		Events:   []hub.EventType{hub.EventModelPostCall},
		Mode:     hub.ModeObserve,
		Priority: 9000,
		Handler:  c.handle,
	})
}

func (c *CostTracker) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev.Model == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	price, ok := modelPricing[ev.Model.Model]
	if !ok {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	cost := float64(ev.Model.InputTokens)/1e6*price.Input +
		float64(ev.Model.OutputTokens)/1e6*price.Output +
		float64(ev.Model.CachedTokens)/1e6*price.CacheRead

	c.mu.Lock()
	c.totalUSD += cost
	c.perModel[ev.Model.Model] += cost
	c.mu.Unlock()

	return &hub.HookResponse{Decision: hub.Allow}
}

// Snapshot returns the current cost totals.
func (c *CostTracker) Snapshot() (totalUSD float64, perModel map[string]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]float64, len(c.perModel))
	for k, v := range c.perModel {
		out[k] = v
	}
	return c.totalUSD, out
}

// TotalUSD returns the total cost tracked so far.
func (c *CostTracker) TotalUSD() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalUSD
}
