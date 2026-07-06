package sections

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/ledger"
)

func newTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func addNode(t *testing.T, l *ledger.Ledger, typ, mission string, content map[string]any) {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AddNode(context.Background(), ledger.Node{
		Type:          typ,
		SchemaVersion: 1,
		CreatedBy:     "test",
		MissionID:     mission,
		Content:       json.RawMessage(raw),
	}); err != nil {
		t.Fatal(err)
	}
}

// addNodeID is addNode but returns the created node's ID so a child node
// (e.g. a dissent's draft_ref) can reference it.
func addNodeID(t *testing.T, l *ledger.Ledger, typ, mission string, content map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	id, err := l.AddNode(context.Background(), ledger.Node{
		Type:          typ,
		SchemaVersion: 1,
		CreatedBy:     "test",
		MissionID:     mission,
		Content:       json.RawMessage(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(id)
}

// lineCount counts rendered bullet lines ("- ...\n") in a section body.
func lineCount(s string) int {
	n := 0
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(ln, "- ") {
			n++
		}
	}
	return n
}

// TestPriorDecisions_HonorsCap proves SectionSpec.Cap (maxItems) bounds
// the rendered list rather than being ignored in favor of a hardcoded
// constant. Seven decisions in scope, cap of 3 -> exactly 3 lines.
func TestPriorDecisions_HonorsCap(t *testing.T) {
	l := newTestLedger(t)
	for i := 0; i < 7; i++ {
		addNode(t, l, "decision", "m1", map[string]any{"rationale": "decision body"})
	}
	scope := Scope{MissionID: "m1"}

	out, err := PriorDecisions(context.Background(), scope, l, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineCount(out); got != 3 {
		t.Fatalf("cap not honored: rendered %d lines, want 3\n%s", got, out)
	}

	// Cap 0 == unlimited: all seven render.
	outAll, err := PriorDecisions(context.Background(), scope, l, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineCount(outAll); got != 7 {
		t.Fatalf("cap=0 should be unlimited: rendered %d lines, want 7", got)
	}
}

// TestRecentActivity_HonorsCap proves the reviewer template's cap of 15
// (vs the old hardcoded 10) is actually applied.
func TestRecentActivity_HonorsCap(t *testing.T) {
	l := newTestLedger(t)
	for i := 0; i < 20; i++ {
		addNode(t, l, "activity", "m1", map[string]any{"summary": "did a thing"})
	}
	out, err := RecentActivity(context.Background(), Scope{MissionID: "m1"}, l, 15)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineCount(out); got != 15 {
		t.Fatalf("recent activity cap not honored: %d lines, want 15", got)
	}
}

// TestDissentHistory_ScopeIsolation proves a dissent recorded under a
// different loop does not leak into another loop's projection.
func TestDissentHistory_ScopeIsolation(t *testing.T) {
	l := newTestLedger(t)
	// Real dissent nodes carry only draft_ref (NOT loop_ref); their loop is
	// determined by the draft they object to. Build the real schema: a draft
	// per loop, then a dissent referencing each. Scoping must resolve
	// dissent.draft_ref -> draft.loop_ref, not read a loop_ref off the
	// dissent itself.
	draftA := addNodeID(t, l, "draft", "m1", map[string]any{"draft_type": "sow", "loop_ref": "loop-A"})
	draftB := addNodeID(t, l, "draft", "m1", map[string]any{"draft_type": "sow", "loop_ref": "loop-B"})
	addNode(t, l, "dissent", "m1", map[string]any{"objection": "loop A concern", "draft_ref": draftA})
	addNode(t, l, "dissent", "m1", map[string]any{"objection": "loop B concern", "draft_ref": draftB})

	out, err := DissentHistory(context.Background(), Scope{MissionID: "m1", LoopID: "loop-A"}, l, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "loop A concern") {
		t.Errorf("loop-A dissent missing from its own projection:\n%s", out)
	}
	if strings.Contains(out, "loop B concern") {
		t.Errorf("loop-B dissent leaked into loop-A projection:\n%s", out)
	}
}

// TestPriorDecisions_ScopeIsolation proves a decision recorded under a
// different task does not leak into another task's projection, while an
// untagged (mission-global) decision still surfaces.
func TestPriorDecisions_ScopeIsolation(t *testing.T) {
	l := newTestLedger(t)
	addNode(t, l, "decision", "m1", map[string]any{"rationale": "task X decision", "task_dag_scope": "task-X"})
	addNode(t, l, "decision", "m1", map[string]any{"rationale": "task Y decision", "task_dag_scope": "task-Y"})
	addNode(t, l, "decision", "m1", map[string]any{"rationale": "global decision"}) // no task_dag_scope

	out, err := PriorDecisions(context.Background(), Scope{MissionID: "m1", TaskID: "task-X"}, l, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "task X decision") {
		t.Errorf("in-task decision missing:\n%s", out)
	}
	if !strings.Contains(out, "global decision") {
		t.Errorf("untagged mission-global decision was dropped:\n%s", out)
	}
	if strings.Contains(out, "task Y decision") {
		t.Errorf("other-task decision leaked into task-X projection:\n%s", out)
	}
}

// TestSDMAdvisories_BranchIsolation proves branch scoping.
func TestSDMAdvisories_BranchIsolation(t *testing.T) {
	l := newTestLedger(t)
	addNode(t, l, "advisory", "m1", map[string]any{"advisory": "branch main advisory", "branch_ref": "main"})
	addNode(t, l, "advisory", "m1", map[string]any{"advisory": "branch feat advisory", "branch_ref": "feat"})

	out, err := SDMAdvisories(context.Background(), Scope{MissionID: "m1", BranchID: "main"}, l, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "branch main advisory") {
		t.Errorf("own-branch advisory missing:\n%s", out)
	}
	if strings.Contains(out, "branch feat advisory") {
		t.Errorf("other-branch advisory leaked:\n%s", out)
	}
}
