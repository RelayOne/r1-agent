package sessionctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Call dials a sessionctl Unix socket, writes one NDJSON request, reads one
// NDJSON response, and returns. Single-shot -- no keep-alive.
//
// If the dial fails with ECONNREFUSED, the socket file is stale (server is
// gone); Call prunes it via PruneStaleSocket and returns a wrapped error
// so callers can surface a clear "session ended" message.
func Call(sock string, req Request) (Response, error) {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		if isConnRefused(err) {
			PruneStaleSocket(sock)
			return Response{}, fmt.Errorf("sessionctl: session ended (socket pruned): %w", err)
		}
		return Response{}, err
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	dec := json.NewDecoder(c)
	if err := dec.Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// DiscoverSessions globs the socket directory for "stoke-*.sock" files.
// Returns the list of socket paths. Does NOT prune or verify them --
// callers should attempt Call() and handle ECONNREFUSED.
func DiscoverSessions(ctlDir string) ([]string, error) {
	if ctlDir == "" {
		ctlDir = "/tmp"
	}
	matches, err := filepath.Glob(filepath.Join(ctlDir, "stoke-*.sock"))
	if err != nil {
		return nil, err
	}
	if matches == nil {
		return []string{}, nil
	}
	return matches, nil
}

// SessionIDFromSocket strips "stoke-" prefix and ".sock" suffix from a
// socket path to recover the session ID. Returns "" if the path
// doesn't match the expected shape.
func SessionIDFromSocket(path string) string {
	base := filepath.Base(path)
	const prefix, suffix = "stoke-", ".sock"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, suffix) {
		return ""
	}
	return base[len(prefix) : len(base)-len(suffix)]
}

// PruneStaleSocket removes path if it exists and is stale (connect
// returns ECONNREFUSED). Returns true if pruned. Silent no-op if
// connect succeeds (the socket is live) or if path does not exist.
func PruneStaleSocket(path string) bool {
	c, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		if isConnRefused(err) {
			os.Remove(path)
			return true
		}
		// ENOENT or other: nothing to prune.
		return false
	}
	c.Close()
	return false
}

// isConnRefused returns true if err's chain contains ECONNREFUSED. Falls
// back to a string-contains check for platforms where the error type
// chain doesn't expose syscall.Errno directly.
func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return strings.Contains(err.Error(), "connection refused")
}
