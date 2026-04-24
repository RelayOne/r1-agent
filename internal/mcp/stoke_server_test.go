package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeProc is a test spawnFunc handle that simulates a subprocess without
// actually exec'ing anything. Callers can control when the process "exits"
// via the exitCh channel.
type fakeProc struct {
	pid      int
	exitCode int
	waitErr  error
	exitCh   chan struct{}
	killed   int32
	stdoutW  io.Writer
	stderrW  io.Writer
	args     []string
	env      []string
	workdir  string
}

func (p *fakeProc) Wait() error {
	<-p.exitCh
	if p.waitErr != nil {
		return p.waitErr
	}
	return nil
}

func (p *fakeProc) Kill() error {
	if atomic.CompareAndSwapInt32(&p.killed, 0, 1) {
		close(p.exitCh)
	}
	return nil
}

func (p *fakeProc) Pid() int { return p.pid }

// newFakeSpawner builds a spawnFunc that records what it was called with
// and returns a fakeProc the test can trigger to finish.
func newFakeSpawner(t *testing.T) (spawnFunc, *[]*fakeProc, *sync.Mutex) {
	t.Helper()
	var procs []*fakeProc
	var mu sync.Mutex
	spawner := func(bin string, args []string, workdir string, env []string, stdout, stderr io.Writer) (processHandle, error) {
		mu.Lock()
		defer mu.Unlock()
		p := &fakeProc{
			pid:     1000 + len(procs),
			exitCh:  make(chan struct{}),
			stdoutW: stdout,
			stderrW: stderr,
			args:    append([]string{bin}, args...),
			env:     env,
			workdir: workdir,
		}
		// Write a deterministic line to each stream so the log-tail path can
		// be exercised.
		fmt.Fprintf(stdout, "fake-stoke-started pid=%d\n", p.pid)
		fmt.Fprintf(stderr, "fake-stoke-stderr pid=%d\n", p.pid)
		procs = append(procs, p)
		return p, nil
	}
	return spawner, &procs, &mu
}

func TestStokeServer_BuildFromSOW_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	s := NewStokeServer("/fake/stoke")
	spawner, procs, pmu := newFakeSpawner(t)
	s.spawner = spawner

	sowJSON := `{"id":"test","name":"test","sessions":[{"id":"s1","title":"t","tasks":[{"id":"t1","description":"x"}],"acceptance_criteria":[{"id":"a1","description":"d"}]}]}`
	out, err := s.HandleToolCall("stoke_build_from_sow", map[string]interface{}{
		"repo_root":       tmp,
		"sow":             sowJSON,
		"runner":          "native",
		"native_base_url": "http://localhost:8000",
		"native_model":    "claude-sonnet-4-6",
		"env": map[string]interface{}{
			"LITELLM_API_KEY": "sk-test",
			"FOO":             "bar",
		},
	})
	if err != nil {
		t.Fatalf("build_from_sow: %v", err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	missionID, _ := resp["mission_id"].(string)
	if !strings.HasPrefix(missionID, "m-") {
		t.Fatalf("bad mission_id: %q", missionID)
	}

	// Verify SOW was persisted with a mission-scoped filename.
	sowPath, _ := resp["sow_path"].(string)
	if !strings.Contains(sowPath, missionID) {
		t.Errorf("sow_path %q does not include mission id", sowPath)
	}
	if data, err := os.ReadFile(sowPath); err != nil || !strings.Contains(string(data), `"id":"test"`) {
		t.Errorf("sow file not written correctly: err=%v data=%s", err, data)
	}

	// Verify cmd args include all the runtime flags.
	cmd, _ := resp["command"].([]interface{})
	cmdStr := fmt.Sprintf("%v", cmd)
	for _, want := range []string{"sow", "--runner", "native", "--native-base-url", "http://localhost:8000", "--native-model", "claude-sonnet-4-6"} {
		if !strings.Contains(cmdStr, want) {
			t.Errorf("command missing %q: %v", want, cmd)
		}
	}

	// Verify the process was spawned with the custom env merged in.
	pmu.Lock()
	if len(*procs) != 1 {
		pmu.Unlock()
		t.Fatalf("expected 1 proc, got %d", len(*procs))
	}
	proc := (*procs)[0]
	pmu.Unlock()
	envBlob := strings.Join(proc.env, "\x00")
	if !strings.Contains(envBlob, "LITELLM_API_KEY=sk-test") {
		t.Errorf("LITELLM_API_KEY not propagated into subprocess env")
	}
	if !strings.Contains(envBlob, "FOO=bar") {
		t.Errorf("FOO not propagated into subprocess env")
	}
	if proc.workdir != tmp {
		t.Errorf("workdir = %q want %q", proc.workdir, tmp)
	}

	// Status should be "running" before the fake proc exits.
	statusOut, err := s.HandleToolCall("stoke_get_mission_status", map[string]interface{}{"mission_id": missionID})
	if err != nil {
		t.Fatalf("get_mission_status: %v", err)
	}
	if !strings.Contains(statusOut, `"status": "running"`) {
		t.Errorf("expected running status:\n%s", statusOut)
	}

	// Logs should include the fake-proc stdout line.
	logsOut, err := s.HandleToolCall("stoke_get_mission_logs", map[string]interface{}{"mission_id": missionID, "tail_lines": float64(50)})
	if err != nil {
		t.Fatalf("get_mission_logs: %v", err)
	}
	if !strings.Contains(logsOut, "fake-stoke-started") {
		t.Errorf("stdout log should contain fake proc output:\n%s", logsOut)
	}

	// Trigger clean exit and wait for waiter goroutine
	close(proc.exitCh)
	waitForStatus(t, s, missionID, "success", 2*time.Second)

	// After completion the status should flip to success
	statusOut, _ = s.HandleToolCall("stoke_get_mission_status", map[string]interface{}{"mission_id": missionID})
	if !strings.Contains(statusOut, `"status": "success"`) {
		t.Errorf("expected success status:\n%s", statusOut)
	}
}

func TestStokeServer_YAMLDetection(t *testing.T) {
	tmp := t.TempDir()
	s := NewStokeServer("/fake/stoke")
	spawner, procs, pmu := newFakeSpawner(t)
	s.spawner = spawner

	yamlSOW := `id: test
name: test
sessions:
  - id: s1
    title: t
    tasks:
      - id: t1
        description: x
`
	out, err := s.HandleToolCall("stoke_build_from_sow", map[string]interface{}{
		"repo_root": tmp,
		"sow":       yamlSOW,
	})
	if err != nil {
		t.Fatalf("build_from_sow: %v", err)
	}
	var resp map[string]interface{}
	json.Unmarshal([]byte(out), &resp)
	sowPath, _ := resp["sow_path"].(string)
	if !strings.HasSuffix(sowPath, ".yaml") {
		t.Errorf("yaml SOW should be written to .yaml file, got %q", sowPath)
	}

	pmu.Lock()
	close((*procs)[0].exitCh)
	pmu.Unlock()
	missionID, _ := resp["mission_id"].(string)
	waitForStatus(t, s, missionID, "success", 2*time.Second)
}

func TestStokeServer_CancelMission(t *testing.T) {
	tmp := t.TempDir()
	s := NewStokeServer("/fake/stoke")
	spawner, procs, pmu := newFakeSpawner(t)
	s.spawner = spawner

	out, err := s.HandleToolCall("stoke_build_from_sow", map[string]interface{}{
		"repo_root": tmp,
		"sow":       `{"id":"test","name":"test"}`,
	})
	if err != nil {
		t.Fatalf("build_from_sow: %v", err)
	}
	var resp map[string]interface{}
	json.Unmarshal([]byte(out), &resp)
	missionID, _ := resp["mission_id"].(string)

	// Cancel the running mission
	cancelOut, err := s.HandleToolCall("stoke_cancel_mission", map[string]interface{}{
		"mission_id": missionID,
	})
	if err != nil {
		t.Fatalf("cancel_mission: %v", err)
	}
	if !strings.Contains(cancelOut, "cancelling") {
		t.Errorf("expected cancelling status:\n%s", cancelOut)
	}

	pmu.Lock()
	if atomic := (*procs)[0]; atomic != nil {
		// Kill() should have been called; it closes exitCh atomically.
	}
	pmu.Unlock()

	waitForStatus(t, s, missionID, "cancelled", 2*time.Second)

	// Cancelling a finished mission is a no-op
	out2, err := s.HandleToolCall("stoke_cancel_mission", map[string]interface{}{"mission_id": missionID})
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if !strings.Contains(out2, "already finished") {
		t.Errorf("expected already-finished message:\n%s", out2)
	}
}

func TestStokeServer_ValidationErrors(t *testing.T) {
	s := NewStokeServer("/fake/stoke")
	spawner, _, _ := newFakeSpawner(t)
	s.spawner = spawner

	cases := []struct {
		name    string
		args    map[string]interface{}
		wantErr string
	}{
		{"missing repo_root", map[string]interface{}{"sow": "{}"}, "repo_root is required"},
		{"missing sow", map[string]interface{}{"repo_root": "/tmp/test"}, "sow is required"},
		{"too short sow", map[string]interface{}{"repo_root": "/tmp/test", "sow": "x"}, "too short"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.HandleToolCall("stoke_build_from_sow", tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestStokeServer_ListMissions_Ordering(t *testing.T) {
	tmp := t.TempDir()
	s := NewStokeServer("/fake/stoke")
	spawner, procs, pmu := newFakeSpawner(t)
	s.spawner = spawner

	// Spawn three missions a few ns apart so StartedAt is distinct.
	var ids []string
	for i := 0; i < 3; i++ {
		out, err := s.HandleToolCall("stoke_build_from_sow", map[string]interface{}{
			"repo_root": filepath.Join(tmp, fmt.Sprintf("m%d", i)),
			"sow":       `{"id":"test","name":"test"}`,
		})
		if err != nil {
			t.Fatalf("build_from_sow %d: %v", i, err)
		}
		var resp map[string]interface{}
		json.Unmarshal([]byte(out), &resp)
		mid, ok := resp["mission_id"].(string)
		if !ok {
			t.Fatalf("mission_id: unexpected type: %T", resp["mission_id"])
		}
		ids = append(ids, mid)
		time.Sleep(2 * time.Millisecond) // ensure monotonic clock separates them
	}

	listOut, err := s.HandleToolCall("stoke_list_missions", map[string]interface{}{})
	if err != nil {
		t.Fatalf("list_missions: %v", err)
	}
	var list map[string]interface{}
	json.Unmarshal([]byte(listOut), &list)
	missions, _ := list["missions"].([]interface{})
	if len(missions) != 3 {
		t.Fatalf("expected 3 missions, got %d", len(missions))
	}
	// Newest first: the last id should be first in the list
	m0, ok := missions[0].(map[string]interface{})
	if !ok {
		t.Fatalf("missions[0]: unexpected type: %T", missions[0])
	}
	first := m0["mission_id"]
	if first != ids[len(ids)-1] {
		t.Errorf("expected newest-first ordering, got %v first (wanted %s)", first, ids[len(ids)-1])
	}

	// Exit all processes so the test can clean up.
	pmu.Lock()
	for _, p := range *procs {
		close(p.exitCh)
	}
	pmu.Unlock()
	for _, id := range ids {
		waitForStatus(t, s, id, "success", 2*time.Second)
	}
}

func TestStokeServer_LogTailCapping(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "log.txt")
	w, err := newCappedWriter(path, 200)
	if err != nil {
		t.Fatalf("new capped writer: %v", err)
	}
	// Write 500 bytes in chunks
	for i := 0; i < 50; i++ {
		if _, err := w.Write([]byte("0123456789")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	w.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// After truncation, the file should be at most ~half the cap plus the notice line.
	if info.Size() > 300 {
		t.Errorf("capped file too large: %d bytes", info.Size())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "truncated") {
		t.Errorf("expected truncation notice in log:\n%s", data)
	}
}

func TestStokeServer_ToolDefinitions(t *testing.T) {
	s := NewStokeServer("")
	tools := s.ToolDefinitions()
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	for _, want := range []string{
		"stoke_build_from_sow",
		"stoke_get_mission_status",
		"stoke_get_mission_logs",
		"stoke_cancel_mission",
		"stoke_list_missions",
	} {
		if !names[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
}

// TestStokeServer_RealSubprocessCancel exercises the real spawn path: it
// spawns `sleep 60` (or `cmd /c timeout 60` on Windows), then cancels via
// the MCP tool and verifies the process exits within the SIGTERM window.
// This is the only test that actually fork+exec's a child, so it's gated
// on a real shell binary being available and runs in <5s by design.
func TestStokeServer_RealSubprocessCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real subprocess test in -short mode")
	}
	// /bin/sleep is the simplest no-output long-running process. Skip the
	// test cleanly if it's missing (CI sandboxes sometimes don't have it).
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("no /bin/sleep available")
	}

	tmp := t.TempDir()
	s := NewStokeServer("/bin/sleep")
	// Override the spawner to call sleep directly with a long duration.
	// We can't go through stoke sow here because stoke isn't built into
	// the test binary; the real spawner is what we want to exercise.
	s.spawner = func(bin string, args []string, workdir string, env []string, stdout, stderr io.Writer) (processHandle, error) {
		// Use /bin/sleep 60 so the cancel path has something real to kill.
		return realSpawn("/bin/sleep", []string{"60"}, workdir, env, stdout, stderr)
	}

	out, err := s.HandleToolCall("stoke_build_from_sow", map[string]interface{}{
		"repo_root": tmp,
		"sow":       `{"id":"real","name":"real subprocess test"}`,
	})
	if err != nil {
		t.Fatalf("build_from_sow: %v", err)
	}
	var resp map[string]interface{}
	json.Unmarshal([]byte(out), &resp)
	missionID, _ := resp["mission_id"].(string)
	pid, _ := resp["pid"].(float64)
	if pid <= 0 {
		t.Fatalf("expected real pid, got %v", pid)
	}

	// Verify the process is actually alive
	if err := syscallTestProcessAlive(int(pid)); err != nil {
		t.Fatalf("subprocess not running: %v", err)
	}

	// Cancel and wait for the status flip
	cancelStart := time.Now()
	if _, err := s.HandleToolCall("stoke_cancel_mission", map[string]interface{}{"mission_id": missionID}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitForStatus(t, s, missionID, "cancelled", 5*time.Second)
	elapsed := time.Since(cancelStart)
	if elapsed > 4*time.Second {
		t.Errorf("cancel took %s — should escalate to SIGKILL within ~3s", elapsed)
	}

	// And the process should now be reaped
	if err := syscallTestProcessAlive(int(pid)); err == nil {
		t.Errorf("subprocess pid %d still alive after cancel", int(pid))
	}
}

// syscallTestProcessAlive returns nil if the process is alive (kill -0 success).
func syscallTestProcessAlive(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// On Unix, signal 0 tests existence without sending anything.
	return proc.Signal(syscall.Signal(0))
}

// waitForStatus polls the server until mission reaches the expected status
// or the deadline expires.
func waitForStatus(t *testing.T, s *StokeServer, missionID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := s.HandleToolCall("stoke_get_mission_status", map[string]interface{}{"mission_id": missionID})
		if err == nil && strings.Contains(out, fmt.Sprintf(`"status": %q`, want)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, _ := s.HandleToolCall("stoke_get_mission_status", map[string]interface{}{"mission_id": missionID})
	t.Fatalf("mission %s never reached status %q within %s:\n%s", missionID, want, timeout, out)
}
