package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/r1env"
	"github.com/RelayOne/r1/internal/runtrack"
)

// ensureStokeServer does a best-effort check that stoke-server is
// listening on port 3948. If not, it tries to spawn one in the
// background via `nohup stoke-server &`. Non-fatal: if launch fails,
// a stoke invocation still works — the dashboard just won't be
// available. Set STOKE_NO_SERVER=1 to skip entirely.
func ensureStokeServer() {
	if r1env.Get("R1_NO_SERVER", "STOKE_NO_SERVER") == "1" {
		return
	}
	if serverAlive(runtrack.DefaultServerPort()) {
		return
	}
	// Locate the stoke-server binary next to our own executable, or
	// fall back to $PATH. If neither, silently skip.
	bin := findStokeServer()
	if bin == "" {
		return
	}
	cmd := exec.Command(bin) // #nosec G204 -- Stoke self-invocation or dev-tool binary with Stoke-generated args.
	// Detach: own process group so SIGINT to stoke doesn't kill it.
	setSidOnCmd(cmd)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return
	}
	// Don't wait — let the server keep running after stoke exits.
	_ = cmd.Process.Release()
	// Give it a moment to bind before callers expect it.
	for i := 0; i < 20; i++ {
		if serverAlive(runtrack.DefaultServerPort()) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// serverAlive reports whether something is listening on :<port>.
func serverAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// findStokeServer locates the stoke-server binary. Priority:
//  1. Sibling of our own executable (same dir)
//  2. $PATH lookup
//  3. Hard-coded /home/eric/repos/r1-agent/stoke-server for dev
func findStokeServer() string {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "stoke-server")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if p, err := exec.LookPath("stoke-server"); err == nil {
		return p
	}
	cand := "/home/eric/repos/r1-agent/stoke-server"
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return ""
}

// registerStokeInstance writes a runtrack manifest for the currently
// running stoke invocation, starts a heartbeat goroutine, and returns
// the cleanup stop function. Call at the start of `r1 sow` (or
// other long-running command). Safe to call with an empty runID —
// in that case it's a no-op.
func registerStokeInstance(
	runID string,
	command string,
	args []string,
	repoRoot string,
	sowName string,
	mode string,
	model string,
	stokeBuild string,
	workerLogsDir string,
	logFile string,
) (stop func()) {
	if runID == "" {
		return func() {}
	}
	m := runtrack.Manifest{
		RunID:         runID,
		PID:           os.Getpid(),
		PPID:          os.Getppid(),
		Command:       command,
		Args:          strings.Join(args, " "),
		RepoRoot:      repoRoot,
		SOWName:       sowName,
		Mode:          mode,
		Model:         model,
		StokeBuild:    stokeBuild,
		WorkerLogsDir: workerLogsDir,
		LogFile:       logFile,
	}
	reg, err := runtrack.Register(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ runtrack.Register: %v (continuing without dashboard)\n", err)
		return func() {}
	}
	stopHB := reg.StartHeartbeat(5 * time.Second)
	return func() {
		stopHB()
		_ = reg.Close()
	}
}
