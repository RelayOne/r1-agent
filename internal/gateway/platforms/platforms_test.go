package platforms

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/gateway"
)

func TestMemoryClient_RoundTrip(t *testing.T) {
	mc := NewMemoryClient()
	mc.PushInbound(gateway.Message{ID: "m1", Text: "hi"})
	got, err := mc.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got.ID != "m1" {
		t.Errorf("id=%q want m1", got.ID)
	}
}

func TestMemoryClient_DeliverRecords(t *testing.T) {
	mc := NewMemoryClient()
	_ = mc.Deliver(context.Background(), gateway.Outbound{Text: "hello"})
	if len(mc.Sent) != 1 {
		t.Fatalf("got %d sent want 1", len(mc.Sent))
	}
	if mc.Sent[0].Text != "hello" {
		t.Errorf("text=%q", mc.Sent[0].Text)
	}
}

func TestAdapter_PlatformReturns(t *testing.T) {
	for _, p := range []struct {
		adapter *Adapter
		want    gateway.Platform
	}{
		{NewTelegram(NewMemoryClient()), gateway.PlatformTelegram},
		{NewDiscord(NewMemoryClient()), gateway.PlatformDiscord},
		{NewSlack(NewMemoryClient()), gateway.PlatformSlack},
	} {
		if got := p.adapter.Platform(); got != p.want {
			t.Errorf("Platform()=%q want %q", got, p.want)
		}
	}
}

func TestAdapter_SendViaClient(t *testing.T) {
	mc := NewMemoryClient()
	a := NewTelegram(mc)
	err := a.Send(context.Background(), gateway.Outbound{Text: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mc.Sent) != 1 {
		t.Errorf("client should have received: %v", mc.Sent)
	}
}

func TestAdapter_SendRateLimiting(t *testing.T) {
	// Use a fast MinInterval so the test doesn't wall-sleep.
	mc := NewMemoryClient()
	a := New(gateway.PlatformTelegram, mc, 50*time.Millisecond)

	start := time.Now()
	for i := 0; i < 3; i++ {
		_ = a.Send(context.Background(), gateway.Outbound{Text: "x"})
	}
	elapsed := time.Since(start)
	// 3 sends at 50ms each ≈ at least 100ms (first sends
	// immediately, 2nd waits 50, 3rd waits 50).
	if elapsed < 80*time.Millisecond {
		t.Errorf("rate limiting should slow down; elapsed=%v", elapsed)
	}
}

func TestAdapter_SendPropagatesError(t *testing.T) {
	mc := &failingClient{err: errors.New("platform down")}
	a := NewTelegram(mc)
	err := a.Send(context.Background(), gateway.Outbound{})
	if err == nil {
		t.Error("expected client error to propagate")
	}
}

type failingClient struct {
	err error
}

func (f *failingClient) Deliver(_ context.Context, _ gateway.Outbound) error {
	return f.err
}
func (f *failingClient) Poll(ctx context.Context) (gateway.Message, error) {
	<-ctx.Done()
	return gateway.Message{}, nil
}

func TestAdapter_StartDeliversInbound(t *testing.T) {
	mc := NewMemoryClient()
	mc.PushInbound(gateway.Message{ID: "m1", Text: "hello"})
	a := NewTelegram(mc)

	var got atomic.Int32
	var mu sync.Mutex
	var seen gateway.Message

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = a.Start(ctx, func(m gateway.Message) {
		mu.Lock()
		seen = m
		mu.Unlock()
		got.Add(1)
		cancel()
	})

	if got.Load() != 1 {
		t.Errorf("cb fired=%d want 1", got.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if seen.Platform != gateway.PlatformTelegram {
		t.Errorf("platform=%q want telegram (adapter overrides)", seen.Platform)
	}
}

func TestAdapter_NoClientErrors(t *testing.T) {
	a := &Adapter{PlatformID: gateway.PlatformTelegram}
	err := a.Send(context.Background(), gateway.Outbound{})
	if err == nil {
		t.Error("expected error when client nil")
	}
	err = a.Start(context.Background(), func(gateway.Message) {})
	if err == nil {
		t.Error("expected Start error when client nil")
	}
}
