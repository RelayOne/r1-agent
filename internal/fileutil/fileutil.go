// Package fileutil provides shared file system operations with proper error
// handling, permission constants, and path safety checks. Centralizes the
// os.MkdirAll/WriteFile patterns used throughout the codebase.
package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Standard file permission constants used across the codebase.
const (
	DirPerms        os.FileMode = 0o755 // Standard directory permissions.
	PrivateDirPerms os.FileMode = 0o700 // Private directory (credentials, session data).
	FilePerms       os.FileMode = 0o644 // Standard file permissions.
	PrivatePerms    os.FileMode = 0o600 // Private file (credentials).
)

// EnsureDir creates a directory (and parents) with the given permissions.
// Returns a descriptive error on failure.
func EnsureDir(path string, perm os.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	return nil
}

// EnsureRuntimeDir creates a runtime directory with standard permissions (0755).
// Used for harness-owned directories outside the worktree.
func EnsureRuntimeDir(path string) error {
	return EnsureDir(path, DirPerms)
}

// EnsurePrivateDir creates a directory with restricted permissions (0700).
// Used for session data, credentials, and other sensitive directories.
func EnsurePrivateDir(path string) error {
	return EnsureDir(path, PrivateDirPerms)
}

// WriteFileAtomic writes data to a file atomically (write to .tmp, then rename).
// Prevents partial writes from corrupting data on crash.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := EnsureDir(filepath.Dir(path), DirPerms); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // clean up on rename failure
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// FileExists returns true if the path exists and is a regular file.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// DirExists returns true if the path exists and is a directory.
func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// SafePath validates that name does not escape root via path traversal or symlinks.
// Returns the absolute resolved path on success.
func SafePath(root, name string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	target := filepath.Join(absRoot, name)
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve target: %w", err)
	}
	if !strings.HasPrefix(abs, absRoot+string(filepath.Separator)) && abs != absRoot {
		return "", fmt.Errorf("path traversal rejected: %q escapes %q", name, root)
	}
	return abs, nil
}
