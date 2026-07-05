//go:build !windows

package engine

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/procutil"
)

// TestGroupCancelKillsGrandchildren reproduces audit A002: cancelling the
// run ctx must tear down the WHOLE process group, not just the leader.
// Before setupGroupLifecycle, exec's default cmd.Cancel signalled only the
// sh leader and the grandchild sleep survived as an orphan.
func TestGroupCancelKillsGrandchildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// sh spawns a background grandchild in the same pgid, prints a marker,
	// then blocks. SIGTERM to the group must take out both.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 300 & echo started; wait")
	setupGroupLifecycle(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	leader := cmd.Process.Pid

	// Ensure the grandchild exists before cancelling.
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatalf("reading start marker: %v (line %q)", err, line)
	}

	cancel()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Wait hung after ctx cancel despite WaitDelay")
	}
	reapGroupOnCancel(ctx, leader)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// GroupAlive is a kill(-pgid, 0) existence probe, which still
		// succeeds for a KILLED-but-unreaped grandchild (a zombie). When
		// this test's PID-1 ancestor does not reap orphans — e.g. CI runs
		// `go test` directly as PID 1, no init/shell reaper — the SIGKILL'd
		// sleep lingers as a zombie forever and GroupAlive never clears.
		// The test's intent is that the grandchild is DEAD, not that it was
		// reaped (reaping needs a subreaper we don't control), so accept a
		// group whose only survivors are zombies.
		if !groupHasLiveProcess(leader) {
			return // grandchild dead (killed or reaped), not orphaned+running
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = procutil.KillGroup(leader) // don't leak the sleep on failure
	t.Fatal("process group still alive after ctx cancel — grandchild orphaned (a live, non-zombie process remains)")
}

// groupHasLiveProcess reports whether any RUNNABLE (non-zombie) process
// remains in the process group led by pgid. It scans /proc so it can tell a
// killed-but-unreaped zombie (state Z) apart from a still-running process —
// a distinction kill(-pgid, 0) cannot make. Used to assert a grandchild was
// actually killed even where nothing reaps the resulting zombie.
func groupHasLiveProcess(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// No /proc (non-Linux): fall back to the coarse existence probe.
		return procutil.GroupAlive(pgid)
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // process exited between readdir and read
		}
		// /proc/<pid>/stat: "pid (comm) state ppid pgrp ...". comm can
		// contain spaces/parens, so index from the LAST ')'.
		s := string(data)
		rp := strings.LastIndexByte(s, ')')
		if rp < 0 || rp+2 >= len(s) {
			continue
		}
		fields := strings.Fields(s[rp+2:]) // [state, ppid, pgrp, ...]
		if len(fields) < 3 {
			continue
		}
		state := fields[0]
		pgrp, err := strconv.Atoi(fields[2])
		if err != nil || pgrp != pgid {
			continue
		}
		if state != "Z" && state != "X" && state != "x" {
			return true // a live, non-zombie group member
		}
		_ = pid
	}
	return false
}
