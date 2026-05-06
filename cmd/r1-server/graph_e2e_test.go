//go:build e2e

// Package main — graph_e2e_test.go
//
// Spec 2 §6 + checklist T11: Playwright + headless Chromium e2e
// rendering 3000 fixture nodes, scrolling/scrubbing/focusing for
// 5 seconds, asserting mean FPS ≥ 30 and no console errors.
//
// This test is deliberately gated behind `//go:build e2e` so the
// default `go test ./...` lane stays fast (no browser, no npm,
// no playwright install). It runs in the release-rehearsal CI
// lane only — see cloudbuild.yaml's e2e step.
//
// Mechanics:
//
//   1. Build the r1-server binary into a tmpdir.
//   2. Spawn it on an ephemeral port with a fixture session DB
//      seeded with 3000 nodes (graph_e2e_fixture.go generates the
//      seed; written alongside this file in the same lane).
//   3. Exec the Playwright runner at cmd/r1-server/e2e/graph-fps.mjs,
//      passing the server URL + session id + duration via env vars.
//   4. The runner instruments performance.now() across rAFs for
//      the duration and prints a JSON line {meanFps, p99Frame, errors}.
//   5. This Go test parses that JSON, asserts meanFps >= 30 AND
//      len(errors) == 0.
//
// Prerequisites for running locally:
//   cd web && npm i && npx playwright install chromium
//   R1_SERVER_UI_V2=1 go test -tags=e2e ./cmd/r1-server/...

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	e2eFixtureNodes = 3000
	e2eDurationSec  = 5
	e2eMinMeanFPS   = 30.0
)

// fpsResult is the JSON shape graph-fps.mjs emits to stdout. Any
// other output is captured to the test log for debugging.
type fpsResult struct {
	MeanFPS  float64  `json:"meanFps"`
	P99Frame float64  `json:"p99Frame"` // milliseconds
	Errors   []string `json:"errors"`
	Frames   int      `json:"frames"`
}

func TestGraph3kFPS(t *testing.T) {
	if os.Getenv("R1_SERVER_UI_V2") != "1" {
		t.Skip("R1_SERVER_UI_V2=1 required for v2 graph test")
	}
	runner := filepath.Join("e2e", "graph-fps.mjs")
	if _, err := os.Stat(runner); err != nil {
		t.Skipf("playwright runner missing at %s: %v", runner, err)
	}

	// Build the server binary so we test the embedded UI as it'll
	// ship — `go run` would re-compile each invocation and skips
	// the embed assertions the build pipeline would catch.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "r1-server")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build r1-server: %v", err)
	}

	// Spawn the server. Address is selected by the OS (port 0); the
	// runner reads R1_SERVER_BASE_URL from its env so dynamic ports
	// flow through cleanly.
	addr := startServer(t, bin)
	t.Logf("r1-server up at %s", addr)

	// Exec the runner. Timeout = duration + 30s slack.
	timeout := time.Duration(e2eDurationSec+30) * time.Second
	cmd := exec.Command("node", runner)
	cmd.Env = append(os.Environ(),
		"R1_SERVER_BASE_URL="+addr,
		"R1_SERVER_E2E_DURATION_SEC="+itoa(e2eDurationSec),
		"R1_SERVER_E2E_FIXTURE_NODES="+itoa(e2eFixtureNodes),
	)
	out, err := runWithTimeout(cmd, timeout)
	if err != nil {
		t.Fatalf("playwright runner failed: %v\n%s", err, out)
	}

	// Parse the LAST JSON line; the runner prints diagnostics above it.
	jsonLine := lastJSONLine(out)
	if jsonLine == "" {
		t.Fatalf("playwright runner produced no JSON line:\n%s", out)
	}
	var res fpsResult
	if err := json.Unmarshal([]byte(jsonLine), &res); err != nil {
		t.Fatalf("parse runner JSON: %v\nline: %s", err, jsonLine)
	}
	if len(res.Errors) > 0 {
		t.Errorf("console errors during e2e: %v", res.Errors)
	}
	if res.MeanFPS < e2eMinMeanFPS {
		t.Errorf("meanFps = %.2f, want >= %.2f (frames=%d, p99frame=%.2fms)",
			res.MeanFPS, e2eMinMeanFPS, res.Frames, res.P99Frame)
	} else {
		t.Logf("meanFps=%.2f frames=%d p99frame=%.2fms (PASS)",
			res.MeanFPS, res.Frames, res.P99Frame)
	}
}

func lastJSONLine(out []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	last := ""
	for scanner.Scan() {
		l := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(l, "{") && strings.HasSuffix(l, "}") {
			last = l
		}
	}
	return last
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// startServer launches r1-server on an ephemeral port and waits
// up to 10s for /healthz to come back 200. Returns the base URL.
// Stops the process via t.Cleanup.
func startServer(t *testing.T, bin string) string {
	t.Helper()
	cmd := exec.Command(bin, "--addr", "127.0.0.1:0", "--ui-v2", "1")
	cmd.Env = append(os.Environ(), "R1_SERVER_UI_V2=1")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start r1-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		procReap := cmd.Process.Wait
		_, _ = procReap()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// The server prints `listening on http://127.0.0.1:NNNN` —
		// scan stdout/stderr for the URL.
		if url := scanForListenURL(stdout.Bytes(), stderr.Bytes()); url != "" {
			return url
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("r1-server did not announce listen address within 10s\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	return ""
}

func scanForListenURL(streams ...[]byte) string {
	for _, s := range streams {
		scanner := bufio.NewScanner(bytes.NewReader(s))
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if i := strings.Index(line, "http://127.0.0.1:"); i >= 0 {
				rest := line[i:]
				end := len(rest)
				for j, c := range rest {
					if c == ' ' || c == '\t' || c == '"' {
						end = j
						break
					}
				}
				return rest[:end]
			}
		}
	}
	return ""
}

func runWithTimeout(cmd *exec.Cmd, d time.Duration) ([]byte, error) {
	out := &bytes.Buffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	cmdReap := cmd.Wait
	go func() { done <- cmdReap() }()
	select {
	case err := <-done:
		return out.Bytes(), err
	case <-time.After(d):
		_ = cmd.Process.Kill()
		<-done
		return out.Bytes(), os.ErrDeadlineExceeded
	}
}
