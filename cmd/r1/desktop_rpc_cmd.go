// desktop_rpc_cmd.go — `r1 desktop-rpc` JSON-RPC 2.0 server (R1D-1.2).
//
// The Tauri Rust host (Tier 2) spawns this process once per desktop
// session. It reads NDJSON JSON-RPC 2.0 requests from stdin and writes
// NDJSON responses to stdout. Server-pushed events (session.delta,
// ledger.appended, cost.tick, etc.) are also written to stdout as
// NDJSON lines with an "event" field instead of "id"/"result"/"error".
//
// The process exits when stdin is closed (Tauri cancelled the session)
// or when it receives `session.cancel` via RPC.
//
// Wire contract: desktop/IPC-CONTRACT.md.
// Handler interface: internal/desktopapi.Handler.
//
// Scaffold note (R1D-1.4): desktopapi.NotImplemented{} is wired here.
// Per-verb real implementations land in later R1D-* phases.
// The Tauri host handles the not_implemented error code gracefully by
// rendering a "coming soon" state in the affected panel.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/desktopapi"
	"github.com/RelayOne/r1/internal/stokerr"
)

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 envelope types
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErrorBody   `json:"error,omitempty"`
}

type rpcErrorBody struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    rpcErrorData `json:"data"`
}

type rpcErrorData struct {
	StokeCode string `json:"stoke_code"`
}

// ---------------------------------------------------------------------------
// runDesktopRPCCmd entry point
// ---------------------------------------------------------------------------

func runDesktopRPCCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("desktop-rpc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sessionID string
	fs.StringVar(&sessionID, "session-id", "", "session ID assigned by the Tauri host")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "desktop-rpc: %v\n", err)
		return 2
	}

	if sessionID == "" {
		sessionID = fmt.Sprintf("desktop-%d", time.Now().UnixNano())
	}

	srv := &desktopRPCServer{
		sessionID: sessionID,
		handler:   &desktopapi.NotImplemented{},
		stdout:    stdout,
		stderr:    stderr,
	}

	return srv.serve(stdin)
}

// ---------------------------------------------------------------------------
// desktopRPCServer — reads stdin, dispatches, writes stdout
// ---------------------------------------------------------------------------

type desktopRPCServer struct {
	sessionID string
	handler   desktopapi.Handler
	stdout    io.Writer
	stderr    io.Writer
}

func (s *desktopRPCServer) serve(stdin io.Reader) int {
	// Announce readiness so the Tauri host knows the process is live.
	s.pushEvent("session.started", map[string]any{
		"session_id": s.sessionID,
		"at":         time.Now().UTC().Format(time.RFC3339),
	})

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.writeError(nil, -32700, "parse_error",
				fmt.Sprintf("JSON parse error: %v", err))
			continue
		}
		if req.JSONRPC != "2.0" {
			s.writeError(req.ID, -32600, "invalid_request",
				`jsonrpc field must be "2.0"`)
			continue
		}
		s.dispatch(req)
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(s.stderr, "desktop-rpc stdin error: %v\n", err)
		return 1
	}

	s.pushEvent("session.ended", map[string]any{
		"session_id": s.sessionID,
		"reason":     "ok",
		"at":         time.Now().UTC().Format(time.RFC3339),
	})
	return 0
}

// dispatch routes a parsed RPC request to the matching Handler method.
func (s *desktopRPCServer) dispatch(req rpcRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	switch req.Method {
	// --- Session control (§2.1) ---
	case "session.start":
		var p desktopapi.SessionStartRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.SessionStart(ctx, p)
		s.respond(req.ID, res, err)

	case "session.pause":
		var p desktopapi.SessionIDRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.SessionPause(ctx, p)
		s.respond(req.ID, res, err)

	case "session.resume":
		var p desktopapi.SessionIDRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.SessionResume(ctx, p)
		s.respond(req.ID, res, err)

	// session.send is handled directly by the Tauri host (§5 Tauri-only
	// verbs); if it arrives here via the JSON-RPC path, treat it as a
	// no-op success since the prompt was already consumed from stdin.
	case "session.send":
		s.respond(req.ID, struct{}{}, nil)

	case "session.cancel":
		// Graceful self-termination: flush session.ended then exit.
		s.pushEvent("session.ended", map[string]any{
			"session_id": s.sessionID,
			"reason":     "cancelled",
			"at":         time.Now().UTC().Format(time.RFC3339),
		})
		s.respond(req.ID, struct{}{}, nil)
		os.Exit(0)

	// --- Ledger query (§2.2) ---
	case "ledger.get_node":
		var p desktopapi.LedgerGetNodeRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.LedgerGetNode(ctx, p)
		s.respond(req.ID, res, err)

	case "ledger.list_events":
		var p desktopapi.LedgerListEventsRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.LedgerListEvents(ctx, p)
		s.respond(req.ID, res, err)

	// --- Memory inspection (§2.3) ---
	case "memory.list_scopes":
		res, err := s.handler.MemoryListScopes(ctx)
		s.respond(req.ID, res, err)

	case "memory.query":
		var p desktopapi.MemoryQueryRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.MemoryQuery(ctx, p)
		s.respond(req.ID, res, err)

	// --- Cost (§2.4) ---
	case "cost.get_current":
		var p desktopapi.CostGetCurrentRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.CostGetCurrent(ctx, p)
		s.respond(req.ID, res, err)

	case "cost.get_history":
		var p desktopapi.CostGetHistoryRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.CostGetHistory(ctx, p)
		s.respond(req.ID, res, err)

	// --- Descent state (§2.5) ---
	case "descent.current_tier":
		var p desktopapi.DescentCurrentTierRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.DescentCurrentTier(ctx, p)
		s.respond(req.ID, res, err)

	case "descent.tier_history":
		var p desktopapi.DescentTierHistoryRequest
		if !s.parseParams(req, &p) {
			return
		}
		res, err := s.handler.DescentTierHistory(ctx, p)
		s.respond(req.ID, res, err)

	// Tauri-only skill verbs: return not_implemented so the Rust cache
	// layer falls back to its local logic.
	case "skill.list", "skill.get":
		s.writeError(req.ID, -32010, "not_implemented",
			req.Method+": handled by Tauri host skill cache")

	default:
		s.writeError(req.ID, -32601, "method_not_found",
			fmt.Sprintf("method not found: %s", req.Method))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// parseParams unmarshals req.Params into dst; on error writes an RPC error
// and returns false.
func (s *desktopRPCServer) parseParams(req rpcRequest, dst any) bool {
	if len(req.Params) == 0 || string(req.Params) == "null" {
		return true // empty params OK for methods with all-optional fields
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		s.writeError(req.ID, -32602, "invalid_params",
			fmt.Sprintf("params parse error: %v", err))
		return false
	}
	return true
}

// respond writes either a success or error response for id.
func (s *desktopRPCServer) respond(id json.RawMessage, result any, err error) {
	if err != nil {
		code, stoke, msg := errorToRPCCode(err)
		s.writeError(id, code, stoke, msg)
		return
	}
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	s.writeLine(resp)
}

// writeError writes a JSON-RPC error response.
func (s *desktopRPCServer) writeError(
	id json.RawMessage,
	code int,
	stokeCode string,
	message string,
) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcErrorBody{
			Code:    code,
			Message: message,
			Data:    rpcErrorData{StokeCode: stokeCode},
		},
	}
	s.writeLine(resp)
}

// pushEvent writes a server-pushed NDJSON event line to stdout.
func (s *desktopRPCServer) pushEvent(event string, payload map[string]any) {
	payload["event"] = event
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintln(s.stdout, string(b))
}

// writeLine serialises v to JSON and writes it as a single NDJSON line.
func (s *desktopRPCServer) writeLine(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(s.stderr, "desktop-rpc marshal error: %v\n", err)
		return
	}
	fmt.Fprintln(s.stdout, string(b))
}

// errorToRPCCode maps a Go error to a JSON-RPC (code, stokeCode, message) triple.
func errorToRPCCode(err error) (code int, stokeCode, message string) {
	var se *stokerr.Error
	if ok := asStorkeErr(err, &se); !ok {
		return -32603, "internal", err.Error()
	}
	switch se.Code {
	case stokerr.Code("not_implemented"):
		return -32010, "not_implemented", se.Error()
	case stokerr.ErrValidation:
		return -32001, "validation", se.Error()
	case stokerr.ErrNotFound:
		return -32002, "not_found", se.Error()
	case stokerr.ErrConflict:
		return -32003, "conflict", se.Error()
	case stokerr.ErrAppendOnly:
		return -32004, "append_only_violation", se.Error()
	case stokerr.ErrPermission:
		return -32005, "permission_denied", se.Error()
	case stokerr.ErrBudgetExceeded:
		return -32006, "budget_exceeded", se.Error()
	case stokerr.ErrTimeout:
		return -32007, "timeout", se.Error()
	case stokerr.ErrCrashRecovery:
		return -32008, "crash_recovery", se.Error()
	case stokerr.ErrSchemaVersion:
		return -32009, "schema_version", se.Error()
	default:
		return -32099, "internal", se.Error()
	}
}

// asStorkeErr attempts a type assertion to *stokerr.Error.
func asStorkeErr(err error, target **stokerr.Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*stokerr.Error)
	if ok {
		*target = e
	}
	return ok
}
