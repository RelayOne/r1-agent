package sessionctl

import (
	"runtime"
	"strings"
	"testing"
)

// TestSunPathMax pins the platform-aware sun_path limit: 107 usable bytes on
// Linux (sun_path 108 incl NUL), 103 on Darwin/BSD (sun_path 104 incl NUL).
func TestSunPathMax(t *testing.T) {
	got := sunPathMax()
	want := 107
	if runtime.GOOS == "darwin" {
		want = 103
	}
	if got != want {
		t.Fatalf("sunPathMax() = %d, want %d on %s", got, want, runtime.GOOS)
	}
}

// TestStartServerRejectsOverlongPath asserts the rejection threshold: a path of
// length (sunPathMax+1) is rejected by StartServer, while shorter paths pass the
// length guard. We drive StartServer via a SessionID padded so the joined
// socket path lands exactly one byte over the limit.
func TestStartServerRejectsOverlongPath(t *testing.T) {
	max := sunPathMax()

	// socketPath joins dir + "/stoke-" + sessionID + ".sock". Build a sessionID
	// long enough that the full path is sunPathMax+1 bytes, forcing rejection
	// regardless of the bind succeeding.
	dir := "/tmp"
	fixed := len(socketPath(dir, "")) // length of all the non-sessionID parts
	overLen := (max + 1) - fixed
	if overLen <= 0 {
		t.Skipf("fixed path overhead %d already exceeds limit %d", fixed, max)
	}
	sessionID := strings.Repeat("a", overLen)

	path := socketPath(dir, sessionID)
	if len(path) != max+1 {
		t.Fatalf("constructed path length = %d, want %d", len(path), max+1)
	}

	_, err := StartServer(Opts{SocketDir: dir, SessionID: sessionID})
	if err == nil {
		t.Fatalf("StartServer accepted over-long path (%d bytes > %d limit)", len(path), max)
	}
	if !strings.Contains(err.Error(), "sun_path limit") {
		t.Fatalf("error did not name the limit: %v", err)
	}
}

// TestStartServerAcceptsLinux104 asserts that a 104-byte path -- which the old
// hard `>= 104` guard wrongly rejected -- is now ACCEPTED on Linux, where
// sun_path is 108 bytes and 104 binds fine. We assert the length-check boundary
// only: a 104-byte path must not trip the sun_path guard.
func TestStartServerAcceptsLinux104(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("104-byte acceptance is Linux-specific; GOOS=%s", runtime.GOOS)
	}

	dir := "/tmp"
	fixed := len(socketPath(dir, ""))
	want := 104
	idLen := want - fixed
	if idLen <= 0 {
		t.Skipf("fixed path overhead %d already >= %d", fixed, want)
	}
	sessionID := strings.Repeat("a", idLen)

	path := socketPath(dir, sessionID)
	if len(path) != want {
		t.Fatalf("constructed path length = %d, want %d", len(path), want)
	}

	srv, err := StartServer(Opts{SocketDir: dir, SessionID: sessionID})
	if err != nil {
		t.Fatalf("StartServer rejected 104-byte path on Linux (regression): %v", err)
	}
	if srv != nil {
		_ = srv.Close()
	}
}
