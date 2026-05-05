// jsonrpc/desktopapi.go — Phase E item 30: bind every desktopapi.Handler
// method to its JSON-RPC verb.
//
// The Handler interface (`internal/desktopapi.Handler`) carries the 11
// "long-trip" verbs from `desktop/IPC-CONTRACT.md` §2 — the ones that
// round-trip from the Tauri Rust host to the Go subprocess. The
// remaining 4 (`session.send`, `session.cancel`, `skill.list`,
// `skill.get`) live on the Tauri-only surface (§5).
//
// `RegisterDesktopAPI` reflects each method onto the dispatcher with a
// thin adapter that:
//
//  1. unmarshals params into the verb's typed request struct;
//  2. invokes the Handler method;
//  3. returns the typed response (the dispatcher marshals it back).
//
// Param-decode failures map to CodeInvalidParams. Handler returns flow
// through ErrorFromGo (which already handles stokerr/desktopapi
// sentinels). nil handler is a programmer error — RegisterDesktopAPI
// panics so misconfiguration surfaces at process start, not on the
// first inbound call.
package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/desktopapi"
)

// RegisterDesktopAPI installs all 11 IPC-CONTRACT §2 verbs on the
// dispatcher. The verb-to-method mapping mirrors the contract doc 1:1:
//
//	session.start         -> Handler.SessionStart
//	session.pause         -> Handler.SessionPause
//	session.resume        -> Handler.SessionResume
//	ledger.get_node       -> Handler.LedgerGetNode
//	ledger.list_events    -> Handler.LedgerListEvents
//	memory.list_scopes    -> Handler.MemoryListScopes
//	memory.query          -> Handler.MemoryQuery
//	cost.get_current      -> Handler.CostGetCurrent
//	cost.get_history      -> Handler.CostGetHistory
//	descent.current_tier  -> Handler.DescentCurrentTier
//	descent.tier_history  -> Handler.DescentTierHistory
//
// A nil dispatcher or handler panics (programmer error — wiring should
// never silently drop verbs).
func RegisterDesktopAPI(d *Dispatcher, h desktopapi.Handler) {
	if d == nil {
		panic("jsonrpc: RegisterDesktopAPI: nil dispatcher")
	}
	if h == nil {
		panic("jsonrpc: RegisterDesktopAPI: nil handler")
	}

	// session.* — three verbs that all take SessionStartRequest /
	// SessionIDRequest. The error path uses the same ErrorFromGo
	// translation as every other verb.
	d.Register("session.start", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.SessionStartRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.SessionStart(ctx, req)
	})
	d.Register("session.pause", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.SessionIDRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.SessionPause(ctx, req)
	})
	d.Register("session.resume", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.SessionIDRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.SessionResume(ctx, req)
	})

	// ledger.* — two read-only inspection verbs.
	d.Register("ledger.get_node", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.LedgerGetNodeRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.LedgerGetNode(ctx, req)
	})
	d.Register("ledger.list_events", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.LedgerListEventsRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.LedgerListEvents(ctx, req)
	})

	// memory.* — list_scopes takes no params; we still accept (and
	// discard) any provided params object so old clients that pass
	// `{}` don't error.
	d.Register("memory.list_scopes", func(ctx context.Context, params json.RawMessage) (any, error) {
		return h.MemoryListScopes(ctx)
	})
	d.Register("memory.query", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.MemoryQueryRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.MemoryQuery(ctx, req)
	})

	// cost.* — two verbs.
	d.Register("cost.get_current", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.CostGetCurrentRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.CostGetCurrent(ctx, req)
	})
	d.Register("cost.get_history", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.CostGetHistoryRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.CostGetHistory(ctx, req)
	})

	// descent.* — two verbs.
	d.Register("descent.current_tier", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.DescentCurrentTierRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DescentCurrentTier(ctx, req)
	})
	d.Register("descent.tier_history", func(ctx context.Context, params json.RawMessage) (any, error) {
		var req desktopapi.DescentTierHistoryRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return h.DescentTierHistory(ctx, req)
	})
}

// decodeParams unmarshals raw params into v. Empty params is allowed —
// every verb's request struct uses omitempty/optional fields so the
// zero value is a valid call (e.g. `cost.get_current` with no
// session_id returns the global tally).
//
// Failure produces a *stokerr-shaped error via the dispatcher's
// CodeInvalidParams path: we wrap with %w on json.Unmarshal so the
// error chain stays useful for tests.
func decodeParams(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return invalidParamsError(err)
	}
	return nil
}

// invalidParamsError builds an error that ErrorFromGo will map to
// CodeInvalidParams when surfaced through the dispatch layer. We don't
// import stokerr.Validation here because that maps to CodeValidation
// (-32001), and JSON-RPC §5.1 reserves -32602 for "invalid params" —
// distinct semantics. Using a sentinel error type keeps the mapping
// local to this package.
func invalidParamsError(cause error) error {
	return &paramsError{cause: cause}
}

// paramsError is the sentinel for params-decode failures. Implements
// error and exposes the cause via Unwrap so errors.As keeps working.
type paramsError struct {
	cause error
}

func (p *paramsError) Error() string { return fmt.Sprintf("invalid params: %v", p.cause) }
func (p *paramsError) Unwrap() error { return p.cause }
