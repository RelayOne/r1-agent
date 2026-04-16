package vecindex

import "testing"

func seed() *ToolIndex {
	idx := NewToolIndex()
	idx.Add(ToolDescriptor{
		Name:        "code_search",
		Description: "Search the codebase for symbols, functions, and types",
		Tags:        []string{"code", "search", "symbols"},
	})
	idx.Add(ToolDescriptor{
		Name:        "calendar_read",
		Description: "Read calendar events for a given date range",
		Tags:        []string{"calendar", "read"},
	})
	idx.Add(ToolDescriptor{
		Name:        "email_send",
		Description: "Send an email message via the configured provider",
		Tags:        []string{"email", "send"},
	})
	idx.Add(ToolDescriptor{
		Name:        "file_write",
		Description: "Write a file to disk at the given path",
		Tags:        []string{"filesystem", "write"},
	})
	return idx
}

func TestRetrieve_TagMatchWins(t *testing.T) {
	idx := seed()
	hits := idx.Retrieve("find functions in the codebase", 3)
	if len(hits) == 0 {
		t.Fatal("expected results for codebase query")
	}
	if hits[0].Descriptor.Name != "code_search" {
		t.Errorf("top=%q want code_search", hits[0].Descriptor.Name)
	}
}

func TestRetrieve_EmailQuery(t *testing.T) {
	idx := seed()
	hits := idx.Retrieve("send an email to my team", 3)
	if len(hits) == 0 {
		t.Fatal("expected results")
	}
	if hits[0].Descriptor.Name != "email_send" {
		t.Errorf("top=%q want email_send", hits[0].Descriptor.Name)
	}
}

func TestRetrieve_K_Limit(t *testing.T) {
	idx := seed()
	hits := idx.Retrieve("send write file email", 2)
	if len(hits) > 2 {
		t.Errorf("k=2 should cap results, got %d", len(hits))
	}
}

func TestRetrieve_EmptyQueryReturnsNothing(t *testing.T) {
	idx := seed()
	if hits := idx.Retrieve("", 10); len(hits) != 0 {
		t.Errorf("empty query should return nothing (not flood with all tools), got %d", len(hits))
	}
}

func TestRetrieve_Deterministic(t *testing.T) {
	idx := seed()
	a := idx.Retrieve("code symbols", 3)
	b := idx.Retrieve("code symbols", 3)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic size: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Descriptor.Name != b[i].Descriptor.Name {
			t.Errorf("order differs at %d: %q vs %q", i, a[i].Descriptor.Name, b[i].Descriptor.Name)
		}
	}
}

func TestAdd_ReplacesByName(t *testing.T) {
	idx := NewToolIndex()
	idx.Add(ToolDescriptor{Name: "t1", Description: "apples", Tags: []string{"fruit"}})
	idx.Add(ToolDescriptor{Name: "t1", Description: "bananas", Tags: []string{"fruit"}})
	if idx.Len() != 1 {
		t.Errorf("len=%d want 1 (replace not append)", idx.Len())
	}
	hits := idx.Retrieve("bananas", 1)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit")
	}
	// Old description should not appear.
	hits2 := idx.Retrieve("apples", 1)
	if len(hits2) != 0 {
		t.Errorf("old description leaked: %v", hits2)
	}
}

func TestToolIndex_Remove(t *testing.T) {
	idx := seed()
	n := idx.Len()
	idx.Remove("email_send")
	if idx.Len() != n-1 {
		t.Errorf("Len after Remove=%d want %d", idx.Len(), n-1)
	}
	if hits := idx.Retrieve("send email", 5); len(hits) > 0 && hits[0].Descriptor.Name == "email_send" {
		t.Error("removed tool should not surface")
	}
	// Remove unknown is a noop.
	idx.Remove("made-up")
}

func TestRetrieve_EmptyIndex(t *testing.T) {
	idx := NewToolIndex()
	if hits := idx.Retrieve("anything", 10); len(hits) != 0 {
		t.Error("empty index should return 0 hits")
	}
}

func TestRetriever_InterfaceSatisfied(t *testing.T) {
	var _ Retriever = NewToolIndex()
}
