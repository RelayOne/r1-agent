package daemondisco

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withTempHome points the package's R1_HOME override at a tmp dir.
// Restores any prior value on cleanup.
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

func TestDiscovery_RoundTrip(t *testing.T) {
	home := withTempHome(t)

	wantPID := 4242
	wantSock := "/run/user/1000/r1/r1.sock"
	wantPort := 50123
	wantTok := "deadbeefcafef00d"
	wantVer := "r1-test-v0"

	path, err := WriteDiscovery(wantPID, wantSock, wantPort, wantTok, wantVer)
	if err != nil {
		t.Fatalf("WriteDiscovery: %v", err)
	}
	if want := filepath.Join(home, FileName); path != want {
		t.Errorf("WriteDiscovery returned %q, want %q", path, want)
	}

	got, err := ReadDiscovery()
	if err != nil {
		t.Fatalf("ReadDiscovery: %v", err)
	}
	if got.PID != wantPID {
		t.Errorf("PID = %d, want %d", got.PID, wantPID)
	}
	if got.SockPath != wantSock {
		t.Errorf("SockPath = %q, want %q", got.SockPath, wantSock)
	}
	if got.Port != wantPort {
		t.Errorf("Port = %d, want %d", got.Port, wantPort)
	}
	if got.Token != wantTok {
		t.Errorf("Token = %q, want %q", got.Token, wantTok)
	}
	if got.Version != wantVer {
		t.Errorf("Version = %q, want %q", got.Version, wantVer)
	}
}

func TestDiscovery_WritesMode0600(t *testing.T) {
	if isWindows() {
		t.Skip("file-mode bits are not load-bearing on Windows")
	}
	home := withTempHome(t)
	if _, err := WriteDiscovery(1, "/x", 1, "tok", "v"); err != nil {
		t.Fatalf("WriteDiscovery: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, FileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %v, want 0600", mode)
	}
}

func TestDiscovery_RefusesMode0644(t *testing.T) {
	if isWindows() {
		t.Skip("file-mode-based mode rejection is POSIX-only")
	}
	home := withTempHome(t)
	if _, err := WriteDiscovery(1, "/x", 1, "tok", "v"); err != nil {
		t.Fatalf("WriteDiscovery: %v", err)
	}
	// Loosen the mode out-of-band (simulating a misconfigured
	// umask or a hand-edited file).
	path := filepath.Join(home, FileName)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod 0644: %v", err)
	}
	got, err := ReadDiscovery()
	if err == nil {
		t.Fatalf("ReadDiscovery succeeded with mode 0644: got=%+v", got)
	}
	if !errors.Is(err, ErrUnsafeMode) {
		t.Errorf("ReadDiscovery err = %v, want ErrUnsafeMode", err)
	}
}

func TestDiscovery_AtomicWrite(t *testing.T) {
	// After WriteDiscovery, the .tmp file must be gone (rename
	// atomized it onto the final path).
	home := withTempHome(t)
	if _, err := WriteDiscovery(1, "/x", 1, "tok", "v"); err != nil {
		t.Fatalf("WriteDiscovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, FileName+".tmp")); !os.IsNotExist(err) {
		t.Errorf(".tmp leftover after WriteDiscovery: stat err = %v", err)
	}
}

func TestDiscovery_OverwritesExisting(t *testing.T) {
	withTempHome(t)
	if _, err := WriteDiscovery(1, "/old", 1, "old", "v0"); err != nil {
		t.Fatalf("first WriteDiscovery: %v", err)
	}
	if _, err := WriteDiscovery(2, "/new", 2, "new", "v1"); err != nil {
		t.Fatalf("second WriteDiscovery: %v", err)
	}
	got, err := ReadDiscovery()
	if err != nil {
		t.Fatalf("ReadDiscovery: %v", err)
	}
	if got.PID != 2 || got.SockPath != "/new" {
		t.Errorf("after overwrite: %+v, want PID=2 SockPath=/new", got)
	}
}
