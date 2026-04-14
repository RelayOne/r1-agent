package plan

import (
	"sort"
	"testing"
)

func depsSorted(g *SessionDAG, id string) []string {
	out := append([]string{}, g.Deps[id]...)
	sort.Strings(out)
	return out
}

func TestSessionDAGExplicitIOEdges(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Outputs: []string{"@shared/types"}},
			{ID: "S2", Outputs: []string{"@shared/api-client"}},
			{ID: "S3", Inputs: []string{"@shared/types", "@shared/api-client"}},
		},
	}
	g := BuildSessionDAG(sow)
	if got := depsSorted(g, "S1"); len(got) != 0 {
		t.Fatalf("S1 should be a root; got deps %v", got)
	}
	// S2 has no explicit Inputs. With no file scope either, it falls
	// back to declaration-order → depends on S1.
	if got := depsSorted(g, "S2"); len(got) != 1 || got[0] != "S1" {
		t.Fatalf("S2 should fall back to decl-order dep on S1; got %v", got)
	}
	if got := depsSorted(g, "S3"); len(got) != 2 || got[0] != "S1" || got[1] != "S2" {
		t.Fatalf("S3 should have explicit deps on S1 and S2; got %v", got)
	}
}

func TestSessionDAGFileScopeDisjointParallel(t *testing.T) {
	// Two sessions touching entirely disjoint directories should be
	// independently reachable given a common root. The declaration-
	// order fallback still serializes them in this minimal case, but
	// the test confirms no spurious file-scope edge is added when
	// scopes don't overlap — so a richer DAG (with a root producing
	// shared infra) would correctly run them in parallel.
	sow := &SOW{
		Sessions: []Session{
			{ID: "root", Outputs: []string{"workspace"}},
			{ID: "web", Inputs: []string{"workspace"}, Tasks: []Task{{ID: "T1", Files: []string{"apps/web/page.tsx"}}}},
			{ID: "mobile", Inputs: []string{"workspace"}, Tasks: []Task{{ID: "T2", Files: []string{"apps/mobile/screen.tsx"}}}},
		},
	}
	g := BuildSessionDAG(sow)
	if got := depsSorted(g, "web"); len(got) != 1 || got[0] != "root" {
		t.Fatalf("web should depend only on root via explicit Inputs; got %v", got)
	}
	if got := depsSorted(g, "mobile"); len(got) != 1 || got[0] != "root" {
		t.Fatalf("mobile should depend only on root via explicit Inputs; got %v", got)
	}
}

func TestSessionDAGFileScopeOverlapAddsEdge(t *testing.T) {
	// No explicit I/O → the inference must look at file scope. S2's
	// file is inside S1's declared directory → S1 → S2.
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1", Tasks: []Task{{ID: "T1", Files: []string{"apps/web/"}}}},
			{ID: "S2", Tasks: []Task{{ID: "T2", Files: []string{"apps/web/app/page.tsx"}}}},
		},
	}
	g := BuildSessionDAG(sow)
	if got := depsSorted(g, "S2"); len(got) != 1 || got[0] != "S1" {
		t.Fatalf("S2 should depend on S1 via file-scope overlap; got %v", got)
	}
}

func TestSessionDAGDeclarationOrderFallback(t *testing.T) {
	// Two sessions with NO I/O and NO file-scope hints must fall back
	// to declaration-order serialization so legacy SOWs keep working.
	sow := &SOW{
		Sessions: []Session{
			{ID: "S1"},
			{ID: "S2"},
			{ID: "S3"},
		},
	}
	g := BuildSessionDAG(sow)
	if got := depsSorted(g, "S1"); len(got) != 0 {
		t.Fatalf("S1 root; got %v", got)
	}
	if got := depsSorted(g, "S2"); len(got) != 1 || got[0] != "S1" {
		t.Fatalf("S2 → S1; got %v", got)
	}
	if got := depsSorted(g, "S3"); len(got) != 1 || got[0] != "S2" {
		t.Fatalf("S3 → S2; got %v", got)
	}
}

func TestSessionDAGRoots(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{ID: "A", Outputs: []string{"x"}},
			{ID: "B", Outputs: []string{"y"}},
			{ID: "C", Inputs: []string{"x", "y"}},
		},
	}
	g := BuildSessionDAG(sow)
	roots := g.RootSessions(sow)
	sort.Strings(roots)
	// A is root (no I/O, position 0). B has no Inputs and no file
	// scope → declaration-order fallback → B depends on A, not a
	// root. C has explicit deps. Only A is a root.
	if len(roots) != 1 || roots[0] != "A" {
		t.Fatalf("expected [A] as roots, got %v", roots)
	}
}

func TestSessionDAGBlockers(t *testing.T) {
	g := &SessionDAG{
		Deps: map[string][]string{
			"C": {"A", "B"},
		},
	}
	blockers := g.Blockers("C", map[string]bool{"A": true})
	if len(blockers) != 1 || blockers[0] != "B" {
		t.Fatalf("expected [B] unresolved, got %v", blockers)
	}
	blockers = g.Blockers("C", map[string]bool{"A": true, "B": true})
	if len(blockers) != 0 {
		t.Fatalf("expected no blockers, got %v", blockers)
	}
}
