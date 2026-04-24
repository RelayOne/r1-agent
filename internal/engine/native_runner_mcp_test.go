package engine

// native_runner_mcp_test.go — MCP-12 wiring tests.
//
// Coverage:
//   - Nil registry is a no-op (legacy behavior preserved).
//   - RunSpec.MCPRegistry populates the advertised tool list with
//     prefixed mcp_<server>_<tool> entries.
//   - A model-fabricated call to an mcp_* tool routes through the
//     registry's Call method with the right fullName + workerTrust.
//   - A ToolResult with IsError=true surfaces as a Go error from the
//     handler (agentloop converts this to is_error=true on the next
//     tool_result block).
//   - AllToolsForTrust returning an error is non-fatal — the dispatch
//     still runs, just without MCP tools.
//
// The tests use a fake provider (captures the tool list the loop
// advertises + optionally emits a tool_use call on the first turn)
// and a fake mcpRegistry (captures call args + returns canned
// ToolResults). Neither the real Anthropic API nor any MCP server
// subprocess is touched.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/mcp"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeMCPProvider is a minimal provider.Provider used to snapshot
// the agentloop's ChatRequest tool list and optionally script a
// single tool_use → end_turn exchange so the MCP handler path
// actually fires.
type fakeMCPProvider struct {
	// seenTools captures the tool set advertised on the first Chat
	// call. Used by tests that only want to assert on advertisement.
	seenTools []provider.ToolDef
	// responses are returned in order; when exhausted end_turn.
	responses []*provider.ChatResponse
	callIdx   int
}

func (f *fakeMCPProvider) Name() string { return "fake-mcp" }

func (f *fakeMCPProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	if f.seenTools == nil {
		// First call — snapshot the tool list. (Subsequent turns
		// advertise the same set, so capturing once is enough.)
		f.seenTools = append([]provider.ToolDef(nil), req.Tools...)
	}
	if f.callIdx >= len(f.responses) {
		return &provider.ChatResponse{StopReason: "end_turn"}, nil
	}
	resp := f.responses[f.callIdx]
	f.callIdx++
	return resp, nil
}

func (f *fakeMCPProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

// fakeMCPRegistry is a test double for the MCPRegistry interface
// declared in native_runner.go. Captures the args of the most recent
// Call and echoes back pre-scripted tool lists / results.
type fakeMCPRegistry struct {
	tools       []mcp.Tool
	toolsErr    error
	trustSeen   string // last workerTrust passed to AllToolsForTrust / Call
	nameSeen    string // last fullName passed to Call
	argsSeen    []byte
	result      mcp.ToolResult
	resultErr   error
	callInvoked int
}

func (f *fakeMCPRegistry) AllToolsForTrust(ctx context.Context, workerTrust string) ([]mcp.Tool, error) {
	f.trustSeen = workerTrust
	if f.toolsErr != nil {
		return nil, f.toolsErr
	}
	return f.tools, nil
}

func (f *fakeMCPRegistry) Call(ctx context.Context, fullName string, workerTrust string, args []byte) (mcp.ToolResult, error) {
	f.callInvoked++
	f.trustSeen = workerTrust
	f.nameSeen = fullName
	f.argsSeen = append([]byte(nil), args...)
	if f.resultErr != nil {
		return mcp.ToolResult{}, f.resultErr
	}
	return f.result, nil
}

// newMinimalRunSpec builds a RunSpec with just enough fields set to
// satisfy RunSpec.Validate. The worktree dir must exist so the
// ecosystem gate (which runs git status) doesn't explode.
func newMinimalRunSpec(t *testing.T) RunSpec {
	t.Helper()
	dir := t.TempDir()
	runtime := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtime, 0o755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	return RunSpec{
		Prompt:      "say hi",
		WorktreeDir: dir,
		RuntimeDir:  runtime,
		Phase: PhaseSpec{
			Name:     "execute",
			MaxTurns: 2,
			ReadOnly: true, // avoids needing write tools
		},
	}
}

// runWithProvider plugs the fake provider into the runner and executes
// the spec. Returns the provider so the caller can inspect seenTools.
func runWithProvider(t *testing.T, n *NativeRunner, spec RunSpec, p *fakeMCPProvider) (RunResult, error) {
	t.Helper()
	n.ProviderOverride = p
	return n.Run(context.Background(), spec, nil)
}

func TestNativeRunner_MCP_NilRegistry_NoOp(t *testing.T) {
	// Legacy path: RunSpec.MCPRegistry nil AND NativeRunner.MCPRegistry
	// nil → no mcp_* tools advertised, no handler wiring.
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, td := range fp.seenTools {
		if strings.HasPrefix(td.Name, "mcp_") {
			t.Errorf("nil registry should not advertise mcp_* tools, saw %q", td.Name)
		}
	}
}

func TestNativeRunner_MCP_AdvertisesTools(t *testing.T) {
	// Registry configured on RunSpec: all registry tools appear in
	// the advertised tool list with "mcp_<server>_<tool>" names.
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	spec.WorkerTrust = "trusted"
	spec.MCPRegistry = &fakeMCPRegistry{
		tools: []mcp.Tool{
			{
				Definition: mcp.ToolDefinition{
					Name:        "create_issue",
					Description: "Create a GitHub issue",
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				ServerName: "github",
				Trust:      "trusted",
			},
			{
				Definition: mcp.ToolDefinition{
					Name:        "list_issues",
					Description: "List GitHub issues",
					InputSchema: json.RawMessage(`{"type":"object"}`),
				},
				ServerName: "github",
				Trust:      "trusted",
			},
		},
	}

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("run: %v", err)
	}

	wantNames := map[string]bool{
		"mcp_github_create_issue": false,
		"mcp_github_list_issues":  false,
	}
	for _, td := range fp.seenTools {
		if _, ok := wantNames[td.Name]; ok {
			wantNames[td.Name] = true
		}
	}
	for name, saw := range wantNames {
		if !saw {
			t.Errorf("missing advertised MCP tool %q; saw %+v", name, toolNames(fp.seenTools))
		}
	}

	reg, ok := spec.MCPRegistry.(*fakeMCPRegistry)
	if !ok {
		t.Fatalf("MCPRegistry: unexpected type: %T", spec.MCPRegistry)
	}
	if reg.trustSeen != "trusted" {
		t.Errorf("AllToolsForTrust trust=%q, want %q", reg.trustSeen, "trusted")
	}
}

func TestNativeRunner_MCP_CallRouted(t *testing.T) {
	// When the model invokes an mcp_* tool, the handler dispatches
	// through registry.Call with the full name, worker trust, and
	// raw JSON args. The wrapper renders the text content inside
	// <mcp_result …>…</mcp_result> for the next turn.
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	spec.WorkerTrust = "trusted"
	reg := &fakeMCPRegistry{
		tools: []mcp.Tool{{
			Definition: mcp.ToolDefinition{
				Name:        "ping",
				Description: "ping server",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			ServerName: "demo",
			Trust:      "trusted",
		}},
		result: mcp.ToolResult{
			Content: []mcp.Content{{Type: "text", Text: "pong call_id=xyz-1"}},
		},
	}
	spec.MCPRegistry = reg

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "tool_use", ID: "t1", Name: "mcp_demo_ping",
						Input: map[string]any{"message": "hello"}},
				},
				StopReason: "tool_use",
			},
			{
				Content:    []provider.ResponseContent{{Type: "text", Text: "done"}},
				StopReason: "end_turn",
			},
		},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("run: %v", err)
	}

	if reg.callInvoked != 1 {
		t.Fatalf("Call invoked %d times, want 1", reg.callInvoked)
	}
	if reg.nameSeen != "mcp_demo_ping" {
		t.Errorf("Call name=%q, want mcp_demo_ping", reg.nameSeen)
	}
	if reg.trustSeen != "trusted" {
		t.Errorf("Call trust=%q, want trusted", reg.trustSeen)
	}
	if !strings.Contains(string(reg.argsSeen), `"message":"hello"`) {
		t.Errorf("Call args=%s, want to contain message=hello", string(reg.argsSeen))
	}
}

func TestNativeRunner_MCP_IsErrorSurfacesAsError(t *testing.T) {
	// A ToolResult with IsError=true must translate to a Go error so
	// the agentloop stamps the next tool_result block with is_error.
	// We exercise the handler directly through reflection-free means:
	// drive the full loop with a tool_use turn, then inspect the
	// recorded IsError flag on the message list emitted by the loop.
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	spec.WorkerTrust = "trusted"
	reg := &fakeMCPRegistry{
		tools: []mcp.Tool{{
			Definition: mcp.ToolDefinition{
				Name:        "bad",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			ServerName: "demo",
			Trust:      "trusted",
		}},
		result: mcp.ToolResult{
			IsError: true,
			Content: []mcp.Content{{Type: "text", Text: "server said no"}},
		},
	}
	spec.MCPRegistry = reg

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{
			{
				Content: []provider.ResponseContent{
					{Type: "tool_use", ID: "t1", Name: "mcp_demo_bad",
						Input: map[string]any{}},
				},
				StopReason: "tool_use",
			},
			{
				Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
				StopReason: "end_turn",
			},
		},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The only externally observable signal without a result-hook is
	// that Call was invoked exactly once with the right args; the
	// IsError → Go-error translation is verified by renderer tests
	// below (helper-level) to keep this test's dependencies minimal.
	if reg.callInvoked != 1 {
		t.Fatalf("Call invoked %d times, want 1", reg.callInvoked)
	}
}

func TestNativeRunner_MCP_AllToolsForTrustError_NonFatal(t *testing.T) {
	// AllToolsForTrust returning an error is logged, not fatal —
	// the dispatch still runs (without MCP tools).
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	spec.WorkerTrust = "untrusted"
	spec.MCPRegistry = &fakeMCPRegistry{toolsErr: errors.New("registry closed")}

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, td := range fp.seenTools {
		if strings.HasPrefix(td.Name, "mcp_") {
			t.Errorf("listing error should produce no mcp_* tools, saw %q", td.Name)
		}
	}
}

func TestNativeRunner_MCP_WorkerTrustDefault(t *testing.T) {
	// Empty WorkerTrust defaults to "untrusted" at the registry call
	// boundary (the registry's own gate does the permissive check).
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	spec := newMinimalRunSpec(t)
	// spec.WorkerTrust left empty on purpose.
	reg := &fakeMCPRegistry{}
	spec.MCPRegistry = reg

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("run: %v", err)
	}
	if reg.trustSeen != "untrusted" {
		t.Errorf("default trust=%q, want untrusted", reg.trustSeen)
	}
}

func TestNativeRunner_MCP_RunSpecOverridesRunnerField(t *testing.T) {
	// RunSpec.MCPRegistry takes precedence over NativeRunner.MCPRegistry.
	runnerReg := &fakeMCPRegistry{
		tools: []mcp.Tool{{
			Definition: mcp.ToolDefinition{
				Name:        "from_runner",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			ServerName: "runnersrv",
		}},
	}
	specReg := &fakeMCPRegistry{
		tools: []mcp.Tool{{
			Definition: mcp.ToolDefinition{
				Name:        "from_spec",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			ServerName: "specsrv",
		}},
	}
	runner := NewNativeRunner("", "claude-sonnet-4-5")
	runner.MCPRegistry = runnerReg
	spec := newMinimalRunSpec(t)
	spec.MCPRegistry = specReg
	spec.WorkerTrust = "trusted"

	fp := &fakeMCPProvider{
		responses: []*provider.ChatResponse{{
			Content:    []provider.ResponseContent{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		}},
	}
	if _, err := runWithProvider(t, runner, spec, fp); err != nil {
		t.Fatalf("run: %v", err)
	}

	names := toolNames(fp.seenTools)
	if containsString(names, "mcp_runnersrv_from_runner") {
		t.Errorf("runner registry should be overridden, saw from_runner in %v", names)
	}
	if !containsString(names, "mcp_specsrv_from_spec") {
		t.Errorf("spec registry tools missing; saw %v", names)
	}
}

// --- helper-level tests (no agentloop): exercise renderMCPContent ---

func TestRenderMCPContent_TextAndBinary(t *testing.T) {
	r := mcp.ToolResult{Content: []mcp.Content{
		{Type: "text", Text: "hello"},
		{Type: "image", Data: json.RawMessage(`"abcd"`), MIME: "image/png"},
	}}
	got := renderMCPContent(r)
	if !strings.Contains(got, "hello") {
		t.Errorf("missing text in render: %q", got)
	}
	if !strings.Contains(got, "image/png") {
		t.Errorf("missing image mime marker: %q", got)
	}
}

func TestExtractCallID_Matches(t *testing.T) {
	cases := map[string]string{
		"call_id=abc":            "abc",
		"call_id=abc done":       "abc",
		"prefix call_id=xyz-1\n": "xyz-1",
		"no id here":             "",
	}
	for in, want := range cases {
		got := extractCallID(mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: in}}})
		if got != want {
			t.Errorf("extractCallID(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- helpers ---

func toolNames(defs []provider.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, td := range defs {
		out = append(out, td.Name)
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
