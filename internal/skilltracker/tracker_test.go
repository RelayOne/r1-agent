package skilltracker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

func newTrackerWithLedger(t *testing.T) (*Tracker, *ledger.Ledger, string) {
	t.Helper()
	dir := t.TempDir()
	led, err := ledger.New(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	return New(led), led, filepath.Join(dir, "ledger")
}

func readSkillUnloaded(t *testing.T, ledgerRoot string) []nodes.SkillUnloaded {
	t.Helper()
	chainDir := filepath.Join(ledgerRoot, "chain")
	contentDir := filepath.Join(ledgerRoot, "content")
	entries, err := os.ReadDir(chainDir)
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	out := []nodes.SkillUnloaded{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(chainDir, e.Name()))
		if err != nil {
			t.Fatalf("read chain %s: %v", e.Name(), err)
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil || head.Type != "skill_unloaded" {
			continue
		}
		contentRaw, err := os.ReadFile(filepath.Join(contentDir, e.Name()))
		if err != nil {
			continue
		}
		var wrap struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(contentRaw, &wrap); err == nil && len(wrap.Content) > 0 {
			contentRaw = wrap.Content
		}
		var su nodes.SkillUnloaded
		if err := json.Unmarshal(contentRaw, &su); err == nil {
			out = append(out, su)
		}
	}
	return out
}

func TestTracker_NoteThenLoadedReportsTrue(t *testing.T) {
	tr, _, _ := newTrackerWithLedger(t)
	if err := tr.Note(LoadInfo{
		StanceID: "st-1", SkillRef: "alpha", LoadID: "ld-1",
		StanceRole: "cto", TaskScope: "task-7", LoadedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Note: %v", err)
	}
	if !tr.Loaded("st-1", "alpha") {
		t.Errorf("Loaded(st-1, alpha) = false, want true")
	}
	if tr.Loaded("st-1", "beta") {
		t.Errorf("Loaded(st-1, beta) = true (never noted)")
	}
}

func TestTracker_NoteRejectsEmptyFields(t *testing.T) {
	tr, _, _ := newTrackerWithLedger(t)
	if err := tr.Note(LoadInfo{StanceID: "", SkillRef: "x"}); err == nil {
		t.Error("empty StanceID should error")
	}
	if err := tr.Note(LoadInfo{StanceID: "st", SkillRef: ""}); err == nil {
		t.Error("empty SkillRef should error")
	}
}

func TestTracker_DropEmitsSkillUnloaded(t *testing.T) {
	tr, _, root := newTrackerWithLedger(t)
	_ = tr.Note(LoadInfo{
		StanceID: "st-1", SkillRef: "alpha", LoadID: "ld-1",
		StanceRole: "cto", TaskScope: "task-7",
	})
	id, err := tr.Drop(context.Background(), "st-1", "alpha", "explicit_unload")
	if err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if id == "" {
		t.Error("Drop returned empty NodeID for tracked skill")
	}
	if tr.Loaded("st-1", "alpha") {
		t.Error("Drop should remove the entry")
	}
	emitted := readSkillUnloaded(t, root)
	if len(emitted) != 1 {
		t.Fatalf("got %d unload events, want 1", len(emitted))
	}
	if emitted[0].Reason != "explicit_unload" {
		t.Errorf("Reason = %q, want explicit_unload", emitted[0].Reason)
	}
	if emitted[0].LoadRef != "ld-1" {
		t.Errorf("LoadRef = %q, want ld-1", emitted[0].LoadRef)
	}
}

func TestTracker_DropOnUntrackedIsNoop(t *testing.T) {
	tr, _, root := newTrackerWithLedger(t)
	id, err := tr.Drop(context.Background(), "st-x", "never-loaded", "explicit_unload")
	if err != nil {
		t.Errorf("untracked Drop should not error: %v", err)
	}
	if id != "" {
		t.Errorf("untracked Drop should return empty NodeID, got %q", id)
	}
	if got := readSkillUnloaded(t, root); len(got) != 0 {
		t.Errorf("untracked Drop should not emit, got %d events", len(got))
	}
}

func TestTracker_CloseScopeDropsAllSkillsInScope(t *testing.T) {
	tr, _, root := newTrackerWithLedger(t)
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "alpha", LoadID: "ld-a", StanceRole: "cto", TaskScope: "task-7"})
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "beta", LoadID: "ld-b", StanceRole: "cto", TaskScope: "task-7"})
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "gamma", LoadID: "ld-g", StanceRole: "cto", TaskScope: "task-other"})

	dropped, err := tr.CloseScope(context.Background(), "st-1", "task-7")
	if err != nil {
		t.Fatalf("CloseScope: %v", err)
	}
	if dropped != 2 {
		t.Errorf("CloseScope dropped %d, want 2", dropped)
	}
	if tr.Loaded("st-1", "alpha") || tr.Loaded("st-1", "beta") {
		t.Error("CloseScope should remove all task-7 skills")
	}
	if !tr.Loaded("st-1", "gamma") {
		t.Error("CloseScope should NOT touch task-other skill")
	}
	emitted := readSkillUnloaded(t, root)
	if len(emitted) != 2 {
		t.Fatalf("got %d unload events, want 2", len(emitted))
	}
	for _, e := range emitted {
		if e.Reason != "scope_exit" {
			t.Errorf("Reason = %q, want scope_exit", e.Reason)
		}
	}
}

func TestTracker_CloseScopeIsIdempotent(t *testing.T) {
	tr, _, _ := newTrackerWithLedger(t)
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "alpha", LoadID: "ld-1", StanceRole: "cto", TaskScope: "task-7"})

	first, err := tr.CloseScope(context.Background(), "st-1", "task-7")
	if err != nil || first != 1 {
		t.Errorf("first CloseScope: %v, dropped %d", err, first)
	}
	second, err := tr.CloseScope(context.Background(), "st-1", "task-7")
	if err != nil {
		t.Errorf("second CloseScope errored: %v", err)
	}
	if second != 0 {
		t.Errorf("second CloseScope dropped %d, want 0 (idempotent)", second)
	}
}

func TestTracker_EvictByCompactorEmitsCorrectReason(t *testing.T) {
	tr, _, root := newTrackerWithLedger(t)
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "alpha", LoadID: "ld-a", StanceRole: "cto", TaskScope: "task-1"})
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "beta", LoadID: "ld-b", StanceRole: "cto", TaskScope: "task-1"})

	dropped, err := tr.EvictByCompactor(context.Background(), "st-1", []EvictionRequest{
		{SkillRef: "alpha", TokensFreed: 1500},
		{SkillRef: "beta", TokensFreed: 800},
	})
	if err != nil {
		t.Fatalf("EvictByCompactor: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped %d, want 2", dropped)
	}
	emitted := readSkillUnloaded(t, root)
	if len(emitted) != 2 {
		t.Fatalf("got %d unload events, want 2", len(emitted))
	}
	tokens := map[string]int{}
	for _, e := range emitted {
		if e.Reason != "compactor_evicted" {
			t.Errorf("Reason = %q, want compactor_evicted", e.Reason)
		}
		tokens[e.SkillRef] = e.BudgetTokensFreed
	}
	if tokens["alpha"] != 1500 || tokens["beta"] != 800 {
		t.Errorf("token counts not threaded through: %v", tokens)
	}
}

func TestTracker_EvictByCompactorSkipsUnknownSkills(t *testing.T) {
	tr, _, _ := newTrackerWithLedger(t)
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "alpha", LoadID: "ld-a", StanceRole: "cto", TaskScope: "task-1"})

	dropped, err := tr.EvictByCompactor(context.Background(), "st-1", []EvictionRequest{
		{SkillRef: "alpha"},
		{SkillRef: "ghost-skill"},
	})
	if err != nil {
		t.Fatalf("EvictByCompactor: %v", err)
	}
	if dropped != 1 {
		t.Errorf("dropped %d, want 1 (ghost-skill skipped)", dropped)
	}
}

func TestTracker_NilLedgerIsBestEffortNoop(t *testing.T) {
	tr := New(nil)
	_ = tr.Note(LoadInfo{StanceID: "st", SkillRef: "x", LoadID: "ld", StanceRole: "cto", TaskScope: "t"})
	id, err := tr.Drop(context.Background(), "st", "x", "scope_exit")
	if err != nil {
		t.Errorf("nil-ledger Drop should be no-op: %v", err)
	}
	if id != "" {
		t.Errorf("nil-ledger Drop should return empty id, got %q", id)
	}
	// Tracking still happens — the skill is removed from local state.
	if tr.Loaded("st", "x") {
		t.Error("nil-ledger Drop should still remove from in-memory tracker")
	}
}

func TestTracker_Snapshot(t *testing.T) {
	tr, _, _ := newTrackerWithLedger(t)
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "alpha", LoadID: "ld-a", StanceRole: "cto", TaskScope: "t"})
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "beta", LoadID: "ld-b", StanceRole: "cto", TaskScope: "t"})
	_ = tr.Note(LoadInfo{StanceID: "st-2", SkillRef: "gamma", LoadID: "ld-g", StanceRole: "dev", TaskScope: "t"})
	snap := tr.Snapshot()
	if len(snap) != 2 {
		t.Errorf("Snapshot len = %d, want 2 stances", len(snap))
	}
	if len(snap["st-1"]) != 2 {
		t.Errorf("st-1 has %d skills, want 2", len(snap["st-1"]))
	}
	// Confirm Snapshot is a copy: mutating it doesn't affect tracker state.
	delete(snap, "st-1")
	if !tr.Loaded("st-1", "alpha") {
		t.Error("Snapshot should be a copy — mutating it must not affect Tracker")
	}
}

func TestTracker_RoundtripValidatesUnloadFields(t *testing.T) {
	// LoadID empty → SkillUnloaded.Validate should reject. Tracker
	// catches this by surfacing the EmitSkillUnloaded error.
	tr, _, _ := newTrackerWithLedger(t)
	_ = tr.Note(LoadInfo{StanceID: "st-1", SkillRef: "alpha", LoadID: "", StanceRole: "cto", TaskScope: "t"})
	_, err := tr.Drop(context.Background(), "st-1", "alpha", "scope_exit")
	if err == nil {
		t.Error("Drop with empty LoadID should surface validation error")
	}
	if !strings.Contains(err.Error(), "skill_unloaded") && !strings.Contains(err.Error(), "load_ref") {
		t.Errorf("error should mention skill_unloaded validation: %v", err)
	}
}
