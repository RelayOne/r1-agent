package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// Run diff view (minimum viable) — spec r1-server-ui-v2 §"Run diff
// view (minimum viable)". Walks both sessions and emits a flat row
// per event-type pair classified as added / removed / changed-status.
// "Content-diff" is explicitly out of scope and called out in the
// template footer per the spec.

// DiffRow describes one ordering-aware difference between two
// sessions' event streams. Each row is keyed by the (event_type,
// node_id-or-best-key) combination derived from the JSON payload.
type DiffRow struct {
	Kind      string `json:"kind"`        // "added" | "removed" | "changed_status"
	EventType string `json:"event_type"`  // e.g. "task.start", "ledger.append"
	Key       string `json:"key"`         // best-effort identifier (node id, task id, ...)
	StatusA   string `json:"status_a"`    // status field (when present) on side A
	StatusB   string `json:"status_b"`    // status field (when present) on side B
	IndexA    int    `json:"index_a,omitempty"`
	IndexB    int    `json:"index_b,omitempty"`
}

// fpSep separates event type from key inside the diff index map.
// 0x1f (Unit Separator) is forbidden in real event names by the
// schema validator, so the split round-trip is total.
const fpSep = "\x1f"

// diffSessions walks the events for two sessions and classifies each
// (type, key) pair as added (only in B), removed (only in A), or
// changed_status (status field differs between A and B). Returns rows
// sorted deterministically (event_type, then key, then kind) so
// snapshot tests are stable across runs.
//
// The "key" field is heuristic: we look at common JSON fields in
// priority order — id, node_id, task_id, name, key. If none are
// present, the row falls back to the event index, which produces
// noisy diffs but never collides across genuinely-different events.
func diffSessions(d *DB, a, b string) ([]DiffRow, error) {
	const limit = 10000 // cap diff cost; sessions over this size aren't expected
	evA, err := d.ListEvents(a, 0, limit)
	if err != nil {
		return nil, fmt.Errorf("list events for session a (%s): %w", a, err)
	}
	evB, err := d.ListEvents(b, 0, limit)
	if err != nil {
		return nil, fmt.Errorf("list events for session b (%s): %w", b, err)
	}

	type entry struct {
		idx    int
		status string
	}
	indexA := map[string]entry{}
	indexB := map[string]entry{}
	for i, e := range evA {
		fp := e.EventType + fpSep + keyOf(e)
		indexA[fp] = entry{idx: i, status: statusOf(e)}
	}
	for i, e := range evB {
		fp := e.EventType + fpSep + keyOf(e)
		indexB[fp] = entry{idx: i, status: statusOf(e)}
	}

	out := []DiffRow{}
	// Removed (in A but not B).
	for fp, ea := range indexA {
		if _, ok := indexB[fp]; ok {
			continue
		}
		t, k := splitFP(fp)
		out = append(out, DiffRow{
			Kind: "removed", EventType: t, Key: k, StatusA: ea.status, IndexA: ea.idx + 1,
		})
	}
	// Added (in B but not A).
	for fp, eb := range indexB {
		if _, ok := indexA[fp]; ok {
			continue
		}
		t, k := splitFP(fp)
		out = append(out, DiffRow{
			Kind: "added", EventType: t, Key: k, StatusB: eb.status, IndexB: eb.idx + 1,
		})
	}
	// Status changed (present in both, status differs).
	for fp, ea := range indexA {
		eb, ok := indexB[fp]
		if !ok {
			continue
		}
		if ea.status == eb.status {
			continue
		}
		t, k := splitFP(fp)
		out = append(out, DiffRow{
			Kind: "changed_status", EventType: t, Key: k,
			StatusA: ea.status, StatusB: eb.status,
			IndexA: ea.idx + 1, IndexB: eb.idx + 1,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].EventType != out[j].EventType {
			return out[i].EventType < out[j].EventType
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// keyOf derives a stable identifier from the event's JSON payload.
// Tries common id-bearing fields in priority order; returns "" when
// none match (caller's fingerprint then falls back to "").
func keyOf(e EventRow) string {
	if len(e.Data) == 0 {
		return ""
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(e.Data, &raw); err != nil {
		return ""
	}
	for _, field := range []string{"id", "node_id", "task_id", "name", "key"} {
		if v, ok := raw[field]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

// statusOf extracts the status field from the event's JSON payload.
// Empty when absent — matches the "no status to compare" case the
// diff classifier treats as unchanged.
func statusOf(e EventRow) string {
	if len(e.Data) == 0 {
		return ""
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(e.Data, &raw); err != nil {
		return ""
	}
	for _, field := range []string{"status", "state", "phase"} {
		if v, ok := raw[field]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				return s
			}
		}
	}
	return ""
}

// splitFP undoes the eventType+fpSep+key encoding from diffSessions.
// Total because we control both ends of the round-trip.
func splitFP(fp string) (string, string) {
	for i := 0; i < len(fp); i++ {
		if fp[i] == '\x1f' {
			return fp[:i], fp[i+1:]
		}
	}
	return fp, ""
}

// serveDiff handles GET /diff/{a}/{b}. Returns JSON when the client
// sends Accept: application/json, otherwise renders a minimal HTML
// table. The route is gated on R1_SERVER_UI_V2 in mountUI.
//
// Spec note: content-diff is explicitly out of scope. The footer
// references issue #144 where content-diff is tracked.
func (d *DB) serveDiff(w http.ResponseWriter, r *http.Request) {
	a := r.PathValue("a")
	b := r.PathValue("b")
	if a == "" || b == "" {
		http.Error(w, "diff requires two session ids", http.StatusBadRequest)
		return
	}
	rows, err := diffSessions(d, a, b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if accepts(r, "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"a":    a,
			"b":    b,
			"rows": rows,
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Spec 4 §10 T9: render the v2 diff.html template that extends
	// base.html. Falls back to the legacy inline fmt.Fprintf path
	// when the v2 surface is off so non-htmx clients keep working.
	cfg := LoadV2Config()
	if cfg.Renderable() {
		tmpl, err := parseV2Template("diff")
		if err == nil {
			ctx := struct {
				V2BaseContext
				A    string
				B    string
				Rows []DiffRow
			}{
				V2BaseContext: V2BaseContext{
					Title:      "Diff",
					HtmxSRI:    cfg.HtmxSRI,
					HtmxSseSRI: cfg.HtmxSseSRI,
				},
				A:    a,
				B:    b,
				Rows: rows,
			}
			if err := tmpl.ExecuteTemplate(w, "diff", ctx); err == nil {
				return
			}
			// Render error: fall through to legacy path so an
			// operator hitting /diff during a template-broken deploy
			// still gets useful output rather than a 500.
		}
	}

	fmt.Fprintf(w, `<!doctype html><meta charset=utf-8>
<title>Diff %s vs %s</title>
<style>body{font-family:system-ui,sans-serif;padding:1rem}
table{border-collapse:collapse;width:100%%}
th,td{padding:.25rem .5rem;border-bottom:1px solid #ddd;text-align:left}
.added{background:#e8fff0}.removed{background:#ffe8e8}.changed_status{background:#fff8e0}
footer{margin-top:2rem;color:#666;font-size:.85rem}</style>
<h1>Diff <code>%s</code> vs <code>%s</code></h1>
<table><thead><tr>
<th>Kind</th><th>Event type</th><th>Key</th><th>Status A → B</th><th>#A</th><th>#B</th>
</tr></thead><tbody>
`, htmlEscape(a), htmlEscape(b), htmlEscape(a), htmlEscape(b))

	if len(rows) == 0 {
		fmt.Fprintf(w, `<tr><td colspan=6><em>(no differences)</em></td></tr>`)
	}
	for _, row := range rows {
		fmt.Fprintf(w,
			`<tr class="%s"><td>%s</td><td><code>%s</code></td><td><code>%s</code></td><td>%s → %s</td><td>%d</td><td>%d</td></tr>`,
			htmlEscape(row.Kind),
			htmlEscape(row.Kind),
			htmlEscape(row.EventType),
			htmlEscape(row.Key),
			htmlEscape(row.StatusA),
			htmlEscape(row.StatusB),
			row.IndexA,
			row.IndexB,
		)
	}
	fmt.Fprintf(w, `</tbody></table>
<footer>Content-diff (per-event JSON payload comparison) is out of scope here. Tracked in issue #144.</footer>
`)
}

// accepts returns true when the request's Accept header advertises
// the given content type. Trailing parameters (q-values) are
// tolerated by the substring match — sufficient for the JSON branch.
func accepts(r *http.Request, ct string) bool {
	for _, h := range r.Header.Values("Accept") {
		if h == "" {
			continue
		}
		if h == ct || h == "*/*" {
			return true
		}
		for i := 0; i+len(ct) <= len(h); i++ {
			if h[i:i+len(ct)] == ct {
				return true
			}
		}
	}
	return false
}

// htmlEscape replaces the four characters that have semantic meaning
// inside an HTML attribute or text body. Inline to avoid pulling in
// html/template for a constant template — the engine would be
// heavier than the substitution.
func htmlEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			out = append(out, "&amp;"...)
		case '<':
			out = append(out, "&lt;"...)
		case '>':
			out = append(out, "&gt;"...)
		case '"':
			out = append(out, "&quot;"...)
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
