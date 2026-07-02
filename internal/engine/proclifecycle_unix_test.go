//go:build !windows

package engine

import (
	"bufio"
	"context"
	"os/exec"
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
		if !procutil.GroupAlive(leader) {
			return // whole group gone, grandchild included
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = procutil.KillGroup(leader) // don't leak the sleep on failure
	t.Fatal("process group still alive after ctx cancel — grandchild orphaned")
}
