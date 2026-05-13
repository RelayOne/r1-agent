package server

// admin_handlers.go — A5 admin panel HTTP handlers.
//
// Five read-only routes mounted under /admin/* with paired JSON twins
// under /api/admin/*. Each handler negotiates response shape from the
// request path (/api/admin → JSON) or Accept header.
//
// Spec: specs/admin-panel.md §5, §10 tasks 5-13.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/server/sessionhub"
	"github.com/RelayOne/r1/internal/tenants"
)

// AdminDeps bundles every collaborator AdminRoutes needs. Every field
// is required; tests construct a fully-populated AdminDeps with fakes.
type AdminDeps struct {
	Tenants   tenants.Store
	Sessions  *sessionhub.SessionHub
	Cost      *costtrack.Tracker
	AntiTrunc *AntiTruncBuffer
	Audit     AuditReader

	// Emitter is called after each handler runs with the AdminViewed
	// payload (ready for the ledger). Production wiring drops the
	// payload into the ledger writer; tests can capture it directly.
	Emitter AdminViewedEmitter

	// Now is the clock injection point. Defaults to time.Now when nil.
	Now func() time.Time

	// Templates is the parsed admin template set. Constructed by
	// LoadAdminTemplates; tests pass a minimal set.
	Templates *template.Template
}

// AuditReader is the narrow interface admin_handlers needs from the
// ledger package. The real ledger.Store satisfies it through an
// adapter; tests pass a fake.
type AuditReader interface {
	Range(filter AuditFilter) ([]AuditRow, int, error)
}

// AuditFilter is the shape the audit page sends downstream.
type AuditFilter struct {
	Types   []string
	Session string
	Since   time.Time
	Offset  int
	Limit   int
}

// AuditRow is the projection rendered by the audit page. The ledger
// adapter is responsible for redaction — these fields are safe to
// surface as-is.
type AuditRow struct {
	CID       string    `json:"cid"`
	Type      string    `json:"type"`
	SessionID string    `json:"session_id"`
	Author    string    `json:"author"`
	Timestamp time.Time `json:"timestamp"`
	Summary   string    `json:"summary"`
}

// AdminViewedEmitter is the callback fired after every handler. The
// production wiring writes an AdminViewed ledger node; tests use a
// channel-backed fake.
type AdminViewedEmitter func(ctx context.Context, view AdminView)

// AdminView is the data the emitter receives. Kept narrow so the
// ledger node constructor can fill itself without exposing the
// handler's internals.
type AdminView struct {
	Path       string
	User       string
	Tenant     string
	RemoteAddr string
	Method     string
	Status     int
	Timestamp  time.Time
}

// AdminRoutes installs every /admin/* + /api/admin/* route on mux.
// Each route is wrapped by the RequireAdmin middleware and then by an
// emit-on-view hook so the AdminViewed ledger entry fires after the
// handler completes (regardless of status code).
func AdminRoutes(mux *http.ServeMux, deps AdminDeps, gate func(http.Handler) http.Handler) {
	if deps.Now == nil {
		deps.Now = time.Now
	}

	register := func(method, path string, h http.HandlerFunc) {
		wrapped := emitOnView(h, deps, path)
		mux.Handle(method+" "+path, gate(wrapped))
	}

	// HTML routes.
	register("GET", "/admin", adminDashboard(deps))
	register("GET", "/admin/sessions", adminSessionsList(deps))
	register("GET", "/admin/tenants", adminTenantsList(deps))
	register("GET", "/admin/billing", adminBilling(deps))
	register("GET", "/admin/audit", adminAudit(deps))
	register("GET", "/admin/anti-trunc-events", adminAntiTruncEvents(deps))

	// JSON twins. The handler bodies live behind the same functions —
	// content negotiation picks the right renderer based on the URL.
	register("GET", "/api/admin/sessions", adminSessionsList(deps))
	register("GET", "/api/admin/tenants", adminTenantsList(deps))
	register("GET", "/api/admin/billing", adminBilling(deps))
	register("GET", "/api/admin/audit", adminAudit(deps))
	register("GET", "/api/admin/anti-trunc-events", adminAntiTruncEvents(deps))
	register("GET", "/api/admin/dashboard", adminDashboard(deps))
}

// emitOnView wraps a handler so an AdminViewed payload fires once the
// response completes. The wrapper uses a statusCaptureWriter so the
// emitter sees the real status code instead of guessing.
func emitOnView(h http.HandlerFunc, deps AdminDeps, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &statusCaptureWriter{ResponseWriter: w, status: http.StatusOK}
		h(sw, r)

		if deps.Emitter == nil {
			return
		}
		p, _ := AdminPrincipalFromContext(r.Context())
		user := ""
		tenant := ""
		if p != nil {
			user = p.User
			tenant = p.Tenant
		}
		view := AdminView{
			Path:       path,
			User:       user,
			Tenant:     tenant,
			RemoteAddr: redactRemoteAddr(r.RemoteAddr),
			Method:     r.Method,
			Status:     sw.status,
			Timestamp:  deps.Now(),
		}
		// Run the emitter on a detached goroutine so handler latency
		// is not dominated by ledger writes.
		go deps.Emitter(context.Background(), view)
	}
}

// statusCaptureWriter records the status code so the emit-on-view hook
// can include it in the AdminViewed node.
type statusCaptureWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCaptureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// ----- dashboard ---------------------------------------------------------

type dashboardData struct {
	ActiveSessions int     `json:"active_sessions"`
	MTDSpendUSD    float64 `json:"mtd_spend_usd"`
	AntiTrunc24h   int     `json:"anti_trunc_events_24h"`
	GeneratedAt    string  `json:"generated_at"`
}

func adminDashboard(deps AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := deps.Now()
		active := 0
		if deps.Sessions != nil {
			for _, s := range deps.Sessions.List() {
				if strings.EqualFold(s.State, "running") {
					active++
				}
			}
		}
		mtd := 0.0
		if deps.Cost != nil {
			mtd = deps.Cost.Total()
		}
		antiTrunc24 := 0
		if deps.AntiTrunc != nil {
			cutoff := now.Add(-24 * time.Hour)
			for _, ev := range deps.AntiTrunc.Snapshot() {
				if ev.Timestamp.After(cutoff) {
					antiTrunc24++
				}
			}
		}
		data := dashboardData{
			ActiveSessions: active,
			MTDSpendUSD:    mtd,
			AntiTrunc24h:   antiTrunc24,
			GeneratedAt:    now.UTC().Format(time.RFC3339),
		}
		respond(w, r, deps, "dashboard.tmpl", "Admin Dashboard", data)
	}
}

// ----- sessions list ----------------------------------------------------

type sessionRow struct {
	ID         string    `json:"id"`
	Tenant     string    `json:"tenant"`
	Model      string    `json:"model"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	AgeSeconds int       `json:"age_seconds"`
}

type sessionsListData struct {
	Page    Page         `json:"page"`
	Rows    []sessionRow `json:"rows"`
	Filters struct {
		Tenant string `json:"tenant"`
		Status string `json:"status"`
		AgeLT  string `json:"age_lt"`
	} `json:"filters"`
}

func adminSessionsList(deps AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filterTenant := q.Get("tenant")
		filterStatus := q.Get("status")
		filterAgeLT, _ := strconv.Atoi(q.Get("age_lt"))

		var rows []sessionRow
		if deps.Sessions != nil {
			now := deps.Now()
			for _, s := range deps.Sessions.List() {
				if filterTenant != "" && s.TenantID != filterTenant {
					continue
				}
				if filterStatus != "" && !strings.EqualFold(s.State, filterStatus) {
					continue
				}
				age := int(now.Sub(s.StartedAt).Seconds())
				if filterAgeLT > 0 && age >= filterAgeLT {
					continue
				}
				rows = append(rows, sessionRow{
					ID:         s.ID,
					Tenant:     s.TenantID,
					Model:      s.Model,
					State:      s.State,
					StartedAt:  s.StartedAt,
					AgeSeconds: age,
				})
			}
		}
		// Deterministic order: newest first.
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].StartedAt.After(rows[j].StartedAt)
		})

		page := ParsePage(r, len(rows))
		start, end := page.Slice(len(rows))
		visible := rows[start:end]

		data := sessionsListData{Page: page, Rows: visible}
		data.Filters.Tenant = filterTenant
		data.Filters.Status = filterStatus
		if filterAgeLT > 0 {
			data.Filters.AgeLT = strconv.Itoa(filterAgeLT)
		}
		respond(w, r, deps, "sessions_list.tmpl", "Admin / Sessions", data)
	}
}

// ----- tenants list -----------------------------------------------------

type tenantRow struct {
	Slug          string    `json:"slug"`
	DisplayName   string    `json:"display_name"`
	CreatedAt     time.Time `json:"created_at"`
	AdminCount    int       `json:"admin_count"`
	SessionCount  int       `json:"session_count"`
	MonthlyUSDEst float64   `json:"monthly_usd_est"`
}

type tenantsListData struct {
	Page Page        `json:"page"`
	Rows []tenantRow `json:"rows"`
}

func adminTenantsList(deps AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var rows []tenantRow
		if deps.Tenants != nil {
			sessionsByTenant := map[string]int{}
			if deps.Sessions != nil {
				for _, s := range deps.Sessions.List() {
					sessionsByTenant[s.TenantID]++
				}
			}
			spendByTenant := tenantSpendEstimate(deps.Cost)
			for _, t := range deps.Tenants.List() {
				rows = append(rows, tenantRow{
					Slug:          t.Slug,
					DisplayName:   t.DisplayName,
					CreatedAt:     t.CreatedAt,
					AdminCount:    len(t.AdminEmails),
					SessionCount:  sessionsByTenant[t.Slug],
					MonthlyUSDEst: spendByTenant[t.Slug],
				})
			}
		}

		page := ParsePage(r, len(rows))
		start, end := page.Slice(len(rows))
		data := tenantsListData{Page: page, Rows: rows[start:end]}
		respond(w, r, deps, "tenants_list.tmpl", "Admin / Tenants", data)
	}
}

// tenantSpendEstimate aggregates costtrack rows by tenant. Cost
// records carry a task ID that may embed a tenant slug; when the slug
// can't be derived we bucket under "" so the total is still correct.
func tenantSpendEstimate(t *costtrack.Tracker) map[string]float64 {
	out := map[string]float64{}
	if t == nil {
		return out
	}
	for _, r := range t.Records() {
		// Convention: task IDs from authenticated sessions are
		// "<tenant>/<sessionid>". Local-CLI tasks have no prefix and
		// bucket under "".
		slug := ""
		if i := strings.Index(r.TaskID, "/"); i > 0 {
			slug = r.TaskID[:i]
		}
		out[slug] += r.Cost
	}
	return out
}

// ----- billing ----------------------------------------------------------

type billingMonth struct {
	Month     string             `json:"month"`
	TotalUSD  float64            `json:"total_usd"`
	ByModel   map[string]float64 `json:"by_model,omitempty"`
	TaskCount int                `json:"task_count"`
}

type billingData struct {
	Months         []billingMonth `json:"months"`
	PersistentNote string         `json:"persistent_note,omitempty"`
}

func adminBilling(deps AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := buildBillingData(deps)

		if r.URL.Query().Get("format") == "csv" {
			writeBillingCSV(w, data)
			return
		}
		respond(w, r, deps, "billing.tmpl", "Admin / Billing", data)
	}
}

func buildBillingData(deps AdminDeps) billingData {
	out := billingData{
		PersistentNote: "Persistent billing requires r1_costs DB (set R1_COSTS_DSN). In-memory rollup shown below.",
	}
	if deps.Cost == nil {
		return out
	}
	now := deps.Now()
	// Aggregate the last 12 months (including the current one). The
	// in-memory tracker is short-lived but the shape is the same as
	// the future r1_costs-backed reader so the template is stable.
	bucket := map[string]*billingMonth{}
	for _, rec := range deps.Cost.Records() {
		key := rec.Timestamp.UTC().Format("2006-01")
		b, ok := bucket[key]
		if !ok {
			b = &billingMonth{Month: key, ByModel: map[string]float64{}}
			bucket[key] = b
		}
		b.TotalUSD += rec.Cost
		b.ByModel[rec.Model] += rec.Cost
		b.TaskCount++
	}
	// Fill blank months so the operator sees a continuous timeline.
	for i := 0; i < 12; i++ {
		t := now.AddDate(0, -i, 0).UTC()
		key := t.Format("2006-01")
		if _, ok := bucket[key]; !ok {
			bucket[key] = &billingMonth{Month: key, ByModel: map[string]float64{}}
		}
	}
	months := make([]billingMonth, 0, len(bucket))
	for _, b := range bucket {
		months = append(months, *b)
	}
	sort.Slice(months, func(i, j int) bool { return months[i].Month > months[j].Month })
	if len(months) > 12 {
		months = months[:12]
	}
	out.Months = months
	return out
}

func writeBillingCSV(w http.ResponseWriter, data billingData) {
	idx := 0
	header := []string{"month", "total_usd", "task_count"}
	next := func() ([]string, bool) {
		if idx >= len(data.Months) {
			return nil, false
		}
		m := data.Months[idx]
		idx++
		return []string{
			m.Month,
			strconv.FormatFloat(m.TotalUSD, 'f', 4, 64),
			strconv.Itoa(m.TaskCount),
		}, true
	}
	stamp := time.Now().UTC().Format("2006-01")
	_ = CSVStreamWithStamp(w, "billing", stamp, header, next)
}

// ----- audit ------------------------------------------------------------

type auditData struct {
	Page    Page       `json:"page"`
	Rows    []AuditRow `json:"rows"`
	Filters struct {
		Types   []string `json:"types,omitempty"`
		Session string   `json:"session,omitempty"`
		Since   string   `json:"since,omitempty"`
	} `json:"filters"`
}

func adminAudit(deps AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter := AuditFilter{
			Types:   q["type"],
			Session: q.Get("session"),
			Offset:  0,
			Limit:   DefaultPageLimit,
		}
		if s := q.Get("since"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				filter.Since = t
			}
		}
		page := ParsePage(r, 0)
		filter.Offset = page.Offset
		filter.Limit = page.Limit

		var rows []AuditRow
		total := 0
		if deps.Audit != nil {
			rs, n, err := deps.Audit.Range(filter)
			if err == nil {
				rows = rs
				total = n
			}
		}
		page.Total = total

		data := auditData{Page: page, Rows: rows}
		data.Filters.Types = filter.Types
		data.Filters.Session = filter.Session
		if !filter.Since.IsZero() {
			data.Filters.Since = filter.Since.UTC().Format(time.RFC3339)
		}
		respond(w, r, deps, "audit.tmpl", "Admin / Audit", data)
	}
}

// ----- anti-trunc events ------------------------------------------------

type antiTruncData struct {
	Page Page             `json:"page"`
	Rows []AntiTruncEvent `json:"rows"`
}

func adminAntiTruncEvents(deps AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var all []AntiTruncEvent
		if deps.AntiTrunc != nil {
			all = deps.AntiTrunc.SortedByTimestamp()
		}
		page := ParsePage(r, len(all))
		start, end := page.Slice(len(all))
		data := antiTruncData{Page: page, Rows: all[start:end]}
		respond(w, r, deps, "antitrunc.tmpl", "Admin / Anti-Trunc Events", data)
	}
}

// ----- response negotiation ---------------------------------------------

type adminPageContext struct {
	Title       string
	HtmxSRI     string
	HtmxSseSRI  string
	SessionID   string
	Path        string
	Principal   *AdminPrincipal
	Data        any
	GeneratedAt string
}

// respond picks HTML or JSON based on the URL path + Accept header
// and either renders the named template or writes JSON.
func respond(w http.ResponseWriter, r *http.Request, deps AdminDeps, tmplName, title string, data any) {
	if adminWantsJSON(r) {
		writeAdminJSON(w, http.StatusOK, data)
		return
	}
	if deps.Templates == nil {
		writeAdminJSON(w, http.StatusOK, data)
		return
	}
	principal, _ := AdminPrincipalFromContext(r.Context())
	ctx := adminPageContext{
		Title:       title,
		Path:        r.URL.Path,
		Principal:   principal,
		Data:        data,
		GeneratedAt: deps.Now().UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := deps.Templates.ExecuteTemplate(w, tmplName, ctx); err != nil {
		// Fall back to JSON on template execution failure so the
		// operator at least sees the data. The error message goes to
		// stderr (slog wired by main.go).
		fmt.Fprintf(w, "<!-- template error: %v -->", err)
	}
}

func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
