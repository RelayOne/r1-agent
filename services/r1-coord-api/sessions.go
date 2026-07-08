// sessions.go — the central active-session registry.
//
// Every r1 daemon reports its sessions here (POST /v1/sessions/report,
// JWT-gated) and operators list them (GET /v1/sessions, operator-role
// gated). "Active" is defined by a heartbeat TTL: a session that has not
// re-reported within the TTL is dropped from the listing, and a session
// reported with a terminal status (completed/failed/killed) is removed
// immediately. The registry is in-process state: it holds live presence,
// not history — a restart simply waits one heartbeat interval for daemons
// to re-populate it. Durable session history stays in each daemon's own
// session store (internal/session); this endpoint is the fleet-wide
// "what is running right now" view the admin panel renders.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1-coord-api/internal/auth"
)

// sessionStatuses is the closed set of statuses a report may carry.
// active/idle keep the row alive; completed/failed/killed remove it.
var sessionStatuses = map[string]bool{
	"active":    true,
	"idle":      true,
	"completed": true,
	"failed":    true,
	"killed":    true,
}

func isTerminalSessionStatus(s string) bool {
	return s == "completed" || s == "failed" || s == "killed"
}

// sessionRow is one active session as reported by a daemon and rendered
// by GET /v1/sessions — one row per active session.
type sessionRow struct {
	DaemonID     string    `json:"daemon_id"`
	SessionID    string    `json:"session_id"`
	Workdir      string    `json:"workdir,omitempty"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	LastActivity time.Time `json:"last_activity"`
	CostUSD      float64   `json:"cost_usd"`
}

func (r sessionRow) key() string { return r.DaemonID + "\x00" + r.SessionID }

// sessionRegistry is a TTL-based presence store keyed by
// (daemon_id, session_id). All methods are safe for concurrent use.
type sessionRegistry struct {
	mu   sync.Mutex
	rows map[string]sessionRow
	ttl  time.Duration
	now  func() time.Time // injectable for tests
}

func newSessionRegistry(ttl time.Duration) *sessionRegistry {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &sessionRegistry{
		rows: make(map[string]sessionRow),
		ttl:  ttl,
		now:  time.Now,
	}
}

// Report upserts a session row. A terminal status removes the row (the
// session is no longer active, so it must not appear in the listing).
func (s *sessionRegistry) Report(row sessionRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isTerminalSessionStatus(row.Status) {
		delete(s.rows, row.key())
		return
	}
	if prev, ok := s.rows[row.key()]; ok {
		// Preserve the original start time across heartbeats that omit it.
		if row.StartedAt.IsZero() {
			row.StartedAt = prev.StartedAt
		}
		if row.Workdir == "" {
			row.Workdir = prev.Workdir
		}
	}
	s.rows[row.key()] = row
}

// Active prunes expired rows and returns the remaining ones in a
// deterministic order: most recent activity first, key as tiebreaker.
func (s *sessionRegistry) Active() []sessionRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]sessionRow, 0, len(s.rows))
	for k, r := range s.rows {
		if now.Sub(r.LastActivity) > s.ttl {
			delete(s.rows, k)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActivity.Equal(out[j].LastActivity) {
			return out[i].LastActivity.After(out[j].LastActivity)
		}
		return out[i].key() < out[j].key()
	})
	return out
}

// sessionReportRequest is the POST /v1/sessions/report body. Timestamps
// are RFC3339 strings; last_activity defaults to the server clock so a
// heartbeat body can be minimal.
type sessionReportRequest struct {
	DaemonID     string  `json:"daemon_id"`
	SessionID    string  `json:"session_id"`
	Workdir      string  `json:"workdir"`
	Status       string  `json:"status"`
	StartedAt    string  `json:"started_at"`
	LastActivity string  `json:"last_activity"`
	CostUSD      float64 `json:"cost_usd"`
}

// validate applies the report schema. It returns the parsed row or a
// human-readable reason for the 400.
func (req sessionReportRequest) validate(now time.Time) (sessionRow, string) {
	if strings.TrimSpace(req.DaemonID) == "" {
		return sessionRow{}, "daemon_id is required"
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return sessionRow{}, "session_id is required"
	}
	status := req.Status
	if status == "" {
		status = "active"
	}
	if !sessionStatuses[status] {
		return sessionRow{}, "status must be one of: active, idle, completed, failed, killed"
	}
	if req.CostUSD < 0 {
		return sessionRow{}, "cost_usd must be >= 0"
	}
	row := sessionRow{
		DaemonID:     strings.TrimSpace(req.DaemonID),
		SessionID:    strings.TrimSpace(req.SessionID),
		Workdir:      req.Workdir,
		Status:       status,
		CostUSD:      req.CostUSD,
		LastActivity: now,
	}
	if req.StartedAt != "" {
		t, err := time.Parse(time.RFC3339, req.StartedAt)
		if err != nil {
			return sessionRow{}, "started_at must be RFC3339"
		}
		row.StartedAt = t
	}
	if req.LastActivity != "" {
		t, err := time.Parse(time.RFC3339, req.LastActivity)
		if err != nil {
			return sessionRow{}, "last_activity must be RFC3339"
		}
		row.LastActivity = t
	}
	return row, ""
}

// hasOperatorRole reports whether the verified claims carry the operator
// role. GET /v1/sessions is an operator surface: an authenticated
// non-operator token (e.g. a plain member SSO login) must not enumerate
// the fleet's sessions.
func hasOperatorRole(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	for _, r := range claims.Roles {
		if r == "operator" {
			return true
		}
	}
	return false
}

// handleSessionsReport ingests daemon heartbeats. JWT-gated by the
// middleware (any authenticated principal may report its own sessions);
// the body is schema-validated before it touches the registry.
func handleSessionsReport(reg *sessionRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		var req sessionReportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON body"})
			return
		}
		row, reason := req.validate(reg.now())
		if reason != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
			return
		}
		reg.Report(row)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": true})
	}
}

// handleSessionsList serves GET /v1/sessions: operator-JWT-gated,
// paginated (page >= 1, 1 <= page_size <= 200, default 50), one row per
// active session, deterministic order (most recent activity first).
func handleSessionsList(reg *sessionRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		if !hasOperatorRole(auth.FromContext(r.Context())) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "operator role required"})
			return
		}
		page, err := positiveQueryInt(r, "page", 1)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "page must be a positive integer"})
			return
		}
		pageSize, err := positiveQueryInt(r, "page_size", 50)
		if err != nil || pageSize > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "page_size must be an integer in [1,200]"})
			return
		}
		rows := reg.Active()
		total := len(rows)
		start := (page - 1) * pageSize
		if start > total {
			start = total
		}
		end := start + pageSize
		if end > total {
			end = total
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"sessions":       rows[start:end],
			"total":          total,
			"page":           page,
			"page_size":      pageSize,
			"active_ttl_sec": int(reg.ttl.Seconds()),
		})
	}
}

// positiveQueryInt parses a positive integer query parameter with a
// default. Returns an error for zero, negative, or non-numeric values.
func positiveQueryInt(r *http.Request, name string, def int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, errors.New(name + " must be a positive integer")
	}
	return n, nil
}
