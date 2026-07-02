package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/memory"
)

// seedEpisodic writes an agent-memory.json under dir/.r1 with the given
// (content, tag) pairs — the real episodic surface the command reads.
func seedEpisodic(t *testing.T, dir string, entries [][2]string) {
	t.Helper()
	store, err := memory.NewStore(memory.Config{Path: filepath.Join(dir, ".r1", "agent-memory.json")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, e := range entries {
		store.RememberWithContext(memory.CatGotcha, e[0], "", "", e[1])
	}
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestMemoryConsolidate_EndToEnd is the A101 activation proof: episodic
// entries in the live memory store → `r1 memory consolidate` →
// persisted semantic insights.
func TestMemoryConsolidate_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	seedEpisodic(t, dir, [][2]string{
		{"always run go vet before commit", "ci"},
		{"vet catches printf format bugs", "ci"},
		{"prefer table-driven tests", "testing"},
	})

	var out, errb bytes.Buffer
	code := runMemoryConsolidateCmd([]string{"--repo", dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("consolidate exit=%d stderr=%s", code, errb.String())
	}
	// Two labels (ci, testing) => two insights.
	if !strings.Contains(out.String(), "scanned 3 episodic, added 2 semantic") {
		t.Errorf("stdout=%q want scanned 3 / added 2", out.String())
	}

	// The insights must be durably persisted in the semantic tier.
	semantic, err := memory.NewFileStore(filepath.Join(dir, ".r1", "memory", "semantic.json"))
	if err != nil {
		t.Fatalf("open semantic: %v", err)
	}
	items, err := semantic.Query(context.Background(), memory.Query{Tier: memory.TierSemantic})
	if err != nil {
		t.Fatalf("query semantic: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("semantic items=%d want 2 (%+v)", len(items), items)
	}

	byID := map[string]memory.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	ci, ok := byID["insight-ci"]
	if !ok {
		t.Fatalf("missing insight-ci; got %v", byID)
	}
	if ci.Content != `consolidated 2 episodic memories labeled "ci"` {
		t.Errorf("insight-ci content=%q", ci.Content)
	}
	if _, ok := byID["insight-testing"]; !ok {
		t.Errorf("missing insight-testing; got %v", byID)
	}
}

// TestMemoryConsolidate_Idempotent proves a deterministic ExtractFunc:
// running twice over the same corpus yields the same insight set (Put
// overwrites by ID rather than duplicating).
func TestMemoryConsolidate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	seedEpisodic(t, dir, [][2]string{
		{"one", "a"},
		{"two", "a"},
	})
	var out bytes.Buffer
	for i := 0; i < 2; i++ {
		out.Reset()
		if code := runMemoryConsolidateCmd([]string{"--repo", dir}, &out, &out); code != 0 {
			t.Fatalf("run %d exit nonzero: %s", i, out.String())
		}
	}
	semantic, err := memory.NewFileStore(filepath.Join(dir, ".r1", "memory", "semantic.json"))
	if err != nil {
		t.Fatal(err)
	}
	items, _ := semantic.Query(context.Background(), memory.Query{Tier: memory.TierSemantic})
	if len(items) != 1 {
		t.Errorf("after two runs semantic items=%d want 1 (idempotent)", len(items))
	}
}

// TestMemoryConsolidate_EmptyStore: no episodic entries is success with
// zero insights, not an error.
func TestMemoryConsolidate_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := runMemoryConsolidateCmd([]string{"--repo", dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("empty consolidate exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "scanned 0 episodic, added 0 semantic") {
		t.Errorf("stdout=%q want scanned 0 / added 0", out.String())
	}
}

// TestMemoryConsolidate_JSON exercises the machine-readable report.
func TestMemoryConsolidate_JSON(t *testing.T) {
	dir := t.TempDir()
	seedEpisodic(t, dir, [][2]string{{"x", "k"}})
	var out, errb bytes.Buffer
	code := runMemoryConsolidateCmd([]string{"--repo", dir, "--json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"insights_added":1`) {
		t.Errorf("json out=%q want insights_added:1", out.String())
	}
}

// TestRunConsolidateDaemon_TicksAndStops exercises the --interval
// daemon path deterministically via a context deadline (no signals).
func TestRunConsolidateDaemon_TicksAndStops(t *testing.T) {
	dir := t.TempDir()
	seedEpisodic(t, dir, [][2]string{{"x", "k"}})
	job, _, err := buildConsolidationJob(dir, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("buildConsolidationJob: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if code := runConsolidateDaemon(ctx, job, &out, false); code != 0 {
		t.Fatalf("daemon exit=%d", code)
	}
	if !strings.Contains(out.String(), "scanned") {
		t.Errorf("daemon produced no run report: %q", out.String())
	}
}

// TestMemoryDispatch_ConsolidateVerb proves `r1 memory consolidate`
// routes through the top-level memory dispatcher.
func TestMemoryDispatch_ConsolidateVerb(t *testing.T) {
	dir := t.TempDir()
	seedEpisodic(t, dir, [][2]string{{"x", "k"}})
	var out, errb bytes.Buffer
	code := runMemoryCmd([]string{"consolidate", "--repo", dir}, &out, &errb)
	if code != 0 {
		t.Fatalf("dispatch exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "added 1 semantic") {
		t.Errorf("stdout=%q", out.String())
	}
}
