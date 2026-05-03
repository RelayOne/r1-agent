package main

// serve_cmd_test.go — TASK-40 tests.
//
//   TestServeCmd_FlagParse                    every flag round-trips
//                                             through parseServeFlags.
//   TestServeCmd_AddrEmptySpawnsDiscovery     when --addr is empty,
//                                             portFromAddr handles
//                                             it gracefully (the
//                                             auto-spawn path itself
//                                             lives in TASK-42).
//   TestServeCmd_MutuallyExclusiveFlags       --install + --uninstall
//                                             rejected; --no-tcp +
//                                             --no-unix rejected.
//   TestServeCmd_LegacyPortFlag               --port=N populates --addr.

import (
	"fmt"
	"strings"
	"testing"
)

func TestServeCmd_FlagParse(t *testing.T) {
	args := []string{
		"--addr", "127.0.0.1:9091",
		"--token", "tk-flag",
		"--single-session",
		"--enable-agent-routes",
		"--enable-queue-routes",
		"--config", "/etc/r1.yml",
		"--repo", "/tmp/repo",
		"--data-dir", "/tmp/data",
	}
	opts, err := parseServeFlags(args)
	if err != nil {
		t.Fatalf("parseServeFlags: %v", err)
	}
	if opts.Addr != "127.0.0.1:9091" {
		t.Errorf("Addr: got %q", opts.Addr)
	}
	if opts.Token != "tk-flag" {
		t.Errorf("Token: got %q", opts.Token)
	}
	if !opts.SingleSession {
		t.Error("SingleSession: not set")
	}
	if !opts.EnableAgentRoutes {
		t.Error("EnableAgentRoutes: not set")
	}
	if !opts.EnableQueueRoutes {
		t.Error("EnableQueueRoutes: not set")
	}
	if opts.ConfigPath != "/etc/r1.yml" {
		t.Errorf("ConfigPath: got %q", opts.ConfigPath)
	}
	if opts.Repo != "/tmp/repo" {
		t.Errorf("Repo: got %q", opts.Repo)
	}
	if opts.DataDir != "/tmp/data" {
		t.Errorf("DataDir: got %q", opts.DataDir)
	}
}

func TestServeCmd_FlagParse_NoTCP(t *testing.T) {
	opts, err := parseServeFlags([]string{"--no-tcp"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.NoTCP {
		t.Error("NoTCP: not set")
	}
}

func TestServeCmd_FlagParse_NoUnix(t *testing.T) {
	opts, err := parseServeFlags([]string{"--no-unix"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.NoUnix {
		t.Error("NoUnix: not set")
	}
}

func TestServeCmd_FlagParse_Install(t *testing.T) {
	opts, err := parseServeFlags([]string{"--install"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.Install {
		t.Error("Install: not set")
	}
}

func TestServeCmd_FlagParse_Uninstall(t *testing.T) {
	opts, err := parseServeFlags([]string{"--uninstall"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.Uninstall {
		t.Error("Uninstall: not set")
	}
}

func TestServeCmd_FlagParse_Status(t *testing.T) {
	opts, err := parseServeFlags([]string{"--status"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.Status {
		t.Error("Status: not set")
	}
}

func TestServeCmd_MutuallyExclusiveFlags(t *testing.T) {
	cases := [][]string{
		{"--install", "--uninstall"},
		{"--no-tcp", "--no-unix"},
	}
	for _, args := range cases {
		_, err := parseServeFlags(args)
		if err == nil {
			t.Errorf("conflict %v: want error, got nil", args)
		}
	}
}

func TestServeCmd_LegacyPortFlag(t *testing.T) {
	// --port without --addr: should populate --addr as 127.0.0.1:N.
	opts, err := parseServeFlags([]string{"--port", "8420"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Addr != "127.0.0.1:8420" {
		t.Errorf("legacy --port: Addr=%q want 127.0.0.1:8420", opts.Addr)
	}
}

func TestServeCmd_LegacyPortFlag_AddrWins(t *testing.T) {
	// Both --port and --addr set: --addr wins.
	opts, err := parseServeFlags([]string{"--port", "8420", "--addr", "127.0.0.1:9999"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Addr != "127.0.0.1:9999" {
		t.Errorf("--addr should override --port; got %q", opts.Addr)
	}
}

func TestServeCmd_PortFromAddr(t *testing.T) {
	cases := map[string]int{
		"127.0.0.1:8420": 8420,
		"localhost:9091": 9091,
		"":               0,
		"127.0.0.1:":     0,
		"127.0.0.1":      0,
	}
	for addr, want := range cases {
		got := portFromAddr(addr)
		if got != want {
			t.Errorf("portFromAddr(%q): got %d, want %d", addr, got, want)
		}
	}
}

func TestServeCmd_AddrEmptySpawnsDiscovery(t *testing.T) {
	// Spec-named test (TASK-40 line 597): "when --addr is empty,
	// downstream callers (TASK-42 daemonHTTP) read ~/.r1/daemon.json
	// for port + token; if missing, attempt to spawn `r1 serve` and
	// retry with 2s timeout."
	//
	// This test verifies the parser-side contract that flows into
	// the auto-spawn path: an empty --addr must round-trip as empty
	// through parseServeFlags (so daemonHTTP's empty-addr branch can
	// trigger), AND the legacy --port flag must NOT silently fill
	// it in (since legacy semantics use the same empty signal for a
	// different default). The end-to-end auto-spawn is tested in
	// daemon_http_test.go::TestDaemonHTTP_AutoSpawn.
	opts, err := parseServeFlags([]string{"--token", "tk"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Addr != "" {
		t.Errorf("empty --addr should stay empty post-parse; got %q", opts.Addr)
	}
	if opts.Port != 0 {
		t.Errorf("legacy --port should default to 0 when unset; got %d", opts.Port)
	}
	if opts.Token != "tk" {
		t.Errorf("Token: got %q, want tk", opts.Token)
	}
	// Confirm the resolveDaemonEndpoint path treats empty addr as
	// "consult discovery" — wired in TASK-42 daemonHTTP. This is the
	// contractual link between serveCmd's empty-addr and TASK-42's
	// auto-spawn.
	dir := t.TempDir()
	t.Setenv("R1_HOME", dir)
	// daemon.json absent + spawn forced to fail → resolveDaemonEndpoint
	// must error (rather than silently returning a stale endpoint).
	origSpawn := spawnDaemon
	spawnDaemon = func() error { return errSpawnDisabled }
	defer func() { spawnDaemon = origSpawn }()
	_, derr := resolveDaemonEndpoint("", "")
	if derr == nil {
		t.Fatal("empty addr + missing discovery + failing spawn: want error, got nil")
	}
	if !strings.Contains(derr.Error(), "spawn") {
		t.Errorf("error should propagate spawn failure; got %v", derr)
	}
}

// errSpawnDisabled is the sentinel the AddrEmpty test feeds into
// spawnDaemon to verify the resolve path surfaces spawn failures
// rather than silently degrading.
var errSpawnDisabled = fmt.Errorf("spawn disabled by test")

func TestServeCmd_SingleSessionModeRoundtrip(t *testing.T) {
	// setSingleSessionMode + IsSingleSessionMode round-trip.
	prev := IsSingleSessionMode()
	defer setSingleSessionMode(prev)
	setSingleSessionMode(true)
	if !IsSingleSessionMode() {
		t.Error("after set(true): IsSingleSessionMode = false")
	}
	setSingleSessionMode(false)
	if IsSingleSessionMode() {
		t.Error("after set(false): IsSingleSessionMode = true")
	}
}

func TestServeCmd_UnknownFlagRejected(t *testing.T) {
	_, err := parseServeFlags([]string{"--no-such-flag"})
	if err == nil {
		t.Error("unknown flag: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-flag") {
		// The flag package's error message includes the flag name.
		// Tolerate any form so this test isn't tied to stdlib's
		// exact wording.
		t.Logf("unknown flag error: %v (acceptable)", err)
	}
}
