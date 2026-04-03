package branch

import (
	"testing"
)

func TestForkAndAppend(t *testing.T) {
	trunk := []Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Fix the bug."},
	}

	e := NewExplorer(trunk)
	b := e.Fork("approach-A")

	if b.ID == "" {
		t.Error("branch should have ID")
	}
	if len(b.Messages) != 2 {
		t.Errorf("expected 2 trunk messages, got %d", len(b.Messages))
	}

	e.Append(b.ID, Message{Role: "assistant", Content: "I'll try approach A"})
	if len(e.Get(b.ID).Messages) != 3 {
		t.Error("should have 3 messages after append")
	}
}

func TestForkIsolation(t *testing.T) {
	trunk := []Message{{Role: "user", Content: "hello"}}
	e := NewExplorer(trunk)

	a := e.Fork("A")
	b := e.Fork("B")

	e.Append(a.ID, Message{Role: "assistant", Content: "approach A"})

	// B should not see A's message
	bBranch := e.Get(b.ID)
	if len(bBranch.Messages) != 1 {
		t.Errorf("branch B should have 1 msg, got %d", len(bBranch.Messages))
	}
}

func TestCompleteAndBest(t *testing.T) {
	e := NewExplorer(nil)
	a := e.Fork("A")
	b := e.Fork("B")

	e.Complete(a.ID, 0.7)
	e.Complete(b.ID, 0.9)

	best := e.Best()
	if best == nil {
		t.Fatal("expected best branch")
	}
	if best.ID != b.ID {
		t.Errorf("expected B as best, got %s", best.ID)
	}
}

func TestSelect(t *testing.T) {
	e := NewExplorer(nil)
	a := e.Fork("A")
	e.Complete(a.ID, 1.0)

	selected := e.Select()
	if selected == nil || selected.Status != "selected" {
		t.Error("should select best branch")
	}
}

func TestFail(t *testing.T) {
	e := NewExplorer(nil)
	a := e.Fork("A")

	e.Fail(a.ID, "compilation error")
	if e.Get(a.ID).Status != "failed" {
		t.Error("should be failed")
	}
	if e.Get(a.ID).Metadata["failure_reason"] != "compilation error" {
		t.Error("should have failure reason")
	}
}

func TestActive(t *testing.T) {
	e := NewExplorer(nil)
	e.Fork("A")
	b := e.Fork("B")
	e.Complete(b.ID, 1.0)

	active := e.Active()
	if len(active) != 1 {
		t.Errorf("expected 1 active, got %d", len(active))
	}
}

func TestForkFrom(t *testing.T) {
	e := NewExplorer(nil)
	parent := e.Fork("parent")
	e.Append(parent.ID, Message{Role: "assistant", Content: "parent work"})

	child, err := e.ForkFrom(parent.ID, "child")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != parent.ID {
		t.Error("child should reference parent")
	}
	if len(child.Messages) != 1 {
		t.Errorf("child should inherit parent messages, got %d", len(child.Messages))
	}
}

func TestForkFromNotFound(t *testing.T) {
	e := NewExplorer(nil)
	_, err := e.ForkFrom("nonexistent", "child")
	if err == nil {
		t.Error("should error on missing parent")
	}
}

func TestPrune(t *testing.T) {
	e := NewExplorer(nil)
	a := e.Fork("A")
	e.Fork("B")
	e.Fail(a.ID, "bad")

	pruned := e.Prune()
	if pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", pruned)
	}
	if len(e.All()) != 1 {
		t.Errorf("expected 1 remaining, got %d", len(e.All()))
	}
}

func TestCount(t *testing.T) {
	e := NewExplorer(nil)
	e.Fork("A")
	b := e.Fork("B")
	c := e.Fork("C")
	e.Complete(b.ID, 1.0)
	e.Fail(c.ID, "err")

	counts := e.Count()
	if counts["active"] != 1 || counts["completed"] != 1 || counts["failed"] != 1 {
		t.Errorf("unexpected counts: %v", counts)
	}
}

func TestAppendToNonActive(t *testing.T) {
	e := NewExplorer(nil)
	a := e.Fork("A")
	e.Complete(a.ID, 1.0)

	err := e.Append(a.ID, Message{Role: "user", Content: "more"})
	if err == nil {
		t.Error("should not append to completed branch")
	}
}

func TestBestNone(t *testing.T) {
	e := NewExplorer(nil)
	if e.Best() != nil {
		t.Error("should return nil with no completed branches")
	}
}

func TestSummary(t *testing.T) {
	e := NewExplorer(nil)
	e.Fork("A")
	s := e.Summary()
	if s == "" {
		t.Error("summary should not be empty")
	}
}
