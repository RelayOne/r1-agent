// Package jsonrpc implements the JSON-RPC 2.0 envelope format used by the
// r1d-server multi-session daemon (specs/r1d-server.md Phase E item 28).
//
// # Envelope
//
// Per `desktop/IPC-CONTRACT.md` §1 the wire shapes are:
//
//	request:       { "jsonrpc": "2.0", "id": <id>, "method": <verb>, "params": <obj> }
//	notification:  { "jsonrpc": "2.0",            "method": <verb>, "params": <obj> }
//	response (ok): { "jsonrpc": "2.0", "id": <id>, "result": <obj> }
//	response (err):{ "jsonrpc": "2.0", "id": <id>, "error":  { "code": int, "message": string, "data": <obj> } }
//
// "id" is `string | number | null`. We store it as `json.RawMessage` so the
// caller's chosen type is echoed back byte-for-byte (per JSON-RPC 2.0 §5).
//
// Notifications are requests with no `id` field. The dispatcher routes them
// to the same handler chain as requests but DROPS the response (a
// notification has no callback, by definition).
//
// Batch is supported per spec §6: an incoming `[ ... ]` array is decoded as
// N envelopes; the dispatcher routes each in sequence and returns an array
// of N responses (skipping notifications, which produce no response).
//
// # Error codes
//
// Per `desktop/IPC-CONTRACT.md` §3 we map the standard JSON-RPC reserved
// codes (-32700..-32603) plus the R1 application range (-32001..-32099).
// Every application error carries `data.stoke_code` so clients can
// pattern-match on the taxonomy string instead of the numeric id.
//
// # Why a dedicated package
//
// `internal/server/ws/handler.go` (TASK-29) and the SSE bridge (TASK-33)
// both speak JSON-RPC envelopes. Putting the shape + codec here keeps
// transport packages small and lets test suites round-trip the wire format
// without booting an HTTP server. See dispatch_test.go.
package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/RelayOne/r1/internal/desktopapi"
	"github.com/RelayOne/r1/internal/stokerr"
)

// Version is the JSON-RPC protocol version literal that MUST appear on every
// envelope. Anything else is a parse-time invariant violation.
const Version = "2.0"

// ----------------------------------------------------------------------
// Envelope shapes
// ----------------------------------------------------------------------

// Request is the inbound envelope. ID is omitted for notifications. We use
// json.RawMessage for ID and Params so the caller's bytes survive a round
// trip without a re-encode (preserves number-vs-string distinction for ID,
// preserves field order in error.data for deterministic test goldens).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the request is a notification (no id).
// Per JSON-RPC 2.0 §4.1, a notification's id field is absent (NOT
// `id: null` — that's a regular request whose caller chose null).
func (r *Request) IsNotification() bool {
	return len(r.ID) == 0
}

// Response is the outbound envelope. Exactly one of Result / Error MUST be
// non-nil per JSON-RPC 2.0 §5. The dispatch layer enforces that invariant.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the error sub-object. Code is the numeric JSON-RPC error code;
// Message is a human-readable summary; Data is the optional structured
// payload (we put `stoke_code` here per IPC-CONTRACT §3.2).
type Error struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// ----------------------------------------------------------------------
// Error codes (IPC-CONTRACT.md §3)
// ----------------------------------------------------------------------

// Standard JSON-RPC 2.0 reserved codes (§5.1).
const (
	CodeParseError     = -32700 // malformed JSON
	CodeInvalidRequest = -32600 // envelope wrong shape
	CodeMethodNotFound = -32601 // unknown method
	CodeInvalidParams  = -32602 // params don't match method signature
	CodeInternalError  = -32603 // unexpected server-side failure
)

// R1 application codes (-32000..-32099), mapped from internal/stokerr.
// IPC-CONTRACT.md §3.2 is the source of truth — these constants MUST stay
// in sync with that table.
const (
	CodeValidation        = -32001 // stokerr.ErrValidation
	CodeNotFound          = -32002 // stokerr.ErrNotFound
	CodeConflict          = -32003 // stokerr.ErrConflict
	CodeAppendOnly        = -32004 // stokerr.ErrAppendOnly
	CodePermissionDenied  = -32005 // stokerr.ErrPermission
	CodeBudgetExceeded    = -32006 // stokerr.ErrBudgetExceeded
	CodeTimeout           = -32007 // stokerr.ErrTimeout
	CodeCrashRecovery     = -32008 // stokerr.ErrCrashRecovery
	CodeSchemaVersion     = -32009 // stokerr.ErrSchemaVersion
	CodeNotImplemented    = -32010 // desktopapi.ErrNotImplemented sentinel
	CodeInternalTaxonomy  = -32099 // stokerr.ErrInternal catch-all
)

// stokeCodeFor maps a stokerr.Code to its numeric JSON-RPC code. The
// fallback is CodeInternalTaxonomy because an unknown taxonomy string
// always indicates a bug (a stokerr.Code added without updating this
// mapping, which the unit tests catch).
func stokeCodeFor(c stokerr.Code) int {
	switch c {
	case stokerr.ErrValidation:
		return CodeValidation
	case stokerr.ErrNotFound:
		return CodeNotFound
	case stokerr.ErrConflict:
		return CodeConflict
	case stokerr.ErrAppendOnly:
		return CodeAppendOnly
	case stokerr.ErrPermission:
		return CodePermissionDenied
	case stokerr.ErrBudgetExceeded:
		return CodeBudgetExceeded
	case stokerr.ErrTimeout:
		return CodeTimeout
	case stokerr.ErrCrashRecovery:
		return CodeCrashRecovery
	case stokerr.ErrSchemaVersion:
		return CodeSchemaVersion
	case stokerr.ErrInternal:
		return CodeInternalTaxonomy
	}
	// "not_implemented" lives outside the stokerr canonical taxonomy but
	// is registered as a *stokerr.Error code by desktopapi. Match by
	// raw string so we don't have to re-export the constant from
	// desktopapi.
	if string(c) == "not_implemented" {
		return CodeNotImplemented
	}
	return CodeInternalTaxonomy
}

// ErrorFromGo translates an arbitrary Go error to a JSON-RPC error.
//
//   - *paramsError    -> CodeInvalidParams (jsonrpc-internal sentinel for
//     params decode failures from RegisterDesktopAPI's adapters; -32602
//     per JSON-RPC §5.1)
//   - *stokerr.Error  -> taxonomy-mapped numeric code, data.stoke_code = .Code
//   - desktopapi.ErrNotImplemented (matched via errors.Is) -> CodeNotImplemented
//   - any other       -> CodeInternalError, data.stoke_code = "internal"
//
// Returns nil if err is nil so callers can wrap unconditionally.
func ErrorFromGo(err error) *Error {
	if err == nil {
		return nil
	}
	// Params decode failure — must come BEFORE the stokerr.Error path
	// because paramsError doesn't carry a stokerr.Code (it's a
	// transport-layer concern, not an application taxonomy).
	var pe *paramsError
	if errors.As(err, &pe) {
		return &Error{
			Code:    CodeInvalidParams,
			Message: err.Error(),
			Data:    map[string]any{"stoke_code": "validation"},
		}
	}
	// errors.Is for the desktopapi sentinel — it's a *stokerr.Error so
	// the type-assert path also handles it, but we keep the explicit
	// branch so the ladder is documented.
	if errors.Is(err, desktopapi.ErrNotImplemented) {
		return &Error{
			Code:    CodeNotImplemented,
			Message: err.Error(),
			Data:    map[string]any{"stoke_code": "not_implemented"},
		}
	}
	var se *stokerr.Error
	if errors.As(err, &se) {
		return &Error{
			Code:    stokeCodeFor(se.Code),
			Message: se.Error(),
			Data:    map[string]any{"stoke_code": string(se.Code)},
		}
	}
	return &Error{
		Code:    CodeInternalError,
		Message: err.Error(),
		Data:    map[string]any{"stoke_code": "internal"},
	}
}

// NewError builds a *Error directly. Used by transport layers for shape
// errors (parse error, invalid request) that don't have a Go error chain.
func NewError(code int, message string) *Error {
	stoke := stokeCodeForJSONRPC(code)
	return &Error{
		Code:    code,
		Message: message,
		Data:    map[string]any{"stoke_code": stoke},
	}
}

// stokeCodeForJSONRPC reverses stokeCodeFor for the few codes that have a
// canonical taxonomy mirror. Used by NewError so even hand-built errors
// carry the data.stoke_code field for client pattern-matching.
func stokeCodeForJSONRPC(code int) string {
	switch code {
	case CodeParseError, CodeInvalidRequest:
		return "validation"
	case CodeMethodNotFound:
		return "not_found"
	case CodeInvalidParams:
		return "validation"
	case CodeInternalError, CodeInternalTaxonomy:
		return "internal"
	case CodeValidation:
		return "validation"
	case CodeNotFound:
		return "not_found"
	case CodeConflict:
		return "conflict"
	case CodeAppendOnly:
		return "append_only_violation"
	case CodePermissionDenied:
		return "permission_denied"
	case CodeBudgetExceeded:
		return "budget_exceeded"
	case CodeTimeout:
		return "timeout"
	case CodeCrashRecovery:
		return "crash_recovery"
	case CodeSchemaVersion:
		return "schema_version"
	case CodeNotImplemented:
		return "not_implemented"
	}
	return "internal"
}

// ----------------------------------------------------------------------
// Method handler interface
// ----------------------------------------------------------------------

// MethodFunc is the dispatcher callback for one JSON-RPC method. It
// receives the decoded params (json.RawMessage so the handler can pick its
// own concrete type) and returns either a marshal-able result or an error.
//
// Handlers MAY return:
//
//   - a *stokerr.Error: dispatch maps the code via stokeCodeFor.
//   - desktopapi.ErrNotImplemented: dispatch maps to CodeNotImplemented.
//   - any other error: dispatch maps to CodeInternalError.
//
// Handlers MUST NOT return both a non-nil result and a non-nil error.
type MethodFunc func(ctx context.Context, params json.RawMessage) (any, error)

// Dispatcher routes JSON-RPC requests to registered method handlers.
// Concurrent-safe to register methods at construction time and dispatch
// from many goroutines afterward; the method map is treated as
// immutable post-Register (we don't lock on dispatch — the WS handler
// dispatches from a single reader goroutine per connection).
type Dispatcher struct {
	methods map[string]MethodFunc
}

// NewDispatcher constructs an empty dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{methods: make(map[string]MethodFunc)}
}

// Register installs a handler for one method name. Replaces any prior
// registration for the same name (last-write-wins so transport tests can
// stub real handlers without unregister calls).
func (d *Dispatcher) Register(method string, fn MethodFunc) {
	d.methods[method] = fn
}

// HasMethod reports whether a method is registered. Useful for the SSE
// bridge which exposes only a subset of verbs.
func (d *Dispatcher) HasMethod(method string) bool {
	_, ok := d.methods[method]
	return ok
}

// Dispatch routes one decoded request to the matching handler and
// returns the response envelope. For notifications, returns nil — the
// caller MUST NOT write any response.
//
// Dispatch never panics on a known handler error; an unknown handler
// returns CodeMethodNotFound. A handler panic is recovered and turned
// into CodeInternalError so a buggy verb can't crash the daemon.
func (d *Dispatcher) Dispatch(ctx context.Context, req *Request) *Response {
	if req == nil {
		// Defensive: caller should never hand us nil. Treat as parse.
		return &Response{JSONRPC: Version, Error: NewError(CodeInvalidRequest, "nil request")}
	}
	if req.JSONRPC != Version {
		if req.IsNotification() {
			return nil
		}
		return &Response{JSONRPC: Version, ID: req.ID, Error: NewError(CodeInvalidRequest, fmt.Sprintf("jsonrpc version %q not supported", req.JSONRPC))}
	}
	if req.Method == "" {
		if req.IsNotification() {
			return nil
		}
		return &Response{JSONRPC: Version, ID: req.ID, Error: NewError(CodeInvalidRequest, "method required")}
	}

	fn, ok := d.methods[req.Method]
	if !ok {
		if req.IsNotification() {
			// Per JSON-RPC §4.1, server should NOT respond to a
			// notification, even on method-not-found. Drop silently.
			return nil
		}
		return &Response{JSONRPC: Version, ID: req.ID, Error: NewError(CodeMethodNotFound, "method not found: "+req.Method)}
	}

	result, err := safeInvoke(ctx, fn, req.Params)
	if req.IsNotification() {
		// Drop the result. Per spec, a notification gets no response
		// even on success.
		return nil
	}
	if err != nil {
		return &Response{JSONRPC: Version, ID: req.ID, Error: ErrorFromGo(err)}
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return &Response{JSONRPC: Version, ID: req.ID, Error: NewError(CodeInternalError, "marshal result: "+marshalErr.Error())}
	}
	return &Response{JSONRPC: Version, ID: req.ID, Result: encoded}
}

// safeInvoke runs a MethodFunc inside a recover so a panicking handler
// turns into CodeInternalError instead of crashing the daemon.
func safeInvoke(ctx context.Context, fn MethodFunc, params json.RawMessage) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
	return fn(ctx, params)
}

// ----------------------------------------------------------------------
// Wire codec — DecodeRequest / EncodeResponse / batch helpers
// ----------------------------------------------------------------------

// DecodeRequest parses a single envelope from raw bytes. Use
// DecodeBatchOrSingle when the wire allows either a single envelope or a
// batch array.
func DecodeRequest(b []byte) (*Request, error) {
	var req Request
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// EncodeResponse marshals a response envelope to bytes. Returns an error
// only on a malformed *Response (programmer error).
func EncodeResponse(r *Response) ([]byte, error) {
	if r == nil {
		return nil, errors.New("jsonrpc: nil response")
	}
	if r.JSONRPC == "" {
		r.JSONRPC = Version
	}
	return json.Marshal(r)
}

// DecodeBatchOrSingle parses bytes as either a single Request or a batch
// of Requests. Returns (single, nil, nil) for a single envelope and
// (nil, batch, nil) for an array. An empty batch is rejected per
// JSON-RPC 2.0 §6 ("rpc call with an empty Array" -> Invalid Request).
func DecodeBatchOrSingle(b []byte) (*Request, []*Request, error) {
	// Trim leading whitespace cheaply (json.Unmarshal would tolerate it
	// but we need the first non-whitespace byte to discriminate).
	first := firstNonWS(b)
	if first == 0 {
		return nil, nil, errors.New("jsonrpc: empty payload")
	}
	if first == '[' {
		var batch []*Request
		if err := json.Unmarshal(b, &batch); err != nil {
			return nil, nil, err
		}
		if len(batch) == 0 {
			return nil, nil, errors.New("jsonrpc: empty batch")
		}
		return nil, batch, nil
	}
	req, err := DecodeRequest(b)
	if err != nil {
		return nil, nil, err
	}
	return req, nil, nil
}

// firstNonWS returns the first non-whitespace byte of b, or 0 when b is
// all whitespace.
func firstNonWS(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c
		}
	}
	return 0
}

// DispatchBatch routes a batch of envelopes and returns the responses.
// Notifications are filtered out of the response slice (a batch of pure
// notifications produces a nil slice, signalling the transport layer to
// skip the response write entirely — JSON-RPC 2.0 §6).
func (d *Dispatcher) DispatchBatch(ctx context.Context, reqs []*Request) []*Response {
	var out []*Response
	for _, req := range reqs {
		resp := d.Dispatch(ctx, req)
		if resp == nil {
			continue
		}
		out = append(out, resp)
	}
	return out
}

// EncodeBatch marshals a batch of responses. Empty input returns
// `[]byte("null")` so callers writing to a stream can branch on the
// output (and tests can verify no-response semantics).
func EncodeBatch(resps []*Response) ([]byte, error) {
	if len(resps) == 0 {
		return []byte("null"), nil
	}
	return json.Marshal(resps)
}

// ----------------------------------------------------------------------
// Notification helper — used by the WS handler to push $/event frames
// ----------------------------------------------------------------------

// Notification is the outbound notification envelope. Mirrors Request
// but distinct on the wire because outbound notifications are encoded
// here (the inbound Request type doubles for both).
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// NewNotification builds a notification envelope.
func NewNotification(method string, params any) *Notification {
	return &Notification{JSONRPC: Version, Method: method, Params: params}
}

// EncodeNotification marshals a notification envelope to bytes.
func EncodeNotification(n *Notification) ([]byte, error) {
	if n == nil {
		return nil, errors.New("jsonrpc: nil notification")
	}
	if n.JSONRPC == "" {
		n.JSONRPC = Version
	}
	return json.Marshal(n)
}
