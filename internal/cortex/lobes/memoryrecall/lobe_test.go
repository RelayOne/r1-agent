package memoryrecall

import (
	"context"
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/wisdom"
)

// fakeMemoryStore is the in-memory test double. Real *memory.Store would
// also work but pulling in the on-disk JSON path complicates the test
// surface. The Lobe accepts the narrow memoryStore interface, so this
// struct satisfies it directly.
type fakeMemoryStore struct {
	entries []memory.Entry
}

func (f *fakeMemoryStore) Recall(query string, limit int) []memory.Entry {
	if limit <= 0 || len(f.entries) == 0 {
		return nil
	}
	out := make([]memory.Entry, 0, len(f.entries))
	for _, e := range f.entries {
		// naive "any word matches" — enough for unit tests.
		if query == "" || containsAny(e.Content, query) || containsAny(e.Context, query) {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

func (f *fakeMemoryStore) RecallByCategory(cat memory.Category) []memory.Entry {
	out := make([]memory.Entry, 0)
	for _, e := range f.entries {
		if e.Category == cat {
			out = append(out, e)
		}
	}
	return out
}

func containsAny(haystack, needle string) bool {
	if haystack == "" || needle == "" {
		return false
	}
	// Case-sensitive contains is enough for tests.
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

type fakeWisdomStore struct {
	items []wisdom.Learning
}

func (f *fakeWisdomStore) Learnings() []wisdom.Learning {
	out := make([]wisdom.Learning, len(f.items))
	copy(out, f.items)
	return out
}

// TestMemoryRecallLobe_BuildsIndexOnStart asserts that the first Run
// triggers a corpus rebuild and that DocCount reflects the union of
// memory + wisdom inputs. Spec item 6.
func TestMemoryRecallLobe_BuildsIndexOnStart(t *testing.T) {
	mem := &fakeMemoryStore{
		entries: []memory.Entry{
			{ID: "m1", Category: memory.CatGotcha, Content: "deadlock on close"},
			{ID: "m2", Category: memory.CatPattern, Content: "use defer cancel"},
			{ID: "m3", Category: memory.CatFix, Content: "added context cancellation"},
		},
	}
	wis := &fakeWisdomStore{
		items: []wisdom.Learning{
			{TaskID: "t1", Description: "build flake on race", Category: wisdom.Gotcha},
			{TaskID: "t2", Description: "prefer table-driven tests", Category: wisdom.Pattern},
		},
	}

	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := newMemoryRecallLobeForTest(ws, mem, wis, bus)

	if lobe.IndexBuilt() {
		t.Fatal("expected indexBuilt=false before Run")
	}

	if err := lobe.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !lobe.IndexBuilt() {
		t.Fatal("expected indexBuilt=true after Run")
	}

	want := 5 // 3 memory + 2 wisdom
	if got := lobe.DocCount(); got != want {
		t.Fatalf("DocCount = %d, want %d", got, want)
	}
}

// TestMemoryRecallLobe_LobeInterfaceShape pins the cortex.Lobe contract
// at compile-time. If the interface drifts, this fails to build.
func TestMemoryRecallLobe_LobeInterfaceShape(t *testing.T) {
	var _ cortex.Lobe = (*MemoryRecallLobe)(nil)

	lobe := newMemoryRecallLobeForTest(nil, &fakeMemoryStore{}, &fakeWisdomStore{}, nil)
	if got := lobe.ID(); got != "memory-recall" {
		t.Errorf("ID = %q, want memory-recall", got)
	}
	if lobe.Kind() != cortex.KindDeterministic {
		t.Errorf("Kind = %v, want KindDeterministic", lobe.Kind())
	}
	if lobe.Description() == "" {
		t.Error("Description must be non-empty")
	}
}
