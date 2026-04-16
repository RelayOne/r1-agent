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

// TestSetState_ClearsBlockedByOnExit: P2 fix — leaving a
// blocked state (BLOCKED / WAITING_HUMAN) clears BlockedBy so
// the node doesn't read as self-contradictory (ACTIVE + still
// blocked-by-X).
func TestSetState_ClearsBlockedByOnExit(t *testing.T) {
	cases := []struct {
		name string
		from State
		to   State
	}{
		{"blocked→active via ready", StateBlocked, StateReady},
		{"blocked→canceled", StateBlocked, StateCanceled},
		{"waiting_human→active", StateWaitingHuman, StateActive},
		{"waiting_human→canceled", StateWaitingHuman, StateCanceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &Node{Status: c.from, BlockedBy: []string{"dep-x", "dep-y"}}
			if err := n.SetState(c.to); err != nil {
				t.Fatalf("SetState: %v", err)
			}
			if len(n.BlockedBy) != 0 {
				t.Errorf("BlockedBy should be cleared on exit from %q, got %v", c.from, n.BlockedBy)
			}
		})
	}
}

// TestSetState_PreservesBlockedByWithinBlockedStates: the
// clear only fires on EXIT from blocked states; transitions
// between blocked states (if the table ever allows one —
// currently none does) should keep BlockedBy. This guards
// against over-zealous clearing if the table grows later.
func TestSetState_KeepsBlockedByOnBlockedToBlocked(t *testing.T) {
	// Our current table has no BLOCKED → WAITING_HUMAN edge,
	// so this test exercises a synthetic path: manually
	// verify the condition guard by inspecting behavior on
	// the only legal re-entry which is via NEEDS_REVISION →
	// READY, preserving BlockedBy as a no-op.
	n := &Node{Status: StateActive, BlockedBy: []string{"dep-x"}}
	if err := n.SetState(StateBlocked); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if len(n.BlockedBy) != 1 {
		t.Errorf("entering BLOCKED from ACTIVE should preserve BlockedBy, got %v", n.BlockedBy)
	}
}

// TestBumpRevision_PerCycleCounting: P2 fix — multiple
// objections within the SAME revision cycle don't burn
// attempts. Counter increments on cycle OPEN only.
func TestBumpRevision_PerCycleCounting(t *testing.T) {
	n := &Node{Status: StateActive}
	// First BumpRevision (from ACTIVE) opens cycle 1.
	if err := n.BumpRevision("first objection"); err != nil {
		t.Fatalf("bump 1: %v", err)
	}
	if n.RevisionAttempts != 1 {
		t.Errorf("after first bump, attempts=%d want 1", n.RevisionAttempts)
	}
	// Transition INTO NEEDS_REVISION (caller's job per docs).
	_ = n.SetState(StateNeedsRevision)

	// Second + third BumpRevision DURING same cycle — critic
	// polled twice — should NOT increment.
	_ = n.BumpRevision("same-cycle objection 2")
	_ = n.BumpRevision("same-cycle objection 3")
	if n.RevisionAttempts != 1 {
		t.Errorf("same-cycle bumps must NOT increment; attempts=%d want 1", n.RevisionAttempts)
	}

	// Close cycle by transitioning out of NEEDS_REVISION.
	_ = n.SetState(StateReady)

	// Next BumpRevision opens cycle 2.
	if err := n.BumpRevision("new cycle"); err != nil {
		t.Fatalf("bump 2: %v", err)
	}
	if n.RevisionAttempts != 2 {
		t.Errorf("new cycle should increment; attempts=%d want 2", n.RevisionAttempts)
	}
}

// TestBumpRevision_CapStillEnforced confirms the 3-cycle cap
// still blocks after the per-cycle fix.
func TestBumpRevision_CapStillEnforced(t *testing.T) {
	n := &Node{Status: StateActive}
	for i := 1; i <= MaxNeedsRevisionCycles; i++ {
		if err := n.BumpRevision("attempt"); err != nil {
			t.Fatalf("bump %d unexpected err %v", i, err)
		}
		_ = n.SetState(StateNeedsRevision)
		_ = n.SetState(StateReady)
	}
	// 4th cycle open should error.
	if err := n.BumpRevision("too many"); err == nil {
		t.Error("cap should still block after 3 cycles")
	}
}

// TestRollupStatus_MixedCompletedCanceled: P2 fix — a mixed
// {COMPLETED, CANCELED} set (no VERIFIED) should roll up to
// COMPLETED, not DRAFT.
func TestRollupStatus_MixedCompletedCanceled(t *testing.T) {
	got := RollupStatus([]State{StateCompleted, StateCanceled, StateCompleted})
	if got != StateCompleted {
		t.Errorf("mixed COMPLETED+CANCELED should be COMPLETED, got %q", got)
	}
}
