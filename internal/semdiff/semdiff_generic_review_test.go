package semdiff

import "testing"

func TestExprString_GenericReceiverDistinct(t *testing.T) {
	old := "package p\ntype Stack[T any] struct{}\nfunc (s *Stack[T]) Push(v T) {}\ntype Queue[T any] struct{}\nfunc (q *Queue[T]) Push(v T) {}\n"
	// Change ONLY Stack.Push's body. Queue.Push is unchanged.
	neu := "package p\ntype Stack[T any] struct{}\nfunc (s *Stack[T]) Push(v T) { _ = v }\ntype Queue[T any] struct{}\nfunc (q *Queue[T]) Push(v T) {}\n"
	a := Analyze(old, neu, "x.go")
	if a == nil {
		t.Fatal("nil analysis")
	}
	// Before the fix, both generic Push methods collided on "method ?.Push"
	// and one was dropped, so the changed-symbol set was wrong. After the
	// fix they are distinct identities; exactly one symbol changed.
	changed := 0
	for _, c := range a.Changes {
		if c.Name == "Push" {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("expected exactly 1 changed Push (Stack), got %d (%v)", changed, a.Changes)
	}
}
