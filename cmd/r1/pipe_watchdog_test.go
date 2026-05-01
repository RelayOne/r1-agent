package main

import (
	"bytes"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestPipeWatcher_TracksActivity verifies that a Write bumps the
// internal timestamp so SilenceDuration() goes back near zero.
func TestPipeWatcher_TracksActivity(t *testing.T) {
	var buf bytes.Buffer
	pw := newPipeWatcher(&buf)

	// Immediately after construction, silence is ~0.
	if d := pw.SilenceDuration(); d > 50*time.Millisecond {
		t.Fatalf("initial silence too large: %v", d)
	}

	// Sleep past a visible threshold, then silence must reflect it.
	time.Sleep(120 * time.Millisecond)
	if d := pw.SilenceDuration(); d < 100*time.Millisecond {
		t.Fatalf("expected silence >=100ms, got %v", d)
	}

	// A Write resets the timestamp.
	if _, err := pw.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if d := pw.SilenceDuration(); d > 50*time.Millisecond {
		t.Fatalf("silence after write should be near zero, got %v", d)
	}
	if got := buf.String(); got != "hello" {
		t.Fatalf("buffer = %q, want %q", got, "hello")
	}
}

// TestPipeWatcher_EmptyWriteDoesNotReset guards against a
// subprocess that calls Write with zero bytes masking a real hang.
func TestPipeWatcher_EmptyWriteDoesNotReset(t *testing.T) {
	var buf bytes.Buffer
	pw := newPipeWatcher(&buf)
	time.Sleep(120 * time.Millisecond)
	if _, err := pw.Write(nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	if d := pw.SilenceDuration(); d < 100*time.Millisecond {
		t.Fatalf("empty write must not reset timestamp, got silence %v", d)
	}
}

// TestKillChildProcessGroup_KillsSilentChild spawns a child that
// prints once and then sleeps forever, attaches the pipe watcher,
// and verifies that once SilenceDuration exceeds a short threshold,
// killChildProcessGroup takes the child down within ~grace period.
//
// This is the end-to-end assertion for H-4: the watchdog must kill
// the CC subprocess via its process group when stdout goes quiet
// for longer than the threshold, regardless of outer-loop writes
// to the log file.
func TestKillChildProcessGroup_KillsSilentChild(t *testing.T) {
	// bash is a hard requirement on every supported platform
	// (Linux/macOS CI); failing loudly beats silently skipping.
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatalf("bash must be on PATH for this test: %v", err)
	}
	// Print a line, then sleep 60s. With a silence threshold of
	// 300ms the watchdog should trip within ~0.6s.
	cmd := exec.Command("bash", "-c", "echo hello; sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf bytes.Buffer
	pw := newPipeWatcher(&buf)
	cmd.Stdout = pw

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	const silenceThreshold = 300 * time.Millisecond
	start := time.Now()
	deadline := time.After(5 * time.Second)
	killed := false
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

watchdogLoop:
	for {
		select {
		case <-deadline:
			t.Fatalf("watchdog never tripped; silence=%v after 5s", pw.SilenceDuration())
		case <-tick.C:
			if pw.SilenceDuration() >= silenceThreshold {
				killChildProcessGroup(cmd, 100*time.Millisecond)
				killed = true
				break watchdogLoop
			}
		}
	}
	if !killed {
		t.Fatal("killChildProcessGroup not invoked")
	}

	select {
	case <-done:
		elapsed := time.Since(start)
		// Threshold 300ms + tick 50ms + grace 100ms + Wait overhead.
		if elapsed > 2*time.Second {
			t.Errorf("child took %v to die (expected <2s)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child survived SIGKILL for >2s")
	}

	// The pre-kill output must have been captured.
	if !bytes.Contains(buf.Bytes(), []byte("hello")) {
		t.Errorf("pre-kill output missing from buffer: %q", buf.String())
	}
}

// TestKillChildProcessGroup_NoProcessIsSafe guards the nil guards.
func TestKillChildProcessGroup_NoProcessIsSafe(t *testing.T) {
	// Nil cmd must not panic.
	if got := killChildProcessGroup(nil, 10*time.Millisecond); got {
		t.Errorf("nil cmd returned true")
	}
	// Cmd that has not been started (Process == nil) must not panic.
	cmd := exec.Command("true")
	if got := killChildProcessGroup(cmd, 10*time.Millisecond); got {
		t.Errorf("unstarted cmd returned true")
	}
}
