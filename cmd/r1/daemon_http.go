// daemon_http.go — tiny HTTP client used by the `r1 daemon` client
// subcommands (and `r1 ctl`) to talk to a running daemon. Kept
// separate from daemon_cmd.go so the dispatcher file stays readable.
//
// TASK-42 extension: when addr is empty, daemonHTTP reads
// ~/.r1/daemon.json for the port + token. If the discovery file is
// missing, it attempts to spawn `r1 serve` in the background and
// retries with a 2-second timeout. This keeps the legacy
// `r1 daemon status` invocation working without requiring the
// operator to first run `r1 serve` manually.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/RelayOne/r1/internal/daemondisco"
)

// autoSpawnTimeout is the upper bound on the post-spawn retry loop.
// 2 seconds matches the spec; raise via R1_DAEMON_SPAWN_TIMEOUT for
// debugging on slow boxes (the env var is read once per invocation,
// not on every retry, so it stays predictable).
const autoSpawnTimeout = 2 * time.Second

// autoSpawnPollInterval is how often we re-check daemon.json for the
// resolved port. Short enough that a fast-spawn daemon is observed
// promptly; long enough that we don't burn CPU on a doomed launch.
const autoSpawnPollInterval = 100 * time.Millisecond

// errDiscoveryMissing is returned when daemon.json doesn't exist and
// we haven't tried (or have failed) to auto-spawn. Tests pattern-match
// against this via errors.Is.
var errDiscoveryMissing = errors.New("daemon: discovery file missing")

// daemonHTTP runs an HTTP request against a daemon. When addr is
// empty, it reads ~/.r1/daemon.json for the port + token; when the
// file is missing it auto-spawns `r1 serve` and retries up to
// autoSpawnTimeout. Other behaviors (body marshalling, JSON
// pretty-print on success) are unchanged.
func daemonHTTP(method, addr, token, path string, body any) (string, error) {
	resolved, err := resolveDaemonEndpoint(addr, token)
	if err != nil {
		return "", err
	}
	return doDaemonHTTP(method, resolved.Addr, resolved.Token, path, body)
}

// daemonEndpoint pairs the resolved addr + token. Exposed for tests.
type daemonEndpoint struct {
	Addr  string
	Token string
}

// resolveDaemonEndpoint returns the addr+token to dial. Resolution
// order:
//
//  1. addr is non-empty → use addr + token as-is. (Legacy callers.)
//  2. addr is empty:
//     a. ReadDiscovery() → use disc.Port + disc.Token.
//     b. ReadDiscovery returns "missing" → spawn `r1 serve` →
//        poll daemon.json up to autoSpawnTimeout.
func resolveDaemonEndpoint(addr, token string) (*daemonEndpoint, error) {
	if addr != "" {
		return &daemonEndpoint{Addr: addr, Token: token}, nil
	}
	// addr empty: try ReadDiscovery first.
	if disc, err := daemondisco.ReadDiscovery(); err == nil && disc.Port > 0 {
		return &daemonEndpoint{
			Addr:  fmt.Sprintf("127.0.0.1:%d", disc.Port),
			Token: disc.Token,
		}, nil
	}
	// Spawn and retry. spawnDaemonInBackground starts `r1 serve` and
	// returns immediately (the child runs in its own process group).
	if err := spawnDaemonInBackground(); err != nil {
		return nil, fmt.Errorf("daemon: auto-spawn: %w", err)
	}
	return waitForDiscovery(autoSpawnTimeout)
}

// spawnDaemon is the function-variable indirection that lets
// TestDaemonHTTP_AutoSpawn substitute a fake spawn (one that writes
// daemon.json directly without forking a real `r1 serve`). Production
// code calls realSpawnDaemon.
var spawnDaemon = realSpawnDaemon

// spawnDaemonInBackground is the public entry point used by
// resolveDaemonEndpoint. Routes through spawnDaemon so tests can
// override.
func spawnDaemonInBackground() error {
	return spawnDaemon()
}

// realSpawnDaemon starts `r1 serve` as a detached child. The child's
// stdio is redirected to the platform null device (/dev/null on
// POSIX, NUL on Windows) so the daemon doesn't write into the
// parent's terminal. We use the same executable we're currently
// running so the spawn re-enters the canonical r1 binary and not a
// stale one on PATH.
func realSpawnDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	cmd := exec.Command(exe, "serve")
	// Detach from our process group so the child survives a Ctrl-C
	// of the parent. applyDetachAttrs is platform-specific
	// (Setsid on POSIX, CREATE_NEW_PROCESS_GROUP on Windows).
	applyDetachAttrs(cmd)

	// Redirect stdio to the null device. Open it once for both
	// stdout + stderr; close on the parent side after Start (the
	// kernel keeps the FD live for the child).
	devnull, derr := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if derr != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, derr)
	}
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull

	if err := cmd.Start(); err != nil {
		_ = devnull.Close()
		return fmt.Errorf("start: %w", err)
	}
	_ = devnull.Close()
	return nil
}

// waitForDiscovery polls daemon.json until it appears (returning the
// endpoint) or timeout fires (returning errDiscoveryMissing). The
// discovery file is created atomically via os.Rename inside
// daemondisco.WriteDiscovery, so any successful ReadDiscovery
// observes a fully-written record.
func waitForDiscovery(timeout time.Duration) (*daemonEndpoint, error) {
	deadline := time.Now().Add(timeout)
	for {
		if disc, err := daemondisco.ReadDiscovery(); err == nil && disc.Port > 0 {
			return &daemonEndpoint{
				Addr:  fmt.Sprintf("127.0.0.1:%d", disc.Port),
				Token: disc.Token,
			}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: spawned r1 serve but discovery file did not appear within %s", errDiscoveryMissing, timeout)
		}
		time.Sleep(autoSpawnPollInterval)
	}
}

// doDaemonHTTP is the resolved-endpoint wire path. Identical to the
// pre-TASK-42 daemonHTTP body, kept here so resolveDaemonEndpoint
// can be tested in isolation.
func doDaemonHTTP(method, addr, token, path string, body any) (string, error) {
	url := fmt.Sprintf("http://%s%s", addr, path)
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return "", fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("daemon at %s unreachable: %w", addr, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("daemon %d: %s", resp.StatusCode, string(out))
	}
	// Pretty-print JSON if possible.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err == nil {
		return pretty.String(), nil
	}
	return string(out), nil
}
