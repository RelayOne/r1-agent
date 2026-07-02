package engine

import (
	"context"
	"os/exec"
	"time"

	"github.com/RelayOne/r1/internal/procutil"
)

// setupGroupLifecycle wires group-wide cancellation for a runner command.
// exec's default cmd.Cancel signals only the group leader, so cancelling
// the run ctx (scheduler cancel, boulder execCancel, Ctrl-C) orphaned
// node/bash/MCP children in the new pgid — they kept writing into the
// worktree after the run was dead. Design decision 7 promises group
// SIGTERM→SIGKILL semantics on every teardown path, not just the
// post-result timeout.
//
//   - Setpgid isolation (leader pid == pgid)
//   - ctx cancel → SIGTERM to the whole group (graceful)
//   - WaitDelay bounds Wait if the group ignores SIGTERM
//
// Callers must invoke reapGroupOnCancel after cmd.Wait returns to
// guarantee no survivors when the context was cancelled.
func setupGroupLifecycle(cmd *exec.Cmd) {
	procutil.ConfigureProcessGroup(cmd)
	cmd.Cancel = func() error { return procutil.Terminate(cmd) }
	cmd.WaitDelay = 10 * time.Second
}

// reapGroupOnCancel hard-kills the process group after Wait has returned
// on a cancelled context. procutil.Kill cannot be used here: it resolves
// the pgid via Getpgid on the (already reaped) leader. With Setpgid the
// leader pid IS the pgid, so KillGroup works even post-reap.
func reapGroupOnCancel(ctx context.Context, leaderPid int) {
	if ctx.Err() == nil {
		return
	}
	_ = procutil.KillGroup(leaderPid)
}
