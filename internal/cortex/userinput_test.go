package cortex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// newUserInputCortex builds a router-only Cortex (no Lobes, never
// Started) around the given provider — the same shape the chat REPL's
// mid-turn wiring constructs. Reuses fakeRouterProvider/toolUseResp
// from router_test.go.
func newUserInputCortex(t *testing.T, p provider.Provider) *Cortex {
	t.Helper()
	c, err := New(Config{EventBus: hub.New(), Provider: p})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestOnUserInputMidTurnStopInterruptsOnce is the spec acceptance
// criterion (specs/cortex-core.md §"Acceptance criteria"): WHEN
// OnUserInputMidTurn receives "stop" THE SYSTEM SHALL return a
// DecisionInterrupt from the Router and call turnCancel() exactly
// once. The hard-stop shortlist must short-circuit before the
// provider, so the fake provider records zero calls.
func TestOnUserInputMidTurnStopInterruptsOnce(t *testing.T) {
	t.Parallel()

	fp := &fakeRouterProvider{}
	c := newUserInputCortex(t, fp)

	cancels := 0
	dec, err := c.OnUserInputMidTurn(context.Background(), "stop", func() { cancels++ })
	if err != nil {
		t.Fatalf("OnUserInputMidTurn: %v", err)
	}
	if dec.Kind != DecisionInterrupt {
		t.Fatalf("Kind = %q, want %q", dec.Kind, DecisionInterrupt)
	}
	if cancels != 1 {
		t.Errorf("turnCancel called %d times, want exactly 1", cancels)
	}
	if fp.calls != 0 {
		t.Errorf("provider called %d times, want 0 (hard-stop short-circuit)", fp.calls)
	}
	if dec.Interrupt == nil || dec.Interrupt.Source != "user" {
		t.Errorf("Interrupt = %+v, want Source stamped 'user'", dec.Interrupt)
	}
}

// TestOnUserInputMidTurnInterruptNilCancelSafe: a nil turnCancel must
// not panic — the REPL may route input after a turn already ended.
func TestOnUserInputMidTurnInterruptNilCancelSafe(t *testing.T) {
	t.Parallel()

	c := newUserInputCortex(t, &fakeRouterProvider{})
	dec, err := c.OnUserInputMidTurn(context.Background(), "stop", nil)
	if err != nil {
		t.Fatalf("OnUserInputMidTurn: %v", err)
	}
	if dec.Kind != DecisionInterrupt {
		t.Fatalf("Kind = %q, want %q", dec.Kind, DecisionInterrupt)
	}
}

// TestOnUserInputMidTurnSteerPublishesNote: a steer decision publishes
// a Note into the Workspace (severity mapped from the payload) so the
// main agent picks it up at the next MidturnCheckFn boundary. The
// turn must NOT be cancelled.
func TestOnUserInputMidTurnSteerPublishesNote(t *testing.T) {
	t.Parallel()

	fp := &fakeRouterProvider{resp: toolUseResp("steer", map[string]any{
		"severity": "warning",
		"title":    "use Postgres",
		"body":     "the team standardized on Postgres last sprint",
	})}
	c := newUserInputCortex(t, fp)

	cancels := 0
	dec, err := c.OnUserInputMidTurn(context.Background(), "make sure it's Postgres", func() { cancels++ })
	if err != nil {
		t.Fatalf("OnUserInputMidTurn: %v", err)
	}
	if dec.Kind != DecisionSteer {
		t.Fatalf("Kind = %q, want %q", dec.Kind, DecisionSteer)
	}
	if cancels != 0 {
		t.Errorf("turnCancel called %d times, want 0 (steer keeps the turn alive)", cancels)
	}

	notes := c.Workspace().Snapshot()
	if len(notes) != 1 {
		t.Fatalf("workspace has %d notes, want 1", len(notes))
	}
	n := notes[0]
	if n.LobeID != routerLobeID {
		t.Errorf("LobeID = %q, want %q", n.LobeID, routerLobeID)
	}
	if n.Severity != SevWarning {
		t.Errorf("Severity = %q, want %q", n.Severity, SevWarning)
	}
	if n.Title != "use Postgres" || !strings.Contains(n.Body, "Postgres") {
		t.Errorf("note = %+v, want steer title/body", n)
	}
}

// TestOnUserInputMidTurnSteerClampsSeverityAndTitle: "critical" is
// reserved for system Lobes, so a misbehaving Router model degrades to
// SevAdvice; an over-long title is clamped to Note.Validate's 80-rune
// cap instead of costing the operator the Note.
func TestOnUserInputMidTurnSteerClampsSeverityAndTitle(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 200)
	fp := &fakeRouterProvider{resp: toolUseResp("steer", map[string]any{
		"severity": "critical",
		"title":    long,
		"body":     "b",
	})}
	c := newUserInputCortex(t, fp)

	if _, err := c.OnUserInputMidTurn(context.Background(), "nudge", nil); err != nil {
		t.Fatalf("OnUserInputMidTurn: %v", err)
	}
	notes := c.Workspace().Snapshot()
	if len(notes) != 1 {
		t.Fatalf("workspace has %d notes, want 1", len(notes))
	}
	if notes[0].Severity != SevAdvice {
		t.Errorf("Severity = %q, want %q (critical reserved for system Lobes)", notes[0].Severity, SevAdvice)
	}
	if err := notes[0].Validate(); err != nil {
		t.Errorf("published note fails Validate: %v", err)
	}
}

// TestOnUserInputMidTurnJustChatPublishesInfoNote: just_chat surfaces
// the reply as a SevInfo Note (title = first line, body = full reply)
// and leaves the turn untouched.
func TestOnUserInputMidTurnJustChatPublishesInfoNote(t *testing.T) {
	t.Parallel()

	fp := &fakeRouterProvider{resp: toolUseResp("just_chat", map[string]any{
		"reply": "still compiling the auth package\nno errors so far",
	})}
	c := newUserInputCortex(t, fp)

	cancels := 0
	dec, err := c.OnUserInputMidTurn(context.Background(), "how's it going?", func() { cancels++ })
	if err != nil {
		t.Fatalf("OnUserInputMidTurn: %v", err)
	}
	if dec.Kind != DecisionJustChat {
		t.Fatalf("Kind = %q, want %q", dec.Kind, DecisionJustChat)
	}
	if cancels != 0 {
		t.Errorf("turnCancel called %d times, want 0", cancels)
	}
	notes := c.Workspace().Snapshot()
	if len(notes) != 1 {
		t.Fatalf("workspace has %d notes, want 1", len(notes))
	}
	if notes[0].Severity != SevInfo {
		t.Errorf("Severity = %q, want %q", notes[0].Severity, SevInfo)
	}
	if notes[0].Title != "still compiling the auth package" {
		t.Errorf("Title = %q, want first line of reply", notes[0].Title)
	}
	if !strings.Contains(notes[0].Body, "no errors so far") {
		t.Errorf("Body = %q, want full reply", notes[0].Body)
	}
}

// TestOnUserInputMidTurnQueueMissionNoCortexSideEffects: queue_mission
// returns the payload for the caller's queue and does nothing inside
// the cortex — no Note, no cancel (mission enqueue is out of
// cortex-core scope per the spec).
func TestOnUserInputMidTurnQueueMissionNoCortexSideEffects(t *testing.T) {
	t.Parallel()

	fp := &fakeRouterProvider{resp: toolUseResp("queue_mission", map[string]any{
		"brief":    "after this, also fix the bug in auth.go",
		"priority": "normal",
	})}
	c := newUserInputCortex(t, fp)

	cancels := 0
	dec, err := c.OnUserInputMidTurn(context.Background(), "after this, also fix auth.go", func() { cancels++ })
	if err != nil {
		t.Fatalf("OnUserInputMidTurn: %v", err)
	}
	if dec.Kind != DecisionQueueMission {
		t.Fatalf("Kind = %q, want %q", dec.Kind, DecisionQueueMission)
	}
	if dec.Queue == nil || dec.Queue.Brief != "after this, also fix the bug in auth.go" {
		t.Errorf("Queue = %+v, want the brief payload", dec.Queue)
	}
	if cancels != 0 {
		t.Errorf("turnCancel called %d times, want 0", cancels)
	}
	if notes := c.Workspace().Snapshot(); len(notes) != 0 {
		t.Errorf("workspace has %d notes, want 0", len(notes))
	}
}

// TestOnUserInputMidTurnRouteErrorEnactsNothing: a Route failure
// returns the error, cancels nothing, publishes nothing.
func TestOnUserInputMidTurnRouteErrorEnactsNothing(t *testing.T) {
	t.Parallel()

	fp := &fakeRouterProvider{err: errors.New("provider down")}
	c := newUserInputCortex(t, fp)

	cancels := 0
	_, err := c.OnUserInputMidTurn(context.Background(), "add a tests folder too", func() { cancels++ })
	if err == nil {
		t.Fatal("want error from failing provider, got nil")
	}
	if cancels != 0 {
		t.Errorf("turnCancel called %d times, want 0", cancels)
	}
	if notes := c.Workspace().Snapshot(); len(notes) != 0 {
		t.Errorf("workspace has %d notes, want 0", len(notes))
	}
}

// TestSteerSeverityMapping pins the enum → Severity mapping including
// the degrade-to-advice default.
func TestSteerSeverityMapping(t *testing.T) {
	t.Parallel()

	cases := map[string]Severity{
		"info":     SevInfo,
		"advice":   SevAdvice,
		"warning":  SevWarning,
		"critical": SevAdvice, // reserved for system Lobes
		"":         SevAdvice,
		"bogus":    SevAdvice,
	}
	for in, want := range cases {
		if got := steerSeverity(in); got != want {
			t.Errorf("steerSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClampNoteTitle pins fallback-on-blank and the 80-rune clamp.
func TestClampNoteTitle(t *testing.T) {
	t.Parallel()

	if got := clampNoteTitle("  ", "fallback"); got != "fallback" {
		t.Errorf("blank title = %q, want fallback", got)
	}
	if got := clampNoteTitle("short", "fb"); got != "short" {
		t.Errorf("short title = %q, want unchanged", got)
	}
	long := strings.Repeat("é", 120) // multi-byte runes to catch byte-vs-rune slicing
	got := clampNoteTitle(long, "fb")
	if n := len([]rune(got)); n > noteTitleMaxRunes {
		t.Errorf("clamped title has %d runes, want <= %d", n, noteTitleMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("clamped title %q missing ellipsis", got)
	}
}
