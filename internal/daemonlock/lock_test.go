package daemonlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempHome points the package's R1_HOME override at a tmp dir for
// the duration of t. Restores any prior value on cleanup.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, hadPrev := os.LookupEnv("R1_HOME")
	if err := os.Setenv("R1_HOME", dir); err != nil {
		t.Fatalf("setenv R1_HOME: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("R1_HOME", prev)
		} else {
			_ = os.Unsetenv("R1_HOME")
		}
	})
	return dir
}

func TestLock_AcquiresFresh(t *testing.T) {
	home := withTempHome(t)

	lk, err := Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lk == nil {
		t.Fatal("Acquire returned nil lock without error")
	}
	defer lk.Release()

	want := filepath.Join(home, LockFileName)
	if got := lk.Path(); got != want {
		t.Errorf("lock path = %q, want %q", got, want)
	}

	// File should exist and be 0600 (owner-only on POSIX).
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat lockfile: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		// Some Windows filesystems report wider perms; only assert
		// strict on POSIX.
		if !isWindows() && mode != 0o600 {
			t.Errorf("lockfile mode = %v, want 0600", mode)
		}
	}

	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if len(body) == 0 {
		t.Errorf("lockfile body empty; expected pid")
	}
}

func TestLock_RefusesSecondAcquire(t *testing.T) {
	home := withTempHome(t)

	lk1, err := Acquire()
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer lk1.Release()

	// Pre-populate daemon.json so the contention message has good
	// fields. (In production the daemon writes this after Acquire.)
	disc := []byte(`{"pid":4242,"sock_path":"/tmp/r1.sock","port":9090,"token":"x","version":"v0"}`)
	if err := os.WriteFile(filepath.Join(home, "daemon.json"), disc, 0o600); err != nil {
		t.Fatalf("seed daemon.json: %v", err)
	}

	lk2, err := Acquire()
	if err == nil {
		lk2.Release()
		t.Fatal("second Acquire succeeded; want ErrAlreadyRunning")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire err = %v, want ErrAlreadyRunning", err)
	}
	msg := err.Error()
	for _, want := range []string{"daemon already running", "pid=4242", "/tmp/r1.sock", "use 'r1 ctl' to talk to it."} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

func TestLock_ReleaseIdempotent(t *testing.T) {
	withTempHome(t)
	lk, err := Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lk.Release(); err != nil {
		t.Errorf("first Release: %v", err)
	}
	if err := lk.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}
	// Nil receiver also OK.
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Errorf("nil Release: %v", err)
	}
}

// isWindows reports whether we're running on Windows. Defined as a
// helper rather than inlining runtime.GOOS so the test file imports
// stay small and the lint stays clean.
func isWindows() bool {
	return os.PathSeparator == '\\' && os.PathListSeparator == ';'
}
