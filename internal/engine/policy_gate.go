// policy_gate.go — POL-7 policy hook for the native tool-dispatch path.
//
// Wires the policy.Client (from policy.NewFromEnv) into every gated tool
// invocation routed through NativeRunner. Gated surfaces:
//
//   - bash (including aliases: Bash, execute_bash, run_command)
//   - file_write (aliases: write_file, Write, write, create_file,
//     edit_file, Edit, edit, str_replace_editor)
//   - file_read (aliases: read_file, Read, read)
//   - mcp_<server>_<tool> (every MCP-backed tool routed via the native
//     runner's extra-handler wiring)
//
// Non-gated tools (grep, glob, env_exec, env_copy_in, env_copy_out,
// request_clarification, etc.) fall through untouched so the POL-7 hook
// is a strict, auditable subset rather than a blanket gate.
//
// Fail-closed semantics: if the Client returns Decision != Allow or a
// non-nil error, the hook returns an error from the tool handler WITHOUT
// invoking the underlying side effect. The agentloop translates a
// non-nil handler error into an is_error=true tool_result block, so the
// model sees the denial as a tool failure rather than a silent drop.
//
// Spec-deviation: lazy package-level init via sync.Once instead of
// struct-field injection. Rationale — NewNativeRunner has seven call
// sites across cmd/stoke/, internal/app/, and internal/engine test
// files, and RunSpec is already a 200-line struct; threading a
// policy.Client through every construction / test-double site would
// balloon the blast radius of this one task well beyond the ONE-commit
// budget. The singleton reads environment once (honouring
// CLOUDSWARM_POLICY_ENDPOINT / STOKE_POLICY_FILE) and reuses the same
// Client for every subsequent dispatch — identical to how policy_cmd.go
// already constructs its Client. gateToolCallWith (the injected-client
// variant) is the testability seam and can be reused by a future
// struct-injected path without further refactoring.
//
// Principal: the fixed value `Stoke::"worker"` is sent on every
// request. The gate's Request construction is the single site that
// needs to change once CloudSwarm-auth threads a real per-session
// identity through — no structural redesign required.

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ericmacdougall/stoke/internal/policy"
)

// maxBashCommandLen caps the "command" context value sent to the
// policy backend so a pathological multi-megabyte bash invocation can't
// inflate every PARC request. 512 chars keeps the resource identifier
// (first word) plus enough context for audit without risking OOM on
// the cedar-agent side.
const maxBashCommandLen = 512

// policyOnce guards the lazy singleton init. See package doc for the
// struct-injection deviation rationale.
var (
	policyOnce   sync.Once
	policyClient policy.Client
	policyErr    error
)

// getPolicyClient returns the process-wide policy.Client, constructing
// it on first call via policy.NewFromEnv. The (nil, err) shape is
// preserved so the gate can fail-closed when the env is misconfigured
// (e.g. STOKE_POLICY_FILE points at a malformed YAML) rather than
// silently degrading to the NullClient.
func getPolicyClient() (policy.Client, error) {
	policyOnce.Do(func() {
		c, _, err := policy.NewFromEnv()
		if err != nil {
			policyErr = err
			return
		}
		policyClient = c
	})
	return policyClient, policyErr
}

// policyGateResult is the outcome of a policy check — Allow means the
// caller should proceed with the underlying tool; otherwise the caller
// must surface the error without invoking the side effect.
type policyGateResult struct {
	Allowed bool
	Err     error
}

// gateToolCall is the POL-7 entry point wired from NativeRunner's
// handler closure. It resolves the process-wide policy.Client from
// the env-backed singleton and delegates to gateToolCallWith so the
// core gate logic stays unit-testable with injected Clients.
//
// Non-gated tool names short-circuit with Allowed=true before the
// singleton is touched so grep/glob/env_* dispatch doesn't pay the
// Client-lookup cost on every call.
//
// Principal is the fixed `Stoke::"worker"` value.
func gateToolCall(ctx context.Context, name string, input json.RawMessage) policyGateResult {
	if _, _, _, ok := describeToolCall(name, input); !ok {
		return policyGateResult{Allowed: true}
	}
	client, err := getPolicyClient()
	if err != nil {
		// Env-level misconfiguration: fail closed. Wrap the underlying
		// error so operators see exactly what the problem is without
		// grepping through bootstrap logs.
		return policyGateResult{
			Err: fmt.Errorf("policy: client unavailable: %w", err),
		}
	}
	if client == nil {
		// Belt-and-braces: should never happen — NewFromEnv always
		// returns a non-nil Client on success — but keep the
		// fail-closed invariant in case of future refactors.
		return policyGateResult{
			Err: errors.New("policy: client unavailable (nil client, no error)"),
		}
	}
	return gateToolCallWith(ctx, client, name, input)
}

// gateToolCallWith performs the actual policy check against the
// supplied Client. Extracted from gateToolCall so unit tests can
// inject a test-double Client without poking the sync.Once-backed
// singleton (which would leak state across tests in the same
// process). Non-gated tools return Allowed=true; gated tools
// marshal a PARC-shaped Request and translate the verdict:
//
//   - Decision=Allow, err=nil       → Allowed=true, Err=nil
//   - Decision=Deny OR err != nil   → Allowed=false, Err populated
//   - client == nil                 → Allowed=false, Err populated
//
// The error-on-Deny wording includes action + resource + Reasons +
// Errors so a reviewer reading the worker-log JSONL can trace the
// exact rule (or transport fault) that blocked the call.
func gateToolCallWith(ctx context.Context, client policy.Client, name string, input json.RawMessage) policyGateResult {
	action, resource, gateCtx, ok := describeToolCall(name, input)
	if !ok {
		return policyGateResult{Allowed: true}
	}
	if client == nil {
		return policyGateResult{
			Err: errors.New("policy: client unavailable (nil client)"),
		}
	}
	// Principal is the fixed worker identity.
	req := policy.Request{
		Principal: `Stoke::"worker"`,
		Action:    action,
		Resource:  resource,
		Context:   gateCtx,
	}
	res, checkErr := client.Check(ctx, req)
	if checkErr != nil || res.Decision != policy.DecisionAllow {
		// Reasons / Errors fold into the tool-result error string so
		// the worker log captures the denial for post-mortem review
		// without the gate needing direct access to a streamjson
		// emitter in this scope.
		return policyGateResult{
			Err: fmt.Errorf("policy denied %s on %s: reasons=%v errors=%v",
				action, resource, res.Reasons, res.Errors),
		}
	}
	return policyGateResult{Allowed: true}
}

// describeToolCall maps a tool name + input to the (action, resource,
// context) triple the policy backend expects. Returns ok=false for
// tools that are NOT gated by POL-7 so the caller can short-circuit.
//
// Resource extraction per spec:
//   - bash:       first word of command (e.g. "rm", "curl")
//   - file_read:  absolute path (whatever the tool received)
//   - file_write: absolute path
//   - mcp_*:      "mcp://<server>/<tool>"
//
// Context is the raw args stringified, minus fields that typically
// carry secrets. MCP input bodies in particular are opaque, so we
// forward only the server / tool / size rather than echoing the raw
// body back to the policy backend.
func describeToolCall(name string, input json.RawMessage) (action, resource string, ctx map[string]any, ok bool) {
	switch name {
	case "bash", "Bash", "execute_bash", "run_command":
		return describeBashCall(input)
	case "read_file", "Read", "read":
		return describeFileCall(input, "file.read")
	case "write_file", "Write", "write", "create_file",
		"edit_file", "Edit", "edit", "str_replace_editor":
		return describeFileCall(input, "file.write")
	default:
		if strings.HasPrefix(name, "mcp_") {
			return describeMCPCall(name, input)
		}
		return "", "", nil, false
	}
}

func describeBashCall(input json.RawMessage) (string, string, map[string]any, bool) {
	var args struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd,omitempty"`
		Timeout int    `json:"timeout_ms,omitempty"`
	}
	_ = json.Unmarshal(input, &args) // best-effort; on failure we still deny-check with empty resource
	cmd := strings.TrimSpace(args.Command)
	first := ""
	if cmd != "" {
		// First whitespace-separated token is the "resource" the policy
		// rule matches on — identical to the spec example ("bash.exec"
		// on resource "rm" or "curl"). Quote/escape handling is
		// deliberately naive: we want the rule writer to see the raw
		// intent, not a shell-lexer-normalized form that might mask
		// injection.
		if idx := strings.IndexAny(cmd, " \t\n"); idx > 0 {
			first = cmd[:idx]
		} else {
			first = cmd
		}
	}
	truncated := cmd
	if len(truncated) > maxBashCommandLen {
		truncated = truncated[:maxBashCommandLen]
	}
	return "bash.exec", first, map[string]any{
		"command":    truncated,
		"cwd":        args.Cwd,
		"timeout_ms": args.Timeout,
	}, true
}

func describeFileCall(input json.RawMessage, action string) (string, string, map[string]any, bool) {
	// Accept both `path` (native registry) and `file_path` (some
	// provider aliases) so the gate doesn't silently degrade if a
	// future tool definition uses the alternate key.
	var args struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(input, &args)
	resource := args.Path
	if resource == "" {
		resource = args.FilePath
	}
	return action, resource, map[string]any{}, true
}

func describeMCPCall(name string, input json.RawMessage) (string, string, map[string]any, bool) {
	// MCP tool names the runner advertises look like
	// "mcp_<server>_<tool>". Split on the FIRST underscore after the
	// "mcp_" prefix so server names without underscores round-trip
	// cleanly. Tool names that themselves contain underscores keep
	// them (common for e.g. mcp_linear_create_issue).
	var server, tool string
	rest := strings.TrimPrefix(name, "mcp_")
	if idx := strings.Index(rest, "_"); idx > 0 {
		server = rest[:idx]
		tool = rest[idx+1:]
	} else {
		server = rest
	}
	// Don't echo the raw MCP body back to the policy backend — it may
	// contain tokens or other secrets that the server wraps around.
	// Until a field-level allowlist exists, forward just the size so
	// a rule writer can reason about payload magnitude without seeing
	// its contents.
	ctx := map[string]any{
		"server":     server,
		"tool":       tool,
		"input_size": len(input),
	}
	action := "mcp." + server + "." + tool
	resource := "mcp://" + server + "/" + tool
	return action, resource, ctx, true
}
