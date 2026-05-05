// Package daemonlock implements single-instance enforcement for the
// `r1 serve` per-user daemon (specs/r1d-server.md TASK-11, Phase B).
//
// The thesis: at most one r1 serve process per user, ever. Two daemons
// would race for the same `~/.r1/sessions-index.json`, the same SQLite
// session store, and the same WS port — and silently produce
// half-replayed sessions on restart. We force fail-fast on second
// acquire by holding a flock on `~/.r1/daemon.lock` for the entire
// process lifetime.
//
// The lock is advisory (POSIX `flock(2)` / Windows `LockFileEx`), so a
// process that bypasses this package can still write the same files —
// but every well-behaved entry point goes through here. The lock file
// itself stores the daemon PID as a debugging aid; the *authoritative*
// "who am I" record is the discovery file (`daemondisco`), which the
// helpful error message also reads.
//
// Semantics:
//
//   - `Acquire()` opens `~/.r1/daemon.lock` (creating it 0600), creates
//     the parent dir at 0700 if absent, and tries an EXCLUSIVE
//     non-blocking flock.
//   - On success: writes the current PID into the lock file (truncate +
//     write — flock survives truncation on every supported OS) and
//     returns a `*Lock` that callers must `Release()` on shutdown.
//   - On contention (second acquire): reads `~/.r1/daemon.json` for the
//     existing PID + socket path and returns an error whose `Error()`
//     contains the user-facing message documented in the spec:
//
//	"daemon already running, pid=N, sock=...\n
//	 use 'r1 ctl' to talk to it."
//
// `Release()` unlocks AND closes the file. It is safe to call on a nil
// receiver (so callers can `defer lock.Release()` even when Acquire
// failed) and idempotent on repeat calls.
package daemonlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/gofrs/flock"
)

// LockFileName is the basename of the lock file under ~/.r1/.
// Exported so tests and discovery tooling can reference the same name
// without drift.
const LockFileName = "daemon.lock"

// ErrAlreadyRunning is returned by Acquire when another process holds
// the lock. Callers can `errors.Is` against it to distinguish the
// expected single-instance refusal from real I/O errors. The wrapped
// error always includes the PID + socket path of the running daemon
// (when readable from `~/.r1/daemon.json`).
var ErrAlreadyRunning = errors.New("daemon already running")

// Lock is a held single-instance lock. The zero value is invalid; use
// Acquire to construct.
type Lock struct {
	mu       sync.Mutex
	fl       *flock.Flock
	path     string
	released bool
}

// Path returns the absolute path to the lock file. Useful for logging.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release unlocks and closes the lock file. Safe on nil receivers and
// idempotent on repeat calls. Returns the first error encountered
// (unlock or close); nil on success.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.fl == nil {
		return nil
	}
	l.released = true
	// flock.Unlock() also closes the underlying file descriptor on
	// every supported OS, so we don't double-close here.
	return l.fl.Unlock()
}

// Acquire takes the per-user single-instance lock. On contention it
// returns an error wrapping ErrAlreadyRunning; the error message is
// the user-facing string the spec requires.
//
// The home dir is resolved via os.UserHomeDir(); tests inject an
// alternate root via the R1_HOME env var (consulted by `homeDir`
// below) — production code should not set R1_HOME.
func Acquire() (*Lock, error) {
	dir, err := r1Dir()
	if err != nil {
		return nil, fmt.Errorf("daemonlock: resolve home: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("daemonlock: mkdir %s: %w", dir, err)
	}
	lockPath := filepath.Join(dir, LockFileName)

	fl := flock.New(lockPath)
	got, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("daemonlock: trylock %s: %w", lockPath, err)
	}
	if !got {
		// Another process holds the lock. Read the discovery file
		// for a useful error message, then return a wrapped sentinel.
		msg := contentionMessage(dir)
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, msg)
	}

	// Persist our PID so a future second-acquirer can print it even
	// if `daemon.json` is missing (e.g. crash between flock + write).
	// Truncate + write — flock survives truncation on every supported
	// OS.
	if f, ferr := os.OpenFile(lockPath, os.O_RDWR|os.O_TRUNC, 0o600); ferr == nil {
		_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
		_ = f.Close()
	}

	return &Lock{fl: fl, path: lockPath}, nil
}

// contentionMessage builds the user-facing "already running" line.
// Reads ~/.r1/daemon.json; falls back to the lock file's PID body
// when discovery is missing or malformed (crash mid-startup).
func contentionMessage(dir string) string {
	pid, sock := readContentionDetails(dir)
	if pid == 0 {
		return "daemon already running, pid=?, sock=?\nuse 'r1 ctl' to talk to it."
	}
	if sock == "" {
		sock = "?"
	}
	return fmt.Sprintf("daemon already running, pid=%d, sock=%s\nuse 'r1 ctl' to talk to it.", pid, sock)
}

// readContentionDetails consults daemon.json first, then falls back
// to the PID body inside daemon.lock. Returns (0, "") on total
// failure.
//
// We read daemon.json *directly* rather than delegating to the
// `daemondisco` package to avoid an import cycle and to keep this
// package buildable as a standalone unit. The on-disk schema is owned
// by `daemondisco`; this lock only cares about the PID and SockPath
// fields, which are stable per the spec.
func readContentionDetails(dir string) (pid int, sock string) {
	if pid, sock = readDiscoveryFile(filepath.Join(dir, "daemon.json")); pid != 0 {
		return pid, sock
	}
	body, err := os.ReadFile(filepath.Join(dir, LockFileName))
	if err != nil {
		return 0, ""
	}
	n, err := strconv.Atoi(string(body))
	if err != nil {
		return 0, ""
	}
	return n, ""
}

// discoveryShape mirrors the on-disk schema owned by `daemondisco`.
// Only the fields this package needs are listed; extra JSON fields are
// ignored by the decoder.
type discoveryShape struct {
	PID      int    `json:"pid"`
	SockPath string `json:"sock_path"`
}

// readDiscoveryFile parses ~/.r1/daemon.json without enforcing the
// 0600 mode check (that check belongs to the `daemondisco` reader,
// which is the canonical entry point for downstream code). For the
// contention message we accept whatever's on disk — the goal is a
// *helpful* error, not a security boundary.
func readDiscoveryFile(path string) (pid int, sock string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, ""
	}
	var d discoveryShape
	if err := json.Unmarshal(body, &d); err != nil {
		return 0, ""
	}
	return d.PID, d.SockPath
}

// r1Dir resolves `~/.r1/` (or the test override under $R1_HOME).
func r1Dir() (string, error) {
	if home := os.Getenv("R1_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".r1"), nil
}
