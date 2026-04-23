package sessionctl

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---- helpers --------------------------------------------------------------

// initGitRepo creates a fresh git repo with one commit in dir so rev-parse
// HEAD works. Returns the SHA of the initial commit. Git is a hard
// dependency of the stoke build toolchain, so missing-git is a fatal test
// failure rather than a soft exit.
func initGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git not on PATH: %v", err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ci", "GIT_AUTHOR_EMAIL=ci@example.com",
			"GIT_COMMITTER_NAME=ci", "GIT_COMMITTER_EMAIL=ci@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init", "-q")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(dir+"/seed.txt", []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "seed")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// ---- state machine tests ---------------------------------------------------

func TestTakeover_Request_PausesAgent(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 4242, sig, em.publish, "")

	id, ptyPath, err := tm.Request("manual", 5*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if id == "" || !strings.HasPrefix(id, "tko_") {
		t.Fatalf("takeover id: got %q, want tko_*", id)
	}
	if ptyPath == "" || !strings.Contains(ptyPath, id) {
		t.Fatalf("pty path: got %q, want contains %q", ptyPath, id)
	}
	if sig.pauseCalls != 1 || sig.pausePGID != 4242 {
		t.Fatalf("signaler: calls=%d pgid=%d, want 1/4242", sig.pauseCalls, sig.pausePGID)
	}
	calls := em.Calls()
	if len(calls) != 1 || calls[0].Kind != "operator.takeover_start" {
		t.Fatalf("emit: %+v, want single takeover_start", calls)
	}
	if a := tm.Active(); a == nil || a.ID != id {
		t.Fatalf("Active: got %+v, want id=%q", a, id)
	}
	// Cleanup: release so the timer goroutine exits promptly.
	if _, err := tm.Release(id, "user"); err != nil {
		t.Fatalf("Release cleanup: %v", err)
	}
}

func TestTakeover_Request_SecondActive_Errors(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 100, sig, em.publish, "")

	id, _, err := tm.Request("manual", 5*time.Second)
	if err != nil {
		t.Fatalf("first Request: %v", err)
	}
	_, _, err = tm.Request("manual", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("second Request err: got %v, want 'already active'", err)
	}
	if sig.pauseCalls != 1 {
		t.Fatalf("pause called %d times, want 1 (second must not re-pause)", sig.pauseCalls)
	}
	// Cleanup.
	if _, err := tm.Release(id, "user"); err != nil {
		t.Fatalf("Release cleanup: %v", err)
	}
}

func TestTakeover_Release_Resumes(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 777, sig, em.publish, "")

	id, _, err := tm.Request("manual", 5*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := tm.Release(id, "user"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if sig.resumeCall != 1 || sig.resumePGID != 777 {
		t.Fatalf("resume: calls=%d pgid=%d, want 1/777", sig.resumeCall, sig.resumePGID)
	}
	if tm.Active() != nil {
		t.Fatalf("Active after Release: got non-nil, want nil")
	}
}

func TestTakeover_Release_EmitsEvent(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 1, sig, em.publish, "")

	id, _, err := tm.Request("manual", 5*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := tm.Release(id, "user"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	calls := em.Calls()
	if len(calls) != 2 {
		t.Fatalf("emit calls: got %d, want 2 (start+end): %+v", len(calls), calls)
	}
	if calls[0].Kind != "operator.takeover_start" {
		t.Fatalf("first emit: %q, want operator.takeover_start", calls[0].Kind)
	}
	if calls[1].Kind != "operator.takeover_end" {
		t.Fatalf("second emit: %q, want operator.takeover_end", calls[1].Kind)
	}
	// Spot-check end payload fields.
	b, _ := json.Marshal(calls[1].Payload)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal end payload: %v", err)
	}
	if got["takeover_id"] != id {
		t.Fatalf("takeover_id: got %v, want %q", got["takeover_id"], id)
	}
	if got["reason"] != "user" {
		t.Fatalf("reason: got %v, want 'user'", got["reason"])
	}
	if got["actor"] != "cli:socket" {
		t.Fatalf("actor: got %v, want 'cli:socket'", got["actor"])
	}
}

func TestTakeover_Release_UnknownID_Errors(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 42, sig, em.publish, "")

	id, _, err := tm.Request("manual", 5*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := tm.Release("tko_wrong", "user"); err == nil ||
		!strings.Contains(err.Error(), "unknown takeover_id") {
		t.Fatalf("wrong-id Release err: got %v, want 'unknown takeover_id'", err)
	}
	if sig.resumeCall != 0 {
		t.Fatalf("resume called on wrong id: %d", sig.resumeCall)
	}
	// Actual release should still work.
	if _, err := tm.Release(id, "user"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// And a second release of the same id now errors (state is cleared).
	if _, err := tm.Release(id, "user"); err == nil {
		t.Fatalf("second Release of same id: got nil, want error")
	}
}

func TestTakeover_Timeout_AutoReleases(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 9, sig, em.publish, "")

	if _, _, err := tm.Request("manual", 50*time.Millisecond); err != nil {
		t.Fatalf("Request: %v", err)
	}
	// Wait well past the timeout. 400ms is generous slack for CI.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if tm.Active() == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tm.Active() != nil {
		t.Fatalf("Active after timeout: got non-nil, want nil")
	}
	if sig.resumeCall != 1 {
		t.Fatalf("resume calls after auto-release: %d, want 1", sig.resumeCall)
	}
	// Last emitted event should be takeover_end with reason=timeout.
	calls := em.Calls()
	if len(calls) < 2 {
		t.Fatalf("emits: %+v, want >=2", calls)
	}
	last := calls[len(calls)-1]
	if last.Kind != "operator.takeover_end" {
		t.Fatalf("last emit: %q, want operator.takeover_end", last.Kind)
	}
	b, _ := json.Marshal(last.Payload)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal end: %v", err)
	}
	if got["reason"] != "timeout" {
		t.Fatalf("reason on auto-release: got %v, want 'timeout'", got["reason"])
	}
}

func TestTakeover_WithGitRepo_CapturesPreSHA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sha := initGitRepo(t, dir)

	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 5, sig, em.publish, dir)

	id, _, err := tm.Request("manual", 5*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	a := tm.Active()
	if a == nil || a.PreCommit != sha {
		var pre string
		if a != nil {
			pre = a.PreCommit
		}
		t.Fatalf("PreCommit: got %q, want %q", pre, sha)
	}
	if _, err := tm.Release(id, "user"); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// ---- handler wiring -------------------------------------------------------

func TestTakeoverHandler_RequestStub_NoManager(t *testing.T) {
	t.Parallel()
	h := takeoverRequestHandler(Deps{})
	_, errMsg, _ := h(Request{Payload: json.RawMessage(`{}`)})
	if errMsg == "" || !strings.Contains(errMsg, "takeover unavailable") {
		t.Fatalf("errMsg: got %q, want contains 'takeover unavailable'", errMsg)
	}
}

func TestTakeoverHandler_ReleaseStub_NoManager(t *testing.T) {
	t.Parallel()
	h := takeoverReleaseHandler(Deps{})
	_, errMsg, _ := h(Request{Payload: json.RawMessage(`{}`)})
	if errMsg == "" || !strings.Contains(errMsg, "takeover unavailable") {
		t.Fatalf("errMsg: got %q, want contains 'takeover unavailable'", errMsg)
	}
}

func TestTakeoverHandler_Request_Success(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 11, sig, em.publish, "")
	deps := Deps{SessionID: "sess-1", Takeover: tm}

	h := takeoverRequestHandler(deps)
	payload, _ := json.Marshal(takeoverRequestPayload{Reason: "manual", MaxDurationS: 3})
	data, errMsg, evtID := h(Request{Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	// Event was emitted by the manager, not the handler; evtID is empty.
	if evtID != "" {
		t.Fatalf("evtID: got %q, want empty (handler path)", evtID)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(out["takeover_id"], "tko_") {
		t.Fatalf("takeover_id: %q", out["takeover_id"])
	}
	if !strings.Contains(out["pty_path"], out["takeover_id"]) {
		t.Fatalf("pty_path %q must contain takeover_id %q", out["pty_path"], out["takeover_id"])
	}

	// Release through the handler.
	rh := takeoverReleaseHandler(deps)
	rp, _ := json.Marshal(takeoverReleasePayload{TakeoverID: out["takeover_id"], Reason: "user"})
	_, errMsg, _ = rh(Request{Payload: rp})
	if errMsg != "" {
		t.Fatalf("release errMsg: %q", errMsg)
	}
	if tm.Active() != nil {
		t.Fatalf("Active after release: non-nil")
	}
}

func TestTakeoverHandler_Request_DefaultMaxDuration(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 11, sig, em.publish, "")
	deps := Deps{SessionID: "sess-1", Takeover: tm}

	h := takeoverRequestHandler(deps)
	// No max_duration_s -> server default (10m).
	payload, _ := json.Marshal(takeoverRequestPayload{Reason: "manual"})
	_, errMsg, _ := h(Request{Payload: payload})
	if errMsg != "" {
		t.Fatalf("errMsg: %q", errMsg)
	}
	a := tm.Active()
	if a == nil {
		t.Fatalf("Active: nil")
	}
	if a.MaxDuration != 10*time.Minute {
		t.Fatalf("MaxDuration: got %v, want 10m", a.MaxDuration)
	}
	if _, err := tm.Release(a.ID, "user"); err != nil {
		t.Fatalf("Release cleanup: %v", err)
	}
}

func TestTakeoverHandler_BadPayload_Errors(t *testing.T) {
	t.Parallel()
	sig := &fakeSignaler{}
	em := &fakeEmitter{}
	tm := NewTakeoverManager("sess-1", 11, sig, em.publish, "")
	deps := Deps{Takeover: tm}

	h := takeoverRequestHandler(deps)
	_, errMsg, _ := h(Request{Payload: json.RawMessage(`{"bogus":1}`)})
	if errMsg == "" || !strings.Contains(errMsg, "payload") {
		t.Fatalf("errMsg: got %q, want payload error", errMsg)
	}
}
