// Package main is the `r1` binary wrapper (work-r1-rename.md §S2-3).
//
// The Stoke rename plan ships `r1` as the canonical CLI name while
// keeping `stoke` working during the transition. To guarantee
// zero-divergence between the two names, `r1` is a thin shim that
// locates the `stoke` binary and re-executes it with the same argv,
// stdin/stdout/stderr, and environment.
//
// Resolution order for the underlying `stoke` binary:
//  1. $STOKE_BINARY env var (absolute path or PATH-resolvable name).
//  2. A file named `stoke` (or `stoke.exe` on Windows) in the same
//     directory as the running r1 binary — this is the case in
//     Homebrew + goreleaser installs where both binaries ship side
//     by side in the same bindir.
//  3. `exec.LookPath("stoke")` as a final fallback.
//
// On Unix we use syscall.Exec so the r1 process is fully replaced
// by stoke — signals, exit codes, and process semantics match the
// native `stoke` invocation exactly. On platforms without exec
// (Windows), we fall back to os/exec with signal-forwarding.
//
// NOTE: This wrapper does not have its own `--version`. Running
// `r1 --version` runs `stoke --version`, so the version string is
// identical by construction.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// stokeBinaryName returns the platform-specific filename for the
// stoke binary (e.g., "stoke" on Linux/macOS, "stoke.exe" on Windows).
func stokeBinaryName() string {
	if runtime.GOOS == "windows" {
		return "stoke.exe"
	}
	return "stoke"
}

// locateStoke resolves the absolute path to the `stoke` binary that
// this `r1` shim should delegate to. Returns the absolute path or an
// error describing every location that was checked.
func locateStoke() (string, error) {
	// 1. Explicit override via STOKE_BINARY env.
	if p := os.Getenv("STOKE_BINARY"); p != "" {
		if filepath.IsAbs(p) {
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		} else {
			if resolved, err := exec.LookPath(p); err == nil {
				return resolved, nil
			}
		}
	}

	// 2. Sibling binary in the same directory as r1 itself. This is
	//    the install.sh / Homebrew / goreleaser case: both binaries
	//    land in the same bindir.
	if self, err := os.Executable(); err == nil {
		// Resolve symlinks so r1-as-symlink-to-stoke still finds
		// a real sibling rather than dereferencing to itself.
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		sibling := filepath.Join(filepath.Dir(self), stokeBinaryName())
		if _, err := os.Stat(sibling); err == nil {
			// Guard against the pathological case where r1 IS stoke
			// (e.g. hardlink). Running the same binary twice through
			// exec still works but we prefer a clean error to an
			// infinite loop surfaced via argv[0].
			if sameInode(self, sibling) {
				return "", fmt.Errorf("r1 and stoke resolve to the same file (%s); refusing to self-exec", sibling)
			}
			return sibling, nil
		}
	}

	// 3. PATH lookup.
	if p, err := exec.LookPath("stoke"); err == nil {
		return p, nil
	}

	return "", errors.New("could not locate `stoke` binary: set $STOKE_BINARY or install stoke alongside r1 in the same directory or on $PATH")
}

// sameInode reports whether two paths refer to the same filesystem
// inode (hardlink / same file). Returns false on any stat error so
// it degrades safely.
func sameInode(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

// runStoke delegates to the stoke binary with the given argv[1:].
// On Unix, syscall.Exec replaces the r1 process; it never returns on
// success. On Windows, we fork a child, wait for it, and mirror its
// exit code.
func runStoke(stokePath string, argv []string) error {
	// Preserve argv[0] as "stoke" so stoke's own usage/help text
	// refers to itself accurately. Users see "r1 foo" invoke a
	// process whose argv[0] is "stoke"; that's expected and matches
	// the install.sh symlink behaviour.
	fullArgv := append([]string{"stoke"}, argv...)

	if runtime.GOOS != "windows" {
		// Unix: replace this process with stoke. Signals, exit code,
		// and pid semantics all transfer natively.
		if err := syscall.Exec(stokePath, fullArgv, os.Environ()); err != nil {
			return fmt.Errorf("exec %s: %w", stokePath, err)
		}
		return nil // unreachable
	}

	// Windows fallback: spawn a child and inherit exit code.
	cmd := exec.Command(stokePath, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("run %s: %w", stokePath, err)
	}
	return nil
}

func main() {
	stokePath, err := locateStoke()
	if err != nil {
		fmt.Fprintf(os.Stderr, "r1: %v\n", err)
		os.Exit(127) // command-not-found convention
	}
	if err := runStoke(stokePath, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "r1: %v\n", err)
		os.Exit(1)
	}
}
