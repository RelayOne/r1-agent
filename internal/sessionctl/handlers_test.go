package sessionctl

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// ---- fakes -----------------------------------------------------------------

type fakeEmitter struct {
	mu    sync.Mutex
	calls []emitCall
}

type emitCall struct {
	Kind    string
	Payload any
}

func (f *fakeEmitter) publish(k string, payload any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, emitCall{Kind: k, Payload: payload})
	return "evt-" + k
}

func (f *fakeEmitter) Calls() []emitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]emitCall, len(f.calls))
	copy(out, f.calls)
	return out
}

type fakeSignaler struct {
	mu                     sync.Mutex
	pausePGID, resumePGID  int
	pauseCalls, resumeCall int
	pauseErr, resumeErr    error
}

func (f *fakeSignaler) Pause(pgid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseCalls++
	f.pausePGID = pgid
	return f.pauseErr
}

func (f *fakeSignaler) Resume(pgid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeCall++
	f.resumePGID = pgid
	return f.resumeErr
}

// ---- status ----------------------------------------------------------------

func TestStatus_ReturnsSnapshot(t *testing.T) {
	t.Parallel()
	deps := Deps{
		Status: func() StatusSnapshot {
			return StatusSnapshot{
				State:     "executing",
				Mode:      "ship",
				PlanID:    "plan-1",
				Task:      &Task{ID: "t1", Title: "do a thing", Phase: "execute"},
				CostUSD:   1.25,
				BudgetUSD: 10.00,
				Paused:    false,
			}
		},
	}
	h := statusHandler(deps)
	data, errMsg, evtID := h(Request{Verb: VerbStatus, Payload: json.RawMessage(`{}`)})
	if errMsg != "" {
		t.Fatalf("errMsg: got %q, want empty", errMsg)
	}
	if evtID != "" {
		t.Fatalf("evtID: got %q, want empty (read-only)", evtID)
	}
	var got StatusSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if got.State != "executing" || got.Mode != "ship" || got.CostUSD != 1.25 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if got.Task == nil || got.Task.ID != "t1" {
		t.Fatalf("task missing or wrong: %+v", got.Task)
	}
}

func TestStatus_NoSnapshotFn_ReturnsDefault(t *testing.T) {
	t.Parallel()
	h := statusHandler(Deps{})
	data, errMsg, _ := h(Request{Verb: VerbStatus})
	if errMsg != "" {
		t.Fatalf("errMsg: got %q, want empty", errMsg)
	}
	var got StatusSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if got.State != "idle" || got.Mode != "unknown" {
		t.Fatalf("default snapshot: got %+v, want {idle,unknown}", got)
	}
}

// ---- approve ---------------------------------------------------------------

func TestApprove_WithAskID_Resolves(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	ch, err := r.Register("a", 0)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	em := &fakeEmitter{}
	deps := Deps{SessionID: "sess-1", Router: r, Emit: em.publish}
	h := approveHandler(deps)

	payload, _ := json.Marshal(approvePayload{ApprovalID: "a", Decision: "yes", Reason: "lgtm"})
	data, errMsg, evtID := h(Request{Verb: VerbApprove, Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: got %q, want empty", errMsg)
	}
	if evtID != "evt-operator.approve" {
		t.Fatalf("evtID: got %q, want %q", evtID, "evt-operator.approve")
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if out["matched_ask_id"] != "a" {
		t.Fatalf("matched_ask_id: got %q, want %q", out["matched_ask_id"], "a")
	}
	select {
	case d := <-ch:
		if d.Choice != "yes" || d.Reason != "lgtm" || d.Actor != "cli:socket" {
			t.Fatalf("decision: got %+v", d)
		}
	default:
		t.Fatalf("channel did not receive decision")
	}
	calls := em.Calls()
	if len(calls) != 1 || calls[0].Kind != "operator.approve" {
		t.Fatalf("emit: got %+v, want one operator.approve", calls)
	}
}

func TestApprove_EmptyAskID_UsesOldestOpen(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	ch, _ := r.Register("first", 0)
	if _, err := r.Register("second", 0); err != nil {
		t.Fatalf("Register second: %v", err)
	}
	em := &fakeEmitter{}
	deps := Deps{SessionID: "s", Router: r, Emit: em.publish}
	h := approveHandler(deps)

	payload, _ := json.Marshal(approvePayload{Decision: "yes"})
	data, errMsg, _ := h(Request{Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["matched_ask_id"] != "first" {
		t.Fatalf("matched_ask_id: got %q, want %q", out["matched_ask_id"], "first")
	}
	select {
	case d := <-ch:
		if d.AskID != "first" {
			t.Fatalf("wrong ask resolved: %q", d.AskID)
		}
	default:
		t.Fatalf("first channel did not receive decision")
	}
}

func TestApprove_NoOpenAsks_ReturnsError(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	em := &fakeEmitter{}
	deps := Deps{Router: r, Emit: em.publish}
	h := approveHandler(deps)

	payload, _ := json.Marshal(approvePayload{Decision: "yes"})
	_, errMsg, evtID := h(Request{Payload: payload})
	if errMsg == "" || !strings.Contains(errMsg, "no pending") {
		t.Fatalf("errMsg: got %q, want contains 'no pending'", errMsg)
	}
	if evtID != "" {
		t.Fatalf("evtID: got %q on error path", evtID)
	}
	if len(em.Calls()) != 0 {
		t.Fatalf("emit called on error path: %+v", em.Calls())
	}
}

func TestApprove_UnknownAskID_ReturnsError(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	em := &fakeEmitter{}
	deps := Deps{Router: r, Emit: em.publish}
	h := approveHandler(deps)

	payload, _ := json.Marshal(approvePayload{ApprovalID: "ghost", Decision: "yes"})
	_, errMsg, _ := h(Request{Payload: payload})
	if errMsg == "" {
		t.Fatalf("expected error for unknown ask_id")
	}
	if !errors.Is(errorFromMsg(errMsg), ErrAskUnknown) && !strings.Contains(errMsg, "no longer open") {
		t.Fatalf("errMsg: got %q, want router ErrAskUnknown", errMsg)
	}
}

// errorFromMsg is a shim that lets us compare a sentinel error by its text;
// our handlers return err.Error() rather than sentinels so errors.Is on the
// string is not meaningful -- we fall back to substring matching in the
// caller. This helper just returns a synthetic error so the errors.Is call
// compiles cleanly alongside the substring fallback.
func errorFromMsg(msg string) error { return errors.New(msg) }

// ---- override --------------------------------------------------------------

func TestOverride_EmitsEvent(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	deps := Deps{SessionID: "s", Emit: em.publish}
	h := overrideHandler(deps)

	payload, _ := json.Marshal(overridePayload{ACID: "ac-42", Reason: "flake"})
	data, errMsg, evtID := h(Request{Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if evtID != "evt-operator.override" {
		t.Fatalf("evtID: %q", evtID)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["ac_id"] != "ac-42" {
		t.Fatalf("ac_id: got %q", out["ac_id"])
	}
	if len(em.Calls()) != 1 || em.Calls()[0].Kind != "operator.override" {
		t.Fatalf("emit: %+v", em.Calls())
	}
}

func TestOverride_MissingFields_ReturnsError(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	deps := Deps{Emit: em.publish}
	h := overrideHandler(deps)

	// Missing ac_id.
	payload, _ := json.Marshal(overridePayload{Reason: "x"})
	_, errMsg, _ := h(Request{Payload: payload})
	if errMsg == "" || !strings.Contains(errMsg, "ac_id") {
		t.Fatalf("errMsg for missing ac_id: %q", errMsg)
	}
	// Missing reason.
	payload, _ = json.Marshal(overridePayload{ACID: "a"})
	_, errMsg, _ = h(Request{Payload: payload})
	if errMsg == "" || !strings.Contains(errMsg, "reason") {
		t.Fatalf("errMsg for missing reason: %q", errMsg)
	}
	if len(em.Calls()) != 0 {
		t.Fatalf("emit on error path: %+v", em.Calls())
	}
}

// ---- budget_add ------------------------------------------------------------

func TestBudgetAdd_DryRun_SkipsPublish(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	var got struct {
		delta  float64
		dryRun bool
	}
	deps := Deps{
		Emit: em.publish,
		BudgetAdd: func(delta float64, dryRun bool) (float64, float64, error) {
			got.delta = delta
			got.dryRun = dryRun
			return 5.0, 6.0, nil
		},
	}
	h := budgetAddHandler(deps)
	payload, _ := json.Marshal(budgetAddPayload{DeltaUSD: 1.0, DryRun: true})
	data, errMsg, evtID := h(Request{Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if evtID != "" {
		t.Fatalf("evtID: got %q, want empty (dry run)", evtID)
	}
	if len(em.Calls()) != 0 {
		t.Fatalf("emit called on dry run: %+v", em.Calls())
	}
	var out map[string]float64
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["prev_budget"] != 5.0 || out["new_budget"] != 6.0 {
		t.Fatalf("budgets: got %+v", out)
	}
	if got.delta != 1.0 || !got.dryRun {
		t.Fatalf("BudgetAdd received: delta=%v dryRun=%v", got.delta, got.dryRun)
	}
}

func TestBudgetAdd_Real_Emits(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	deps := Deps{
		SessionID: "s",
		Emit:      em.publish,
		BudgetAdd: func(delta float64, dryRun bool) (float64, float64, error) {
			return 5.0, 7.5, nil
		},
	}
	h := budgetAddHandler(deps)
	payload, _ := json.Marshal(budgetAddPayload{DeltaUSD: 2.5})
	_, errMsg, evtID := h(Request{Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if evtID != "evt-operator.budget_change" {
		t.Fatalf("evtID: %q", evtID)
	}
	calls := em.Calls()
	if len(calls) != 1 || calls[0].Kind != "operator.budget_change" {
		t.Fatalf("emit: %+v", calls)
	}
}

func TestBudgetAdd_NoFn_ReturnsError(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	h := budgetAddHandler(Deps{Emit: em.publish})
	payload, _ := json.Marshal(budgetAddPayload{DeltaUSD: 1.0})
	_, errMsg, _ := h(Request{Payload: payload})
	if errMsg == "" || !strings.Contains(errMsg, "budget tracking") {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if len(em.Calls()) != 0 {
		t.Fatalf("emit on error path: %+v", em.Calls())
	}
}

// ---- pause / resume --------------------------------------------------------

func TestPause_NoSignaler_ReturnsError(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	h := pauseHandler(Deps{Emit: em.publish})
	_, errMsg, _ := h(Request{})
	if errMsg == "" || !strings.Contains(errMsg, "signaler") {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if len(em.Calls()) != 0 {
		t.Fatalf("emit on error: %+v", em.Calls())
	}
}

func TestPause_ZeroPGID_ReturnsError(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	h := pauseHandler(Deps{Signaler: sig, PGID: 0})
	_, errMsg, _ := h(Request{})
	if errMsg == "" || !strings.Contains(errMsg, "signaler") {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if sig.pauseCalls != 0 {
		t.Fatalf("Pause called with zero pgid")
	}
}

func TestPause_CallsSignalerPause(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	deps := Deps{SessionID: "s", Signaler: sig, PGID: 4242, Emit: em.publish}
	h := pauseHandler(deps)
	data, errMsg, evtID := h(Request{})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if evtID != "evt-operator.pause" {
		t.Fatalf("evtID: %q", evtID)
	}
	if sig.pauseCalls != 1 || sig.pausePGID != 4242 {
		t.Fatalf("signaler: calls=%d pgid=%d", sig.pauseCalls, sig.pausePGID)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["paused_at"]; !ok {
		t.Fatalf("paused_at missing: %+v", out)
	}
	calls := em.Calls()
	if len(calls) != 1 || calls[0].Kind != "operator.pause" {
		t.Fatalf("emit: %+v", calls)
	}
}

func TestResume_CallsSignalerResume(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	deps := Deps{SessionID: "s", Signaler: sig, PGID: 123, Emit: em.publish}
	h := resumeHandler(deps)
	_, errMsg, evtID := h(Request{})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if evtID != "evt-operator.resume" {
		t.Fatalf("evtID: %q", evtID)
	}
	if sig.resumeCall != 1 || sig.resumePGID != 123 {
		t.Fatalf("signaler: calls=%d pgid=%d", sig.resumeCall, sig.resumePGID)
	}
	calls := em.Calls()
	if len(calls) != 1 || calls[0].Kind != "operator.resume" {
		t.Fatalf("emit: %+v", calls)
	}
}

func TestPause_SignalerError_Propagates(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{pauseErr: errors.New("boom")}
	em := &fakeEmitter{}
	deps := Deps{Signaler: sig, PGID: 99, Emit: em.publish}
	h := pauseHandler(deps)
	_, errMsg, _ := h(Request{})
	if !strings.Contains(errMsg, "boom") {
		t.Fatalf("errMsg: got %q, want 'boom'", errMsg)
	}
	if len(em.Calls()) != 0 {
		t.Fatalf("emit on signaler error: %+v", em.Calls())
	}
}

// ---- inject ---------------------------------------------------------------

func TestInject_EmptyText_ReturnsError(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	h := injectHandler(Deps{Emit: em.publish})
	payload, _ := json.Marshal(injectPayload{Text: ""})
	_, errMsg, _ := h(Request{Payload: payload})
	if errMsg == "" || !strings.Contains(errMsg, "text") {
		t.Fatalf("errMsg: %q", errMsg)
	}
}

func TestInject_NoInjectFn_ReturnsError(t *testing.T) {
	t.Parallel()
	h := injectHandler(Deps{})
	payload, _ := json.Marshal(injectPayload{Text: "hello"})
	_, errMsg, _ := h(Request{Payload: payload})
	if errMsg == "" || !strings.Contains(errMsg, "inject unavailable") {
		t.Fatalf("errMsg: %q", errMsg)
	}
}

func TestInject_CallsInjectTask(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	var gotText string
	var gotPriority int
	deps := Deps{
		SessionID: "s",
		Emit:      em.publish,
		InjectTask: func(text string, priority int) (string, error) {
			gotText = text
			gotPriority = priority
			return "task-xyz", nil
		},
	}
	h := injectHandler(deps)
	payload, _ := json.Marshal(injectPayload{Text: "do the thing", Priority: 5})
	data, errMsg, evtID := h(Request{Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if evtID != "evt-operator.inject" {
		t.Fatalf("evtID: %q", evtID)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["task_id"] != "task-xyz" {
		t.Fatalf("task_id: %q", out["task_id"])
	}
	if gotText != "do the thing" || gotPriority != 5 {
		t.Fatalf("InjectTask received text=%q priority=%d", gotText, gotPriority)
	}
	calls := em.Calls()
	if len(calls) != 1 || calls[0].Kind != "operator.inject" {
		t.Fatalf("emit: %+v", calls)
	}
}

func TestInject_InjectFnError_Propagates(t *testing.T) {
	t.Parallel()
	em := &fakeEmitter{}
	deps := Deps{
		Emit: em.publish,
		InjectTask: func(text string, priority int) (string, error) {
			return "", errors.New("queue closed")
		},
	}
	h := injectHandler(deps)
	payload, _ := json.Marshal(injectPayload{Text: "x"})
	_, errMsg, _ := h(Request{Payload: payload})
	if !strings.Contains(errMsg, "queue closed") {
		t.Fatalf("errMsg: %q", errMsg)
	}
	if len(em.Calls()) != 0 {
		t.Fatalf("emit on inject error: %+v", em.Calls())
	}
}

// ---- takeover wiring -------------------------------------------------------
// Deep coverage lives in takeover_test.go; here we just check the nil-manager
// path the DefaultHandlers constructor exposes.

func TestTakeoverRequest_NilManager(t *testing.T) {
	t.Parallel()
	h := takeoverRequestHandler(Deps{})
	_, errMsg, evtID := h(Request{Payload: json.RawMessage(`{}`)})
	if errMsg == "" || !strings.Contains(errMsg, "takeover unavailable") {
		t.Fatalf("errMsg: got %q, want contains 'takeover unavailable'", errMsg)
	}
	if evtID != "" {
		t.Fatalf("evtID: got %q, want empty", evtID)
	}
}

func TestTakeoverRelease_NilManager(t *testing.T) {
	t.Parallel()
	h := takeoverReleaseHandler(Deps{})
	_, errMsg, _ := h(Request{Payload: json.RawMessage(`{}`)})
	if errMsg == "" || !strings.Contains(errMsg, "takeover unavailable") {
		t.Fatalf("errMsg: got %q, want contains 'takeover unavailable'", errMsg)
	}
}

// ---- DefaultHandlers sanity -----------------------------------------------

func TestDefaultHandlers_CoversAllVerbs(t *testing.T) {
	t.Parallel()
	h := DefaultHandlers(Deps{})
	wantVerbs := []string{
		VerbStatus, VerbApprove, VerbOverride, VerbBudgetAdd,
		VerbPause, VerbResume, VerbInject,
		VerbTakeoverRequest, VerbTakeoverRelease,
	}
	for _, v := range wantVerbs {
		if _, ok := h[v]; !ok {
			t.Fatalf("DefaultHandlers missing verb %q", v)
		}
	}
	if len(h) != len(wantVerbs) {
		t.Fatalf("DefaultHandlers: got %d verbs, want %d", len(h), len(wantVerbs))
	}
}

// ---- strict payload decoding ----------------------------------------------

func TestDecodeStrict_RejectsUnknownFields(t *testing.T) {
	t.Parallel()
	h := injectHandler(Deps{
		InjectTask: func(text string, priority int) (string, error) { return "x", nil },
	})
	// Unknown field "bogus" -- must be rejected.
	_, errMsg, _ := h(Request{Payload: json.RawMessage(`{"text":"x","bogus":1}`)})
	if errMsg == "" || !strings.Contains(errMsg, "payload") {
		t.Fatalf("errMsg: got %q, want payload error", errMsg)
	}
}

func TestDecodeStrict_EmptyPayload_Allowed(t *testing.T) {
	t.Parallel()
	// Status has an empty payload and must accept nil raw message cleanly.
	h := statusHandler(Deps{})
	_, errMsg, _ := h(Request{})
	if errMsg != "" {
		t.Fatalf("errMsg on empty payload: %q", errMsg)
	}
}
