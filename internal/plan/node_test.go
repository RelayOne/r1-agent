package plan

import (
	"errors"
	"sort"
	"testing"
)

func TestAllStates_Nine(t *testing.T) {
	if got := AllStates(); len(got) != 9 {
		t.Fatalf("AllStates()=%d entries, want 9", len(got))
	}
}

func TestTransitionTable_EveryStateIsFrom(t *testing.T) {
	for _, s := range allStates {
		if _, ok := transitions[s]; !ok {
			t.Errorf("transitions missing from-state %q", s)
		}
	}
}

func TestTransitionTable_TerminalsHaveNoOutEdges(t *testing.T) {
	for _, s := range []State{StateVerified, StateCanceled} {
		if len(transitions[s]) != 0 {
			t.Errorf("terminal state %q should have 0 out-edges, got %d", s, len(transitions[s]))
		}
		if !IsTerminal(s) {
			t.Errorf("IsTerminal(%q)=false", s)
		}
	}
	for _, s := range []State{StateDraft, StateReady, StateActive, StateCompleted, StateBlocked, StateWaitingHuman, StateNeedsRevision} {
		if IsTerminal(s) {
			t.Errorf("IsTerminal(%q)=true, want false", s)
		}
	}
}

func TestSetState_AllowedTransitions(t *testing.T) {
	cases := []struct {
		name string
		from State
		to   State
	}{
		{"draft→ready", StateDraft, StateReady},
		{"ready→active", StateReady, StateActive},
		{"active→completed", StateActive, StateCompleted},
		{"completed→verified", StateCompleted, StateVerified},
		{"active→needs_revision", StateActive, StateNeedsRevision},
		{"needs_revision→ready", StateNeedsRevision, StateReady},
		{"completed→needs_revision", StateCompleted, StateNeedsRevision},
		{"ready→blocked", StateReady, StateBlocked},
		{"blocked→ready", StateBlocked, StateReady},
		{"waiting_human→active", StateWaitingHuman, StateActive},
		{"needs_revision→draft", StateNeedsRevision, StateDraft},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &Node{Status: c.from}
			if err := n.SetState(c.to); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n.Status != c.to {
				t.Errorf("Status=%q want %q", n.Status, c.to)
			}
		})
	}
}

func TestSetState_ForbiddenTransitions(t *testing.T) {
	cases := []struct {
		name string
		from State
		to   State
	}{
		{"verified→anything", StateVerified, StateActive},
		{"verified→ready", StateVerified, StateReady},
		{"canceled→anything", StateCanceled, StateReady},
		{"completed→active", StateCompleted, StateActive},
		{"draft→active", StateDraft, StateActive},
		{"draft→completed", StateDraft, StateCompleted},
		{"ready→completed", StateReady, StateCompleted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &Node{Status: c.from}
			err := n.SetState(c.to)
			if err == nil {
				t.Fatalf("expected ErrInvalidTransition, got nil")
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("want ErrInvalidTransition, got %v", err)
			}
		})
	}
}

func TestSetState_UnknownFromStateErrors(t *testing.T) {
	n := &Node{Status: State("nonsense")}
	if err := n.SetState(StateReady); err == nil {
		t.Fatal("expected error for unknown from-state")
	}
}

func TestBumpRevision_EnforcesCap(t *testing.T) {
	n := &Node{Status: StateActive}
	for i := 1; i <= MaxNeedsRevisionCycles; i++ {
		if err := n.BumpRevision("round " + string(rune('0'+i))); err != nil {
			t.Fatalf("round %d: unexpected %v", i, err)
		}
	}
	if err := n.BumpRevision("too many"); !errors.Is(err, ErrRevisionCapReached) {
		t.Fatalf("want ErrRevisionCapReached, got %v", err)
	}
	if n.RevisionReason == "" {
		t.Error("RevisionReason should be set after BumpRevision")
	}
}

func TestClassifyRevision(t *testing.T) {
	cases := []struct {
		n            int
		crossSection bool
		want         RevisionClass
	}{
		{1, false, RevisionRepair},
		{2, false, RevisionRepair},
		{3, false, RevisionRepair},
		{4, false, RevisionReplan},
		{1, true, RevisionReplan},
		{0, false, RevisionRepair},
	}
	for _, c := range cases {
		got := ClassifyRevision(c.n, c.crossSection)
		if got != c.want {
			t.Errorf("ClassifyRevision(%d, %v)=%q want %q", c.n, c.crossSection, got, c.want)
		}
	}
}

func TestRollupStatus_Priorities(t *testing.T) {
	cases := []struct {
		name     string
		children []State
		want     State
	}{
		{"empty", nil, StateDraft},
		{"all verified", []State{StateVerified, StateVerified, StateVerified}, StateVerified},
		{"mixed completed+verified", []State{StateCompleted, StateVerified}, StateCompleted},
		{"any active", []State{StateReady, StateActive, StateVerified}, StateActive},
		{"any waiting_human (no active)", []State{StateReady, StateWaitingHuman, StateCompleted}, StateWaitingHuman},
		{"any blocked (no active, no waiting)", []State{StateReady, StateBlocked, StateCompleted}, StateBlocked},
		{"verified+canceled", []State{StateVerified, StateCanceled, StateVerified}, StateVerified},
		{"all canceled", []State{StateCanceled, StateCanceled}, StateCanceled},
		{"any needs_revision makes parent active", []State{StateReady, StateNeedsRevision}, StateActive},
		{"all draft", []State{StateDraft, StateDraft}, StateDraft},
		{"any ready (no active/blocked/waiting)", []State{StateReady, StateDraft}, StateReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RollupStatus(c.children); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestAllowedNextStates_MatchesTable(t *testing.T) {
	for from, expected := range transitions {
		got := AllowedNextStates(from)
		if len(got) != len(expected) {
			t.Errorf("AllowedNextStates(%q) len=%d want %d", from, len(got), len(expected))
			continue
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		for _, s := range got {
			if !expected[s] {
				t.Errorf("AllowedNextStates(%q) returned unexpected %q", from, s)
			}
		}
	}
}

func TestLinkType_AllFourDeclared(t *testing.T) {
	want := []LinkType{LinkFinishToStart, LinkArtifactDependency, LinkInformationDependency, LinkApprovalGate}
	for _, lt := range want {
		if string(lt) == "" {
			t.Errorf("link type has empty string value")
		}
	}
}

func TestNodeType_HierarchyDeclared(t *testing.T) {
	for _, nt := range []NodeType{NodeTypeRoot, NodeTypeSection, NodeTypeItem, NodeTypeSubtask} {
		if string(nt) == "" {
			t.Errorf("node type has empty string value")
		}
	}
}
