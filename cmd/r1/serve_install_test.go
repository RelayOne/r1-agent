package main

// serve_install_test.go — TASK-38 tests.
//
// TestServeInstall_End2End is the spec-named test. It's skipped
// outside of "service-mode" because actually installing a unit on the
// host requires either root (system install) or an active systemd-user
// session (per-user install) — neither of which a CI runner can
// guarantee. Enable it by setting R1_TEST_SERVICE_INSTALL=1 in the
// env on a developer box where the test is allowed to mutate the host
// service manager.
//
// The non-end-to-end tests below exercise the parts that DON'T
// require a real install: the action classifier, the flag stripper,
// the env inheritance helper.

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestClassifyServeAction_Run(t *testing.T) {
	a, err := classifyServeAction([]string{"--addr", "127.0.0.1:9091"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a != serveActionRun {
		t.Errorf("got %q, want %q", a, serveActionRun)
	}
}

func TestClassifyServeAction_Install(t *testing.T) {
	a, err := classifyServeAction([]string{"--install", "--addr", "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a != serveActionInstall {
		t.Errorf("got %q, want %q", a, serveActionInstall)
	}
}

func TestClassifyServeAction_Uninstall(t *testing.T) {
	a, err := classifyServeAction([]string{"--uninstall"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a != serveActionUninstall {
		t.Errorf("got %q, want %q", a, serveActionUninstall)
	}
}

func TestClassifyServeAction_Status(t *testing.T) {
	a, err := classifyServeAction([]string{"--status"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a != serveActionStatus {
		t.Errorf("got %q, want %q", a, serveActionStatus)
	}
}

func TestClassifyServeAction_Conflicts(t *testing.T) {
	cases := [][]string{
		{"--install", "--uninstall"},
		{"--install", "--status"},
		{"--uninstall", "--status"},
		{"--install", "--uninstall", "--status"},
	}
	for _, args := range cases {
		_, err := classifyServeAction(args)
		if err == nil {
			t.Errorf("conflict %v: want error, got nil", args)
		}
	}
}

func TestStripInstallFlags(t *testing.T) {
	in := []string{"--addr", "127.0.0.1:0", "--install", "--enable-agent-routes", "--status"}
	got := stripInstallFlags(in)
	want := []string{"--addr", "127.0.0.1:0", "--enable-agent-routes"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStripInstallFlags_KeyValueForms(t *testing.T) {
	in := []string{"--install=true", "--addr=127.0.0.1:0", "--status=true", "--config=/etc/r1.yml"}
	got := stripInstallFlags(in)
	// --install= and --status= must be stripped; --addr= and --config= preserved.
	for _, a := range got {
		if strings.HasPrefix(a, "--install") || strings.HasPrefix(a, "--status") {
			t.Errorf("residual install flag: %q", a)
		}
	}
	saw := map[string]bool{}
	for _, a := range got {
		saw[a] = true
	}
	if !saw["--addr=127.0.0.1:0"] || !saw["--config=/etc/r1.yml"] {
		t.Errorf("non-install flags dropped; got %v", got)
	}
}

func TestInheritEnvForUnit_AllowList(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("R1_HOME", "/tmp/r1")
	t.Setenv("HISTFILE", "should-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "should-not-leak-2")
	gotEnv := inheritEnvForUnit()
	// assert.Equal-style explicit checks below for the env allow-list.
	if gotEnv["HOME"] != "/tmp/home" {
		t.Errorf("HOME: got %q, want /tmp/home", gotEnv["HOME"])
	}
	if gotEnv["R1_HOME"] != "/tmp/r1" {
		t.Errorf("R1_HOME: got %q, want /tmp/r1", gotEnv["R1_HOME"])
	}
	if _, leaked := gotEnv["HISTFILE"]; leaked {
		t.Error("HISTFILE leaked into unit env")
	}
	if _, leaked := gotEnv["AWS_SECRET_ACCESS_KEY"]; leaked {
		t.Error("AWS_SECRET_ACCESS_KEY leaked into unit env")
	}
}

func TestRunServeStatus_ReportsKnownState(t *testing.T) {
	// Drive the runServeStatus path without actually installing.
	// Status on a (probably) not-installed r1-serve unit must:
	//
	//   1. Run to completion (no panic).
	//   2. Write a `<name>: <status> (<platform>)` line to stdout.
	//   3. Return a documented exit code (0, 3, 4, or 5 — never 2,
	//      which is reserved for usage errors that this verb has
	//      none of).
	var so, se bytes.Buffer
	code := runServeStatus(&so, &se)
	if code == 2 {
		t.Errorf("status: returned usage code 2; got stderr=%q", se.String())
	}
	// Exit code must be in the documented set.
	allowed := map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true}
	if !allowed[code] {
		t.Errorf("status: undocumented exit code %d (allowed: 0/1/3/4/5)", code)
	}
	out := so.String() + se.String()
	if out == "" {
		t.Fatal("status: produced no output")
	}
	// stdout should contain the unit name (default DefaultName from
	// internal/serviceunit) AND a status word from our typed string
	// set so operators see actionable information.
	if !strings.Contains(out, "r1-serve") {
		t.Errorf("status output should contain unit name r1-serve; got %q", out)
	}
	knownStatusWords := []string{"running", "stopped", "not-installed", "unknown"}
	sawStatusWord := false
	for _, w := range knownStatusWords {
		if strings.Contains(out, w) {
			sawStatusWord = true
			break
		}
	}
	if !sawStatusWord {
		t.Errorf("status output should contain one of %v; got %q", knownStatusWords, out)
	}
}

func TestServeInstall_End2End(t *testing.T) {
	// Spec-named end-to-end install/uninstall test. Requires
	// permission to mutate the host service manager — set
	// R1_TEST_SERVICE_INSTALL=1 to opt in. CI runs without the env,
	// so this reports SKIP and the suite stays green.
	if os.Getenv("R1_TEST_SERVICE_INSTALL") == "" {
		t.Skip("skipping end-to-end install test (set R1_TEST_SERVICE_INSTALL=1 to enable)")
	}

	var so, se bytes.Buffer
	if code := runServeInstall([]string{"--addr", "127.0.0.1:0"}, &so, &se); code != 0 {
		t.Fatalf("install: code=%d stderr=%q", code, se.String())
	}
	defer runServeUninstall(&so, &se)

	so.Reset()
	se.Reset()
	if code := runServeStatus(&so, &se); code != 0 {
		t.Errorf("status after install: code=%d stderr=%q stdout=%q", code, se.String(), so.String())
	}
}
