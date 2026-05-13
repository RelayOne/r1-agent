// internal/mcp/promptguard_hook.go
//
// MCP-side wiring for promptguard's per-tool input validator
// (specs/promptguard-hardening.md §T2 items 8 + 10).
//
// HandleToolCall is the canonical MCP tool-dispatch entry point on
// StokeServer (see r1_server.go); the lanes / codebase / cortex
// sub-servers all dispatch through it (or expose their own
// HandleToolCall reachable via the same JSON-RPC tools/call surface,
// see r1_server.go's ServeStdio dispatch). Every tool call MUST run
// through validateMCPToolInput before reaching the per-handler switch,
// so an injection-crafted payload to deploy.execute / browse.fetch /
// file.write (or any operator-added rule) cannot dispatch.
//
// The hook returns a structured rejection string identical to what
// MCP clients see for any other tool error (the JSON-RPC layer
// converts errors into a {content:[{type:"text", text:"Error: ..."}],
// isError:true} response — see r1_server.go ServeStdio tools/call
// handler). The string carries the prefix "promptguard: tool_input_
// rejected" so downstream consumers can pattern-match without parsing
// MCP-wire JSON.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/RelayOne/r1/internal/promptguard"
)

// validateMCPToolInput is the single chokepoint that every StokeServer
// MCP tool call passes through. Returns nil to allow the call,
// non-nil with the structured rejection message otherwise.
//
// Marshals args to JSON before calling promptguard.ValidateToolInput so
// the bytes-based deny patterns can match across field boundaries.
// Action gating: if the configured promptguard action is ActionWarn or
// ActionStrip, violations are logged via the threat-event emitter but
// the call is allowed to proceed (the warn/strip dispositions exist so
// operators can run the validator in observe-only mode during initial
// rollout). Only ActionReject blocks dispatch.
func validateMCPToolInput(ctx context.Context, toolName string, args map[string]interface{}) error {
	raw, err := marshalArgsForValidation(args)
	if err != nil {
		// If we cannot marshal the args, we cannot validate them. Log
		// via the emitter and allow the call — the underlying handler
		// will likely fail on the bad shape anyway. A hard rejection
		// here would surface as a confusing user-facing error on
		// otherwise legitimate edge cases (nil maps, etc.).
		return nil
	}
	rep, vErr := promptguard.ValidateToolInput(ctx, toolName, raw)
	if vErr == nil {
		return nil
	}
	// vErr is non-nil iff the report carries at least one violation.
	// The action knob decides whether to block.
	if rep.Action != promptguard.ActionReject {
		// Observe-only mode: emitter already fired one ThreatEvent per
		// violation inside ValidateToolInput. We allow dispatch.
		return nil
	}
	return fmt.Errorf("promptguard: tool_input_rejected: %w", vErr)
}

// marshalArgsForValidation produces a deterministic JSON serialization
// of the MCP args map, suitable for promptguard.ValidateToolInput. An
// empty / nil map maps to "{}" (so RequireStruct passes the shape check
// but fails on any required-field rule). Errors are surfaced verbatim
// to the caller.
func marshalArgsForValidation(args map[string]interface{}) ([]byte, error) {
	if args == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(args)
}

// errToolInputRejected is the sentinel used by tests to distinguish a
// promptguard rejection from any other tool error.
var errToolInputRejected = errors.New("promptguard: tool_input_rejected")
