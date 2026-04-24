// Package runtrack is the discovery protocol between the stoke CLI
// and stoke-server. Every `stoke sow` (or other long-running) command
// writes a small manifest to the shared instances directory when it
// starts, updates a heartbeat while running, and removes the manifest
// on clean exit. stoke-server tails the instances directory to
// enumerate live + recent instances without invasive coordination.
//
// Design goals:
//   - Zero configuration: manifests live at a predictable well-known
//     path, no IPC ceremony
//   - Crash-safe: if a stoke process dies without cleanup, the stale
//     manifest simply ages out (stoke-server checks PID + heartbeat)
//   - Multiple concurrent stoke invocations from anywhere on the
//     machine coexist without lock contention — filename keyed on
//     run_id (UUID-ish) so no collisions
//   - Tiny payload: server tails the JSONL at the path referenced by
//     the manifest, doesn't duplicate the data here
package runtrack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Manifest describes a live stoke process instance. Written on start,
// updated periodically (heartbeat), removed on clean exit.
type Manifest struct {
	RunID      string `json:"run_id"`      // stable ID from engine.WorkerLogContext.RunID
	PID        int    `json:"pid"`         // OS pid
	PPID       int    `json:"ppid"`        // parent pid
	Command    string `json:"command"`     // e.g. "stoke sow"
	Args       string `json:"args"`        // full CLI line for display
	RepoRoot   string `json:"repo_root"`   // absolute path
	SOWName    string `json:"sow_name"`    // --file basename, if any
	Mode       string `json:"mode"`        // "headless" | "chat" | "interactive"
	Model      string `json:"model"`       // backing model tag
	StokeBuild string `json:"stoke_build"` // git short hash
	WorkerLogsDir string `json:"worker_logs_dir"` // absolute path to .stoke/worker-logs/
	LogFile    string `json:"log_file"`    // stoke-run.log absolute path
	StartedAt  string `json:"started_at"`  // RFC3339Nano
	Heartbeat  string `json:"heartbeat"`   // RFC3339Nano, last touched
	Host       string `json:"host"`        // os.Hostname()
	User       string `json:"user"`        // os.Getenv("USER")
}

// InstancesDir is the well-known directory where all live manifests
// live. Any process can read it; any process can write its own file.
// /tmp is intentional — world-writable, auto-cleaned on reboot.
//
// Override with STOKE_RUNTRACK_DIR for tests.
func InstancesDir() string {
	if d := os.Getenv("STOKE_RUNTRACK_DIR"); d != "" {
		return d
	}
	return "/tmp/stoke/instances"
}

// DefaultServerPort returns the TCP port stoke-server listens on.
// Override with STOKE_SERVER_PORT. Kept in runtrack so CLI and
// server share a single source of truth.
func DefaultServerPort() int {
	if v := os.Getenv("STOKE_SERVER_PORT"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return 3948
}

// Register creates the manifest file and returns a *Registration the
// caller uses to heartbeat + cleanup. Safe to call multiple times;
// subsequent calls update the existing file.
func Register(m Manifest) (*Registration, error) {
	if m.RunID == "" {
		return nil, errors.New("runtrack: Register requires non-empty RunID")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if m.StartedAt == "" {
		m.StartedAt = now
	}
	m.Heartbeat = now
	if m.Host == "" {
		m.Host, _ = os.Hostname()
	}
	if m.User == "" {
		m.User = os.Getenv("USER")
	}
	dir := InstancesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("runtrack: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, m.RunID+".json")
	if err := writeJSON(path, m); err != nil {
		return nil, err
	}
	r := &Registration{path: path, m: m}
	return r, nil
}

// Registration is an open handle on the manifest. Call Heartbeat()
// periodically and Close() at exit. Ownership is single-goroutine —
// callers that spawn background heartbeaters must serialize via the
// registration's internal mutex (we do this automatically below).
type Registration struct {
	mu   sync.Mutex
	path string
	m    Manifest
}

// Heartbeat bumps the manifest's Heartbeat timestamp. stoke-server
// uses this to distinguish live processes from ones that died without
// cleanup (plus PID liveness check as a tie-breaker).
func (r *Registration) Heartbeat() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m.Heartbeat = time.Now().UTC().Format(time.RFC3339Nano)
	return writeJSON(r.path, r.m)
}

// Close removes the manifest. Safe to call multiple times; errors
// removing a non-existent file are swallowed.
func (r *Registration) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	err := os.Remove(r.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// StartHeartbeat spawns a goroutine that calls Heartbeat() every
// interval until the returned stop function is invoked. Convenience
// for the common case.
func (r *Registration) StartHeartbeat(interval time.Duration) (stop func()) {
	if r == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = r.Heartbeat()
			}
		}
	}()
	return func() {
		close(done)
	}
}

// List returns every manifest currently present in the instances dir.
// Callers (stoke-server) combine this with PID / heartbeat checks to
// decide liveness. Errors reading individual files are logged at the
// caller — this function returns what it could read.
func List() ([]Manifest, error) {
	dir := InstancesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Manifest, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IsProcessAlive reports whether pid points at a running process.
// Linux-only best-effort via /proc; on other OS returns true (so we
// don't falsely declare instances dead).
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}
