package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// stubAdapter is a minimal PlatformAdapter for router tests.
type stubAdapter struct {
	p      Platform
	sent   []Outbound
	mu     sync.Mutex
	inbox  chan Message
	sendErr error
}

func (s *stubAdapter) Platform() Platform { return s.p }

func (s *stubAdapter) Send(_ context.Context, out Outbound) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, out)
	return nil
}

func (s *stubAdapter) Start(ctx context.Context, cb func(Message)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-s.inbox:
			cb(m)
		}
	}
}

func TestAllPlatforms_HasNine(t *testing.T) {
	if got := AllPlatforms(); len(got) != 9 {
		t.Errorf("AllPlatforms=%d want 9", len(got))
	}
}

func TestRouter_Register_Dedup(t *testing.T) {
	r := NewRouter(nil)
	r.Register(&stubAdapter{p: PlatformTelegram})
	r.Register(&stubAdapter{p: PlatformDiscord})
	r.Register(&stubAdapter{p: PlatformSlack})
	got := r.Registered()
	if len(got) != 3 {
		t.Errorf("registered=%d want 3", len(got))
	}
	// Re-register same platform — should replace, not add.
	r.Register(&stubAdapter{p: PlatformTelegram})
	got = r.Registered()
	if len(got) != 3 {
		t.Errorf("re-register shouldn't add; got %d", len(got))
	}
}

func TestRouter_Send_RoutesToCorrectPlatform(t *testing.T) {
	r := NewRouter(nil)
	tg := &stubAdapter{p: PlatformTelegram}
	ds := &stubAdapter{p: PlatformDiscord}
	r.Register(tg)
	r.Register(ds)
	err := r.Send(context.Background(), Outbound{
		Platform: PlatformDiscord,
		Text:     "hi",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(tg.sent) != 0 {
		t.Error("telegram adapter should not have received")
	}
	if len(ds.sent) != 1 || ds.sent[0].Text != "hi" {
		t.Errorf("discord adapter missing expected message: %v", ds.sent)
	}
}

func TestRouter_Send_UnknownPlatform(t *testing.T) {
	r := NewRouter(nil)
	err := r.Send(context.Background(), Outbound{Platform: PlatformSignal})
	if !errors.Is(err, ErrNoAdapter) {
		t.Errorf("want ErrNoAdapter, got %v", err)
	}
}

func TestPairingToken_RoundTrip(t *testing.T) {
	r := NewRouter([]byte("test-secret"))
	tok := r.IssuePairingToken("conv-123", time.Minute)
	cid, err := r.VerifyPairingToken(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if cid != "conv-123" {
		t.Errorf("cid=%q want conv-123", cid)
	}
}

func TestPairingToken_RejectsTampered(t *testing.T) {
	r := NewRouter([]byte("test-secret"))
	tok := r.IssuePairingToken("conv-123", time.Minute)
	// Flip the last signature byte.
	tampered := PairingToken(string(tok[:len(tok)-1]) + "Z")
	_, err := r.VerifyPairingToken(tampered)
	if err == nil {
		t.Error("tampered token should fail")
	}
}

func TestPairingToken_RejectsExpired(t *testing.T) {
	r := NewRouter([]byte("test-secret"))
	tok := r.IssuePairingToken("conv-123", -time.Minute)
	_, err := r.VerifyPairingToken(tok)
	if err == nil {
		t.Error("expired token should fail")
	}
}

func TestPairingToken_RejectsWrongSecret(t *testing.T) {
	r1 := NewRouter([]byte("secret-A"))
	r2 := NewRouter([]byte("secret-B"))
	tok := r1.IssuePairingToken("conv", time.Minute)
	_, err := r2.VerifyPairingToken(tok)
	if err == nil {
		t.Error("token signed with different secret should fail verify")
	}
}

func TestConversationCoordinator_BindAndResolve(t *testing.T) {
	c := NewConversationCoordinator()
	c.Bind(PlatformTelegram, "user-tg", "conv-a")
	c.Bind(PlatformDiscord, "user-ds", "conv-a") // cross-platform
	c.Bind(PlatformSlack, "user-sl", "conv-b")

	msgTG := Message{Platform: PlatformTelegram, SenderPlatformID: "user-tg"}
	msgDS := Message{Platform: PlatformDiscord, SenderPlatformID: "user-ds"}
	msgSL := Message{Platform: PlatformSlack, SenderPlatformID: "user-sl"}
	msgUnknown := Message{Platform: PlatformTelegram, SenderPlatformID: "stranger"}

	if cid, ok := c.ResolveConversation(msgTG); !ok || cid != "conv-a" {
		t.Errorf("tg→conv-a, got ok=%v cid=%q", ok, cid)
	}
	if cid, ok := c.ResolveConversation(msgDS); !ok || cid != "conv-a" {
		t.Errorf("ds→conv-a (cross-platform), got ok=%v cid=%q", ok, cid)
	}
	if cid, ok := c.ResolveConversation(msgSL); !ok || cid != "conv-b" {
		t.Errorf("sl→conv-b, got ok=%v cid=%q", ok, cid)
	}
	if _, ok := c.ResolveConversation(msgUnknown); ok {
		t.Error("unknown sender should not resolve")
	}
}

func TestConversationCoordinator_Unbind(t *testing.T) {
	c := NewConversationCoordinator()
	c.Bind(PlatformTelegram, "u", "conv-a")
	c.Unbind(PlatformTelegram, "u")
	if _, ok := c.ResolveConversation(Message{Platform: PlatformTelegram, SenderPlatformID: "u"}); ok {
		t.Error("unbound sender should not resolve")
	}
}

func TestConversationCoordinator_HistoryCap(t *testing.T) {
	c := NewConversationCoordinator()
	for i := 0; i < 300; i++ {
		c.Append("conv", Message{Text: "msg"})
	}
	if h := c.History("conv"); len(h) > 256 {
		t.Errorf("history len=%d exceeds cap", len(h))
	}
}

func TestCronScheduler_FiresDueTask(t *testing.T) {
	fired := 0
	send := func(_ context.Context, cid, text string) error {
		if cid == "c1" && text == "hello" {
			fired++
		}
		return nil
	}
	s := NewCronScheduler(send)
	s.SetClock(func() time.Time { return time.Date(2026, 4, 16, 12, 0, 30, 0, time.UTC) })
	s.Schedule(ScheduledTask{
		ID:             "t1",
		ConversationID: "c1",
		Text:           "hello",
		DeliverAt:      time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC), // 30s before now
	})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if fired != 1 {
		t.Errorf("fired=%d want 1", fired)
	}
	// One-shot task should be removed after firing.
	if len(s.List()) != 0 {
		t.Errorf("one-shot should be removed; got %v", s.List())
	}
}

func TestCronScheduler_DoesNotFireFutureTask(t *testing.T) {
	fired := 0
	send := func(_ context.Context, _, _ string) error {
		fired++
		return nil
	}
	s := NewCronScheduler(send)
	s.SetClock(func() time.Time { return time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC) })
	s.Schedule(ScheduledTask{
		ID:             "t1",
		ConversationID: "c1",
		DeliverAt:      time.Date(2026, 4, 16, 13, 0, 0, 0, time.UTC), // 1hr future
	})
	_ = s.Tick(context.Background())
	if fired != 0 {
		t.Errorf("future task should not fire; fired=%d", fired)
	}
	if len(s.List()) != 1 {
		t.Errorf("future task should remain scheduled")
	}
}

func TestCronScheduler_RepeatReschedules(t *testing.T) {
	fired := 0
	send := func(_ context.Context, _, _ string) error {
		fired++
		return nil
	}
	s := NewCronScheduler(send)
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	s.Schedule(ScheduledTask{
		ID:             "repeating",
		ConversationID: "c1",
		DeliverAt:      now.Add(-time.Second),
		Repeat:         time.Hour,
	})
	_ = s.Tick(context.Background())
	if fired != 1 {
		t.Errorf("fired=%d want 1", fired)
	}
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("repeating task should remain; got %v", got)
	}
	expected := now.Add(-time.Second).Add(time.Hour)
	if !got[0].DeliverAt.Equal(expected) {
		t.Errorf("next fire=%v want %v", got[0].DeliverAt, expected)
	}
}

func TestCronScheduler_Cancel(t *testing.T) {
	s := NewCronScheduler(func(_ context.Context, _, _ string) error { return nil })
	s.Schedule(ScheduledTask{ID: "t1", DeliverAt: time.Now().Add(time.Hour)})
	s.Cancel("t1")
	if len(s.List()) != 0 {
		t.Error("Cancel should remove the task")
	}
}

func TestSplitDots_HandlesDottedConversationID(t *testing.T) {
	// ConversationID with a dot; token still parses.
	parts := splitDots("conv.with.dot.1234567890.abcdef")
	if len(parts) != 3 {
		t.Fatalf("got %v want 3 parts", parts)
	}
	if parts[0] != "conv.with.dot" {
		t.Errorf("cid=%q want conv.with.dot", parts[0])
	}
}
