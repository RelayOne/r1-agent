package server

import (
	"context"
	"encoding/json"

	"github.com/ericmacdougall/stoke/internal/hub"
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
