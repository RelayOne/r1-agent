// Package main — datadir.go
//
// XDG-aware global data directory resolution for r1-server.
// r1-server stores its SQLite database and structured logs under
// a per-machine, per-user data dir so multiple repositories share
// one visualizer instance. Overridable via R1_DATA_DIR for tests
// and containerized deployments.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// globalDataDir returns the OS-appropriate application data dir for
// r1-server. Precedence:
//
//  1. R1_DATA_DIR env var (explicit override, used by tests)
//  2. Platform-native location:
//     - darwin:   ~/Library/Application Support/r1
//     - linux:    ${XDG_DATA_HOME:-~/.local/share}/r1
//     - windows:  %APPDATA%/r1
//     - other:    ~/.r1
//
// The function does not create the directory — callers should
// os.MkdirAll on first use so permission errors surface with
// context.
func globalDataDir() (string, error) {
	if v := os.Getenv("R1_DATA_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "r1"), nil
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "r1"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "r1"), nil
	case "linux":
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "r1"), nil
		}
		return filepath.Join(home, ".local", "share", "r1"), nil
	default:
		return filepath.Join(home, ".r1"), nil
	}
}

// ensureDataDir resolves and creates the data dir, returning its
// absolute path. Returns an error if creation fails.
func ensureDataDir() (string, error) {
	dir, err := globalDataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir data dir %s: %w", dir, err)
	}
	return dir, nil
}
