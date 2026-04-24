// Package a2a — httpserver.go
//
// STOKE-013/-018 HTTP transport. Wraps the Agent Card
// generator + Task handlers in a net/http-compatible
// handler so operators can serve /.well-known/agent.json
// and /a2a/rpc from a single mux — either via the
// standalone cmd/stoke-a2a/ binary or by mounting into an
// existing HTTP server via (*Server).Handler().
//
// Routes:
//
//   GET  /.well-known/agent-card.json  → the Agent Card, JSON-encoded (A2A v1.0 canonical)
//   GET  /.well-known/agent.json       → HTTP 308 Permanent Redirect to the
//                                         canonical path (legacy, kept alive
//                                         for 30 days after v1.0 landing;
//                                         sunset 2026-05-22)
//   POST /a2a/rpc                      → JSON-RPC 2.0 dispatch to
//                                         a2a.task.submit / .status / .cancel
//   GET  /healthz                      → simple liveness check
//
// Auth: optional bearer token gate via `Authorization: Bearer
// <token>` on the /a2a/rpc endpoint. The agent card is ALWAYS
// served without auth (discovery must be open) — the card
// itself declares the auth scheme callers need to use for
// task submission.
package a2a

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JSONRPC-2.0 error codes matching the spec.
const (
	RPCParseError     = -32700
	RPCInvalidRequest = -32600
	RPCMethodNotFound = -32601
	RPCInvalidParams  = -32602
	RPCInternalError  = -32603

	// A2A-specific: caller supplied a bad / absent bearer
	// token on a gated endpoint.
	RPCUnauthorized = -32000
)

// Server mounts A2A routes on an http.ServeMux.
//
// Thread-safety: the card can be updated live via SetCard
// under a write lock; concurrent readers get the snapshot
// via the RLock path.
type Server struct {
	mu       sync.RWMutex
	card     AgentCard
	store    TaskStore
	token    string // empty = no auth on /a2a/rpc
	handlers http.Handler
}

// CanonicalCardPath is the A2A v1.0 canonical Agent Card URL.
// Peers SHOULD fetch this path first; the legacy path below
// 308-redirects here during the 30-day migration window.
const CanonicalCardPath = "/.well-known/agent-card.json"

// LegacyCardPath is the A2A v0.x Agent Card URL retained for 30
// days after v1.0.0 lands. Returns HTTP 308 Permanent Redirect
// to CanonicalCardPath. Removal target: 2026-05-22.
const LegacyCardPath = "/.well-known/agent.json"

// NewServer returns a Server with the given initial card +
// task store. Auth token is optional; pass "" for open dev
// mode.
//
// The returned Server's Handler() can be mounted into a
// larger mux via:
//   mux.Handle("/", a2aSrv.Handler())
// or the server can be run standalone via ListenAndServe.
func NewServer(card AgentCard, store TaskStore, token string) *Server {
	s := &Server{card: card, store: store, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc(CanonicalCardPath, s.handleCard)
	mux.HandleFunc(LegacyCardPath, s.handleLegacyCardRedirect)
	mux.HandleFunc("/a2a/rpc", s.handleRPC)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	s.handlers = mux
	return s
}

// Handler returns the registered mux, for callers who want
// to compose the A2A routes into a parent server.
func (s *Server) Handler() http.Handler { return s.handlers }

// SetCard atomically swaps the served Agent Card — used by
// operators on capability-set changes without restarting.
func (s *Server) SetCard(c AgentCard) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.card = c
}

// ListenAndServe runs a standalone A2A HTTP server on addr.
// Blocking; returns the http.Server error on shutdown.
//
// ReadHeaderTimeout guards against Slowloris — attackers drip header
// bytes one-at-a-time to hold connections open indefinitely. 10s is
// comfortably above any legitimate client (A2A agent-card responses
// are bounded JSON; clients send headers quickly) while capping the
// attack window.
func (s *Server) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.handlers,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server.ListenAndServe()
}

// handleLegacyCardRedirect serves a 308 Permanent Redirect from the
// A2A v0.x path (`/.well-known/agent.json`) to the v1.0 canonical
// path (`/.well-known/agent-card.json`). The `Deprecation` and
// `Sunset` headers signal the 30-day removal window per RFC 8594 /
// RFC 9745 so callers can log the deprecation in their own
// observability.
//
// 308 is used instead of 301/302 because it MUST preserve the
// request method (GET stays GET) and MUST NOT allow the client to
// silently rewrite to GET on POST — here the card is GET-only but
// the semantic is still correct: "this resource has permanently
// moved, preserve the method".
func (s *Server) handleLegacyCardRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// RFC 9745 Deprecation header = Unix timestamp of deprecation
	// landing; RFC 8594 Sunset header = HTTP-date of removal.
	// Deprecation: 2026-04-22T00:00:00Z (v1.0.0 landing).
	// Sunset:      2026-05-22T00:00:00Z (30-day window).
	w.Header().Set("Deprecation", `@1745280000`)
	w.Header().Set("Sunset", "Fri, 22 May 2026 00:00:00 GMT")
	w.Header().Set("Link", `<`+CanonicalCardPath+`>; rel="successor-version"`)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.Redirect(w, r, CanonicalCardPath, http.StatusPermanentRedirect)
}

// handleCard serves the Agent Card. Open access (no auth)
// so discovery always works.
func (s *Server) handleCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	card := s.card
	s.mu.RUnlock()
	b, err := card.ToJSON()
	if err != nil {
		http.Error(w, "card encode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// A2A spec allows cross-origin discovery; open CORS so
	// browsers and peer agents can consume the card.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(b)
}

// handleRPC dispatches JSON-RPC 2.0 method calls to the
// Task handlers. Methods:
//
//   a2a.task.submit   → HandleSubmit
//   a2a.task.status   → HandleStatus
//   a2a.task.cancel   → HandleCancel
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.token != "" {
		if !checkBearer(r.Header.Get("Authorization"), s.token) {
			writeRPCError(w, nil, RPCUnauthorized, "unauthorized")
			return
		}
	}
	// Decode into a map first so we can detect whether `id`
	// was actually present (JSON-RPC notifications have no
	// id and MUST NOT receive a response).
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&raw); err != nil {
		writeRPCError(w, nil, RPCParseError, "parse error: "+err.Error())
		return
	}
	// Reject trailing tokens after the first JSON value so
	// garbage like `{...}junk` can't slip through as a valid
	// request.
	if dec.More() {
		writeRPCError(w, nil, RPCParseError, "trailing content after JSON request")
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	// Re-marshal/unmarshal through the typed struct for
	// type-strictness on known fields.
	if b, _ := json.Marshal(raw); len(b) > 0 {
		_ = json.Unmarshal(b, &req)
	}
	_, hasID := raw["id"]
	if req.JSONRPC != "2.0" {
		if hasID {
			writeRPCError(w, req.ID, RPCInvalidRequest, "jsonrpc must be 2.0")
		}
		return
	}
	ctx := r.Context()
	// writeOK / writeErr are scoped so notifications (no id)
	// produce NO response, per JSON-RPC 2.0.
	writeOK := func(result any) {
		if !hasID {
			return
		}
		writeRPCResult(w, req.ID, result)
	}
	writeErr := func(code int, msg string) {
		if !hasID {
			return
		}
		writeRPCError(w, req.ID, code, msg)
	}
	switch req.Method {
	case "a2a.task.submit":
		t, err := HandleSubmit(ctx, s.store, req.Params)
		if err != nil {
			writeErr(RPCInvalidParams, err.Error())
			return
		}
		writeOK(t)
	case "a2a.task.status":
		t, err := HandleStatus(ctx, s.store, req.Params)
		if err != nil {
			writeErr(RPCInvalidParams, err.Error())
			return
		}
		writeOK(t)
	case "a2a.task.cancel":
		t, err := HandleCancel(ctx, s.store, req.Params)
		if err != nil {
			writeErr(RPCInvalidParams, err.Error())
			return
		}
		writeOK(t)
	default:
		writeErr(RPCMethodNotFound, "unknown method: "+req.Method)
	}
}

// checkBearer tests `Authorization: Bearer <token>` against
// the configured token. Case-insensitive on the "Bearer"
// prefix; exact match on the token itself.
func checkBearer(header, want string) bool {
	if header == "" {
		return false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return false
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	return parts[1] == want
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNil(id),
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNil(id),
		"error":   map[string]any{"code": code, "message": msg},
	})
}

// rawOrNil returns the raw JSON id, or json.RawMessage("null")
// when the id is empty — the JSON-RPC spec says responses
// must echo the request id, with null used for parse-level
// errors where the id couldn't be recovered.
func rawOrNil(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
