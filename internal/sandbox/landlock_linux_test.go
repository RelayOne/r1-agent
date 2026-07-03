package sandbox

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestLandlockFSMaskClamping(t *testing.T) {
	const v1Mask = uint64(0x1fff) // EXECUTE..MAKE_SYM
	cases := []struct {
		abi  int
		want uint64
	}{
		{1, v1Mask},
		{2, v1Mask | unix.LANDLOCK_ACCESS_FS_REFER},
		{3, v1Mask | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE},
		{4, v1Mask | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE},
		{5, v1Mask | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV},
		{6, v1Mask | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV},
	}
	for _, tc := range cases {
		if got := landlockFSMask(tc.abi); got != tc.want {
			t.Errorf("landlockFSMask(%d) = %#x, want %#x", tc.abi, got, tc.want)
		}
	}
}

// TestLandlockRODoesNotGrantRunWholesale pins that the read baseline does
// NOT grant all of /run — that directory holds daemon control sockets
// (/run/docker.sock, /run/containerd/*) whose reachability is a full host
// escape, and Landlock's allow-list is the only lever for this backend (the
// bwrap DenyRead socket mask does not apply). Only the narrow DNS-resolver
// runtime path is allowed.
func TestLandlockRODoesNotGrantRunWholesale(t *testing.T) {
	for _, p := range landlockROPaths {
		if p == "/run" {
			t.Fatalf("landlockROPaths must not grant /run wholesale (exposes daemon sockets); paths: %v", landlockROPaths)
		}
	}
	found := false
	for _, p := range landlockROPaths {
		if p == "/run/systemd/resolve" {
			found = true
		}
	}
	if !found {
		t.Errorf("landlockROPaths should keep the narrow DNS-resolver runtime path; paths: %v", landlockROPaths)
	}
}

func TestLandlockFileAccessSubsetOfMask(t *testing.T) {
	for abi := 1; abi <= 6; abi++ {
		fileBits := landlockFileAccess(abi)
		mask := landlockFSMask(abi)
		if fileBits&^mask != 0 {
			t.Errorf("abi %d: file access %#x not a subset of handled mask %#x", abi, fileBits, mask)
		}
		if fileBits&unix.LANDLOCK_ACCESS_FS_READ_DIR != 0 {
			t.Errorf("abi %d: file access must not contain dir-only READ_DIR", abi)
		}
	}
}
