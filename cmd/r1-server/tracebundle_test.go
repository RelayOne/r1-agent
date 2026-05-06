// Package main — tracebundle_test.go
//
// Spec 4 §10 T16 + T17: round-trip the tracebundle export against
// a fixture source; verify every spec'd file is present + headers.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureSource implements TracebundleSource for tests.
type fixtureSource struct {
	id      string
	root    string
	chain   []TracebundleNode
	edges   []TracebundleEdge
	content map[string][]byte
	wiped   map[string]bool
}

func (f *fixtureSource) SessionID() string                  { return f.id }
func (f *fixtureSource) ChainRootHash() string              { return f.root }
func (f *fixtureSource) Chain() []TracebundleNode           { return f.chain }
func (f *fixtureSource) Edges() []TracebundleEdge           { return f.edges }
func (f *fixtureSource) IsRedacted(id string) bool          { return f.wiped[id] }
func (f *fixtureSource) Content(id string) ([]byte, error)  { return f.content[id], nil }

func makeFixtureSource() *fixtureSource {
	src := &fixtureSource{
		id:    "sess-fix",
		root:  "abcdef0123456789",
		wiped: map[string]bool{"node-r": true},
		content: map[string][]byte{
			"node-1": []byte(`{"hello":"world"}`),
			"node-2": []byte(`{"answer":42}`),
			"node-3": []byte(`{"third":"node"}`),
			"node-4": []byte(`{"fourth":"alive"}`),
		},
	}
	for _, id := range []string{"node-1", "node-2", "node-3", "node-4", "node-r"} {
		src.chain = append(src.chain, TracebundleNode{
			ID:            id,
			Type:          "agent_io",
			SchemaVersion: 1,
			CreatedBy:     "test",
		})
	}
	src.edges = []TracebundleEdge{
		{From: "node-1", To: "node-2", Kind: "follows"},
		{From: "node-2", To: "node-3", Kind: "follows"},
	}
	return src
}

func TestTracebundle_RoundTrip(t *testing.T) {
	src := makeFixtureSource()
	var buf bytes.Buffer
	if err := writeTracebundle(&buf, src); err != nil {
		t.Fatalf("writeTracebundle: %v", err)
	}

	gzr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		files[hdr.Name] = body
	}

	for _, want := range []string{
		"manifest.json", "chain.ndjson", "edges.ndjson",
		"content/node-1.json", "content/node-2.json",
		"content/node-3.json", "content/node-4.json",
		"content/redacted.json",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("file missing from bundle: %s", want)
		}
	}
	// Redacted blob should NOT be present
	if _, ok := files["content/node-r.json"]; ok {
		t.Errorf("redacted node-r unexpectedly present in bundle")
	}

	// manifest is well-formed JSON with the spec'd fields
	var mani tracebundleManifest
	if err := json.Unmarshal(files["manifest.json"], &mani); err != nil {
		t.Fatalf("manifest unmarshal: %v", err)
	}
	if mani.Format != "tracebundle" {
		t.Errorf("manifest.format = %q, want tracebundle", mani.Format)
	}
	if mani.Version != tracebundleFormatVersion {
		t.Errorf("manifest.version = %d, want %d", mani.Version, tracebundleFormatVersion)
	}
	if mani.SessionID != "sess-fix" {
		t.Errorf("manifest.session_id = %q, want sess-fix", mani.SessionID)
	}
	if mani.ChainRootHash != "abcdef0123456789" {
		t.Errorf("manifest.chain_root_hash mismatch")
	}

	// chain.ndjson: 5 lines, one per node
	chainLines := strings.Count(strings.TrimRight(string(files["chain.ndjson"]), "\n"), "\n") + 1
	if chainLines != 5 {
		t.Errorf("chain.ndjson lines = %d, want 5", chainLines)
	}

	// edges.ndjson: 2 lines
	edgeLines := strings.Count(strings.TrimRight(string(files["edges.ndjson"]), "\n"), "\n") + 1
	if edgeLines != 2 {
		t.Errorf("edges.ndjson lines = %d, want 2", edgeLines)
	}

	// redacted.json: exactly the one wiped node
	var summary redactedSummary
	if err := json.Unmarshal(files["content/redacted.json"], &summary); err != nil {
		t.Fatalf("redacted.json unmarshal: %v", err)
	}
	if len(summary.Redacted) != 1 || summary.Redacted[0].NodeID != "node-r" {
		t.Errorf("redacted summary = %+v, want [{node-r}]", summary.Redacted)
	}
}

// (Spec D — D-UI2-7 — deleted TestServeTracebundle_404OnFlagOff.
// The R1_SERVER_UI_V2 toggle was removed; the tracebundle export
// is now always reachable when its route is mounted.)

func TestServeTracebundle_HeadersAndBody(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	src := makeFixtureSource()
	h := serveTracebundle(src)
	req := httptest.NewRequest("GET", "/api/session/sess-fix/export.tracebundle", nil)
	req.SetPathValue("id", "sess-fix")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/gzip" {
		t.Errorf("Content-Type = %q, want application/gzip", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Errorf("Cache-Control = %q, want private, no-cache", got)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `attachment`) || !strings.Contains(cd, `sess-fix.tracebundle`) {
		t.Errorf("Content-Disposition = %q, want attachment + filename", cd)
	}
	if rec.Body.Len() == 0 {
		t.Error("body should be non-empty")
	}
}

func TestServeTracebundle_404OnSessionMismatch(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	src := makeFixtureSource()
	h := serveTracebundle(src)
	req := httptest.NewRequest("GET", "/api/session/some-other-id/export.tracebundle", nil)
	req.SetPathValue("id", "some-other-id")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 404 {
		t.Errorf("session mismatch: status = %d, want 404", rec.Code)
	}
}
