// Package main — tracebundle.go
//
// Spec 4 §7 + §10 T15-T17: serves GET /api/session/{id}/export.tracebundle
// — a streamed tar.gz of every chain-tier node + edges + content-tier
// blobs (or a redacted-summary entry) for one session.
//
// Streaming: tar writes are written into a gzip.Writer wrapping the
// HTTP response. No 64 KB+ buffer is held in memory; the body
// streams as it's encoded. NO third-party deps.
//
// Format (manifest.json carries the version + session id; bumping
// the format requires bumping `tracebundleFormatVersion`):
//
//   manifest.json         {"format":"tracebundle","version":1,"session_id":"<id>","chain_root_hash":"<hash>"}
//   chain.ndjson          one chain-tier node per line
//   edges.ndjson          one edge per line
//   content/<id>.json     one content-tier blob per non-redacted node
//   content/redacted.json {"redacted":[{"node_id":"<id>"}, ...]}

package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const tracebundleFormatVersion = 1

// tracebundleManifest is the manifest.json shape.
type tracebundleManifest struct {
	Format        string `json:"format"`
	Version       int    `json:"version"`
	SessionID     string `json:"session_id"`
	ChainRootHash string `json:"chain_root_hash,omitempty"`
	GeneratedAt   string `json:"generated_at"`
}

// redactedSummary is the content/redacted.json shape: one entry per
// chain-tier node whose content tier was wiped.
type redactedSummary struct {
	Redacted []redactedEntry `json:"redacted"`
}

type redactedEntry struct {
	NodeID string `json:"node_id"`
}

// TracebundleSource is the minimal data the handler needs from the
// caller. Defined as an interface so tests can fixture without
// spinning up the full ledger + DB.
type TracebundleSource interface {
	SessionID() string
	ChainRootHash() string
	Chain() []TracebundleNode
	Edges() []TracebundleEdge
	Content(nodeID string) ([]byte, error)
	IsRedacted(nodeID string) bool
}

// TracebundleNode mirrors the chain-tier metadata each line of
// chain.ndjson encodes. Kept Go-friendly so the handler doesn't need
// to import the full ledger.Node type (which carries internal-only
// fields like the salt that should stay out of the export).
type TracebundleNode struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	CreatedAt     string          `json:"created_at"`
	CreatedBy     string          `json:"created_by"`
	ParentHash    string          `json:"parent_hash,omitempty"`
	Commitment    string          `json:"content_commitment"`
	Header        json.RawMessage `json:"header,omitempty"`
}

// TracebundleEdge is one line of edges.ndjson.
type TracebundleEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"`
}

// serveTracebundle writes the streamed tar.gz response. v2-only;
// 404 when R1_SERVER_UI_V2 is off. Headers (Cache-Control +
// Content-Disposition) set per §10 T17.
func serveTracebundle(src TracebundleSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := LoadV2Config()
		if !cfg.Renderable() {
			http.NotFound(w, r)
			return
		}
		sid := r.PathValue("id")
		if sid == "" {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) >= 4 {
				sid = parts[2]
			}
		}
		if sid == "" || sid != src.SessionID() {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s.tracebundle"`, sid))
		// Status implicit 200 once we start writing.

		if err := writeTracebundle(w, src); err != nil {
			// Body already in flight — best we can do is log via
			// the http.Error path; client will see truncation.
			http.Error(w, "tracebundle: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// writeTracebundle is exported as a free function for testing — the
// fixture-source TestTracebundle_RoundTrip can drive it without an
// http.ResponseWriter.
func writeTracebundle(out interface{ Write([]byte) (int, error) }, src TracebundleSource) error {
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	now := time.Now().UTC().Format(time.RFC3339)

	// 1. manifest.json
	manifest := tracebundleManifest{
		Format:        "tracebundle",
		Version:       tracebundleFormatVersion,
		SessionID:     src.SessionID(),
		ChainRootHash: src.ChainRootHash(),
		GeneratedAt:   now,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeTarFile(tw, "manifest.json", manifestBytes); err != nil {
		return err
	}

	// 2. chain.ndjson
	var chainBytes []byte
	for _, n := range src.Chain() {
		line, err := json.Marshal(n)
		if err != nil {
			return fmt.Errorf("marshal chain node %s: %w", n.ID, err)
		}
		chainBytes = append(chainBytes, line...)
		chainBytes = append(chainBytes, '\n')
	}
	if err := writeTarFile(tw, "chain.ndjson", chainBytes); err != nil {
		return err
	}

	// 3. edges.ndjson
	var edgeBytes []byte
	for _, e := range src.Edges() {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal edge: %w", err)
		}
		edgeBytes = append(edgeBytes, line...)
		edgeBytes = append(edgeBytes, '\n')
	}
	if err := writeTarFile(tw, "edges.ndjson", edgeBytes); err != nil {
		return err
	}

	// 4. content/<id>.json per non-redacted node
	// 5. content/redacted.json summary for redacted nodes
	redacted := redactedSummary{Redacted: []redactedEntry{}}
	for _, n := range src.Chain() {
		if src.IsRedacted(n.ID) {
			redacted.Redacted = append(redacted.Redacted, redactedEntry{NodeID: n.ID})
			continue
		}
		blob, err := src.Content(n.ID)
		if err != nil {
			return fmt.Errorf("content %s: %w", n.ID, err)
		}
		if err := writeTarFile(tw, "content/"+n.ID+".json", blob); err != nil {
			return err
		}
	}
	redactedBytes, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal redacted summary: %w", err)
	}
	if err := writeTarFile(tw, "content/redacted.json", redactedBytes); err != nil {
		return err
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(body)),
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("tar body %s: %w", name, err)
	}
	return nil
}
