package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/policy"
)

// fakePolicyClient is a controllable Client double for gate tests.
// Records the single most recent Request so assertions can verify the
// PARC shape the gate constructed from tool arguments.
type fakePolicyClient struct {
	decision policy.Decision
	reasons  []string
	errs     []string
	retErr   error

	seen    policy.Request
	seenCnt int
}

func (f *fakePolicyClient) Check(_ context.Context, req policy.Request) (policy.Result, error) {
	f.seen = req
	f.seenCnt++
	return policy.Result{
		Decision: f.decision,
		Reasons:  f.reasons,
		Errors:   f.errs,
	}, f.retErr
}

func allowClient() *fakePolicyClient {
	return &fakePolicyClient{decision: policy.DecisionAllow, reasons: []string{"ok"}}
}

func denyClient(reasons ...string) *fakePolicyClient {
	return &fakePolicyClient{decision: policy.DecisionDeny, reasons: reasons}
}

// TestDescribeToolCall_GatedSurfaces asserts that every gated tool
// name (including its aliases) produces the right (action, resource,
// context) triple. This is the contract between the native runner's
// handler and the policy backend — if the mapping ever drifts, every
// cedar rule on the other side silently becomes a no-op.
func TestDescribeToolCall_GatedSurfaces(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		tool         string
		input        string
		wantAction   string
		wantResource string
		wantCtxKeys  []string
	}{
		{
			name:         "bash canonical",
			tool:         "bash",
			input:        `{"command":"rm -rf /tmp/x","cwd":"/w"}`,
			wantAction:   "bash.exec",
			wantResource: "rm",
			wantCtxKeys:  []string{"command", "cwd", "timeout_ms"},
		},
		{
			name:         "bash alias Bash",
			tool:         "Bash",
			input:        `{"command":"curl https://x"}`,
			wantAction:   "bash.exec",
			wantResource: "curl",
		},
		{
			name:         "bash alias execute_bash",
			tool:         "execute_bash",
			input:        `{"command":"ls -la"}`,
			wantAction:   "bash.exec",
			wantResource: "ls",
		},
		{
			name:         "bash alias run_command",
			tool:         "run_command",
			input:        `{"command":"git status"}`,
			wantAction:   "bash.exec",
			wantResource: "git",
		},
		{
			name:         "bash single-word command",
			tool:         "bash",
			input:        `{"command":"pwd"}`,
			wantAction:   "bash.exec",
			wantResource: "pwd",
		},
		{
			name:         "read_file canonical",
			tool:         "read_file",
			input:        `{"path":"/abs/path.go"}`,
			wantAction:   "file.read",
			wantResource: "/abs/path.go",
		},
		{
			name:         "read_file alias Read with file_path",
			tool:         "Read",
			input:        `{"file_path":"/alt/path.go"}`,
			wantAction:   "file.read",
			wantResource: "/alt/path.go",
		},
		{
			name:         "write_file canonical",
			tool:         "write_file",
			input:        `{"path":"/abs/out.go"}`,
			wantAction:   "file.write",
			wantResource: "/abs/out.go",
		},
		{
			name:         "edit_file routes to file.write",
			tool:         "edit_file",
			input:        `{"path":"/abs/edit.go","old_string":"a","new_string":"b"}`,
			wantAction:   "file.write",
			wantResource: "/abs/edit.go",
		},
		{
			name:         "str_replace_editor routes to file.write",
			tool:         "str_replace_editor",
			input:        `{"path":"/abs/sr.go"}`,
			wantAction:   "file.write",
			wantResource: "/abs/sr.go",
		},
		{
			name:         "create_file routes to file.write",
			tool:         "create_file",
			input:        `{"path":"/abs/new.go"}`,
			wantAction:   "file.write",
			wantResource: "/abs/new.go",
		},
		{
			name:         "mcp single-underscore server",
			tool:         "mcp_demo_ping",
			input:        `{"message":"hi"}`,
			wantAction:   "mcp.demo.ping",
			wantResource: "mcp://demo/ping",
			wantCtxKeys:  []string{"server", "tool", "input_size"},
		},
		{
			name:         "mcp tool with underscore in name",
			tool:         "mcp_linear_create_issue",
			input:        `{"title":"x"}`,
			wantAction:   "mcp.linear.create_issue",
			wantResource: "mcp://linear/create_issue",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, resource, ctx, ok := describeToolCall(tc.tool, json.RawMessage(tc.input))
			if !ok {
				t.Fatalf("describeToolCall(%q) ok=false, want true (gated)", tc.tool)
			}
			if action != tc.wantAction {
				t.Errorf("action = %q, want %q", action, tc.wantAction)
			}
			if resource != tc.wantResource {
				t.Errorf("resource = %q, want %q", resource, tc.wantResource)
			}
			for _, k := range tc.wantCtxKeys {
				if _, exists := ctx[k]; !exists {
					t.Errorf("ctx missing key %q; got %v", k, ctx)
				}
			}
		})
	}
}

// TestDescribeToolCall_NonGatedShortCircuits asserts that grep / glob /
// env_* / request_clarification and other non-POL-7 tools return
// ok=false so the gate runs its short-circuit path. If this ever
// regresses, every read-only tool call would pay a policy round-trip.
func TestDescribeToolCall_NonGatedShortCircuits(t *testing.T) {
	t.Parallel()
	nonGated := []string{
		"grep", "Grep", "search",
		"glob", "Glob", "find_files",
		"env_exec", "env_copy_in", "env_copy_out",
		"request_clarification",
		"some_unknown_tool",
		"", // empty name is not gated — runner's allowedTools check rejects it first
	}
	for _, name := range nonGated {
		_, _, _, ok := describeToolCall(name, json.RawMessage(`{}`))
		if ok {
			t.Errorf("describeToolCall(%q) ok=true, want false (non-gated)", name)
		}
	}
}

// TestDescribeBashCall_TruncatesLongCommand asserts the 512-char
// command cap so a pathological multi-megabyte bash invocation can't
// inflate every PARC request body.
func TestDescribeBashCall_TruncatesLongCommand(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", maxBashCommandLen*3)
	input := json.RawMessage(`{"command":"echo ` + long + `"}`)
	_, resource, ctx, ok := describeToolCall("bash", input)
	if !ok {
		t.Fatalf("bash describe ok=false")
	}
	if resource != "echo" {
		t.Errorf("resource = %q, want echo", resource)
	}
	cmd, _ := ctx["command"].(string)
	if len(cmd) > maxBashCommandLen {
		t.Errorf("command len = %d, want <= %d", len(cmd), maxBashCommandLen)
	}
}

// TestDescribeBashCall_MalformedInput asserts best-effort parsing:
// malformed JSON must still resolve to "bash.exec" with an empty
// resource (rather than crash) so the policy backend sees a deny-able
// request instead of the gate silently allowing through.
func TestDescribeBashCall_MalformedInput(t *testing.T) {
	t.Parallel()
	action, resource, ctx, ok := describeToolCall("bash", json.RawMessage(`{not-json`))
	if !ok {
		t.Fatalf("bash describe ok=false, want true")
	}
	if action != "bash.exec" {
		t.Errorf("action = %q, want bash.exec", action)
	}
	if resource != "" {
		t.Errorf("resource = %q, want empty", resource)
	}
	if ctx == nil {
		t.Errorf("ctx = nil, want non-nil (even if empty fields)")
	}
}

// TestGateToolCallWith_AllowVerdict asserts the happy path: an Allow
// verdict returns Allowed=true with no error and the Client sees a
// PARC-shaped Request with the fixed worker principal.
func TestGateToolCallWith_AllowVerdict(t *testing.T) {
	t.Parallel()
	c := allowClient()
	res := gateToolCallWith(context.Background(), c, "bash",
		json.RawMessage(`{"command":"ls"}`))
	if !res.Allowed {
		t.Fatalf("Allowed=false on allow verdict, err=%v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err=%v on allow verdict, want nil", res.Err)
	}
	if c.seenCnt != 1 {
		t.Fatalf("Check invoked %d times, want 1", c.seenCnt)
	}
	if c.seen.Principal != `Stoke::"worker"` {
		t.Errorf("Principal = %q, want Stoke::\"worker\"", c.seen.Principal)
	}
	if c.seen.Action != "bash.exec" {
		t.Errorf("Action = %q, want bash.exec", c.seen.Action)
	}
	if c.seen.Resource != "ls" {
		t.Errorf("Resource = %q, want ls", c.seen.Resource)
	}
}

// TestGateToolCallWith_DenyVerdict asserts fail-closed on Deny: the
// gate returns Allowed=false with an error whose string includes the
// action, resource, and reasons so post-mortem JSONL grep finds it.
func TestGateToolCallWith_DenyVerdict(t *testing.T) {
	t.Parallel()
	c := denyClient("rule-42", "cat:danger")
	res := gateToolCallWith(context.Background(), c, "bash",
		json.RawMessage(`{"command":"rm -rf /"}`))
	if res.Allowed {
		t.Fatalf("Allowed=true on deny verdict, want false")
	}
	if res.Err == nil {
		t.Fatalf("Err=nil on deny verdict, want populated")
	}
	msg := res.Err.Error()
	for _, want := range []string{"policy denied", "bash.exec", "rm", "rule-42", "cat:danger"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Err=%q missing substring %q", msg, want)
		}
	}
}

// TestGateToolCallWith_CheckErrorIsFailClosed asserts that a non-nil
// error from Client.Check (transport fault, backend down, etc.)
// routes through the same fail-closed path as an explicit Deny.
// This matches the failclosed_test.go contract on the policy side.
func TestGateToolCallWith_CheckErrorIsFailClosed(t *testing.T) {
	t.Parallel()
	c := &fakePolicyClient{
		decision: policy.DecisionDeny,
		errs:     []string{"policy-engine unavailable: dial tcp: connect: refused"},
		retErr:   errors.New("transport fault"),
	}
	res := gateToolCallWith(context.Background(), c, "write_file",
		json.RawMessage(`{"path":"/tmp/x"}`))
	if res.Allowed {
		t.Fatalf("Allowed=true on transport fault, want false (fail-closed)")
	}
	if res.Err == nil {
		t.Fatalf("Err=nil on transport fault, want populated")
	}
	if !strings.Contains(res.Err.Error(), "file.write") {
		t.Errorf("Err=%q missing action", res.Err)
	}
}

// TestGateToolCallWith_NilClientIsFailClosed asserts that a nil
// Client (should never happen in production but a refactor could
// introduce it) fails closed with a clear error rather than allowing
// the call through by mistake.
func TestGateToolCallWith_NilClientIsFailClosed(t *testing.T) {
	t.Parallel()
	res := gateToolCallWith(context.Background(), nil, "bash",
		json.RawMessage(`{"command":"ls"}`))
	if res.Allowed {
		t.Fatalf("Allowed=true with nil client, want false (fail-closed)")
	}
	if res.Err == nil {
		t.Fatalf("Err=nil with nil client, want populated")
	}
	if !strings.Contains(res.Err.Error(), "policy") {
		t.Errorf("Err=%q missing 'policy' prefix", res.Err)
	}
}

// TestGateToolCallWith_NonGatedBypassesClient asserts that non-gated
// tool names bypass the Client.Check call entirely — grep / glob /
// env_* must never pay a policy round-trip.
func TestGateToolCallWith_NonGatedBypassesClient(t *testing.T) {
	t.Parallel()
	c := denyClient("should-not-be-consulted")
	res := gateToolCallWith(context.Background(), c, "grep",
		json.RawMessage(`{"pattern":"x"}`))
	if !res.Allowed {
		t.Fatalf("Allowed=false for non-gated tool, err=%v", res.Err)
	}
	if c.seenCnt != 0 {
		t.Fatalf("Check invoked %d times for non-gated tool, want 0", c.seenCnt)
	}
}

// TestGateToolCallWith_MCPRoundTrip asserts the MCP path threads the
// right server/tool identifiers all the way through — a regression
// here would leave cedar rules on mcp.<server>.<tool> matching against
// empty strings.
func TestGateToolCallWith_MCPRoundTrip(t *testing.T) {
	t.Parallel()
	c := allowClient()
	res := gateToolCallWith(context.Background(), c,
		"mcp_linear_create_issue",
		json.RawMessage(`{"title":"bug"}`))
	if !res.Allowed {
		t.Fatalf("Allowed=false for MCP call, err=%v", res.Err)
	}
	if c.seen.Action != "mcp.linear.create_issue" {
		t.Errorf("Action = %q, want mcp.linear.create_issue", c.seen.Action)
	}
	if c.seen.Resource != "mcp://linear/create_issue" {
		t.Errorf("Resource = %q, want mcp://linear/create_issue", c.seen.Resource)
	}
	if srv, _ := c.seen.Context["server"].(string); srv != "linear" {
		t.Errorf("ctx.server = %q, want linear", srv)
	}
	if tool, _ := c.seen.Context["tool"].(string); tool != "create_issue" {
		t.Errorf("ctx.tool = %q, want create_issue", tool)
	}
	// The raw MCP body must NOT be echoed back in the context — only
	// its size. This is the secret-hygiene guarantee described in
	// describeMCPCall.
	if _, leaked := c.seen.Context["title"]; leaked {
		t.Errorf("ctx leaked raw MCP body field %q", "title")
	}
}

// TestGateToolCall_EnvBackedNullClient_AllowsWhenPolicyUnset asserts
// the env-backed singleton path (the one the real runner uses) ends
// up with a NullClient in the default-no-env case and therefore
// allows gated tools. Hermetic: unsets both policy env vars via
// t.Setenv so CI-level policy config doesn't change the assertion.
//
// Deny / error paths via the singleton are NOT re-tested here: the
// sync.Once latches once per process and any second test would read
// the cached Client. Those paths are exhaustively covered via
// gateToolCallWith with an injected Client — the supported seam for
// test doubles.
func TestGateToolCall_EnvBackedNullClient_AllowsWhenPolicyUnset(t *testing.T) {
	// Intentionally NOT t.Parallel — the singleton is process-wide.
	// Running serially here still leaves the singleton latched for
	// the rest of the process, but every other test in this file
	// uses gateToolCallWith with an injected Client, so there's no
	// cross-test interference.
	t.Setenv("CLOUDSWARM_POLICY_ENDPOINT", "")
	t.Setenv("R1_POLICY_FILE", "")
	res := gateToolCall(context.Background(), "bash",
		json.RawMessage(`{"command":"pwd"}`))
	if !res.Allowed {
		t.Fatalf("Allowed=false with NullClient, err=%v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err=%v with NullClient, want nil", res.Err)
	}
}
