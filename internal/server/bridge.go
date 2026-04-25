package server

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1-agent/internal/hub"
)

// BridgeHubToEventBus registers a wildcard observer on the hub.Bus that
// forwards all events as JSON to the server's EventBus for SSE/WebSocket clients.
func BridgeHubToEventBus(bus *hub.Bus, serverBus *EventBus) {
	bus.Register(hub.Subscriber{
		ID:     "server.bridge",
		Events: []hub.EventType{"*"},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			data, err := json.Marshal(ev)
			if err != nil {
				return nil
			}
			serverBus.Publish(string(data))
			return nil
		},
	})
}

// BridgeHubToDashboard registers a subscriber that interprets task lifecycle
// events and updates the DashboardState for REST API queries.
func BridgeHubToDashboard(bus *hub.Bus, state *DashboardState) {
	if state == nil {
		return
	}

	taskEvents := []hub.EventType{
		hub.EventTaskDispatched,
		hub.EventTaskStarted,
		hub.EventTaskCompleted,
		hub.EventTaskFailed,
		hub.EventTaskRetrying,
		hub.EventTaskBlocked,
		hub.EventTaskSkipped,
	}

	bus.Register(hub.Subscriber{
		ID:     "server.dashboard_state",
		Events: taskEvents,
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			snap := TaskSnapshot{
				ID:    ev.TaskID,
				Phase: ev.Phase,
			}

			switch ev.Type {
			case hub.EventTaskDispatched:
				snap.Status = "pending"
			case hub.EventTaskStarted:
				snap.Status = "running"
			case hub.EventTaskCompleted:
				snap.Status = "completed"
			case hub.EventTaskFailed:
				snap.Status = "failed"
			case hub.EventTaskRetrying:
				snap.Status = "retrying"
			case hub.EventTaskBlocked:
				snap.Status = "blocked"
			case hub.EventTaskSkipped:
				snap.Status = "completed"
			}

			// Preserve existing snapshot data (cost, duration) if present.
			if existing := state.Get(ev.TaskID); existing != nil {
				if snap.Phase == "" {
					snap.Phase = existing.Phase
				}
				if snap.CostUSD == 0 {
					snap.CostUSD = existing.CostUSD
				}
				if snap.DurationMs == 0 {
					snap.DurationMs = existing.DurationMs
				}
				if snap.Worker == "" {
					snap.Worker = existing.Worker
				}
				if snap.Description == "" {
					snap.Description = existing.Description
				}
				snap.Attempt = existing.Attempt
				if ev.Type == hub.EventTaskRetrying {
					snap.Attempt++
				}
			}

			// Extract cost and model info from model events.
			if ev.Model != nil {
				snap.CostUSD += ev.Model.CostUSD
				snap.Worker = ev.Model.Model
			}

			state.Update(snap)
			return nil
		},
	})
}
