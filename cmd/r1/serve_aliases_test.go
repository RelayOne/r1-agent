package main

// serve_aliases_test.go — TASK-41 tests.
//
//   TestMain_DaemonAlias_PrintsDeprecationStderr      `r1 daemon` alias
//                                                     prints a one-line
//                                                     deprecation hint
//                                                     to stderr.
//   TestMain_AgentServeAlias_PrintsDeprecationStderr  Same for `r1
//                                                     agent-serve`.
//
// We DON'T invoke runDaemonAlias / runAgentServeAlias with real flags
// (the legacy commands open listeners and call os.Exit on flag
// errors). Tests check the hint emission shape by exercising the hint
// constants directly + a stderr-capturing helper.

import (
	"bytes"
	"strings"
	"testing"
)

func TestMain_DaemonAlias_WritesHintAndForwards(t *testing.T) {
	// End-to-end via the daemonForwarder indirection: the alias
	// writes the deprecation hint BEFORE the forward and the forward
	// receives the operator's args verbatim. Stubbed forwarder
	// avoids dialing the daemon's stateDir / WAL files.
	var (
		called  bool
		gotArgs []string
		origFwd = daemonForwarder
	)
	daemonForwarder = func(args []string) {
		called = true
		gotArgs = args
	}
	defer func() { daemonForwarder = origFwd }()

	var stderr bytes.Buffer
	runDaemonAlias([]string{"start", "--addr", "127.0.0.1:0"}, &stderr)

	if !called {
		t.Error("forwarder not called")
	}
	if len(gotArgs) != 3 || gotArgs[0] != "start" || gotArgs[1] != "--addr" {
		t.Errorf("forwarder args: got %v", gotArgs)
	}
	got := stderr.String()
	if !strings.HasPrefix(got, "r1 daemon: deprecated") {
		t.Errorf("hint not at head of stderr; got %q", got)
	}
}

func TestMain_AgentServeAlias_WritesHintAndForwards(t *testing.T) {
	// agentServeCmd unconditionally opens a listener and os.Exits on
	// flag errors, so the alias's daemonForwarder/agentServeForwarder
	// indirection lets us inject a stub that records the args without
	// running the real command. End-to-end: the hint is written
	// before the forward call, and the forward call receives the
	// caller's args verbatim.
	var (
		called    bool
		gotArgs   []string
		origFwd   = agentServeForwarder
	)
	agentServeForwarder = func(args []string) {
		called = true
		gotArgs = args
	}
	defer func() { agentServeForwarder = origFwd }()

	var stderr bytes.Buffer
	runAgentServeAlias([]string{"--addr", ":0"}, &stderr)

	if !called {
		t.Error("forwarder not called")
	}
	if len(gotArgs) != 2 || gotArgs[0] != "--addr" || gotArgs[1] != ":0" {
		t.Errorf("forwarder args: got %v, want [--addr :0]", gotArgs)
	}
	got := stderr.String()
	if !strings.HasPrefix(got, "r1 agent-serve: deprecated") {
		t.Errorf("stderr prefix: got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("hint must end with newline")
	}
}
