package main

// ctl_cmd_test.go — CDC-13/14 wrapper tests.
//
// Each test spins up a real sessionctl.Server in a tempdir with
// test-supplied handler functions that return canned responses, invokes
// the per-verb runner with bytes.Buffer writers, and asserts on exit
// code + output. No /tmp collisions because every test passes
// --ctl-dir <t.TempDir()>.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/sessionctl"
)

// ctlTestSession is a handle to a running sessionctl.Server plus the
// tempdir the socket lives in. Close() tears down the server.
type ctlTestSession struct {
	dir       string
	sessionID string
	srv       *sessionctl.Server
}

func (s *ctlTestSession) Close() { _ = s.srv.Close() }

// newCtlTestSession starts a Server with the given handlers under a new
// tempdir. SessionID is derived from test name + PID + nanoseconds so
// concurrent tests never collide.
func newCtlTestSession(t *testing.T, handlers map[string]sessionctl.Handler) *ctlTestSession {
	t.Helper()
	dir := t.TempDir()
	sid := fmt.Sprintf("test-%d-%d", os.Getpid(), time.Now().UnixNano())
	srv, err := sessionctl.StartServer(sessionctl.Opts{
		SocketDir: dir,
		SessionID: sid,
		Handlers:  handlers,
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	return &ctlTestSession{dir: dir, sessionID: sid, srv: srv}
}

// ---- status -----------------------------------------------------------------

func TestStatus_DiscoveryEmpty(t *testing.T) {
	dir := t.TempDir()
	var out, errBuf bytes.Buffer
	code := runStatusCmd([]string{"--ctl-dir", dir}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no running sessions") {
		t.Errorf("stdout=%q; want 'no running sessions'", out.String())
	}
}

func TestStatus_DiscoveryOne(t *testing.T) {
	snap := sessionctl.StatusSnapshot{
		State:     "executing",
		Mode:      "chat",
		CostUSD:   0.42,
		BudgetUSD: 5.00,
	}
	raw, _ := json.Marshal(snap)
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbStatus: func(req sessionctl.Request) (json.RawMessage, string, string) {
			return raw, "", ""
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runStatusCmd([]string{"--ctl-dir", sess.dir}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, sess.sessionID) {
		t.Errorf("stdout missing session id: %q", s)
	}
	if !strings.Contains(s, "executing") {
		t.Errorf("stdout missing state: %q", s)
	}
	if !strings.Contains(s, "chat") {
		t.Errorf("stdout missing mode: %q", s)
	}
	if !strings.Contains(s, "$0.42") {
		t.Errorf("stdout missing cost: %q", s)
	}
}

func TestStatus_BySessionID(t *testing.T) {
	snap := sessionctl.StatusSnapshot{
		State:     "waiting",
		Mode:      "ship",
		PlanID:    "pln_abc",
		CostUSD:   1.87,
		BudgetUSD: 4.00,
		Paused:    true,
		Task:      &sessionctl.Task{ID: "T3", Title: "descent", Phase: "plan"},
	}
	raw, _ := json.Marshal(snap)
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbStatus: func(req sessionctl.Request) (json.RawMessage, string, string) {
			return raw, "", ""
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runStatusCmd([]string{"--ctl-dir", sess.dir, sess.sessionID}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{sess.sessionID, "waiting", "ship", "pln_abc", "T3", "descent", "yes"} {
		if !strings.Contains(s, want) {
			t.Errorf("stdout missing %q: %q", want, s)
		}
	}
}

func TestStatus_JSON(t *testing.T) {
	snap := sessionctl.StatusSnapshot{State: "idle", Mode: "chat"}
	raw, _ := json.Marshal(snap)
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbStatus: func(req sessionctl.Request) (json.RawMessage, string, string) {
			return raw, "", ""
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runStatusCmd([]string{"--ctl-dir", sess.dir, "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	var parsed []map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out.String())
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 entry, got %d", len(parsed))
	}
	if parsed[0]["session_id"] != sess.sessionID {
		t.Errorf("session_id=%v, want %s", parsed[0]["session_id"], sess.sessionID)
	}
}

// ---- approve ----------------------------------------------------------------

func TestApprove_Success(t *testing.T) {
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbApprove: func(req sessionctl.Request) (json.RawMessage, string, string) {
			var p struct {
				ApprovalID string `json:"approval_id"`
				Decision   string `json:"decision"`
				Reason     string `json:"reason"`
			}
			_ = json.Unmarshal(req.Payload, &p)
			if p.Decision != "yes" {
				return nil, "unexpected decision: " + p.Decision, ""
			}
			return json.RawMessage(`{"matched_ask_id":"ask-1"}`), "", "evt-42"
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runApproveCmd(
		[]string{"--ctl-dir", sess.dir, sess.sessionID, "--decision", "yes"},
		&out, &errBuf,
	)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "ask=ask-1") {
		t.Errorf("stdout missing ask id: %q", s)
	}
	if !strings.Contains(s, "event=evt-42") {
		t.Errorf("stdout missing event id: %q", s)
	}
}

func TestApprove_NoOpenAsks(t *testing.T) {
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbApprove: func(req sessionctl.Request) (json.RawMessage, string, string) {
			return nil, "no pending approvals", ""
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runApproveCmd(
		[]string{"--ctl-dir", sess.dir, sess.sessionID},
		&out, &errBuf,
	)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "no pending approvals") {
		t.Errorf("stderr missing error: %q", errBuf.String())
	}
}

// ---- override ---------------------------------------------------------------

func TestOverride_Success(t *testing.T) {
	var captured struct {
		acID, reason string
	}
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbOverride: func(req sessionctl.Request) (json.RawMessage, string, string) {
			var p struct {
				ACID   string `json:"ac_id"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(req.Payload, &p)
			captured.acID = p.ACID
			captured.reason = p.Reason
			return json.RawMessage(`{"ac_id":"` + p.ACID + `"}`), "", "evt-9"
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runOverrideCmd(
		[]string{"--ctl-dir", sess.dir, sess.sessionID, "AC-7", "--reason", "operator-force"},
		&out, &errBuf,
	)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if captured.acID != "AC-7" {
		t.Errorf("ac_id=%q, want AC-7", captured.acID)
	}
	if captured.reason != "operator-force" {
		t.Errorf("reason=%q, want operator-force", captured.reason)
	}
	if !strings.Contains(out.String(), "event=evt-9") {
		t.Errorf("stdout missing event id: %q", out.String())
	}
}

// ---- budget -----------------------------------------------------------------

func TestBudget_DryRun_Flag(t *testing.T) {
	var captured struct {
		delta  float64
		dryRun bool
	}
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbBudgetAdd: func(req sessionctl.Request) (json.RawMessage, string, string) {
			var p struct {
				DeltaUSD float64 `json:"delta_usd"`
				DryRun   bool    `json:"dry_run"`
			}
			_ = json.Unmarshal(req.Payload, &p)
			captured.delta = p.DeltaUSD
			captured.dryRun = p.DryRun
			return json.RawMessage(`{"prev_budget":5.00,"new_budget":6.50}`), "", "evt-b"
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runBudgetCmd(
		[]string{"--ctl-dir", sess.dir, sess.sessionID, "--add", "1.5", "--dry-run"},
		&out, &errBuf,
	)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if captured.delta != 1.5 {
		t.Errorf("delta_usd=%v, want 1.5", captured.delta)
	}
	if !captured.dryRun {
		t.Errorf("dry_run=false, want true")
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("stdout should note dry-run: %q", out.String())
	}
}

func TestBudget_RequiresAddFlag(t *testing.T) {
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbBudgetAdd: func(req sessionctl.Request) (json.RawMessage, string, string) {
			t.Fatal("handler should not be called when --add is missing")
			return nil, "", ""
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runBudgetCmd(
		[]string{"--ctl-dir", sess.dir, sess.sessionID},
		&out, &errBuf,
	)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 (usage); stderr=%q", code, errBuf.String())
	}
}

// ---- pause / resume ---------------------------------------------------------

func TestPause_Success(t *testing.T) {
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbPause: func(req sessionctl.Request) (json.RawMessage, string, string) {
			return json.RawMessage(`{"paused_at":"2026-04-21T00:00:00Z"}`), "", "evt-p"
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runPauseCmd([]string{"--ctl-dir", sess.dir, sess.sessionID}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "paused at 2026-04-21T00:00:00Z") {
		t.Errorf("stdout=%q", out.String())
	}
}

func TestResume_Success(t *testing.T) {
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbResume: func(req sessionctl.Request) (json.RawMessage, string, string) {
			return json.RawMessage(`{"resumed_at":"2026-04-21T00:05:00Z"}`), "", "evt-r"
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runResumeCmd([]string{"--ctl-dir", sess.dir, sess.sessionID}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "resumed at 2026-04-21T00:05:00Z") {
		t.Errorf("stdout=%q", out.String())
	}
}

// ---- inject -----------------------------------------------------------------

func TestInject_JoinsArgs(t *testing.T) {
	var captured struct {
		text     string
		priority int
	}
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbInject: func(req sessionctl.Request) (json.RawMessage, string, string) {
			var p struct {
				Text     string `json:"text"`
				Priority int    `json:"priority"`
			}
			_ = json.Unmarshal(req.Payload, &p)
			captured.text = p.Text
			captured.priority = p.Priority
			return json.RawMessage(`{"task_id":"T99"}`), "", "evt-i"
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runInjectCmd(
		[]string{"--ctl-dir", sess.dir, sess.sessionID, "run", "these", "tests", "again"},
		&out, &errBuf,
	)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if captured.text != "run these tests again" {
		t.Errorf("text=%q, want %q", captured.text, "run these tests again")
	}
	if !strings.Contains(out.String(), "task=T99") {
		t.Errorf("stdout missing task id: %q", out.String())
	}
}

// ---- takeover ---------------------------------------------------------------

func TestTakeover_HandlerErrorRelayed(t *testing.T) {
	sess := newCtlTestSession(t, map[string]sessionctl.Handler{
		sessionctl.VerbTakeoverRequest: func(req sessionctl.Request) (json.RawMessage, string, string) {
			return nil, "takeover deferred to CDC-10", ""
		},
	})
	defer sess.Close()

	var out, errBuf bytes.Buffer
	code := runTakeoverCmd([]string{"--ctl-dir", sess.dir, sess.sessionID}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%q stderr=%q", code, out.String(), errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "CDC-10") {
		t.Errorf("stderr missing CDC-10 marker: %q", errBuf.String())
	}
}

// ---- dispatcher -------------------------------------------------------------

func TestRunCtlCmd_UnknownVerb(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runCtlCmd("frobnicate", nil, &out, &errBuf)
	if code != 2 {
		t.Errorf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown verb") {
		t.Errorf("stderr missing 'unknown verb': %q", errBuf.String())
	}
}

func TestSessionSocketPath_DefaultsToTmp(t *testing.T) {
	if got := sessionSocketPath("", "abc"); got != "/tmp/stoke-abc.sock" {
		t.Errorf("got %q, want /tmp/stoke-abc.sock", got)
	}
	if got := sessionSocketPath("/var/run", "xyz"); got != "/var/run/stoke-xyz.sock" {
		t.Errorf("got %q, want /var/run/stoke-xyz.sock", got)
	}
}

func TestUlid_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := ulid()
		if seen[id] {
			t.Fatalf("duplicate id on iter %d: %s", i, id)
		}
		seen[id] = true
	}
}
