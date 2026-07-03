package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

// probeLandlockABI returns the kernel's Landlock ABI version, or 0 when
// Landlock is unavailable (old kernel, LSM not enabled, or the syscall
// blocked by a seccomp profile — common inside containers).
func probeLandlockABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// landlockFSMask returns the filesystem access bits this kernel ABI can
// handle. Handled bits unknown to the kernel make landlock_create_ruleset
// fail with EINVAL, so the mask must be clamped to the probed ABI.
func landlockFSMask(abi int) uint64 {
	// ABI 1 baseline: execute/read/write plus all make/remove bits
	// (LANDLOCK_ACCESS_FS_EXECUTE .. LANDLOCK_ACCESS_FS_MAKE_SYM).
	m := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		m |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		m |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		m |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return m
}

// landlockFileAccess returns the subset of the fs mask that is valid on a
// PATH_BENEATH rule whose parent fd is a regular FILE — dir-only bits
// (READ_DIR, MAKE_*, REMOVE_*) on a file fd make landlock_add_rule fail
// with EINVAL.
func landlockFileAccess(abi int) uint64 {
	m := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE)
	if abi >= 3 {
		m |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		m |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return m
}

// landlockROPaths is the baseline read+execute allow-list: toolchains,
// libraries, and system config a shell command needs to run at all. $HOME
// is deliberately absent — that is where the credential stores live — with
// three narrow exceptions appended in applyLandlock (git/go read their
// per-user config on every invocation and hard-fail styles vary).
var landlockROPaths = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64",
	"/etc", "/opt", "/var", "/run", "/proc", "/sys", "/dev",
}

// landlockRWPaths is the baseline write allow-list beyond the policy's
// AllowWrite/WriteCaches: a writable TMPDIR, plus /dev/null and /dev/tty
// for the ubiquitous shell redirects (both are in the RO baseline above,
// which grants only read).
var landlockRWPaths = []string{"/tmp", "/dev/null", "/dev/tty", "/dev/shm"}

// applyLandlock builds and applies the ruleset to the CURRENT process.
// Must only be called from the __sandbox-exec helper child — after this
// returns, the restrictions are irrevocable for this process tree.
func applyLandlock(p Policy) error {
	abi := landlockABIProbe()
	if abi < 1 {
		return fmt.Errorf("landlock unavailable at apply time")
	}
	if !p.AllowEgress && abi < 4 {
		return fmt.Errorf("landlock ABI %d cannot restrict egress", abi)
	}

	attr := unix.LandlockRulesetAttr{Access_fs: landlockFSMask(abi)}
	if !p.AllowEgress && abi >= 4 {
		// Handling the net bits with zero net rules = deny all TCP
		// bind/connect. UDP/ICMP are not restrictable before ABI 7-era
		// kernels; documented narrowing.
		attr.Access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	rulesetFD := int(fd)
	defer unix.Close(rulesetFD)

	roAccess := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR)
	rwAccess := landlockFSMask(abi)

	roPaths := append([]string(nil), landlockROPaths...)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roPaths = append(roPaths,
			filepath.Join(home, ".gitconfig"),
			filepath.Join(home, ".config", "git"),
			filepath.Join(home, ".config", "go"),
		)
	}
	roPaths = append(roPaths, p.AllowRead...)
	for _, path := range roPaths {
		if err := addLandlockRule(rulesetFD, path, roAccess, abi); err != nil {
			return err
		}
	}
	rwPaths := append([]string(nil), landlockRWPaths...)
	rwPaths = append(rwPaths, p.AllowWrite...)
	rwPaths = append(rwPaths, p.WriteCaches...)
	for _, path := range rwPaths {
		if err := addLandlockRule(rulesetFD, path, rwAccess, abi); err != nil {
			return err
		}
	}

	// Required before RESTRICT_SELF for processes without CAP_SYS_ADMIN;
	// also blocks setuid/setgid re-escalation inside the sandbox.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(NO_NEW_PRIVS): %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

// addLandlockRule grants access beneath path. Nonexistent or unopenable
// paths are skipped: baseline entries like /lib32 legitimately don't exist
// everywhere, and a path that cannot be opened cannot be granted (skipping
// narrows, never widens, the sandbox).
func addLandlockRule(rulesetFD int, path string, access uint64, abi int) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= landlockFileAccess(abi)
	}
	if access == 0 {
		return nil
	}
	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(fd),
	}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_add_rule(%s): %w", path, errno)
	}
	return nil
}

// applyAndExec applies the policy to this process and replaces it with
// argv. Only returns on error.
func applyAndExec(p Policy, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty argv")
	}
	if err := applyLandlock(p); err != nil {
		return err
	}
	// LookPath after restrict_self: PATH dirs are in the RO baseline, so
	// resolution still works — and the resolved binary is guaranteed
	// executable under the ruleset we just applied.
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("lookpath %q: %w", argv[0], err)
	}
	return unix.Exec(path, argv, os.Environ())
}
