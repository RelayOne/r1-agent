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
	// When --addr is empty (no --port either), parseServeFlags
	// returns an empty Addr. The runServeLoop applies a sane default;
	// we assert the parser doesn't reject the empty case here so the
	// downstream auto-spawn logic in TASK-42 has a clean signal.
	opts, err := parseServeFlags([]string{"--token", "tk"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Addr != "" {
		t.Errorf("empty --addr should stay empty post-parse; got %q", opts.Addr)
	}
	if opts.Token != "tk" {
		t.Errorf("Token: got %q, want tk", opts.Token)
	}
}

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
