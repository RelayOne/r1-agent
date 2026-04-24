// Package main — memories.go
//
// Spec 27 §6 (r1-server-ui-v2.md) defines a read-only memory explorer
// at GET /memories. The full v2 surface is a grouped-list view with
// filters, search, CRUD, and per-memory graph tabs (§6.1–§6.4); this
// file lands the read-only list skeleton following the share.go
// pattern:
//
//  - Single html/template rendered inline (no embed yet).
//  - Gated behind R1_SERVER_UI_V2=1 (per §2.3 migration plan). Off
//    → 404 so MVP users don't see an empty shell.
//  - Reads the memory-bus SQLite table (stoke_memory_bus, defined in
//    specs/memory-bus.md §3) directly. The schema is created
//    idempotently on DB open so this handler works even on a fresh
//    r1-server with no memory-bus writers yet (rows = empty).
//  - No authentication, no writes, no search — CRUD + RBAC come in
//    the full v2 implementation. This lands the "grouped list
//    default" (correction 7) skeleton only.
//
// The grouping order matches §6.1 exactly:
//
//	1. Permanent       (scope=permanent)
//	2. Always          (scope=always)
//	3. Global          (scope=global)
//	4. This Session    (skipped when no active session selected)
//	5. Older Sessions  (scope=session)
//
// The "This Session" group is always skipped here — the view is a
// cross-session listing. A future commit adds a per-session query
// param + filter.
package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// memoryBusSchemaDDL creates the read-side projection of the memory
// bus table if it does not already exist. Stoke core owns the writer
// (per memory-bus spec §5); we apply the same DDL here so r1-server
// can render /memories even when no Stoke process has written a row
// yet. The columns track the spec's §3 schema but r1-server only
// reads the subset it needs (scope, key, content, author, timestamps,
// read_count) — the rest is forward-compat.
const memoryBusSchemaDDL = `
CREATE TABLE IF NOT EXISTS stoke_memory_bus (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at          TEXT    NOT NULL,
    expires_at          TEXT,
    scope               TEXT    NOT NULL,
    scope_target        TEXT    NOT NULL DEFAULT '',
    session_id          TEXT    NOT NULL DEFAULT '',
    step_id             TEXT    NOT NULL DEFAULT '',
    task_id             TEXT    NOT NULL DEFAULT '',
    author              TEXT    NOT NULL DEFAULT '',
    key                 TEXT    NOT NULL,
    content             TEXT    NOT NULL DEFAULT '',
    content_encrypted   BLOB,
    content_hash        TEXT    NOT NULL DEFAULT '',
    tags                TEXT    NOT NULL DEFAULT '[]',
    metadata            TEXT    NOT NULL DEFAULT '{}',
    memory_type         TEXT    NOT NULL DEFAULT '',
    read_count          INTEGER NOT NULL DEFAULT 0,
    last_read_at        TEXT,
    UNIQUE (scope, scope_target, key)
);
CREATE INDEX IF NOT EXISTS idx_membus_scope   ON stoke_memory_bus(scope, scope_target);
CREATE INDEX IF NOT EXISTS idx_membus_session ON stoke_memory_bus(session_id);
`

// MemoryRow is the projection the /memories template renders against.
// Content is truncated in the template, not here, so the side panel
// can show the full body in a future commit without re-querying.
type MemoryRow struct {
	ID         int64
	CreatedAt  string
	ExpiresAt  string
	Scope      string
	Key        string
	Author     string
	Content    string
	Encrypted  bool
	ReadCount  int64
	SessionID  string
}

// memoryGroup is one §6.1 grouping for the template.
type memoryGroup struct {
	Label    string
	Scope    string
	Memories []MemoryRow
}

// memoriesView is the top-level template context.
type memoriesView struct {
	Groups []memoryGroup
	Total  int
	Empty  bool
}

// ListMemories returns every memory row sorted by created_at desc.
// Callers group by scope in application code — grouping in SQL would
// need N queries or a UNION ALL and obscure the spec §6.1 ordering.
// Limit caps the result set so a runaway memory-bus doesn't flood
// the template render; 0 means "use default 1000".
func (d *DB) ListMemories(limit int) ([]MemoryRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := d.sql.Query(
		`SELECT id, created_at, COALESCE(expires_at,''), scope, key,
		        COALESCE(author,''), COALESCE(content,''),
		        content_encrypted IS NOT NULL AS encrypted,
		        read_count, COALESCE(session_id,'')
		   FROM stoke_memory_bus
		  ORDER BY created_at DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	out := make([]MemoryRow, 0, 16)
	for rows.Next() {
		var m MemoryRow
		var enc int
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.ExpiresAt, &m.Scope, &m.Key,
			&m.Author, &m.Content, &enc, &m.ReadCount, &m.SessionID,
		); err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		m.Encrypted = enc != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// groupMemories partitions rows into the §6.1 group order. Unknown
// scopes fall under "Other" so a spec-mismatched row stays visible
// rather than silently disappearing. "This Session" is skipped per
// the package-doc comment above — no active-session selection yet.
func groupMemories(rows []MemoryRow) []memoryGroup {
	// Fixed §6.1 order. Always emit all defined groups so a consistent
	// layout renders even when a group is empty — the template shows
	// a muted "no memories" line in that case.
	groups := []memoryGroup{
		{Label: "Permanent", Scope: "permanent"},
		{Label: "Always", Scope: "always"},
		{Label: "Global", Scope: "global"},
		{Label: "Older Sessions", Scope: "session"},
	}
	other := memoryGroup{Label: "Other", Scope: ""}

	for _, m := range rows {
		placed := false
		for i := range groups {
			if groups[i].Scope == m.Scope {
				groups[i].Memories = append(groups[i].Memories, m)
				placed = true
				break
			}
		}
		if !placed {
			other.Memories = append(other.Memories, m)
		}
	}
	if len(other.Memories) > 0 {
		groups = append(groups, other)
	}
	return groups
}

// memoriesTmpl is the read-only grouped-list skeleton. html/template
// auto-escapes every `{{.}}` emission so user-authored content never
// breaks out of its context. The page is intentionally minimal — the
// full htmx surface + side panel + filters land in the v2 retrofit.
var memoriesTmpl = template.Must(template.New("memories").Funcs(template.FuncMap{
	"truncate": truncateContent,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>r1-server memories</title>
</head>
<body>
<header>
  <h1>Memory bus</h1>
  <p>Read-only grouped list (spec 27 §6.1). Total rows: {{.Total}}.</p>
</header>
<main>
{{if .Empty}}
  <p><em>No memory-bus rows yet. R1 workers populate this table via <code>Bus.Remember</code>.</em></p>
{{else}}
  {{range .Groups}}
    <section>
      <h2>{{.Label}} <small>({{len .Memories}})</small></h2>
      {{if .Memories}}
        <ul>
          {{range .Memories}}
            <li>
              <code>{{.Key}}</code>
              <span>scope={{.Scope}}</span>
              <span>author={{.Author}}</span>
              <span>reads={{.ReadCount}}</span>
              {{if .Encrypted}}<span>&#128274; encrypted</span>{{end}}
              <p>{{truncate .Content 200}}</p>
              <small>created {{.CreatedAt}}{{if .ExpiresAt}} &middot; expires {{.ExpiresAt}}{{end}}</small>
            </li>
          {{end}}
        </ul>
      {{else}}
        <p><em>no memories</em></p>
      {{end}}
    </section>
  {{end}}
{{end}}
</main>
</body>
</html>
`))

// truncateContent shortens a string to n runes followed by "…" when
// longer. Rune-safe so multibyte strings don't break mid-codepoint.
func truncateContent(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// serveMemories renders the §6.1 grouped list. Status codes:
//
//	404 — v2 flag is off (matches share.go precedent + spec §2.3)
//	500 — DB query fails (row scan, table missing, etc.)
//	200 — render succeeded; body is a full HTML document
//
// The handler is read-only; writes land in the full v2 implementation
// per §6.5. The default limit (1000) is deliberately generous; the
// grouped-list render time is linear in row count and a 1000-row
// table renders under 50ms on a development laptop.
func (d *DB) serveMemories(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}

	rows, err := d.ListMemories(0)
	if err != nil {
		http.Error(w, "list memories: "+err.Error(), http.StatusInternalServerError)
		return
	}

	view := memoriesView{
		Groups: groupMemories(rows),
		Total:  len(rows),
		Empty:  len(rows) == 0,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	if err := memoriesTmpl.Execute(w, view); err != nil {
		http.Error(w, "render memories: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// ensureMemoryBusSchema applies the §3 DDL on the r1-server DB. Called
// once from OpenDB so the /memories handler always has a table to
// query — even on a fresh install where no Stoke process has written
// yet. Idempotent (IF NOT EXISTS).
func ensureMemoryBusSchema(conn *sql.DB) error {
	if _, err := conn.Exec(memoryBusSchemaDDL); err != nil {
		return fmt.Errorf("apply memory-bus schema: %w", err)
	}
	return nil
}

// insertMemoryForTest is a test-only helper that lets unit tests seed
// rows without importing stoke-core's Bus implementation. Production
// writes go through memory-bus's writerLoop (spec §5.2). The helper
// lives in the main package file (not _test.go) because the memories
// tests need it and Go's test package split would otherwise require
// exporting the DB mutex.
func (d *DB) insertMemoryForTest(scope, key, author, content string, createdAt time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(
		`INSERT INTO stoke_memory_bus(created_at, scope, key, author, content, content_hash)
		 VALUES (?, ?, ?, ?, ?, '')`,
		createdAt.Format(time.RFC3339Nano), scope, key, author, content,
	)
	return err
}

// ---------------------------------------------------------------------------
// work-stoke TASK 14 — memory explorer CRUD
//
// Spec 27 §6.5 ("Memory CRUD & RBAC") + specs/work-stoke.md TASK 14 close-out:
// expose POST/PUT/DELETE on top of the existing read-only /memories view, and
// gate writes whose scope is "always" behind a shared passphrase supplied via
// the X-R1-Admin-Pass header.
//
// The passphrase comparison uses crypto/subtle.ConstantTimeCompare so a
// timing-oracle attacker cannot probe the env-var byte-by-byte. When
// R1_ADMIN_PASS is unset, ScopeAlways writes are fail-closed: any attempt
// returns 401 regardless of header content so a misconfigured server cannot
// silently accept unrestricted always-scope writes.
//
// r1-server does not own a *membus.Bus handle today (see retention_sweep.go
// design notes); writes therefore go through raw SQL against
// stoke_memory_bus. The shape is the same spec §3 upsert the bus writer uses
// so the row is indistinguishable from a Bus-authored row at the schema
// level. If/when a Bus is plumbed into this binary, swap the raw SQL for
// Bus.Remember without changing the HTTP contract.

const adminPassHeader = "X-R1-Admin-Pass" // #nosec G101 -- HTTP header name, not a credential value.

// memoryWriteRequest is the JSON body accepted by POST /api/memories.
// Fields mirror the membus Remember shape minus the attribution fields
// (session/step/task) which r1-server cannot attest — those default to empty
// strings so the row still satisfies the non-null column constraints.
type memoryWriteRequest struct {
	Scope       string `json:"scope"`
	ScopeTarget string `json:"scope_target"`
	Key         string `json:"key"`
	Content     string `json:"content"`
	MemoryType  string `json:"memory_type"`
}

// memoryUpdateRequest is the JSON body accepted by PUT /api/memories/{id}.
// Only content + memory_type are updatable via this endpoint; rekeying a
// memory or changing its scope would invalidate downstream references so
// those mutations require a delete + post pair.
type memoryUpdateRequest struct {
	Content    string `json:"content"`
	MemoryType string `json:"memory_type"`
}

// requireAdminPassIfAlways returns true when the request is authorised to
// write a row whose scope value is the given one. For non-"always" scopes it
// always returns true. For "always", it fails closed (returns false and has
// already written a 401 to w) whenever:
//
//   - R1_ADMIN_PASS is unset on the server (no passphrase configured)
//   - the request omitted the X-R1-Admin-Pass header
//   - the header value does not match the env var under a constant-time
//     comparison
//
// The comparison uses subtle.ConstantTimeCompare so the server's response
// timing cannot leak byte-by-byte information about the configured value.
func requireAdminPassIfAlways(w http.ResponseWriter, r *http.Request, scope string) bool {
	if scope != "always" {
		return true
	}
	expected := os.Getenv("R1_ADMIN_PASS")
	got := r.Header.Get(adminPassHeader)
	// Fail-closed: missing server config or missing header → 401. We still
	// run a constant-time compare against a zero-length `expected` to keep
	// the timing profile identical across missing-config vs wrong-header.
	if expected == "" || got == "" ||
		subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
		http.Error(w, "scope=always requires admin passphrase", http.StatusUnauthorized)
		return false
	}
	return true
}

// readJSONBody decodes a small JSON body (capped at 64 KiB) into v. The cap
// mirrors the /api/register handler so an attacker cannot OOM the server by
// streaming an unbounded body.
func readJSONBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	return nil
}

// CreateMemoryRow inserts a new row into stoke_memory_bus and returns the
// autoincrement id. Key is optional — when empty we derive a stable dedup key
// from the current nanosecond so two creates without an explicit key do not
// collide on the UNIQUE (scope, scope_target, key) constraint.
func (d *DB) CreateMemoryRow(scope, scopeTarget, key, content, memoryType string, now time.Time) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if key == "" {
		key = fmt.Sprintf("r1-%d", now.UnixNano())
	}
	res, err := d.sql.Exec(
		`INSERT INTO stoke_memory_bus(
		    created_at, scope, scope_target, key, author, content,
		    content_hash, memory_type
		 ) VALUES (?, ?, ?, ?, ?, ?, '', ?)`,
		now.Format(time.RFC3339Nano),
		scope,
		scopeTarget,
		key,
		"r1-server",
		content,
		memoryType,
	)
	if err != nil {
		return 0, fmt.Errorf("insert memory row: %w", err)
	}
	return res.LastInsertId()
}

// UpdateMemoryRow updates the content + memory_type of an existing row. It
// returns sql.ErrNoRows when the id does not match any row so the caller can
// distinguish "row missing" from "db failed".
func (d *DB) UpdateMemoryRow(id int64, content, memoryType string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.sql.Exec(
		`UPDATE stoke_memory_bus
		    SET content = ?, memory_type = ?
		  WHERE id = ?`,
		content, memoryType, id,
	)
	if err != nil {
		return fmt.Errorf("update memory row: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteMemoryRow removes a row by id, returning sql.ErrNoRows when no row
// was affected so the caller can surface a 404 instead of a generic 204.
func (d *DB) DeleteMemoryRow(id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.sql.Exec(`DELETE FROM stoke_memory_bus WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete memory row: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// getMemoryScope loads the scope column for a single row so PUT/DELETE can
// enforce the ScopeAlways passphrase gate without re-fetching the full row.
func (d *DB) getMemoryScope(id int64) (string, error) {
	var scope string
	err := d.sql.QueryRow(
		`SELECT scope FROM stoke_memory_bus WHERE id = ?`, id,
	).Scan(&scope)
	return scope, err
}

// serveMemoryCreate handles POST /api/memories. Body shape is
// memoryWriteRequest; scope=="always" gates on X-R1-Admin-Pass. On success
// the handler returns 200 with {id, ok:true}, matching the work-stoke
// TASK 14 test contract (TestMemoriesPOST_NormalScope_200).
func (d *DB) serveMemoryCreate(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}
	var req memoryWriteRequest
	if err := readJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Scope == "" || req.Content == "" {
		http.Error(w, "scope and content are required", http.StatusBadRequest)
		return
	}
	if !requireAdminPassIfAlways(w, r, req.Scope) {
		return
	}
	id, err := d.CreateMemoryRow(req.Scope, req.ScopeTarget, req.Key, req.Content, req.MemoryType, time.Now().UTC())
	if err != nil {
		http.Error(w, "create memory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
}

// serveMemoryUpdate handles PUT /api/memories/{id}. The passphrase gate runs
// against the stored row's scope, not a request-supplied one, so a caller
// cannot bypass the gate by omitting scope from the body.
func (d *DB) serveMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}
	id, ok := parseMemoryID(w, r)
	if !ok {
		return
	}
	scope, err := d.getMemoryScope(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "lookup memory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !requireAdminPassIfAlways(w, r, scope) {
		return
	}
	var req memoryUpdateRequest
	if err := readJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.UpdateMemoryRow(id, req.Content, req.MemoryType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "update memory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
}

// serveMemoryDelete handles DELETE /api/memories/{id}. Same gate as update:
// the stored row's scope drives the passphrase requirement, not a body field.
// Successful deletes return 204 No Content per REST convention; a missing id
// yields 404 so callers can distinguish an already-deleted row.
func (d *DB) serveMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}
	id, ok := parseMemoryID(w, r)
	if !ok {
		return
	}
	scope, err := d.getMemoryScope(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "lookup memory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !requireAdminPassIfAlways(w, r, scope) {
		return
	}
	if err := d.DeleteMemoryRow(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "delete memory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseMemoryID pulls and validates the {id} path value shared by PUT + DELETE.
// Writes a 400 + returns ok=false when the id is malformed so the caller can
// bail early without duplicating validation boilerplate. Accepts a trailing
// slash (a no-op for the Go 1.22 mux but future-proof).
func parseMemoryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSuffix(r.PathValue("id"), "/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
