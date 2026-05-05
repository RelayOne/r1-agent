package jsonrpc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/desktopapi"
	"github.com/RelayOne/r1/internal/stokerr"
)

// fakeDaemon is a programmable DaemonAPI for round-trip coverage.
// Records inbound params on each verb so tests can assert routing.
type fakeDaemon struct {
	mu sync.Mutex

	startReq  DaemonSessionStartRequest
	startResp DaemonSessionStartResponse
	startErr  error

	sendReq  SessionSendRequest
	sendResp SessionSendResponse

	subReq  SessionSubscribeRequest
	subResp SessionSubscribeResponse

	listReq LanesListRequest
	listOut LanesListResponse

	killReq LanesKillRequest

	notesOut CortexNotesResponse

	info DaemonInfoResponse

	shutdownGrace int
	reloadPath    string
}

func (f *fakeDaemon) DaemonSessionStart(ctx context.Context, req DaemonSessionStartRequest) (DaemonSessionStartResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startReq = req
	return f.startResp, f.startErr
}

func (f *fakeDaemon) DaemonSessionPause(ctx context.Context, req SessionIDRequest) (SessionPauseResponse, error) {
	return SessionPauseResponse{PausedAt: "2026-05-02T00:00:00Z"}, nil
}

func (f *fakeDaemon) DaemonSessionResume(ctx context.Context, req SessionIDRequest) (SessionResumeResponse, error) {
	return SessionResumeResponse{ResumedAt: "2026-05-02T00:00:01Z"}, nil
}

func (f *fakeDaemon) DaemonSessionCancel(ctx context.Context, req SessionIDRequest) (SessionCancelResponse, error) {
	return SessionCancelResponse{CancelledAt: "2026-05-02T00:00:02Z"}, nil
}

func (f *fakeDaemon) DaemonSessionSend(ctx context.Context, req SessionSendRequest) (SessionSendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendReq = req
	return f.sendResp, nil
}

func (f *fakeDaemon) DaemonSessionSubscribe(ctx context.Context, req SessionSubscribeRequest) (SessionSubscribeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subReq = req
	return f.subResp, nil
}

func (f *fakeDaemon) DaemonSessionUnsubscribe(ctx context.Context, req SessionUnsubscribeRequest) (SessionUnsubscribeResponse, error) {
	return SessionUnsubscribeResponse{UnsubscribedAt: "2026-05-02T00:00:03Z"}, nil
}

func (f *fakeDaemon) DaemonLanesList(ctx context.Context, req LanesListRequest) (LanesListResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listReq = req
	return f.listOut, nil
}

func (f *fakeDaemon) DaemonLanesKill(ctx context.Context, req LanesKillRequest) (LanesKillResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killReq = req
	return LanesKillResponse{KilledAt: "2026-05-02T00:00:04Z"}, nil
}

func (f *fakeDaemon) DaemonCortexNotes(ctx context.Context, req CortexNotesRequest) (CortexNotesResponse, error) {
	return f.notesOut, nil
}

func (f *fakeDaemon) DaemonInfo(ctx context.Context) (DaemonInfoResponse, error) {
	return f.info, nil
}

func (f *fakeDaemon) DaemonShutdown(ctx context.Context, req DaemonShutdownRequest) (DaemonShutdownResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdownGrace = req.GraceSeconds
	return DaemonShutdownResponse{AcceptedAt: "2026-05-02T00:00:05Z"}, nil
}

func (f *fakeDaemon) DaemonReloadConfig(ctx context.Context, req DaemonReloadConfigRequest) (DaemonReloadConfigResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloadPath = req.Path
	return DaemonReloadConfigResponse{ReloadedAt: "2026-05-02T00:00:06Z", Source: "/etc/r1.yaml"}, nil
}

// dispatchOK sends a request through the dispatcher and returns the
// raw `result` payload. Asserts no error response.
func dispatchOK(t *testing.T, d *Dispatcher, body string) json.RawMessage {
	t.Helper()
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp := d.Dispatch(context.Background(), req)
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	return resp.Result
}

// TestSessionStart_RoundTrip — daemon-variant session.start.
func TestSessionStart_RoundTrip(t *testing.T) {
	fd := &fakeDaemon{startResp: DaemonSessionStartResponse{SessionID: "s-7", StartedAt: "2026-05-02T00:00:00Z"}}
	d := NewDispatcher()
	RegisterDaemonAPI(d, fd)

	body := `{"jsonrpc":"2.0","id":1,"method":"session.start","params":{"workdir":"/tmp/work","model":"claude-sonnet-4-5","prompt":"hello"}}`
	out := dispatchOK(t, d, body)
	if !strings.Contains(string(out), `"session_id":"s-7"`) {
		t.Fatalf("missing session_id: %s", out)
	}
	if fd.startReq.Workdir != "/tmp/work" || fd.startReq.Model != "claude-sonnet-4-5" || fd.startReq.Prompt != "hello" {
		t.Fatalf("handler did not see params: %+v", fd.startReq)
	}
}

// TestSessionSend_DeliversToCortex — session.send delivery semantics.
func TestSessionSend_DeliversToCortex(t *testing.T) {
	fd := &fakeDaemon{sendResp: SessionSendResponse{DeliveredAt: "2026-05-02T01:00:00Z", Seq: 42}}
	d := NewDispatcher()
	RegisterDaemonAPI(d, fd)

	body := `{"jsonrpc":"2.0","id":1,"method":"session.send","params":{"session_id":"s-1","text":"go forth"}}`
	out := dispatchOK(t, d, body)
	if !strings.Contains(string(out), `"seq":42`) {
		t.Fatalf("missing seq: %s", out)
	}
	if fd.sendReq.SessionID != "s-1" || fd.sendReq.Text != "go forth" {
		t.Fatalf("handler did not see params: %+v", fd.sendReq)
	}
}

// TestSubscribe_RoundTripParamsLandOnHandler verifies session.subscribe
// passes since_seq + filter through to the handler.
func TestSubscribe_RoundTripParamsLandOnHandler(t *testing.T) {
	fd := &fakeDaemon{subResp: SessionSubscribeResponse{SubID: "sub-1"}}
	d := NewDispatcher()
	RegisterDaemonAPI(d, fd)

	body := `{"jsonrpc":"2.0","id":1,"method":"session.subscribe","params":{"session_id":"s-1","since_seq":99,"filter":["lane.delta","cost.tick"]}}`
	out := dispatchOK(t, d, body)
	if !strings.Contains(string(out), `"sub":"sub-1"`) {
		t.Fatalf("missing sub: %s", out)
	}
	if fd.subReq.SessionID != "s-1" || fd.subReq.SinceSeq != 99 {
		t.Fatalf("handler did not see params: %+v", fd.subReq)
	}
	if len(fd.subReq.Filter) != 2 || fd.subReq.Filter[0] != "lane.delta" {
		t.Fatalf("filter not propagated: %+v", fd.subReq.Filter)
	}
}

// TestLanesList_AndKill — both lanes verbs round-trip.
func TestLanesList_AndKill(t *testing.T) {
	fd := &fakeDaemon{listOut: LanesListResponse{Lanes: []LaneSummary{
		{LaneID: "lane-1", Kind: "main", Status: "running", StartedAt: "t"},
	}}}
	d := NewDispatcher()
	RegisterDaemonAPI(d, fd)

	out := dispatchOK(t, d, `{"jsonrpc":"2.0","id":1,"method":"lanes.list","params":{"session_id":"s-1"}}`)
	if !strings.Contains(string(out), `"lane-1"`) {
		t.Fatalf("missing lane id: %s", out)
	}

	out = dispatchOK(t, d, `{"jsonrpc":"2.0","id":2,"method":"lanes.kill","params":{"session_id":"s-1","lane_id":"lane-1","reason":"timeout"}}`)
	if !strings.Contains(string(out), `"killed_at"`) {
		t.Fatalf("missing killed_at: %s", out)
	}
	if fd.killReq.LaneID != "lane-1" || fd.killReq.Reason != "timeout" {
		t.Fatalf("kill params: %+v", fd.killReq)
	}
}

// TestCortexNotes_Empty — cortex.notes works with an empty payload.
func TestCortexNotes_Empty(t *testing.T) {
	d := NewDispatcher()
	RegisterDaemonAPI(d, &fakeDaemon{})
	out := dispatchOK(t, d, `{"jsonrpc":"2.0","id":1,"method":"cortex.notes","params":{"session_id":"s-1"}}`)
	if !strings.Contains(string(out), `"notes"`) {
		t.Fatalf("missing notes field: %s", out)
	}
}

// TestDaemonInfo_RoundTrip covers the no-params verb.
func TestDaemonInfo_RoundTrip(t *testing.T) {
	fd := &fakeDaemon{info: DaemonInfoResponse{PID: 4242, Version: "v0.1.0", SessionCount: 3}}
	d := NewDispatcher()
	RegisterDaemonAPI(d, fd)
	out := dispatchOK(t, d, `{"jsonrpc":"2.0","id":1,"method":"daemon.info"}`)
	var v DaemonInfoResponse
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.PID != 4242 || v.SessionCount != 3 {
		t.Fatalf("info: %+v", v)
	}
}

// TestDaemonShutdownAndReloadConfig — both daemon control verbs.
func TestDaemonShutdownAndReloadConfig(t *testing.T) {
	fd := &fakeDaemon{}
	d := NewDispatcher()
	RegisterDaemonAPI(d, fd)
	dispatchOK(t, d, `{"jsonrpc":"2.0","id":1,"method":"daemon.shutdown","params":{"grace_seconds":30}}`)
	if fd.shutdownGrace != 30 {
		t.Fatalf("shutdown grace: %d", fd.shutdownGrace)
	}
	dispatchOK(t, d, `{"jsonrpc":"2.0","id":2,"method":"daemon.reload_config","params":{"path":"/etc/r1.yaml"}}`)
	if fd.reloadPath != "/etc/r1.yaml" {
		t.Fatalf("reload path: %s", fd.reloadPath)
	}
}

// TestSessionStart_DaemonOverridesDesktopAPI verifies that when both
// surfaces are mounted, the daemon variant of session.start wins (so
// the workdir/model fields are honoured rather than silently dropped
// by the inspection-only desktopapi variant).
func TestSessionStart_DaemonOverridesDesktopAPI(t *testing.T) {
	fd := &fakeDaemon{startResp: DaemonSessionStartResponse{SessionID: "daemon-won"}}
	d := NewDispatcher()
	// Mount desktopapi FIRST (so daemon last-write-wins).
	RegisterDesktopAPI(d, desktopapi.NotImplemented{})
	RegisterDaemonAPI(d, fd)
	out := dispatchOK(t, d, `{"jsonrpc":"2.0","id":1,"method":"session.start","params":{"workdir":"/tmp"}}`)
	if !strings.Contains(string(out), `daemon-won`) {
		t.Fatalf("daemon variant did not win: %s", out)
	}
}

// TestRPC_DaemonStokeErrTaxonomyRoundTrip verifies stokerr taxonomy
// surfaces correctly through the daemon adapters too.
func TestRPC_DaemonStokeErrTaxonomyRoundTrip(t *testing.T) {
	fd := &fakeDaemon{startErr: stokerr.New(stokerr.ErrPermission, "blocked")}
	d := NewDispatcher()
	RegisterDaemonAPI(d, fd)
	req, _ := DecodeRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"session.start","params":{"workdir":"/tmp"}}`))
	resp := d.Dispatch(context.Background(), req)
	if resp.Error == nil || resp.Error.Code != CodePermissionDenied {
		t.Fatalf("expected permission_denied, got %+v", resp.Error)
	}
}

// TestRegisterDaemonAPI_PanicsOnNil asserts misconfig surfaces fast.
func TestRegisterDaemonAPI_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	RegisterDaemonAPI(NewDispatcher(), nil)
}
