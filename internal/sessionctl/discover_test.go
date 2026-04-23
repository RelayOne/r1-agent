package sessionctl

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDiscoverSessions_Empty(t *testing.T) {
	dir := t.TempDir()
	got, err := DiscoverSessions(dir)
	if err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestDiscoverSessions_FindsTwo(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "stoke-a.sock")
	b := filepath.Join(dir, "stoke-b.sock")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
	}

	got, err := DiscoverSessions(dir)
	if err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}
	sort.Strings(got)
	want := []string{a, b}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("DiscoverSessions: got %v, want %v", got, want)
	}
}

func TestDiscoverSessions_IgnoresOther(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "stoke-a.sock")
	skip := filepath.Join(dir, "other.sock")
	for _, p := range []string{keep, skip} {
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
	}

	got, err := DiscoverSessions(dir)
	if err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}
	if len(got) != 1 || got[0] != keep {
		t.Errorf("DiscoverSessions: got %v, want [%s]", got, keep)
	}
}

func TestSessionIDFromSocket_HappyPath(t *testing.T) {
	got := SessionIDFromSocket("/tmp/stoke-abc123.sock")
	if got != "abc123" {
		t.Errorf("SessionIDFromSocket: got %q, want %q", got, "abc123")
	}
}

func TestSessionIDFromSocket_BadShape(t *testing.T) {
	cases := []string{
		"/tmp/foo.sock",
		"/tmp/stoke-abc.txt",
		"/tmp/abc.sock",
		"",
	}
	for _, p := range cases {
		if got := SessionIDFromSocket(p); got != "" {
			t.Errorf("SessionIDFromSocket(%q): got %q, want \"\"", p, got)
		}
	}
}

func TestPruneStaleSocket_RemovesECONNREFUSED(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stoke-stale.sock")
	// Create a plain file at the socket path -- connect attempts yield
	// ECONNREFUSED (not a listening socket).
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if !PruneStaleSocket(path) {
		t.Fatalf("PruneStaleSocket: expected true for stale socket")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be removed, stat err=%v", err)
	}
}

func TestPruneStaleSocket_KeepsLive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stoke-live.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if PruneStaleSocket(path) {
		t.Errorf("PruneStaleSocket: expected false for live socket")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("socket should still exist, stat err=%v", err)
	}
}

func TestPruneStaleSocket_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stoke-nope.sock")
	if PruneStaleSocket(path) {
		t.Errorf("PruneStaleSocket: expected false when file absent")
	}
}
