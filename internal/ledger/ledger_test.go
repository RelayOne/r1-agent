package ledger

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestLedger creates a Ledger in a temporary directory.
func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "ledger")
	l, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func makeNode(typ, body, creator string) Node {
	return Node{
		Type:          typ,
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     creator,
		Content:       json.RawMessage(`{"text":"` + body + `"}`),
	}
}

// --- AddNode ---

func TestAddNodeReturnsContentAddressedID(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	n := makeNode("decision", "hello", "stance-1")
	id, err := l.AddNode(ctx, n)
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
	if !strings.HasPrefix(id, "decision-") {
		t.Fatalf("expected decision- prefix, got %q", id)
	}
}

func TestAddNodeSameContentSameID(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	n1 := Node{
		Type:          "skill",
		SchemaVersion: 1,
		CreatedAt:     ts,
		CreatedBy:     "stance-a",
		Content:       json.RawMessage(`{"pattern":"singleton"}`),
	}
	n2 := n1 // exact copy

	id1, err := l.AddNode(ctx, n1)
	if err != nil {
		t.Fatalf("AddNode 1: %v", err)
	}
	id2, err := l.AddNode(ctx, n2)
	if err != nil {
		t.Fatalf("AddNode 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("same content should produce same ID: %q != %q", id1, id2)
	}
}

func TestAddNodeDifferentContentDifferentID(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	n1 := Node{
		Type:          "decision",
		SchemaVersion: 1,
		CreatedAt:     ts,
		CreatedBy:     "stance-a",
		Content:       json.RawMessage(`{"text":"alpha"}`),
	}
	n2 := Node{
		Type:          "decision",
		SchemaVersion: 1,
		CreatedAt:     ts,
		CreatedBy:     "stance-a",
		Content:       json.RawMessage(`{"text":"beta"}`),
	}

	id1, _ := l.AddNode(ctx, n1)
	id2, _ := l.AddNode(ctx, n2)
	if id1 == id2 {
		t.Fatal("different content should produce different IDs")
	}
}

func TestAddNodeRejectsMissingType(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	n := Node{
		SchemaVersion: 1,
		Content:       json.RawMessage(`{}`),
	}
	_, err := l.AddNode(ctx, n)
	if err == nil {
		t.Fatal("expected error for missing type")
	}
}

func TestAddNodeRejectsMissingContent(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	n := Node{
		Type:          "decision",
		SchemaVersion: 1,
	}
	_, err := l.AddNode(ctx, n)
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestAddNodeRejectsZeroSchemaVersion(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	n := Node{
		Type:    "decision",
		Content: json.RawMessage(`{}`),
		// SchemaVersion defaults to 0
	}
	_, err := l.AddNode(ctx, n)
	if err == nil {
		t.Fatal("expected error for schema_version < 1")
	}
}

// --- No Update/Delete/Modify (compile-time check via AST) ---

func TestNoUpdateDeleteModifyMethods(t *testing.T) {
	// Parse the ledger package AST and verify no exported methods named
	// Update, Delete, or Modify exist on *Ledger.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	forbidden := map[string]bool{
		"Update": true, "Delete": true, "Modify": true,
		"Remove": true, "Edit": true, "Set": true,
		"UpdateNode": true, "DeleteNode": true, "ModifyNode": true,
		"RemoveNode": true, "RemoveEdge": true, "DeleteEdge": true,
	}

	for _, pkg := range pkgs {
		for fname, f := range pkg.Files {
			// Skip test files.
			if strings.HasSuffix(fname, "_test.go") {
				continue
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil {
					continue
				}
				// Check if receiver is *Ledger or Ledger.
				for _, field := range fn.Recv.List {
					typStr := typeString(field.Type)
					if typStr == "Ledger" || typStr == "*Ledger" {
						if forbidden[fn.Name.Name] {
							t.Errorf("found forbidden method %s on %s — ledger must be append-only", fn.Name.Name, typStr)
						}
					}
				}
			}
		}
	}
}

func typeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	default:
		return ""
	}
}

// --- Get ---

func TestGetRetrievesNode(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	n := makeNode("task", "implement feature", "stance-1")
	id, err := l.AddNode(ctx, n)
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	got, err := l.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != id {
		t.Fatalf("expected ID %q, got %q", id, got.ID)
	}
	if got.Type != "task" {
		t.Fatalf("expected type task, got %q", got.Type)
	}
}

func TestGetNotFound(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	_, err := l.Get(ctx, "nonexistent-abc123")
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

// --- AddEdge ---

func TestAddEdgeValid(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "first", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("decision", "second", "s1"))

	err := l.AddEdge(ctx, Edge{
		From: id2,
		To:   id1,
		Type: EdgeSupersedes,
	})
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
}

func TestAddEdgeRejectsMissingEndpoint(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "first", "s1"))

	err := l.AddEdge(ctx, Edge{
		From: id1,
		To:   "nonexistent-abc",
		Type: EdgeDependsOn,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent endpoint")
	}
}

func TestAddEdgeRejectsEmptyFields(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	err := l.AddEdge(ctx, Edge{Type: EdgeExtends})
	if err == nil {
		t.Fatal("expected error for empty from/to")
	}
}

func TestAddEdgeRejectsInvalidType(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "a", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("decision", "b", "s1"))

	err := l.AddEdge(ctx, Edge{
		From: id1,
		To:   id2,
		Type: EdgeType("invented"),
	})
	if err == nil {
		t.Fatal("expected error for unknown edge type")
	}
}

// --- Query ---

func TestQueryByType(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	l.AddNode(ctx, makeNode("decision", "d1", "s1"))
	l.AddNode(ctx, makeNode("task", "t1", "s1"))
	l.AddNode(ctx, makeNode("decision", "d2", "s1"))

	results, err := l.Query(ctx, QueryFilter{Type: "decision"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(results))
	}
	for _, n := range results {
		if n.Type != "decision" {
			t.Fatalf("expected type decision, got %q", n.Type)
		}
	}
}

func TestQueryByCreator(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	l.AddNode(ctx, makeNode("decision", "d1", "alice"))
	l.AddNode(ctx, makeNode("decision", "d2", "bob"))
	l.AddNode(ctx, makeNode("task", "t1", "alice"))

	results, err := l.Query(ctx, QueryFilter{CreatedBy: "alice"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 nodes by alice, got %d", len(results))
	}
}

func TestQueryByMissionID(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	n := makeNode("task", "feature", "s1")
	n.MissionID = "m-42"
	l.AddNode(ctx, n)

	n2 := makeNode("task", "other", "s1")
	n2.MissionID = "m-99"
	l.AddNode(ctx, n2)

	results, err := l.Query(ctx, QueryFilter{MissionID: "m-42"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 node, got %d", len(results))
	}
}

func TestQueryWithLimit(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		l.AddNode(ctx, makeNode("decision", "d", "s1"))
	}

	results, err := l.Query(ctx, QueryFilter{Type: "decision", Limit: 2})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(results))
	}
}

func TestQueryByTimeRange(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	early := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	n1 := makeNode("decision", "old", "s1")
	n1.CreatedAt = early
	l.AddNode(ctx, n1)

	n2 := makeNode("decision", "new", "s1")
	n2.CreatedAt = late
	l.AddNode(ctx, n2)

	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	results, err := l.Query(ctx, QueryFilter{Since: &since})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 node after 2025, got %d", len(results))
	}
}

// --- Resolve ---

func TestResolveFollowsSupersedesChain(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "v1", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("decision", "v2", "s1"))
	id3, _ := l.AddNode(ctx, makeNode("decision", "v3", "s1"))

	// id2 supersedes id1, id3 supersedes id2.
	l.AddEdge(ctx, Edge{From: id2, To: id1, Type: EdgeSupersedes})
	l.AddEdge(ctx, Edge{From: id3, To: id2, Type: EdgeSupersedes})

	resolved, err := l.Resolve(ctx, id1)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ID != id3 {
		t.Fatalf("expected resolved to id3 %q, got %q", id3, resolved.ID)
	}
}

func TestResolveNoSupersedes(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id, _ := l.AddNode(ctx, makeNode("decision", "only", "s1"))

	resolved, err := l.Resolve(ctx, id)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ID != id {
		t.Fatalf("expected same node back, got %q", resolved.ID)
	}
}

// --- Walk ---

func TestWalkForward(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("task", "root", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("task", "child", "s1"))
	id3, _ := l.AddNode(ctx, makeNode("task", "grandchild", "s1"))

	l.AddEdge(ctx, Edge{From: id1, To: id2, Type: EdgeDependsOn})
	l.AddEdge(ctx, Edge{From: id2, To: id3, Type: EdgeDependsOn})

	nodes, err := l.Walk(ctx, id1, Forward, []EdgeType{EdgeDependsOn})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes in walk, got %d", len(nodes))
	}
}

func TestWalkBackward(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("task", "root", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("task", "leaf", "s1"))

	l.AddEdge(ctx, Edge{From: id1, To: id2, Type: EdgeExtends})

	nodes, err := l.Walk(ctx, id2, Backward, []EdgeType{EdgeExtends})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes in backward walk, got %d", len(nodes))
	}
}

func TestWalkFiltersByEdgeType(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "root", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("decision", "dep", "s1"))
	id3, _ := l.AddNode(ctx, makeNode("decision", "ref", "s1"))

	l.AddEdge(ctx, Edge{From: id1, To: id2, Type: EdgeDependsOn})
	l.AddEdge(ctx, Edge{From: id1, To: id3, Type: EdgeReferences})

	// Walk only depends_on edges.
	nodes, err := l.Walk(ctx, id1, Forward, []EdgeType{EdgeDependsOn})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(nodes) != 2 { // id1 + id2
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
}

// --- Batch ---

func TestBatchAtomicNodeAndEdges(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	// Pre-existing node.
	existingID, _ := l.AddNode(ctx, makeNode("decision", "existing", "s1"))

	newNode := makeNode("decision", "replacement", "s1")
	err := l.Batch(ctx, []BatchOp{
		{OpType: BatchAddNode, Node: &newNode},
		{OpType: BatchAddEdge, Edge: &Edge{
			From: computeID(newNode), // predict ID
			To:   existingID,
			Type: EdgeSupersedes,
		}},
	})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}

	// Verify the edge was created.
	resolved, err := l.Resolve(ctx, existingID)
	if err != nil {
		t.Fatalf("Resolve after batch: %v", err)
	}
	if resolved.ID == existingID {
		t.Fatal("expected resolve to follow supersedes to new node")
	}
}

func TestBatchRejectsInvalidNode(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	badNode := Node{} // missing type, content, schema_version
	err := l.Batch(ctx, []BatchOp{
		{OpType: BatchAddNode, Node: &badNode},
	})
	if err == nil {
		t.Fatal("expected error for invalid node in batch")
	}
}

func TestBatchRejectsEdgeToNonexistent(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "a", "s1"))
	err := l.Batch(ctx, []BatchOp{
		{OpType: BatchAddEdge, Edge: &Edge{
			From: id1,
			To:   "nonexistent-xyz",
			Type: EdgeDependsOn,
		}},
	})
	if err == nil {
		t.Fatal("expected error for edge to nonexistent node")
	}
}

func TestBatchEmpty(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	err := l.Batch(ctx, nil)
	if err != nil {
		t.Fatalf("empty batch should succeed: %v", err)
	}
}

// --- RebuildIndex ---

func TestRebuildIndexProducesSameResults(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "d1", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("task", "t1", "s2"))
	l.AddEdge(ctx, Edge{From: id1, To: id2, Type: EdgeReferences})

	// Query before rebuild.
	beforeNodes, _ := l.Query(ctx, QueryFilter{Type: "decision"})
	beforeTasks, _ := l.Query(ctx, QueryFilter{Type: "task"})

	// Rebuild.
	if err := l.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}

	// Query after rebuild.
	afterNodes, _ := l.Query(ctx, QueryFilter{Type: "decision"})
	afterTasks, _ := l.Query(ctx, QueryFilter{Type: "task"})

	if len(beforeNodes) != len(afterNodes) {
		t.Fatalf("decision count mismatch: %d vs %d", len(beforeNodes), len(afterNodes))
	}
	if len(beforeTasks) != len(afterTasks) {
		t.Fatalf("task count mismatch: %d vs %d", len(beforeTasks), len(afterTasks))
	}

	// Verify edges survived rebuild.
	refs, err := l.index.EdgesFrom(id1, EdgeReferences)
	if err != nil {
		t.Fatalf("EdgesFrom after rebuild: %v", err)
	}
	if len(refs) != 1 || refs[0] != id2 {
		t.Fatalf("expected edge %s->%s after rebuild, got %v", id1, id2, refs)
	}
}

func TestRebuildIndexFromDeletedDB(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ledger")
	l, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	l.AddNode(ctx, makeNode("decision", "survive", "s1"))

	// Delete the index DB file.
	dbPath := filepath.Join(root, ".index.db")
	l.Close()
	os.Remove(dbPath)
	// Also remove WAL/SHM files if present.
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	// Re-open and rebuild.
	l2, err := New(root)
	if err != nil {
		t.Fatalf("New after delete: %v", err)
	}
	defer l2.Close()

	if err := l2.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}

	results, err := l2.Query(ctx, QueryFilter{Type: "decision"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 decision after rebuild, got %d", len(results))
	}
}

// --- Store persistence ---

func TestStoreNodePersistsToDisk(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	n := makeNode("skill", "pattern-x", "s1")
	id, _ := l.AddNode(ctx, n)

	// Verify file exists.
	path := filepath.Join(l.rootDir, "nodes", id+".json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("node file not found at %s", path)
	}
}

func TestStoreEdgePersistsToDisk(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	id1, _ := l.AddNode(ctx, makeNode("decision", "a", "s1"))
	id2, _ := l.AddNode(ctx, makeNode("decision", "b", "s1"))
	l.AddEdge(ctx, Edge{From: id1, To: id2, Type: EdgeExtends})

	filename := id1 + "-" + id2 + "-" + string(EdgeExtends) + ".json"
	path := filepath.Join(l.rootDir, "edges", filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("edge file not found at %s", path)
	}
}

// --- Edge type coverage ---

func TestAllEdgeTypesAccepted(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	types := []EdgeType{
		EdgeSupersedes, EdgeDependsOn, EdgeContradicts,
		EdgeExtends, EdgeReferences, EdgeResolves, EdgeDistills,
	}

	for _, et := range types {
		id1, _ := l.AddNode(ctx, makeNode("decision", "from-"+string(et), "s1"))
		id2, _ := l.AddNode(ctx, makeNode("decision", "to-"+string(et), "s1"))
		err := l.AddEdge(ctx, Edge{From: id1, To: id2, Type: et})
		if err != nil {
			t.Errorf("AddEdge with type %q: %v", et, err)
		}
	}
}

// --- Resolve with no chain ---

func TestResolveNonexistentNode(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	_, err := l.Resolve(ctx, "ghost-node")
	if err == nil {
		t.Fatal("expected error resolving nonexistent node")
	}
}

// --- Concurrent safety (basic) ---

func TestConcurrentAddNodes(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()

	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			n := makeNode("task", "concurrent-"+string(rune('A'+i)), "s1")
			_, err := l.AddNode(ctx, n)
			errs <- err
		}(i)
	}

	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent AddNode: %v", err)
		}
	}
}
