package cortex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// warmupContent is the constant trivial user message body used by the
// pre-warm pump. Anthropic's API rejects an empty content list, so we
// send a single short text block. Keeping this constant ensures the
// warming request body is byte-stable across pre-warm cycles (a 1-byte
// drift in the cached prefix bytes wastes the cache write).
func warmupContent(text string) json.RawMessage {
	// Minimal user content block: a single text content block.
	// Matches the shape Anthropic expects for messages content.
	raw, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": text},
	})
	return raw
}

// runPreWarmOnce builds and sends a single cache-warming request to the
// provider. It mirrors the main thread's buildRequest so that the
// resulting cached prefix (system blocks + tool list) is byte-identical
// to what the main agentloop will subsequently send. This is the
// invariant called out by spec gotcha #8: "a 1-byte drift = 0% cache
// hit + zero cost savings + silent degradation."
//
// On success, an EventCortexPreWarmFired event is emitted with
// Custom["cache_status"] set to true iff the response indicates a
// cache hit (resp.Usage.CacheRead > 0).
//
// On failure, an EventCortexPreWarmFailed event is emitted with
// Custom["err"] set to the error string and the error is returned to
// the caller. The caller decides whether to fail (Cortex.Start treats
// pre-warm errors as best-effort and continues — see spec line 656).
//
// bus may be nil; in that case events are silently dropped (the
// pre-warm cycle is still attempted and the error/value is returned
// normally).
func runPreWarmOnce(
	ctx context.Context,
	p provider.Provider,
	model, systemPrompt string,
	tools []provider.ToolDef,
	bus *hub.Bus,
) error {
	if p == nil {
		return fmt.Errorf("cortex: prewarm requires a non-nil provider")
	}

	// Cache breakpoint parity: identical sort + identical system block
	// builder as agentloop.Loop.buildRequest.
	sortedTools := agentloop.SortToolsDeterministic(tools)
	systemBlocks := agentloop.BuildCachedSystemPrompt(systemPrompt, "")
	systemJSON, err := json.Marshal(systemBlocks)
	if err != nil {
		// json.Marshal on []SystemBlock is essentially infallible, but
		// be safe and surface it rather than panic.
		emitPreWarmFailed(bus, err)
		return fmt.Errorf("cortex: prewarm marshal system blocks: %w", err)
	}

	req := provider.ChatRequest{
		Model:     model,
		SystemRaw: systemJSON,
		Messages: []provider.ChatMessage{
			{Role: "user", Content: warmupContent("warm")},
		},
		MaxTokens:    1, // Anthropic rejects 0 — 1 is the closest legal value.
		Tools:        sortedTools,
		CacheEnabled: true,
	}

	// Honour ctx best-effort: providers don't take a context directly,
	// so we early-out if the caller already cancelled before issuing
	// the network call.
	if err := ctx.Err(); err != nil {
		return err
	}

	resp, err := p.ChatStream(req, nil)
	if err != nil {
		emitPreWarmFailed(bus, err)
		return fmt.Errorf("cortex: prewarm chat stream: %w", err)
	}

	cacheHit := false
	if resp != nil {
		cacheHit = resp.Usage.CacheRead > 0
	}
	if bus != nil {
		bus.EmitAsync(&hub.Event{
			Type: hub.EventCortexPreWarmFired,
			Custom: map[string]any{
				"cache_status": cacheHit,
			},
		})
	}
	return nil
}

// emitPreWarmFailed publishes a pre-warm failure event. Safe to call
// with a nil bus.
func emitPreWarmFailed(bus *hub.Bus, err error) {
	if bus == nil {
		return
	}
	bus.EmitAsync(&hub.Event{
		Type: hub.EventCortexPreWarmFailed,
		Custom: map[string]any{
			"err": err.Error(),
		},
	})
}

// runPreWarmPump invokes fire on a fixed interval until ctx is
// cancelled. Errors returned by fire are logged at WARN level via
// slog but never terminate the pump — pre-warm is a best-effort cost
// optimisation, not a correctness gate, so a transient API failure
// must not stop subsequent cycles from re-arming the cache.
//
// The pump does not perform an initial fire on entry; the caller is
// responsible for the synchronous initial pre-warm (see TASK-13
// Cortex.Start). This separation keeps runPreWarmPump trivially
// testable and lets the caller decide whether the very first pre-warm
// failure is fatal.
func runPreWarmPump(ctx context.Context, interval time.Duration, fire func(context.Context) error) {
	if interval <= 0 || fire == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fire(ctx); err != nil {
				slog.Warn("cortex prewarm pump fire failed",
					"component", "cortex.prewarm",
					"err", err,
				)
				// Continue: do NOT return on error.
			}
		}
	}
}
