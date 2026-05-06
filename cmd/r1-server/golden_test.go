// Package main — golden_test.go
//
// Spec 5 §4 + §10 T1: golden template suite. Each TestGolden_*
// renders one page template against a deterministic fixture and
// compares against testdata/golden/<name>.html. Run with -update to
// rewrite the goldens after an intentional change:
//
//   go test -run TestGolden -update ./cmd/r1-server/...
//
// Fixture rules:
//   - All timestamps are time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
//     so the formatted output is byte-stable across runs.
//   - Session ids are short literal strings (e.g. "sess-fixture").
//   - SRI values are short literal strings (sha384-FAKEHTMX, etc.) —
//     the real SRI is exercised by sri_test.go, not the golden suite.
package main

import (
	"bytes"
	"flag"
	"html/template"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var goldenUpdate = flag.Bool("update", false, "rewrite golden files")

const fixtureGoldenDate = "2026-01-01T12:00:00Z"

func goldenAssert(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *goldenUpdate {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test -run TestGolden -update ./cmd/r1-server/...` to seed)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: golden mismatch (run `go test -run TestGolden -update` to refresh)\n--- want (%d bytes)\n%s\n--- got (%d bytes)\n%s",
			name, len(want), want, len(got), got)
	}
}

// goldenBaseCtx is the fixture for base.html + every page that
// extends it. Every test seeds this and overrides only the bits its
// page cares about.
func goldenBaseCtx() V2BaseContext {
	return V2BaseContext{
		Title:      "fixture title",
		HtmxSRI:    "sha384-FAKEHTMX",
		HtmxSseSRI: "sha384-FAKESSE",
		SessionID:  "sess-fixture",
	}
}

func renderGolden(t *testing.T, page, blockName string, ctx interface{}) []byte {
	t.Helper()
	tmpl, err := parseV2Template(page)
	if err != nil {
		t.Fatalf("parseV2Template(%s): %v", page, err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, blockName, ctx); err != nil {
		t.Fatalf("execute %s: %v", blockName, err)
	}
	return buf.Bytes()
}

func TestGolden_Base(t *testing.T) {
	got := renderGolden(t, "base", "base", goldenBaseCtx())
	goldenAssert(t, "base.html", got)
}

func TestGolden_Index(t *testing.T) {
	type instanceRow struct {
		ID              string
		Name            string
		Status          string
		EventCount      int
		LastActivityISO string
	}
	ctx := struct {
		V2BaseContext
		Instances []instanceRow
	}{
		V2BaseContext: goldenBaseCtx(),
		Instances: []instanceRow{
			{"sess-a", "session a", "active", 42, fixtureGoldenDate},
			{"sess-b", "session b", "idle", 7, fixtureGoldenDate},
		},
	}
	got := renderGolden(t, "index", "index", ctx)
	goldenAssert(t, "index.html", got)
}

func TestGolden_Session(t *testing.T) {
	type waterfallRow struct {
		NodeID, NodeType, Title, DurationStr, CostStr, TypeIcon string
		CreatedAtUnix                                            int64
		Depth                                                    int
		Chevron                                                  string
		IsRedacted, IsRedactionAnomaly                           bool
	}
	type sessionInfo struct {
		ID, Name      string
		StartedAtUnix int64
		UpdatedAtUnix int64
	}
	type chipItem struct {
		Label, Name, Value, Target string
		Active                     bool
	}
	type filterChips struct {
		Items []chipItem
	}
	ctx := struct {
		V2BaseContext
		Session     sessionInfo
		Rows        []waterfallRow
		LastEventID string
		LastID      string
		Filters     filterChips
	}{
		V2BaseContext: goldenBaseCtx(),
		Session: sessionInfo{
			ID:            "sess-fixture",
			Name:          "fixture session",
			StartedAtUnix: 1735732800,
			UpdatedAtUnix: 1735819200,
		},
		Rows: []waterfallRow{
			{NodeID: "n-1", NodeType: "agent_io", Title: "first", DurationStr: "10ms", CostStr: "$0.001", TypeIcon: "📦", CreatedAtUnix: 1735732800},
			{NodeID: "n-2", NodeType: "agent_io", Title: "second", DurationStr: "15ms", CostStr: "$0.002", TypeIcon: "📦", CreatedAtUnix: 1735732815, IsRedacted: true},
		},
		LastEventID: "0",
		LastID:      "0",
		Filters: filterChips{
			Items: []chipItem{
				{Label: "errors", Name: "types", Value: "error.*", Target: "/session/sess-fixture/waterfall", Active: false},
			},
		},
	}
	got := renderGolden(t, "session", "session", ctx)
	goldenAssert(t, "session.html", got)
}

func TestGolden_SessionGraph(t *testing.T) {
	ctx := SessionGraphContext{
		V2BaseContext: goldenBaseCtx(),
		GraphData:     template.JS(`{"nodes":[],"edges":[]}`),
	}
	got := renderGolden(t, "session-graph", "session-graph", ctx)
	goldenAssert(t, "session-graph.html", got)
}

func TestGolden_SessionStream(t *testing.T) {
	ctx := streamViewContext{
		V2BaseContext: goldenBaseCtx(),
		Session: streamSessionInfo{
			ID:   "sess-fixture",
			Name: "fixture session",
		},
		LastEventID: "0",
	}
	got := renderGolden(t, "session-stream", "session-stream", ctx)
	goldenAssert(t, "session-stream.html", got)
}

func TestGolden_Memories(t *testing.T) {
	type chipItem struct {
		Label, Name, Value, Target string
		Active                     bool
	}
	type filterChips struct {
		Items []chipItem
	}
	type memCard struct {
		ID                int64
		Key, Scope        string
		Snippet           string
		CreatedAtISO      string
		ContentEncrypted  bool
	}
	type memGroup struct {
		Scope string
		Cards []memCard
	}
	ctx := struct {
		V2BaseContext
		Filters filterChips
		Groups  []memGroup
	}{
		V2BaseContext: goldenBaseCtx(),
		Filters: filterChips{
			Items: []chipItem{
				{Label: "session", Name: "scope", Value: "session", Target: "/memories", Active: true},
			},
		},
		Groups: []memGroup{
			{
				Scope: "session",
				Cards: []memCard{
					{ID: 1, Key: "feedback_test", Scope: "session", Snippet: "snippet", CreatedAtISO: fixtureGoldenDate},
				},
			},
		},
	}
	got := renderGolden(t, "memories", "memories", ctx)
	goldenAssert(t, "memories.html", got)
}

func TestGolden_Share(t *testing.T) {
	got := renderGolden(t, "share", "share", shareV2Context{
		V2BaseContext: goldenBaseCtx(),
		Snapshot: shareSnapshot{
			ChainRootHash: "abcdef0123456789",
			CreatedAtISO:  fixtureGoldenDate,
			SessionName:   "fixture snapshot",
		},
		Rows: []shareRow{
			{NodeID: "n-1", NodeType: "agent_io", Title: "row", DurationStr: "1ms", CostStr: "$0", TypeIcon: "📦", CreatedAtUnix: 1735732800},
		},
	})
	goldenAssert(t, "share.html", got)
}

func TestGolden_Diff(t *testing.T) {
	ctx := struct {
		V2BaseContext
		A    string
		B    string
		Rows []DiffRow
	}{
		V2BaseContext: goldenBaseCtx(),
		A:             "alpha",
		B:             "beta",
		Rows: []DiffRow{
			{Kind: "added", EventType: "task.start", Key: "t1", StatusA: "", StatusB: "active", IndexA: 0, IndexB: 0},
		},
	}
	got := renderGolden(t, "diff", "diff", ctx)
	goldenAssert(t, "diff.html", got)
}

// _ pinned imports so tests that use them compile.
var _ = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
