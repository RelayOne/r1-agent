package jsonrpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/server/sessionhub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// withSandboxedHub redirects R1_HOME to a tmp dir so workdir-validation's
// "under ~/.r1/" rule has a known, isolated target, then constructs a
// fresh SessionHub and HubHandler against it. Returns the handler, the
// sandbox path, and a cleanup func.
func withSandboxedHub(t *testing.T) (*HubHandler, string, func()) {
	t.Helper()
	dir := t.TempDir()
	prev, had := os.LookupEnv("R1_HOME")
	t.Setenv("R1_HOME", dir)
	cleanup := func() {
		if had {
			_ = os.Setenv("R1_HOME", prev)
		} else {
			_ = os.Unsetenv("R1_HOME")
		}
	}
	hub, err := sessionhub.NewHub()
	if err != nil {
		cleanup()
		t.Fatalf("NewHub: %v", err)
	}
	h := NewHubHandler(hub)
	return h, dir, cleanup
}

// TestHubHandler_SessionStart_HappyPath exercises the primary wiring:
// DaemonSessionStart returns a session ID that is then findable via
// hub.Get. This is the load-bearing assertion — without it, the handler
// is a no-op shell.
func TestHubHandler_SessionStart_HappyPath(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{
		Workdir: wd,
		Model:   "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatalf("DaemonSessionStart: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatalf("expected non-empty session_id; got empty")
	}
	if resp.StartedAt == "" {
		t.Fatalf("expected non-empty started_at; got empty")
	}
	// Verify the timestamp parses as RFC3339Nano (the contract the
	// daemon promises to clients on the wire).
	if _, parseErr := time.Parse(time.RFC3339Nano, resp.StartedAt); parseErr != nil {
		t.Fatalf("started_at not RFC3339Nano: %q (%v)", resp.StartedAt, parseErr)
	}

	// The session must be findable via the underlying hub — that's the
	// guarantee external callers depend on (a follow-up session.send /
	// session.cancel must reach the same Session).
	got, err := h.Hub.Get(resp.SessionID)
	if err != nil {
		t.Fatalf("hub.Get(%s): %v", resp.SessionID, err)
	}
	if got.SessionRoot != filepath.Clean(wd) {
		t.Fatalf("SessionRoot: got %q, want %q", got.SessionRoot, filepath.Clean(wd))
	}
	if got.Model != "claude-sonnet-4-5" {
		t.Fatalf("Model: got %q, want %q", got.Model, "claude-sonnet-4-5")
	}
}

// TestHubHandler_SessionStart_RejectsEmptyWorkdir asserts the
// fast-path validation: an empty workdir surfaces ErrValidation BEFORE
// the hub is even consulted.
func TestHubHandler_SessionStart_RejectsEmptyWorkdir(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	_, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: ""})
	if err == nil {
		t.Fatal("expected error for empty workdir; got nil")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *stokerr.Error; got %T: %v", err, err)
	}
	if se.Code != stokerr.ErrValidation {
		t.Fatalf("expected ErrValidation; got %v (%v)", se.Code, err)
	}
	// No session must have been registered.
	if got := h.Hub.List(); len(got) != 0 {
		t.Fatalf("expected 0 sessions after validation failure; got %d", len(got))
	}
}

// TestHubHandler_SessionStart_RejectsRelativeWorkdir asserts the
// hub's "must be absolute" rule surfaces as ErrValidation through the
// handler (wrapping sessionhub.ErrInvalidWorkdir).
func TestHubHandler_SessionStart_RejectsRelativeWorkdir(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	_, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: "relative/path"})
	if err == nil {
		t.Fatal("expected error for relative workdir; got nil")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *stokerr.Error; got %T: %v", err, err)
	}
	if se.Code != stokerr.ErrValidation {
		t.Fatalf("expected ErrValidation; got %v", se.Code)
	}
	// The wrapped cause must chain back to sessionhub.ErrInvalidWorkdir
	// so callers using errors.Is can branch on the precise reason.
	if !errors.Is(err, sessionhub.ErrInvalidWorkdir) {
		t.Fatalf("expected errors.Is(err, ErrInvalidWorkdir); got: %v", err)
	}
}

// TestHubHandler_SessionStart_RejectsNonexistentWorkdir covers the
// "does not exist" branch of the hub's workdir validator.
func TestHubHandler_SessionStart_RejectsNonexistentWorkdir(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	// Construct a path under a real tempdir but with a name we know
	// does not exist.
	parent := t.TempDir()
	bogus := filepath.Join(parent, "does-not-exist")

	_, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: bogus})
	if err == nil {
		t.Fatal("expected error for nonexistent workdir; got nil")
	}
	if !errors.Is(err, sessionhub.ErrInvalidWorkdir) {
		t.Fatalf("expected errors.Is(err, ErrInvalidWorkdir); got: %v", err)
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrValidation {
		t.Fatalf("expected stokerr.ErrValidation; got %v", err)
	}
}

// TestHubHandler_DaemonInfo_ReportsSessionCount asserts the one
// production list-style verb: DaemonInfo's SessionCount reflects the
// number of sessions the hub has registered. Stands in for an explicit
// SessionList verb (the DaemonAPI interface does not declare one
// today).
func TestHubHandler_DaemonInfo_ReportsSessionCount(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	// Populate the daemon-info fields the handler returns verbatim so
	// the assertion can verify the wiring (not just the SessionCount).
	h.PID = 4242
	h.Version = "v0.0.0-test"
	h.StartedAt = time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	h.SocketPath = "/tmp/r1.sock"
	h.HTTPPort = 8420

	// Empty hub first.
	info, err := h.DaemonInfo(context.Background())
	if err != nil {
		t.Fatalf("DaemonInfo: %v", err)
	}
	if info.SessionCount != 0 {
		t.Fatalf("SessionCount empty hub: got %d, want 0", info.SessionCount)
	}
	if info.PID != 4242 || info.Version != "v0.0.0-test" {
		t.Fatalf("DaemonInfo did not echo configured fields: %+v", info)
	}
	if info.StartedAt == "" {
		t.Fatal("DaemonInfo.StartedAt empty despite non-zero StartedAt")
	}

	// Add two sessions.
	wd1 := t.TempDir()
	wd2 := t.TempDir()
	r1, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd1})
	if err != nil {
		t.Fatalf("start s1: %v", err)
	}
	r2, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd2})
	if err != nil {
		t.Fatalf("start s2: %v", err)
	}
	if r1.SessionID == r2.SessionID {
		t.Fatalf("expected distinct session ids; got %q for both", r1.SessionID)
	}

	info, err = h.DaemonInfo(context.Background())
	if err != nil {
		t.Fatalf("DaemonInfo (post-create): %v", err)
	}
	if info.SessionCount != 2 {
		t.Fatalf("SessionCount after 2 creates: got %d, want 2", info.SessionCount)
	}
}

// TestHubHandler_SessionCancel_HappyPath verifies cancel removes the
// session from the hub (delete-and-forget semantics, see the body of
// DaemonSessionCancel for the rationale).
func TestHubHandler_SessionCancel_HappyPath(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cancelResp, err := h.DaemonSessionCancel(context.Background(), SessionIDRequest{SessionID: resp.SessionID})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelResp.CancelledAt == "" {
		t.Fatalf("expected non-empty cancelled_at")
	}
	if _, getErr := h.Hub.Get(resp.SessionID); !errors.Is(getErr, sessionhub.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after cancel; got %v", getErr)
	}
}

// TestHubHandler_PerSessionVerbs_RejectMissingSessionID verifies every
// per-session verb returns ErrValidation on an empty session_id and
// ErrNotFound on an unknown one. This is one table-driven test over
// the four verbs that take a SessionIDRequest.
func TestHubHandler_PerSessionVerbs_RejectMissingSessionID(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	type verb struct {
		name string
		call func(string) error
	}
	verbs := []verb{
		{"pause", func(id string) error {
			_, e := h.DaemonSessionPause(context.Background(), SessionIDRequest{SessionID: id})
			return e
		}},
		{"resume", func(id string) error {
			_, e := h.DaemonSessionResume(context.Background(), SessionIDRequest{SessionID: id})
			return e
		}},
		{"cancel", func(id string) error {
			_, e := h.DaemonSessionCancel(context.Background(), SessionIDRequest{SessionID: id})
			return e
		}},
		{"send", func(id string) error {
			_, e := h.DaemonSessionSend(context.Background(), SessionSendRequest{SessionID: id, Text: "hi"})
			return e
		}},
	}
	for _, v := range verbs {
		// Empty -> ErrValidation.
		err := v.call("")
		if err == nil {
			t.Errorf("%s(empty id): expected error, got nil", v.name)
			continue
		}
		var se *stokerr.Error
		if !errors.As(err, &se) || se.Code != stokerr.ErrValidation {
			t.Errorf("%s(empty id): expected ErrValidation, got %v", v.name, err)
		}
		// Unknown -> ErrNotFound.
		err = v.call("s-unknown")
		if err == nil {
			t.Errorf("%s(unknown id): expected error, got nil", v.name)
			continue
		}
		if !errors.As(err, &se) || se.Code != stokerr.ErrNotFound {
			t.Errorf("%s(unknown id): expected ErrNotFound, got %v", v.name, err)
		}
	}
}

// TestHubHandler_BlockedVerbs_SurfaceInternalError asserts the
// handlers that depend on absent Session primitives return ErrInternal
// (rather than panicking or silently succeeding) when called with a
// VALID session id. The dispatcher then surfaces this as a structured
// RPC error so the caller knows the verb is BLOCKED on a follow-up.
func TestHubHandler_BlockedVerbs_SurfaceInternalError(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := resp.SessionID

	type verb struct {
		name string
		call func() error
	}
	// pause/resume/send are NOT BLOCKED anymore — see
	// TestHubHandler_PauseResumeRoundTrip and
	// TestHubHandler_Send_NoActiveRunInbox below for the new positive
	// coverage. They still return errors here because send hits a
	// closed inbox (no Run goroutine in the test), but the error code
	// is ErrValidation, not ErrInternal.
	verbs := []verb{
		{"subscribe", func() error {
			_, e := h.DaemonSessionSubscribe(context.Background(), SessionSubscribeRequest{SessionID: id})
			return e
		}},
		{"lanes.kill", func() error {
			_, e := h.DaemonLanesKill(context.Background(), LanesKillRequest{SessionID: id, LaneID: "lane-x"})
			return e
		}},
		{"shutdown", func() error {
			_, e := h.DaemonShutdown(context.Background(), DaemonShutdownRequest{})
			return e
		}},
		{"reload_config", func() error {
			_, e := h.DaemonReloadConfig(context.Background(), DaemonReloadConfigRequest{})
			return e
		}},
	}
	for _, v := range verbs {
		err := v.call()
		if err == nil {
			t.Errorf("%s: expected BLOCKED error, got nil", v.name)
			continue
		}
		var se *stokerr.Error
		if !errors.As(err, &se) || se.Code != stokerr.ErrInternal {
			t.Errorf("%s: expected ErrInternal, got %v", v.name, err)
			continue
		}
		// The error message MUST name the BLOCKED dependency so a
		// reviewer can grep it.
		if !strings.Contains(se.Message, "BLOCKED") {
			t.Errorf("%s: error message must contain BLOCKED marker; got %q", v.name, se.Message)
		}
	}
}

// TestHubHandler_LanesList_EmptyByDefault asserts the documented
// no-Run-yet behavior: lanes.list returns an empty list (not an error)
// for a freshly-created session.
func TestHubHandler_LanesList_EmptyByDefault(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	out, err := h.DaemonLanesList(context.Background(), LanesListRequest{SessionID: resp.SessionID})
	if err != nil {
		t.Fatalf("lanes.list: %v", err)
	}
	if len(out.Lanes) != 0 {
		t.Fatalf("expected empty lanes list; got %d", len(out.Lanes))
	}
}

// TestHubHandler_RoundTripThroughDispatcher verifies the handler is
// reachable through the production RegisterDaemonAPI adapter — i.e.,
// the JSON-RPC frame for session.start lands on HubHandler.Start and
// returns a structured response. Guards against future signature drift
// breaking the dispatcher integration.
func TestHubHandler_RoundTripThroughDispatcher(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	d := NewDispatcher()
	RegisterDaemonAPI(d, h)

	wd := t.TempDir()
	body := `{"jsonrpc":"2.0","id":1,"method":"session.start","params":{"workdir":"` + wd + `","model":"m"}}`
	req, decErr := DecodeRequest([]byte(body))
	if decErr != nil {
		t.Fatalf("decode: %v", decErr)
	}
	resp := d.Dispatch(context.Background(), req)
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), `"session_id"`) {
		t.Fatalf("missing session_id in response: %s", string(resp.Result))
	}
}

// TestNewHubHandler_PanicsOnNilHub asserts the construction guard —
// nil hub is a programmer error.
func TestNewHubHandler_PanicsOnNilHub(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil hub")
		}
	}()
	_ = NewHubHandler(nil)
}

// TestHubHandler_PauseResumeRoundTrip exercises the new
// Session.Pause/Resume primitives via the JSON-RPC handlers. Asserts
// idempotency, observable state transitions, and PausedAt/ResumedAt
// timestamps land on the response.
func TestHubHandler_PauseResumeRoundTrip(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := resp.SessionID

	// Initial: not paused.
	sess, err := h.Hub.Get(id)
	if err != nil {
		t.Fatalf("hub.Get: %v", err)
	}
	if sess.IsPaused() {
		t.Fatalf("freshly-started session should not be paused")
	}

	// Pause.
	pauseResp, err := h.DaemonSessionPause(context.Background(), SessionIDRequest{SessionID: id})
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if pauseResp.PausedAt == "" {
		t.Errorf("pause response missing PausedAt")
	}
	if !sess.IsPaused() {
		t.Errorf("session should be paused after Pause")
	}

	// Pause again — idempotent.
	if _, err := h.DaemonSessionPause(context.Background(), SessionIDRequest{SessionID: id}); err != nil {
		t.Errorf("second pause should be a no-op, got %v", err)
	}
	if !sess.IsPaused() {
		t.Errorf("session still paused after idempotent Pause")
	}

	// Resume.
	resumeResp, err := h.DaemonSessionResume(context.Background(), SessionIDRequest{SessionID: id})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumeResp.ResumedAt == "" {
		t.Errorf("resume response missing ResumedAt")
	}
	if sess.IsPaused() {
		t.Errorf("session should not be paused after Resume")
	}

	// Resume again — idempotent.
	if _, err := h.DaemonSessionResume(context.Background(), SessionIDRequest{SessionID: id}); err != nil {
		t.Errorf("second resume should be a no-op, got %v", err)
	}
}

// TestHubHandler_Send_PreRun confirms session.send returns
// ErrValidation (NOT ErrInternal) when the session has not started a
// Run goroutine — the inbox is closed because Run never installed it.
// This is the documented "session is not running" path.
func TestHubHandler_Send_PreRun(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_, err = h.DaemonSessionSend(context.Background(), SessionSendRequest{
		SessionID: resp.SessionID,
		Text:      "hello",
	})
	if err == nil {
		t.Fatalf("expected error sending to a session with no Run; got nil")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) || se.Code != stokerr.ErrValidation {
		t.Errorf("expected ErrValidation, got %v", err)
	}
	if !strings.Contains(se.Message, "not running") {
		t.Errorf("error message must mention 'not running'; got %q", se.Message)
	}
}

// TestHubHandler_Send_EmptyTextRejected asserts the validation gate
// fires before the inbox check.
func TestHubHandler_Send_EmptyTextRejected(t *testing.T) {
	h, _, cleanup := withSandboxedHub(t)
	defer cleanup()

	wd := t.TempDir()
	resp, err := h.DaemonSessionStart(context.Background(), DaemonSessionStartRequest{Workdir: wd})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, text := range []string{"", "   ", "\n\t"} {
		_, err := h.DaemonSessionSend(context.Background(), SessionSendRequest{
			SessionID: resp.SessionID,
			Text:      text,
		})
		if err == nil {
			t.Errorf("send(text=%q): expected error, got nil", text)
			continue
		}
		var se *stokerr.Error
		if !errors.As(err, &se) || se.Code != stokerr.ErrValidation {
			t.Errorf("send(text=%q): expected ErrValidation, got %v", text, err)
		}
	}
}
