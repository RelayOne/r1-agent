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
	"strconv"
	"strings"
	"time"

	"github.com/RelayOne/r1-agent/internal/r1env"
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
// Adds POST /api/memories, PUT /api/memories/{id}, DELETE /api/memories/{id}
// on top of the read-only grouped-list view above. Writes that target
// scope=="always" require an operator passphrase carried in the JSON body's
// "passphrase" field; the expected value is sourced from the env pair
// R1_MEMORIES_PASSPHRASE (canonical) / STOKE_MEMORIES_PASSPHRASE (legacy)
// via internal/r1env.Get so the s1-5 dual-accept window applies uniformly.
//
// Fail-closed posture: when the env var is unset every ScopeAlways write is
// rejected. The constant-time comparison keeps the wrong-passphrase path
// indistinguishable from the missing-passphrase path, so a caller cannot
// tell the two states apart via response timing.
//
// r1-server does not own a *membus.Bus handle today — writes go through raw
// SQL against stoke_memory_bus using the same column shape membus's writer
// loop uses. Rows are indistinguishable from Bus-authored rows at the
// schema level; when a Bus handle is plumbed into this binary the raw SQL
// can be swapped for Bus.Remember without changing the HTTP contract.

// memoryWriteRequest is the JSON body accepted by POST /api/memories. The
// Passphrase field is only consulted when Scope == "always"; for every
// other scope it is ignored so normal-scope callers do not need to know the
// value.
type memoryWriteRequest struct {
	Scope       string     `json:"scope"`
	ScopeTarget string     `json:"scope_target"`
	Key         string     `json:"key"`
	Content     string     `json:"content"`
	Tags        []string   `json:"tags"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Passphrase  string     `json:"passphrase"`
}

// memoryUpdateRequest is the JSON body accepted by PUT /api/memories/{id}.
// Scope + key cannot change via update — those mutations would invalidate
// downstream references so they require delete + create. Only content, tags
// and expiry are mutable here.
type memoryUpdateRequest struct {
	Content    string     `json:"content"`
	Tags       []string   `json:"tags"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Passphrase string     `json:"passphrase"`
}

// requirePassphraseIfAlways enforces the scope=="always" passphrase gate.
// For every other scope it returns true unconditionally. For ScopeAlways it
// returns false (after writing the 401 response) whenever:
//
//   - R1_MEMORIES_PASSPHRASE / STOKE_MEMORIES_PASSPHRASE are both unset
//   - the request body omitted the "passphrase" field
//   - the supplied value does not match the expected one under a
//     constant-time comparison
//
// The comparison uses subtle.ConstantTimeCompare so the server's timing
// profile does not leak whether the gate was tripped by a configuration
// gap vs. a caller-side mistake.
func requirePassphraseIfAlways(w http.ResponseWriter, scope, supplied string) bool {
	if scope != "always" {
		return true
	}
	expected := r1env.Get("R1_MEMORIES_PASSPHRASE", "STOKE_MEMORIES_PASSPHRASE")
	if expected == "" || supplied == "" ||
		subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) != 1 {
		http.Error(w, "scope=always requires operator passphrase", http.StatusUnauthorized)
		return false
	}
	return true
}

// readJSONBody decodes a small JSON body (capped at 64 KiB) into v. A
// non-nil error here yields a 400 at the handler so malformed payloads
// never reach SQL. The 64 KiB cap keeps an unbounded streamed body from
// exhausting memory.
func readJSONBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	return nil
}

// encodeTags produces the JSON array string stored in the tags column.
// Nil / empty slices round-trip to "[]" so the NOT NULL DEFAULT '[]'
// constraint is never violated and the column always parses as JSON.
func encodeTags(tags []string) (string, error) {
	if len(tags) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("encode tags: %w", err)
	}
	return string(raw), nil
}

// expiresArg returns the argument passed to SQLite for the expires_at
// column. A nil *time.Time writes SQL NULL (no expiry); a non-nil pointer
// writes the RFC3339Nano UTC rendering so retention_sweep.go can compare
// it lexicographically against time.Now().UTC().Format(time.RFC3339Nano).
func expiresArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// CreateMemoryRow inserts a new row into stoke_memory_bus and returns the
// autoincrement id. Key is optional — when empty we derive a nanosecond-
// unique key so two creates without an explicit key do not collide on the
// UNIQUE (scope, scope_target, key) constraint.
func (d *DB) CreateMemoryRow(scope, scopeTarget, key, content string, tags []string, expiresAt *time.Time, now time.Time) (int64, error) {
	tagsJSON, err := encodeTags(tags)
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if key == "" {
		key = fmt.Sprintf("r1-%d", now.UnixNano())
	}
	res, err := d.sql.Exec(
		`INSERT INTO stoke_memory_bus(
		    created_at, expires_at, scope, scope_target, key,
		    author, content, content_hash, tags
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?)`,
		now.Format(time.RFC3339Nano),
		expiresArg(expiresAt),
		scope,
		scopeTarget,
		key,
		"r1-server",
		content,
		tagsJSON,
	)
	if err != nil {
		return 0, fmt.Errorf("insert memory row: %w", err)
	}
	return res.LastInsertId()
}

// UpdateMemoryRow rewrites the mutable columns (content, tags, expires_at)
// of an existing row. Returns sql.ErrNoRows when the id matches no row so
// the caller surfaces a 404 rather than a silent success.
func (d *DB) UpdateMemoryRow(id int64, content string, tags []string, expiresAt *time.Time) error {
	tagsJSON, err := encodeTags(tags)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.sql.Exec(
		`UPDATE stoke_memory_bus
		    SET content = ?, tags = ?, expires_at = ?
		  WHERE id = ?`,
		content, tagsJSON, expiresArg(expiresAt), id,
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
// was affected so the caller surfaces a 404 instead of a silent 204.
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

// getMemoryScope loads the scope column for a single row so PUT / DELETE
// can enforce the ScopeAlways passphrase gate without re-fetching the full
// row.
func (d *DB) getMemoryScope(id int64) (string, error) {
	var scope string
	err := d.sql.QueryRow(
		`SELECT scope FROM stoke_memory_bus WHERE id = ?`, id,
	).Scan(&scope)
	return scope, err
}

// serveMemoryCreate handles POST /api/memories. 201 on success with the
// body {id, ok:true}; 400 on malformed JSON or missing required fields;
// 401 when scope=="always" and the passphrase gate rejects.
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
	if !requirePassphraseIfAlways(w, req.Scope, req.Passphrase) {
		return
	}
	id, err := d.CreateMemoryRow(req.Scope, req.ScopeTarget, req.Key, req.Content, req.Tags, req.ExpiresAt, time.Now().UTC())
	if err != nil {
		http.Error(w, "create memory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "ok": true})
}

// serveMemoryUpdate handles PUT /api/memories/{id}. The passphrase gate
// runs against the stored row's scope, not a body-supplied one, so a
// caller cannot bypass the gate by omitting scope from the update payload.
// 200 on success; 404 when the id matches no row; 401 when the stored
// scope is "always" and the passphrase fails.
func (d *DB) serveMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}
	id, ok := parseMemoryID(w, r)
	if !ok {
		return
	}
	var req memoryUpdateRequest
	if err := readJSONBody(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	if !requirePassphraseIfAlways(w, scope, req.Passphrase) {
		return
	}
	if err := d.UpdateMemoryRow(id, req.Content, req.Tags, req.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "update memory: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
}

// serveMemoryDelete handles DELETE /api/memories/{id}. 204 on success;
// 404 when the id is unknown (including a repeat DELETE of a row that was
// already removed); 401 when the stored scope is "always" and the caller
// cannot prove the passphrase. The JSON body is optional — only needed
// for scope=="always" rows — and an absent body is decoded as the zero
// memoryUpdateRequest so the gate rejects cleanly.
func (d *DB) serveMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}
	id, ok := parseMemoryID(w, r)
	if !ok {
		return
	}
	var req memoryUpdateRequest
	if r.ContentLength > 0 {
		if err := readJSONBody(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
	if !requirePassphraseIfAlways(w, scope, req.Passphrase) {
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

// parseMemoryID pulls and validates the {id} path value shared by PUT +
// DELETE. Writes a 400 + returns ok=false when the id is malformed so the
// caller can bail early without duplicating validation boilerplate.
func parseMemoryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSuffix(r.PathValue("id"), "/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
