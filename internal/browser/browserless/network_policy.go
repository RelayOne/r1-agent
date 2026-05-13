// network_policy.go: request-interception wiring for the browser
// network policy (spec C6 T5a).
//
// The vanilla CDP way to enforce egress: enable
// Network.setRequestInterception on the session, then on every
// Network.requestIntercepted event evaluate the policy and reply
// with Network.continueInterceptedRequest (allow) or
// {errorReason: "AccessDenied"} (deny).
//
// This file factors that wiring into a single applyNetworkInterception
// helper that the Browserless and in-house clients both call from
// Open. Failures here are non-fatal: if the CDP server (real or
// mock) doesn't implement the Network domain we keep the session
// usable but the policy is enforced ONLY by the URL-level pre-check
// in Navigate. Operators choosing the in-house path who want full
// in-page enforcement should also enable the squid/tinyproxy layer
// (spec T5b).

package browserless

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1/internal/browser"
)

// applyNetworkInterception enables Network.setRequestInterception
// for the session and starts a goroutine that handles inbound
// requestIntercepted events under the policy.
//
// Returns nil on success or when the CDP server rejected the
// Network.* methods (treated as a best-effort soft failure — the
// session is still usable).
func applyNetworkInterception(ctx context.Context, conn *CDPConn, sessionID string, policy browser.NetworkPolicy) error {
	// Enable interception on every URL pattern.
	enableParams := map[string]any{
		"patterns": []any{
			map[string]any{"urlPattern": "*"},
		},
	}
	inner, err := json.Marshal(struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: 1, Method: "Network.setRequestInterception", Params: enableParams})
	if err != nil {
		return err
	}
	var enableResp struct {
		Message string `json:"message"`
	}
	if err := conn.Send(ctx, "Target.sendMessageToTarget",
		map[string]any{"sessionId": sessionID, "message": string(inner)}, &enableResp); err != nil {
		// Soft failure — see file header.
		return nil
	}

	// Subscribe to the event fanout. The Browserless WS forwards
	// target-scoped events via "Target.receivedMessageFromTarget".
	events := conn.Subscribe("Target.receivedMessageFromTarget")
	go runInterceptor(conn, sessionID, policy, events)
	return nil
}

// runInterceptor drains the Target.receivedMessageFromTarget fanout
// and handles Network.requestIntercepted by replying with continue
// (allow) or AccessDenied (deny). Closes when the channel closes
// (provider/session shutdown).
func runInterceptor(conn *CDPConn, sessionID string, policy browser.NetworkPolicy, events <-chan json.RawMessage) {
	for raw := range events {
		var msg struct {
			SessionID string `json:"sessionId"`
			Message   string `json:"message"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil || msg.SessionID != sessionID {
			continue
		}
		var inner struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(msg.Message), &inner); err != nil {
			continue
		}
		if inner.Method != "Network.requestIntercepted" {
			continue
		}
		var ev struct {
			InterceptionID string `json:"interceptionId"`
			Request        struct {
				URL string `json:"url"`
			} `json:"request"`
		}
		if err := json.Unmarshal(inner.Params, &ev); err != nil {
			continue
		}
		host := hostOfURL(ev.Request.URL)
		decision := policy.Decide(host)
		reply := map[string]any{"interceptionId": ev.InterceptionID}
		if !decision.Allowed {
			reply["errorReason"] = "AccessDenied"
		}
		replyInner, _ := json.Marshal(struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params any    `json:"params"`
		}{ID: 1, Method: "Network.continueInterceptedRequest", Params: reply})
		_ = conn.Send(context.Background(), "Target.sendMessageToTarget",
			map[string]any{"sessionId": sessionID, "message": string(replyInner)}, nil)
	}
}
