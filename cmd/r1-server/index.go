// Package main — index.go
//
// work-stoke TASK 12: htmx + Go html/template index dashboard layout.
//
// The pre-TASK-12 surface was a vanilla-JS SPA served from
// cmd/r1-server/ui/index.html. TASK 12 replaces that shell (behind
// R1_SERVER_UI_V2=1, matching every other v2 surface) with a
// server-rendered htmx layout:
//
//	GET /              — renders templates/index.tmpl (full page)
//	GET /api/sessions  — content-negotiates: Accept: text/html returns
//	                     the session grid partial (one session_row.tmpl
//	                     per row); default JSON response is unchanged
//	                     so existing consumers keep working.
//	GET /api/events    — SSE stream of every session's events, merged
//	                     into one firehose. htmx's hx-ext="sse" +
//	                     sse-connect="/api/events" on the index page
//	                     subscribes here; individual rows are rendered
//	                     on the server as <div class="ev"> fragments
//	                     so sse-swap="message" can afterbegin them
//	                     into the log.
//
// ## Feature gating
//
// The htmx index is opt-in behind R1_SERVER_UI_V2=1. When the flag is
// off, GET / falls back to serveIndex (the vanilla-JS SPA shell) and
// /api/sessions + /api/events keep their JSON behaviour. This mirrors
// how /session/{id} trace views were migrated in TASK 13.
//
// ## Template composition
//
// index.tmpl ranges over .Sessions and calls {{template "session_row"
// .}} for each one; session_row.tmpl defines the partial. The same
// session_row template is rendered standalone (as a <div class="grid">
// of rows) by the /api/sessions HTML branch so htmx's 2s poll can swap
// the entire grid in place under #sessions.
//
// All three templates share the same traceFuncMap from trace.go so
// tierColor/formatTime/truncateJSON are available if we ever want to
// inline a mini-waterfall on the index page (not needed today).
package main

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// indexTmpl + sessionsPartialTmpl are parsed once at init. Each gets
// both template files (index.tmpl + session_row.tmpl) so either root
// template can render the other as a named sub-template. Panics on
// parse failure — a broken template is a build-time bug, not runtime
// state.
var (
	indexTmpl = template.Must(
		template.New("index.tmpl").
			Funcs(traceFuncMap).
			ParseFS(embeddedTemplates, "templates/index.tmpl", "templates/session_row.tmpl"),
	)
	sessionsPartialTmpl = template.Must(
		template.New("sessions_partial").
			Funcs(traceFuncMap).
			ParseFS(embeddedTemplates, "templates/session_row.tmpl"),
	)
)

// indexSessionView is the projection of SessionRow that index.tmpl +
// session_row.tmpl consume. Keeps derived fields (ShortID,
// StatusClass, RepoName) out of the DB row so the view layer doesn't
// leak storage concerns into SessionRow's JSON contract.
type indexSessionView struct {
	InstanceID  string
	ShortID     string
	PID         int
	RepoRoot    string
	RepoName    string
	Mode        string
	SowName     string
	Model       string
	Status      string
	StatusClass string
	StartedAt   string
}

// indexView is the root template context for index.tmpl.
type indexView struct {
	Sessions []indexSessionView
	Now      string
}

// indexV2Enabled reports whether the htmx index should serve /. Reads
// the env var each call so tests (and ops) can flip the flag without
// rebuilding. Matches the traceV2Enabled / settingsV2Enabled pattern.
func indexV2Enabled() bool {
	return traceV2Enabled()
}

// buildSessionView converts a SessionRow into the template-facing
// view model. Called once per row on every render — cheap enough that
// we don't cache it; the session list is expected to stay small (the
// dashboard is a dev tool, not a production metrics surface).
func buildSessionView(row SessionRow) indexSessionView {
	sv := indexSessionView{
		InstanceID: row.InstanceID,
		ShortID:    row.InstanceID,
		PID:        row.PID,
		RepoRoot:   row.RepoRoot,
		Mode:       row.Mode,
		SowName:    row.SowName,
		Model:      row.Model,
		Status:     row.Status,
		StartedAt:  prettyTimestamp(row.StartedAt),
	}
	if len(sv.ShortID) > 16 {
		sv.ShortID = sv.ShortID[:16] + "…"
	}
	if row.RepoRoot != "" {
		sv.RepoName = filepath.Base(row.RepoRoot)
	}
	sv.StatusClass = statusClass(row.Status, row.UpdatedAt)
	return sv
}

// statusClass buckets a SessionRow.Status into one of the three
// display states the template knows about. "running" sessions that
// haven't updated their signature within 30s are demoted to "stale"
// so the operator sees the discrepancy at a glance — the scanner is
// the source of truth for liveness, but the index can at least flag
// the obvious cases without reaching into its internals.
func statusClass(status, updatedAt string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		if ts, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			if time.Since(ts) > 30*time.Second {
				return "stale"
			}
		}
		return "running"
	case "ended", "exited", "done", "complete":
		return "ended"
	case "":
		return "ended"
	default:
		return "stale"
	}
}

// prettyTimestamp renders an RFC3339Nano timestamp as HH:MM:SS for
// compact card display. Zero / unparseable values fall back to the
// raw string so we never silently hide data from the operator.
func prettyTimestamp(raw string) string {
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.Local().Format("15:04:05")
	}
	return raw
}

// wantsHTML reports whether the request's Accept header prefers
// text/html over application/json. htmx sets Accept: text/html by
// default on hx-get requests, and we also let the index.tmpl send an
// explicit hx-headers override for safety, so either signal flips to
// the HTML branch.
func wantsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false
	}
	// Rank by first occurrence: if text/html shows up before JSON
	// (or JSON is absent), we render HTML. Exact weighting per RFC
	// 7231 would be overkill; htmx + curl both send unambiguous
	// headers in practice.
	al := strings.ToLower(accept)
	htmlIdx := strings.Index(al, "text/html")
	jsonIdx := strings.Index(al, "application/json")
	switch {
	case htmlIdx >= 0 && jsonIdx < 0:
		return true
	case htmlIdx >= 0 && jsonIdx >= 0:
		return htmlIdx < jsonIdx
	default:
		return false
	}
}

// serveHTMLIndex renders templates/index.tmpl for GET /. When the v2
// flag is off it delegates back to serveIndex so pre-opt-in clients
// keep getting the vanilla-JS SPA shell.
func (d *DB) serveHTMLIndex(w http.ResponseWriter, r *http.Request) {
	if !indexV2Enabled() {
		serveIndex(w, r)
		return
	}
	rows, err := d.ListSessions("")
	if err != nil {
		http.Error(w, "list sessions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	view := indexView{
		Sessions: make([]indexSessionView, 0, len(rows)),
		Now:      time.Now().UTC().Format(time.RFC3339),
	}
	for _, row := range rows {
		view.Sessions = append(view.Sessions, buildSessionView(row))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := indexTmpl.Execute(w, view); err != nil {
		// The response headers are already on the wire, so we can't
		// upgrade to 500 — log and bail. This path only fires on a
		// template bug (which `go test` would catch first).
		http.Error(w, "render index: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// serveSessionsPartial renders the session grid as htmx-swappable
// HTML. It's wired to the SAME path /api/sessions as the JSON
// handler — the router still points at the JSON closure, and that
// closure delegates here when wantsHTML(r) is true. Splitting the
// HTML logic into its own func keeps buildMux readable.
func (d *DB) serveSessionsPartial(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	rows, err := d.ListSessions(status)
	if err != nil {
		http.Error(w, "list sessions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]indexSessionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, buildSessionView(row))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// We render a wrapper <div id="sessions"> so htmx's
	// hx-swap="outerHTML" replaces the original container's attrs +
	// content on every poll. Keeping the hx-* loop attributes on the
	// wrapper means the polling never stops after a swap.
	writeString(w, `<div id="sessions"
       hx-get="/api/sessions"
       hx-trigger="every 2s"
       hx-swap="outerHTML"
       hx-headers='{"Accept":"text/html"}'>`)
	if len(views) == 0 {
		writeString(w, `<p class="empty">No sessions registered yet.</p>`)
	} else {
		writeString(w, `<div class="grid">`)
		for _, sv := range views {
			if err := sessionsPartialTmpl.ExecuteTemplate(w, "session_row", sv); err != nil {
				// Partial bodies already on the wire — same
				// trade-off as serveHTMLIndex.
				return
			}
		}
		writeString(w, `</div>`)
	}
	writeString(w, `</div>`)
}

// writeString is a small wrapper over w.Write that discards the
// byte count so callers don't have to deal with `_, _ =` pairs for
// every HTML chunk emitted by serveSessionsPartial. Returns nothing
// because partial-render errors here are unrecoverable (headers are
// already on the wire); the calling handler logs via slog if needed.
func writeString(w http.ResponseWriter, s string) {
	_, _ = w.Write([]byte(s))
}

// handleAllEventsStream is the factory for GET /api/events — the
// multi-session SSE firehose the index.tmpl page consumes via
// hx-ext="sse" + sse-connect="/api/events".
//
// Design:
//
//   - Poll session_events_all (cross-session query on the existing
//     SQLite table) every sseTickInterval. Cursor is the numeric row
//     id, negotiated via Last-Event-ID / ?after= like the per-session
//     stream.
//   - Format each row as an SSE message with `event: message` so
//     htmx's default sse-swap="message" target fires. The data line
//     is a pre-rendered <div class="ev"> fragment so the client can
//     insert it verbatim without parsing JSON.
//   - Heartbeat every sseHeartbeatInterval; identical to the
//     per-session stream so proxy timeouts stay consistent.
func handleAllEventsStream(db *DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		var cursor int64
		if raw := r.Header.Get("Last-Event-ID"); raw != "" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 {
				cursor = v
			}
		} else if raw := r.URL.Query().Get("after"); raw != "" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 {
				cursor = v
			}
		}

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache, no-transform")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		if _, err := fmt.Fprintf(w, "retry: 2000\n\n"); err != nil {
			return
		}
		flusher.Flush()

		ctx := r.Context()
		ticker := time.NewTicker(sseTickInterval)
		defer ticker.Stop()
		heartbeat := time.NewTicker(sseHeartbeatInterval)
		defer heartbeat.Stop()

		newCursor, err := flushAllEvents(w, flusher, db, cursor, sseInitialBatchLimit)
		if err != nil {
			if logger != nil {
				logger.Debug("sse all initial drain", "err", err)
			}
			return
		}
		cursor = newCursor

		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				if _, err := fmt.Fprintf(w, ": keep-alive %d\n\n", time.Now().Unix()); err != nil {
					return
				}
				flusher.Flush()
			case <-ticker.C:
				newCursor, err := flushAllEvents(w, flusher, db, cursor, 0)
				if err != nil {
					if logger != nil && ctx.Err() == nil {
						logger.Debug("sse all flush", "err", err)
					}
					return
				}
				cursor = newCursor
			}
		}
	}
}

// flushAllEvents reads rows past cursor from session_events for ALL
// sessions and emits each as an SSE message whose data body is the
// rendered <div class="ev"> HTML the index page expects. Returns the
// latest row id written, or the input cursor if nothing was drained.
func flushAllEvents(w http.ResponseWriter, flusher http.Flusher, db *DB, cursor int64, limit int) (int64, error) {
	rows, err := db.ListAllEvents(cursor, limit)
	if err != nil {
		return cursor, err
	}
	if len(rows) == 0 {
		return cursor, nil
	}
	for _, row := range rows {
		html := renderEventLine(row)
		// One data: line per SSE frame. Our renderer escapes HTML
		// entities on untrusted fields (event_type, instance_id)
		// before stringing them together, and the rendered line
		// never contains a raw newline by construction.
		if _, err := fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", row.ID, html); err != nil {
			return cursor, err
		}
		cursor = row.ID
	}
	flusher.Flush()
	return cursor, nil
}

// renderEventLine produces the HTML body for one SSE frame in the
// multi-session firehose. Shape matches the index template's
// .events-log .ev marker so CSS applies to live-inserted rows.
// Escaping: every field that originates outside the server is run
// through template.HTMLEscapeString so a malicious ledger payload
// cannot inject markup into the dashboard.
func renderEventLine(row EventRow) string {
	ts := row.Timestamp
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		ts = t.Local().Format("15:04:05.000")
	}
	instance := row.InstanceID
	if len(instance) > 10 {
		instance = instance[:10] + "…"
	}
	return fmt.Sprintf(
		`<div class="ev"><span class="t">%s</span> <code>%s</code> %s</div>`,
		template.HTMLEscapeString(ts),
		template.HTMLEscapeString(instance),
		template.HTMLEscapeString(row.EventType),
	)
}

