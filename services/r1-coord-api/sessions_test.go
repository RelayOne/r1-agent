package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1-coord-api/internal/auth"
)

// fixedClock returns a controllable clock for TTL/expiry tests.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func mustReport(reg *sessionRegistry, daemon, sess, status string, activity time.Time, cost float64) {
	reg.Report(sessionRow{
		DaemonID:     daemon,
		SessionID:    sess,
		Status:       status,
		LastActivity: activity,
		CostUSD:      cost,
	})
}

// --- registry-level tests -------------------------------------------------

// TestRegistryTTLDrop: a row whose last activity is older than the TTL is
// pruned on the next Active() call and never listed.
func TestRegistryTTLDrop(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	reg := newSessionRegistry(5 * time.Minute)
	reg.now = fixedClock(base)

	mustReport(reg, "d1", "s1", "active", base, 0.10)

	// Within TTL: still active.
	if got := len(reg.Active()); got != 1 {
		t.Fatalf("within TTL: got %d active, want 1", got)
	}

	// Advance clock past TTL: pruned.
	reg.now = fixedClock(base.Add(6 * time.Minute))
	if got := len(reg.Active()); got != 0 {
		t.Fatalf("past TTL: got %d active, want 0 (row must be pruned)", got)
	}

	// And pruning is durable: the map entry was deleted, not just filtered.
	reg.mu.Lock()
	n := len(reg.rows)
	reg.mu.Unlock()
	if n != 0 {
		t.Fatalf("expired row still in map: %d rows", n)
	}
}

// TestRegistryTTLBoundaryExact: exactly-at-TTL is still active (drop is
// strictly greater-than, matching the > comparison in Active()).
func TestRegistryTTLBoundaryExact(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	reg := newSessionRegistry(5 * time.Minute)
	reg.now = fixedClock(base)
	mustReport(reg, "d1", "s1", "active", base, 0)

	reg.now = fixedClock(base.Add(5 * time.Minute)) // exactly TTL
	if got := len(reg.Active()); got != 1 {
		t.Fatalf("exactly at TTL: got %d, want 1 (drop is strictly > ttl)", got)
	}
}

// TestRegistryTerminalStatusRemoval: reporting a terminal status removes an
// existing active row immediately for each terminal status.
func TestRegistryTerminalStatusRemoval(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	for _, term := range []string{"completed", "failed", "killed"} {
		reg := newSessionRegistry(5 * time.Minute)
		reg.now = fixedClock(base)
		mustReport(reg, "d1", "s1", "active", base, 0.25)
		if len(reg.Active()) != 1 {
			t.Fatalf("%s: setup failed, active row not present", term)
		}
		mustReport(reg, "d1", "s1", term, base, 0.25)
		if got := len(reg.Active()); got != 0 {
			t.Fatalf("%s: got %d active after terminal report, want 0", term, got)
		}
	}
}

// TestRegistryTerminalStatusOnUnknownIsNoop: a terminal status for a session
// never seen must not create a row.
func TestRegistryTerminalStatusOnUnknownIsNoop(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	reg := newSessionRegistry(5 * time.Minute)
	reg.now = fixedClock(base)
	mustReport(reg, "d1", "ghost", "completed", base, 0)
	if got := len(reg.Active()); got != 0 {
		t.Fatalf("terminal report for unknown session created %d rows, want 0", got)
	}
}

// TestRegistryOneRowPerSession: repeated reports for the same
// (daemon_id, session_id) upsert a single row, updating activity + cost and
// preserving the original StartedAt/Workdir across heartbeats that omit them.
func TestRegistryOneRowPerSession(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	reg := newSessionRegistry(1 * time.Hour)
	reg.now = fixedClock(base)

	started := base.Add(-10 * time.Minute)
	reg.Report(sessionRow{
		DaemonID: "d1", SessionID: "s1", Status: "active",
		Workdir: "/work/repo", StartedAt: started, LastActivity: base, CostUSD: 0.10,
	})
	// Heartbeat omitting StartedAt + Workdir, with newer activity + cost.
	reg.Report(sessionRow{
		DaemonID: "d1", SessionID: "s1", Status: "active",
		LastActivity: base.Add(30 * time.Second), CostUSD: 0.42,
	})

	rows := reg.Active()
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want exactly 1 per (daemon,session)", len(rows))
	}
	r := rows[0]
	if !r.StartedAt.Equal(started) {
		t.Errorf("StartedAt=%v, want preserved %v", r.StartedAt, started)
	}
	if r.Workdir != "/work/repo" {
		t.Errorf("Workdir=%q, want preserved /work/repo", r.Workdir)
	}
	if r.CostUSD != 0.42 {
		t.Errorf("CostUSD=%v, want updated 0.42", r.CostUSD)
	}
	if !r.LastActivity.Equal(base.Add(30 * time.Second)) {
		t.Errorf("LastActivity=%v, want updated", r.LastActivity)
	}
}

// TestRegistryActiveOrdering: Active() returns most-recent-activity first,
// with the (daemon,session) key as a deterministic tiebreaker.
func TestRegistryActiveOrdering(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	reg := newSessionRegistry(1 * time.Hour)
	reg.now = fixedClock(base)

	mustReport(reg, "d1", "old", "active", base.Add(-2*time.Minute), 0)
	mustReport(reg, "d1", "new", "active", base.Add(-1*time.Minute), 0)
	// Two rows with identical activity → key tiebreak (d1\x00a < d1\x00b).
	mustReport(reg, "d1", "b", "active", base, 0)
	mustReport(reg, "d1", "a", "active", base, 0)

	rows := reg.Active()
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.SessionID
	}
	want := []string{"a", "b", "new", "old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", got, want)
	}
}

// TestRegistryConcurrentReportAndActive exercises the mutex under -race:
// concurrent reporters and readers must not race.
func TestRegistryConcurrentReportAndActive(t *testing.T) {
	reg := newSessionRegistry(1 * time.Hour)
	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func(w int) {
			for i := 0; i < 200; i++ {
				reg.Report(sessionRow{
					DaemonID: fmt.Sprintf("d%d", w), SessionID: fmt.Sprintf("s%d", i),
					Status: "active", LastActivity: time.Now(), CostUSD: 0.01,
				})
			}
			done <- struct{}{}
		}(w)
	}
	for r := 0; r < 4; r++ {
		go func() {
			for i := 0; i < 200; i++ {
				_ = reg.Active()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 12; i++ {
		<-done
	}
}

// --- request validation ---------------------------------------------------

func TestSessionReportValidation(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		req     sessionReportRequest
		wantErr bool
	}{
		{"missing daemon", sessionReportRequest{SessionID: "s1"}, true},
		{"missing session", sessionReportRequest{DaemonID: "d1"}, true},
		{"blank daemon", sessionReportRequest{DaemonID: "   ", SessionID: "s1"}, true},
		{"bad status", sessionReportRequest{DaemonID: "d1", SessionID: "s1", Status: "zombie"}, true},
		{"negative cost", sessionReportRequest{DaemonID: "d1", SessionID: "s1", CostUSD: -1}, true},
		{"bad started_at", sessionReportRequest{DaemonID: "d1", SessionID: "s1", StartedAt: "not-a-time"}, true},
		{"bad last_activity", sessionReportRequest{DaemonID: "d1", SessionID: "s1", LastActivity: "nope"}, true},
		{"valid minimal (default status active)", sessionReportRequest{DaemonID: "d1", SessionID: "s1"}, false},
		{"valid full", sessionReportRequest{DaemonID: "d1", SessionID: "s1", Status: "idle", StartedAt: "2026-07-08T11:00:00Z", LastActivity: "2026-07-08T11:59:00Z", CostUSD: 3.14}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row, reason := c.req.validate(now)
			if c.wantErr && reason == "" {
				t.Fatalf("expected validation error, got none (row=%+v)", row)
			}
			if !c.wantErr && reason != "" {
				t.Fatalf("unexpected validation error: %s", reason)
			}
			if !c.wantErr {
				if c.req.Status == "" && row.Status != "active" {
					t.Errorf("empty status did not default to active: %q", row.Status)
				}
				if row.LastActivity.IsZero() {
					t.Errorf("LastActivity should default to server clock when omitted")
				}
			}
		})
	}
}

// --- HTTP handler tests (through the auth middleware) ---------------------

// routedHandler mirrors main()'s wiring: both session routes are gated by
// the JWT middleware (no public paths), and the GET additionally checks the
// operator role inside the handler.
func routedHandler(reg *sessionRegistry, jwt *auth.JwtService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sessions", handleSessionsList(reg))
	mux.HandleFunc("/v1/sessions/report", handleSessionsReport(reg))
	return auth.Middleware(jwt)(mux)
}

func testJwt() *auth.JwtService {
	return auth.NewJwtServiceHS256("r1-coord-api", "r1-coord-api", []byte("test-secret-please-change-32bytes!"))
}

func mintToken(t *testing.T, jwt *auth.JwtService, roles ...string) string {
	t.Helper()
	tok, err := jwt.Sign(auth.Claims{Sub: "u1", Roles: roles})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

// TestReportJWTGate: /v1/sessions/report rejects unauthenticated requests
// (401) and accepts any authenticated principal (200) — a daemon reports
// its own sessions; no operator role required for report.
func TestReportJWTGate(t *testing.T) {
	reg := newSessionRegistry(5 * time.Minute)
	jwt := testJwt()
	h := routedHandler(reg, jwt)

	// No bearer → 401.
	rr := httptest.NewRecorder()
	body := `{"daemon_id":"d1","session_id":"s1","status":"active"}`
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/sessions/report", strings.NewReader(body)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no-bearer report: status=%d, want 401", rr.Code)
	}

	// Valid non-operator bearer → 200 accepted (report is not operator-gated).
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/report", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mintToken(t, jwt, "member"))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("member report: status=%d body=%q, want 200", rr.Code, rr.Body.String())
	}
	// The row is now visible to an operator listing.
	if got := len(reg.Active()); got != 1 {
		t.Fatalf("registry has %d active after report, want 1", got)
	}
}

// TestReportRejectsInvalidBody: schema validation fires through the handler.
func TestReportRejectsInvalidBody(t *testing.T) {
	reg := newSessionRegistry(5 * time.Minute)
	jwt := testJwt()
	h := routedHandler(reg, jwt)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/report", strings.NewReader(`{"session_id":"s1"}`))
	req.Header.Set("Authorization", "Bearer "+mintToken(t, jwt, "member"))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing daemon_id: status=%d, want 400", rr.Code)
	}
}

// TestListOperatorRoleGate: GET /v1/sessions is 401 unauthenticated,
// 403 for an authenticated non-operator, 200 for an operator.
func TestListOperatorRoleGate(t *testing.T) {
	reg := newSessionRegistry(5 * time.Minute)
	jwt := testJwt()
	h := routedHandler(reg, jwt)

	// Unauthenticated → 401 (middleware).
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no-bearer list: status=%d, want 401", rr.Code)
	}

	// Authenticated non-operator → 403 (handler role check).
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, jwt, "member"))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member list: status=%d, want 403", rr.Code)
	}

	// Operator → 200.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, jwt, "operator"))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("operator list: status=%d body=%q, want 200", rr.Code, rr.Body.String())
	}
}

// listAsOperator issues a GET /v1/sessions with an operator token and
// decodes the JSON envelope.
func listAsOperator(t *testing.T, h http.Handler, jwt *auth.JwtService, query string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions"+query, nil)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, jwt, "operator"))
	h.ServeHTTP(rr, req)
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal list body: %v (raw=%q)", err, rr.Body.String())
	}
	return rr.Code, body
}

// TestListPagination: total is the full active count; each page returns the
// correct slice; out-of-range pages return an empty slice (not an error).
func TestListPagination(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	reg := newSessionRegistry(1 * time.Hour)
	reg.now = fixedClock(base)
	jwt := testJwt()
	h := routedHandler(reg, jwt)

	// 5 sessions, strictly decreasing activity so order is s0..s4.
	for i := 0; i < 5; i++ {
		mustReport(reg, "d1", fmt.Sprintf("s%d", i), "active", base.Add(-time.Duration(i)*time.Minute), 0)
	}

	// page=1 page_size=2 → first 2 rows, total=5.
	code, body := listAsOperator(t, h, jwt, "?page=1&page_size=2")
	if code != http.StatusOK {
		t.Fatalf("page1: status=%d", code)
	}
	if total := body["total"].(float64); total != 5 {
		t.Fatalf("total=%v, want 5", total)
	}
	sessions := body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("page1 len=%d, want 2", len(sessions))
	}
	if id := sessions[0].(map[string]any)["session_id"]; id != "s0" {
		t.Fatalf("page1[0]=%v, want s0 (most recent first)", id)
	}

	// page=3 page_size=2 → last single row (index 4).
	code, body = listAsOperator(t, h, jwt, "?page=3&page_size=2")
	sessions = body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("page3 len=%d, want 1", len(sessions))
	}
	if id := sessions[0].(map[string]any)["session_id"]; id != "s4" {
		t.Fatalf("page3[0]=%v, want s4", id)
	}

	// page=99 → empty slice, still 200, total unchanged.
	code, body = listAsOperator(t, h, jwt, "?page=99&page_size=2")
	if code != http.StatusOK {
		t.Fatalf("page99: status=%d, want 200 (out-of-range is empty, not error)", code)
	}
	if len(body["sessions"].([]any)) != 0 {
		t.Fatalf("page99 should be empty, got %v", body["sessions"])
	}
	if total := body["total"].(float64); total != 5 {
		t.Fatalf("page99 total=%v, want 5", total)
	}
}

// TestListPaginationBounds: invalid page / page_size are rejected with 400.
func TestListPaginationBounds(t *testing.T) {
	reg := newSessionRegistry(1 * time.Hour)
	jwt := testJwt()
	h := routedHandler(reg, jwt)

	for _, q := range []string{"?page=0", "?page=-1", "?page=abc", "?page_size=0", "?page_size=201", "?page_size=abc"} {
		code, _ := listAsOperator(t, h, jwt, q)
		if code != http.StatusBadRequest {
			t.Errorf("query %q: status=%d, want 400", q, code)
		}
	}

	// Defaults (no query) → 200 with default page_size 50.
	code, body := listAsOperator(t, h, jwt, "")
	if code != http.StatusOK {
		t.Fatalf("no query: status=%d, want 200", code)
	}
	if ps := body["page_size"].(float64); ps != 50 {
		t.Fatalf("default page_size=%v, want 50", ps)
	}
}

// TestListDefaultTTLExposed: the response advertises the active TTL so the
// admin UI can label the freshness window.
func TestListDefaultTTLExposed(t *testing.T) {
	reg := newSessionRegistry(90 * time.Second)
	jwt := testJwt()
	h := routedHandler(reg, jwt)
	_, body := listAsOperator(t, h, jwt, "")
	if ttl := body["active_ttl_sec"].(float64); ttl != 90 {
		t.Fatalf("active_ttl_sec=%v, want 90", ttl)
	}
}

// TestHasOperatorRole is a direct unit check of the role predicate,
// including the nil-claims (unauthenticated context) path.
func TestHasOperatorRole(t *testing.T) {
	if hasOperatorRole(nil) {
		t.Error("nil claims must not be operator")
	}
	if hasOperatorRole(&auth.Claims{Roles: []string{"member", "billing"}}) {
		t.Error("non-operator roles must not pass")
	}
	if !hasOperatorRole(&auth.Claims{Roles: []string{"member", "operator"}}) {
		t.Error("operator role must pass")
	}
}

// TestListRejectsNonGET / TestReportRejectsNonPost verify method guards.
func TestSessionMethodGuards(t *testing.T) {
	reg := newSessionRegistry(5 * time.Minute)

	// List handler with operator claims injected directly, wrong method.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextClaimsKey, &auth.Claims{Roles: []string{"operator"}}))
	handleSessionsList(reg)(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to list: status=%d, want 405", rr.Code)
	}

	// Report handler, wrong method.
	rr = httptest.NewRecorder()
	handleSessionsReport(reg)(rr, httptest.NewRequest(http.MethodGet, "/v1/sessions/report", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET to report: status=%d, want 405", rr.Code)
	}
}
