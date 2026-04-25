package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/hitl"
	"github.com/RelayOne/r1-agent/internal/streamjson"
)

// TestDispatchCloudSwarmSOWParsesAndAnnounces verifies the real SOW
// wire-through (spec-2 item 9 end-to-end). A minimal JSON fixture is
// loaded, and the resulting NDJSON stream carries plan.ready with
// real session_ids + task_count, then one stoke.session.start +
// stoke.task.start + stoke.session.end per session.
func TestDispatchCloudSwarmSOWParsesAndAnnounces(t *testing.T) {
	var buf bytes.Buffer
	emitter := streamjson.NewTwoLane(&buf, true)
	svc := hitl.New(emitter, strings.NewReader(""), time.Second)

	code := dispatchCloudSwarmSOW(
		t.Context(),
		"testdata/min_sow.json",
		"https://example.com/repo.git",
		"main",
		"claude-sonnet-4-6",
		"community",
		emitter,
		svc,
	)
	emitter.Drain(time.Second)

	if code != ExitPass {
		t.Errorf("exit=%d, want %d", code, ExitPass)
	}
	out := buf.String()
	// plan.ready must carry the parsed session_count.
	if !strings.Contains(out, `"_stoke.dev/session_count":2`) {
		t.Errorf("plan.ready missing session_count=2: %q", out)
	}
	if !strings.Contains(out, `"_stoke.dev/task_count":2`) {
		t.Errorf("plan.ready missing task_count=2: %q", out)
	}
	// Both sessions announced.
	for _, sid := range []string{"S1", "S2"} {
		if !strings.Contains(out, `"_stoke.dev/session":"`+sid+`"`) {
			t.Errorf("expected session id %s in output: %q", sid, out)
		}
	}
	// Per-task start announced for both tasks.
	for _, tid := range []string{"S1-T1", "S2-T1"} {
		if !strings.Contains(out, `"_stoke.dev/task_id":"`+tid+`"`) {
			t.Errorf("expected task id %s in output: %q", tid, out)
		}
	}
	// stoke.session.end present for both.
	endCount := strings.Count(out, `"subtype":"stoke.session.end"`)
	if endCount != 2 {
		t.Errorf("stoke.session.end count=%d, want 2: %q", endCount, out)
	}
}

// TestDispatchCloudSwarmSOWMalformedReturns2 verifies a SOW path that
// exists but contains invalid JSON returns exit code 2 and emits a
// sow_parse error event (not sow_missing).
func TestDispatchCloudSwarmSOWMalformedReturns2(t *testing.T) {
	// Write a bad SOW to a temp file.
	dir := t.TempDir()
	bad := dir + "/bad.json"
	if err := os.WriteFile(bad, []byte("{not json}"), 0o600); err != nil {
		t.Fatalf("write bad sow: %v", err)
	}
	var buf bytes.Buffer
	emitter := streamjson.NewTwoLane(&buf, true)
	svc := hitl.New(emitter, strings.NewReader(""), time.Second)
	code := dispatchCloudSwarmSOW(t.Context(), bad, "", "", "", "community", emitter, svc)
	emitter.Drain(time.Second)
	if code != ExitBudgetOrUsage {
		t.Errorf("malformed SOW exit=%d, want %d", code, ExitBudgetOrUsage)
	}
	if !strings.Contains(buf.String(), `"_stoke.dev/kind":"sow_parse"`) {
		t.Errorf("expected sow_parse error in output: %q", buf.String())
	}
}

// TestRunCommandSOWEndToEndEmitsCompletSuccess wires the full
// runCommandExitCode entry with a real fixture and verifies the
// terminal complete line carries subtype=success.
func TestRunCommandSOWEndToEndEmitsCompletSuccess(t *testing.T) {
	// Redirect stdout so we can capture the stream.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	done := make(chan int, 1)
	go func() {
		done <- runCommandExitCode([]string{
			"--output", "stream-json",
			"--sow", "testdata/min_sow.json",
		})
	}()
	code := <-done
	_ = w.Close()
	var captured bytes.Buffer
	_, _ = io.Copy(&captured, r)
	os.Stdout = origStdout

	if code != ExitPass {
		t.Errorf("exit code=%d, want %d", code, ExitPass)
	}
	// Final line must be "complete" with subtype success.
	lines := nonEmptyLines(captured.Bytes())
	if len(lines) == 0 {
		t.Fatalf("no output captured")
	}
	final := lines[len(lines)-1]
	var m map[string]any
	if err := json.Unmarshal([]byte(final), &m); err != nil {
		t.Fatalf("unmarshal final line: %v line=%q", err, final)
	}
	if m["type"] != "complete" {
		t.Errorf("final type=%v, want complete; final=%q", m["type"], final)
	}
	if m["subtype"] != "success" {
		t.Errorf("final subtype=%v, want success", m["subtype"])
	}
}

func nonEmptyLines(b []byte) []string {
	var out []string
	var cur []byte
	for _, c := range b {
		if c == '\n' {
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
			}
			continue
		}
		cur = append(cur, c)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}
