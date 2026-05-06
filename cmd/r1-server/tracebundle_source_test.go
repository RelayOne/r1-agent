// Package main — tracebundle_source_test.go
//
// Spec 4 §10 T15-T17 caller wiring: integration test for the
// production ledgerTracebundleSource against a real ledger.Store.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

func newRealLedger(t *testing.T) (*ledger.Ledger, *ledger.Store, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "ledger")
	led, err := ledger.New(root)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	store, err := ledger.NewStore(root)
	if err != nil {
		t.Fatalf("ledger.NewStore: %v", err)
	}
	return led, store, root
}

func TestLedgerTracebundleSource_RoundTripWithRealLedger(t *testing.T) {
	led, store, _ := newRealLedger(t)

	// Add three nodes, redact the second.
	ctx := context.Background()
	for i, body := range []string{
		`{"step":"start"}`,
		`{"step":"middle","sensitive":"yes"}`,
		`{"step":"end"}`,
	} {
		_, err := led.AddNode(ctx, ledger.Node{
			Type:          "agent_io",
			SchemaVersion: 1,
			CreatedAt:     time.Now().UTC(),
			CreatedBy:     "test",
			MissionID:     "sess-real",
			Content:       json.RawMessage(body),
		})
		if err != nil {
			t.Fatalf("AddNode %d: %v", i, err)
		}
	}
	all, err := store.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d nodes, want 3", len(all))
	}
	// Redact the middle node.
	if _, err := store.Redact(ctx, string(all[1].ID), "test_redaction"); err != nil {
		t.Fatalf("Redact: %v", err)
	}

	src := newLedgerTracebundleSource(store, "sess-real")
	if src.SessionID() != "sess-real" {
		t.Errorf("SessionID = %q, want sess-real", src.SessionID())
	}
	if got := src.Chain(); len(got) != 3 {
		t.Errorf("Chain length = %d, want 3", len(got))
	}
	// One redacted node.
	redCount := 0
	for _, n := range src.Chain() {
		if src.IsRedacted(n.ID) {
			redCount++
		}
	}
	if redCount != 1 {
		t.Errorf("redacted count = %d, want 1", redCount)
	}

	// Stream the bundle and verify it parses.
	var buf bytes.Buffer
	if err := writeTracebundle(&buf, src); err != nil {
		t.Fatalf("writeTracebundle: %v", err)
	}
	gzr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip: %v", err)
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
		body, _ := io.ReadAll(tr)
		files[hdr.Name] = body
	}

	for _, want := range []string{"manifest.json", "chain.ndjson", "edges.ndjson", "content/redacted.json"} {
		if _, ok := files[want]; !ok {
			t.Errorf("file missing from bundle: %s", want)
		}
	}
	// Two non-redacted content blobs.
	contentCount := 0
	for name := range files {
		if strings.HasPrefix(name, "content/") && strings.HasSuffix(name, ".json") && name != "content/redacted.json" {
			contentCount++
		}
	}
	if contentCount != 2 {
		t.Errorf("got %d content blobs, want 2 (3 nodes − 1 redacted)", contentCount)
	}

	// Manifest version + session id.
	var mani tracebundleManifest
	if err := json.Unmarshal(files["manifest.json"], &mani); err != nil {
		t.Fatalf("manifest unmarshal: %v", err)
	}
	if mani.SessionID != "sess-real" {
		t.Errorf("manifest.session_id = %q, want sess-real", mani.SessionID)
	}
}

func TestServeTracebundleAdapter_404OnFlagOff(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	db := newTestDB(t)
	req := httptest.NewRequest("GET", "/api/session/x/export.tracebundle", nil)
	req.SetPathValue("id", "x")
	rec := httptest.NewRecorder()
	db.serveTracebundleAdapter(rec, req)
	if rec.Code != 404 {
		t.Errorf("flag off: status = %d, want 404", rec.Code)
	}
}

func TestServeTracebundleAdapter_404OnUnknownSession(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	req := httptest.NewRequest("GET", "/api/session/never-existed/export.tracebundle", nil)
	req.SetPathValue("id", "never-existed")
	rec := httptest.NewRecorder()
	db.serveTracebundleAdapter(rec, req)
	if rec.Code != 404 {
		t.Errorf("unknown session: status = %d, want 404", rec.Code)
	}
}
