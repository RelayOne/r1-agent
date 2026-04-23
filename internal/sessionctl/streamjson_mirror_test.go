package sessionctl

import (
	"testing"
)

// fakeSink records the most-recent EmitOperator arguments so tests can
// assert the mirror fan-out shape without wiring a real streamjson
// emitter.
type fakeSink struct {
	calls   int
	verb    string
	payload any
	eventID string
}

func (f *fakeSink) EmitOperator(verb string, payload any, eventID string) {
	f.calls++
	f.verb = verb
	f.payload = payload
	f.eventID = eventID
}

func TestNewStreamjsonEmit_CallsBothSinks(t *testing.T) {
	sink := &fakeSink{}
	var (
		delKind    string
		delPayload any
		delCalls   int
	)
	delegate := func(kind string, payload any) string {
		delCalls++
		delKind = kind
		delPayload = payload
		return "evt_1"
	}

	emit := NewStreamjsonEmit(sink, delegate)
	payload := map[string]any{"foo": "bar"}
	id := emit("operator.approve", payload)

	if id != "evt_1" {
		t.Errorf("eventID=%q, want evt_1", id)
	}
	if delCalls != 1 {
		t.Errorf("delegate calls=%d, want 1", delCalls)
	}
	if delKind != "operator.approve" {
		t.Errorf("delegate kind=%q, want operator.approve", delKind)
	}
	if _, ok := delPayload.(map[string]any); !ok {
		t.Errorf("delegate payload type=%T, want map[string]any", delPayload)
	}
	if sink.calls != 1 {
		t.Errorf("sink calls=%d, want 1", sink.calls)
	}
	// Decision: streamjson's EmitOperator receives the VERB suffix
	// only; the "stoke.operator." prefix is added by the emitter.
	if sink.verb != "approve" {
		t.Errorf("sink verb=%q, want approve (no operator. prefix)", sink.verb)
	}
}

func TestNewStreamjsonEmit_NilDelegate_Ok(t *testing.T) {
	sink := &fakeSink{}
	emit := NewStreamjsonEmit(sink, nil)

	id := emit("operator.pause", map[string]any{"x": 1})

	if id != "" {
		t.Errorf("eventID=%q, want empty (no delegate)", id)
	}
	if sink.calls != 1 {
		t.Errorf("sink calls=%d, want 1", sink.calls)
	}
	if sink.verb != "pause" {
		t.Errorf("sink verb=%q, want pause", sink.verb)
	}
	if sink.eventID != "" {
		t.Errorf("sink eventID=%q, want empty", sink.eventID)
	}
}

func TestNewStreamjsonEmit_NilSink_Ok(t *testing.T) {
	var (
		delCalls int
		delKind  string
	)
	delegate := func(kind string, payload any) string {
		delCalls++
		delKind = kind
		return "evt_xyz"
	}

	emit := NewStreamjsonEmit(nil, delegate)
	id := emit("operator.resume", nil)

	if id != "evt_xyz" {
		t.Errorf("eventID=%q, want evt_xyz", id)
	}
	if delCalls != 1 {
		t.Errorf("delegate calls=%d, want 1", delCalls)
	}
	if delKind != "operator.resume" {
		t.Errorf("delegate kind=%q, want operator.resume", delKind)
	}
}

func TestNewStreamjsonEmit_EventIDThreaded(t *testing.T) {
	sink := &fakeSink{}
	delegate := func(kind string, payload any) string { return "evt_abc" }

	emit := NewStreamjsonEmit(sink, delegate)
	_ = emit("operator.override", map[string]any{"ac_id": "ac_1"})

	if sink.eventID != "evt_abc" {
		t.Errorf("sink eventID=%q, want evt_abc", sink.eventID)
	}
	if sink.verb != "override" {
		t.Errorf("sink verb=%q, want override", sink.verb)
	}
}

func TestNewStreamjsonEmit_NonOperatorKindBypassesSink(t *testing.T) {
	sink := &fakeSink{}
	var delCalls int
	delegate := func(kind string, payload any) string {
		delCalls++
		return "evt_sys"
	}

	emit := NewStreamjsonEmit(sink, delegate)
	_ = emit("system.heartbeat", map[string]any{"ts": 1})

	if delCalls != 1 {
		t.Errorf("delegate calls=%d, want 1", delCalls)
	}
	if sink.calls != 0 {
		t.Errorf("sink calls=%d, want 0 (non-operator kind should not mirror)", sink.calls)
	}
}

func TestNewStreamjsonEmit_BothNilIsSafe(t *testing.T) {
	emit := NewStreamjsonEmit(nil, nil)
	id := emit("operator.approve", nil)
	if id != "" {
		t.Errorf("eventID=%q, want empty", id)
	}
}
