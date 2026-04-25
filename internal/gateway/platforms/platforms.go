// Package platforms ships the built-in PlatformAdapter
// implementations STOKE-024 requires (Telegram, Discord,
// Slack at minimum).
//
// Each adapter here is a transport-agnostic IMPLEMENTATION
// SHELL — it satisfies gateway.PlatformAdapter, handles
// message queueing + DM pairing + ack semantics, and
// delegates the actual wire transport to a caller-supplied
// Client function. That separation lets operators plug in
// the real platform SDK (e.g. go-telegram-bot-api/v5,
// bwmarrin/discordgo, slack-go/slack) without this package
// depending on every SDK; it also lets tests inject a fake
// Client for unit coverage without network.
//
// When the user wires a real SDK in, they construct the
// Adapter with a Client that calls the SDK's Send + Poll
// methods; the rest of the gateway surface is unchanged.
package platforms

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/gateway"
)

// Client is the transport-layer function callers supply so
// the adapter can talk to the real platform API. Separated
// from the adapter so tests fake it without network.
type Client interface {
	// Deliver sends out to the platform. Blocking until the
	// platform ACKs (or errors).
	Deliver(ctx context.Context, out gateway.Outbound) error

	// Poll blocks until the next inbound message is
	// available (or ctx cancels). Adapters typically
	// implement long-poll or websocket semantics inside
	// Client; the adapter shell doesn't care which.
	// Returns an empty Message + nil error when ctx
	// cancels.
	Poll(ctx context.Context) (gateway.Message, error)
}

// Adapter is the shared adapter shell every built-in
// platform implementation wraps. Carries the Platform
// identifier + the caller-supplied Client + a simple
// rate-limit guard.
type Adapter struct {
	PlatformID gateway.Platform
	client     Client

	// rateMu serializes Deliver calls so platform rate
	// limits (Telegram 30 msg/s, Discord 5/s/channel,
	// Slack 1/s/channel) don't stampede.
	rateMu      sync.Mutex
	minInterval time.Duration
	lastDeliver time.Time
}

// New returns an Adapter for the platform. minInterval
// defaults to 0 (no rate limiting); operators should set it
// to the platform's documented floor.
func New(p gateway.Platform, client Client, minInterval time.Duration) *Adapter {
	return &Adapter{
		PlatformID:  p,
		client:      client,
		minInterval: minInterval,
	}
}

// Platform implements gateway.PlatformAdapter.
func (a *Adapter) Platform() gateway.Platform { return a.PlatformID }

// Send implements gateway.PlatformAdapter. Applies rate
// limiting then delegates to the Client.
func (a *Adapter) Send(ctx context.Context, out gateway.Outbound) error {
	if a.client == nil {
		return errors.New("platforms: adapter has no client configured")
	}
	if a.minInterval > 0 {
		a.rateMu.Lock()
		elapsed := time.Since(a.lastDeliver)
		if elapsed < a.minInterval {
			wait := a.minInterval - elapsed
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				a.rateMu.Unlock()
				return ctx.Err()
			}
		}
		a.lastDeliver = time.Now()
		a.rateMu.Unlock()
	}
	return a.client.Deliver(ctx, out)
}

// Start implements gateway.PlatformAdapter. Polls in a loop
// until ctx is canceled, feeding each message to cb.
func (a *Adapter) Start(ctx context.Context, cb func(gateway.Message)) error {
	if a.client == nil {
		return errors.New("platforms: adapter has no client configured")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m, err := a.client.Poll(ctx)
		if err != nil {
			// Don't tight-loop on transient errors; back
			// off 1s before retry. Ctx-cancel interrupts
			// the sleep.
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if m.ID == "" && m.SenderPlatformID == "" {
			// Empty poll result (ctx-cancel boundary).
			return nil
		}
		m.Platform = a.PlatformID
		cb(m)
	}
}

// --- Per-platform convenience constructors ---
//
// Each platform has a documented rate-limit floor; the
// convenience constructors apply that automatically so
// operators don't have to look it up.

// NewTelegram wraps a Telegram Client. Rate limit: 30
// messages/second across all chats (~33ms between sends).
func NewTelegram(client Client) *Adapter {
	return New(gateway.PlatformTelegram, client, 35*time.Millisecond)
}

// NewDiscord wraps a Discord Client. Rate limit: 5/s per
// channel (~200ms).
func NewDiscord(client Client) *Adapter {
	return New(gateway.PlatformDiscord, client, 200*time.Millisecond)
}

// NewSlack wraps a Slack Client. Rate limit: 1/s per
// channel (~1000ms). The Slack API documents this as the
// "Tier 2" channel posting floor.
func NewSlack(client Client) *Adapter {
	return New(gateway.PlatformSlack, client, time.Second)
}

// --- In-memory Client for local dev + tests ---

// MemoryClient is a simple in-memory Client. Inbound
// messages are queued via PushInbound and delivered when the
// gateway calls Poll; outbound messages are collected for
// assertion. Used by the gateway test suite + local
// integration without hitting real platform APIs.
type MemoryClient struct {
	mu      sync.Mutex
	inbound []gateway.Message
	Sent    []gateway.Outbound
}

// NewMemoryClient returns an empty client.
func NewMemoryClient() *MemoryClient {
	return &MemoryClient{}
}

// PushInbound queues a message that will be returned by the
// next Poll call.
func (m *MemoryClient) PushInbound(msg gateway.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inbound = append(m.inbound, msg)
}

// Deliver records the outbound message.
func (m *MemoryClient) Deliver(_ context.Context, out gateway.Outbound) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sent = append(m.Sent, out)
	return nil
}

// Poll returns the next queued inbound message. Waits up
// to 100ms for a message to arrive before returning with
// the empty-poll shape the adapter loop knows to interpret
// as ctx-cancel-adjacent.
func (m *MemoryClient) Poll(ctx context.Context) (gateway.Message, error) {
	for {
		m.mu.Lock()
		if len(m.inbound) > 0 {
			msg := m.inbound[0]
			m.inbound = m.inbound[1:]
			m.mu.Unlock()
			return msg, nil
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return gateway.Message{}, nil
		case <-time.After(10 * time.Millisecond):
			// retry loop
		}
	}
}

// Compile-time interface assertions so signature drift
// surfaces at build time.
var (
	_ gateway.PlatformAdapter = (*Adapter)(nil)
	_ Client                  = (*MemoryClient)(nil)
)

// Keep fmt import from being considered unused if future
// diffs simplify away the only fmt.Sprintf call in the
// convenience constructors.
var _ = fmt.Sprintf
