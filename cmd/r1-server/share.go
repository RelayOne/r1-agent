// Package main — share.go
//
// Spec 27 §5.3 (r1-server-ui-v2.md) defines a content-addressed share
// route: GET /share/{hash} returns a read-only HTML snapshot of a
// session keyed by its chain-tier root hash. The route is dual-gated:
//
//  1. The whole spec-27 v2 surface is behind R1_SERVER_UI_V2=1 (per
//     §2.3 migration plan; off-by-default for two release cycles).
//  2. Share itself is independently gated by the per-config toggle
//     `r1_server.share_enabled` (default false). Until the YAML config
//     surface for r1-server lands, the toggle is read from the
//     R1_SERVER_SHARE_ENABLED env var so operators can opt in without
//     a recompile.
//
// Either gate off → 404. Both on → the read-only banner page renders
// with the chain root hash, the canonical share URL, and security
// headers (noindex, no-referrer, strict CSP). The full waterfall view
// (chain-tier snapshot loader + share.html htmx template) is part of
// the larger v2 retrofit and is not in this file.
//
// Hash validation: chain root hashes are SHA256 hex per the ledger
// spec (contentid package). We accept 8–64 lowercase hex chars to
// allow both short prefixes (debug/share UX) and full digests; bad
// shapes get 400 not 404 so the reason is unambiguous to clients.
package main

import (
	"html/template"
	"net/http"
	"os"
	"regexp"
)

// hashPattern matches a hex chain-root hash. Lowercase only — the
// ledger emits lowercase and accepting mixed case would let two URLs
// alias the same content, which content-addressing is supposed to
// prevent.
var hashPattern = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// shareTmpl is the read-only banner template parsed once at init.
// html/template auto-escapes the hash on render so URL-encoded
// surprises in the path can't break out of the attribute or text
// context. The page is intentionally tiny (no CSS, no JS) — the
// full htmx waterfall surface is out of scope per spec §5.3.
var shareTmpl = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>r1-server share — {{.Hash}}</title>
</head>
<body>
<header>
  <h1>Read-only snapshot</h1>
  <p>Content-addressed view served by r1-server (spec 27 §5.3).</p>
</header>
<main>
  <dl>
    <dt>Chain root hash</dt>
    <dd><code>{{.Hash}}</code></dd>
    <dt>Canonical URL</dt>
    <dd><code>{{.CanonicalURL}}</code></dd>
  </dl>
  <p>This snapshot is read-only. No edits, no live updates, no authentication required.</p>
</main>
</body>
</html>
`))

// shareView is the data the share template renders against.
type shareView struct {
	Hash         string
	CanonicalURL string
}

// shareEnabled reports whether the per-config share toggle is on.
// Reads R1_SERVER_SHARE_ENABLED on each call so tests + ops can flip
// without restart. Once r1-server gains a YAML config surface this
// becomes a struct field read; the env-var fallback stays as the
// operator escape hatch.
func shareEnabled() bool {
	return os.Getenv("R1_SERVER_SHARE_ENABLED") == "1"
}

// serveShare renders the share view when both feature gates are on
// and the URL hash is well-formed. Status codes:
//
//	404 — either gate is off (matches spec §5.3 "default false" + §2.3
//	      acceptance: v2-only paths 404 in MVP mode)
//	400 — gates are on but the hash failed shape validation
//	200 — gates on, hash valid, HTML rendered
//
// We deliberately serve 404 (not 403) for the disabled cases so the
// route is indistinguishable from "no such route" to unauthenticated
// scanners. Operators who flipped the toggle know to expect 200.
func serveShare(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() || !shareEnabled() {
		http.NotFound(w, r)
		return
	}
	hash := r.PathValue("hash")
	if !hashPattern.MatchString(hash) {
		http.Error(w, "invalid chain root hash", http.StatusBadRequest)
		return
	}

	view := shareView{
		Hash:         hash,
		CanonicalURL: "/share/" + hash,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600, immutable")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; script-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if err := shareTmpl.Execute(w, view); err != nil {
		// Headers already flushed at this point in most cases; best
		// effort log via the http package's default error path.
		http.Error(w, "render share: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// v2Enabled reports whether the spec-27 v2 UI handlers should serve
// content. Reads R1_SERVER_UI_V2 on each call so tests + ops can flip
// the flag without restart. Per spec §2.3 the flag stays off-by-default
// for two release weeks, then defaults on with R1_SERVER_UI_V2=0 as
// the documented escape hatch.
func v2Enabled() bool {
	return os.Getenv("R1_SERVER_UI_V2") == "1"
}
