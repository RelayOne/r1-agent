package hub

import "testing"

// TestLaneStatusValidValues table-tests every LaneStatus value plus the
// rejection paths (empty string, unknown literal). The set is closed: a
// new state requires a wire-version bump per specs/lanes-protocol.md §5.6,
// so this test is intentionally exhaustive.
func TestLaneStatusValidValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status LaneStatus
		valid  bool
		isTerm bool
	}{
		// Non-terminal accepted values.
		{name: "pending", status: LaneStatusPending, valid: true, isTerm: false},
		{name: "running", status: LaneStatusRunning, valid: true, isTerm: false},
		{name: "blocked", status: LaneStatusBlocked, valid: true, isTerm: false},

		// Terminal accepted values.
		{name: "done", status: LaneStatusDone, valid: true, isTerm: true},
		{name: "errored", status: LaneStatusErrored, valid: true, isTerm: true},
		{name: "cancelled", status: LaneStatusCancelled, valid: true, isTerm: true},

		// Rejection paths.
		{name: "empty", status: LaneStatus(""), valid: false, isTerm: false},
		{name: "unknown", status: LaneStatus("running_"), valid: false, isTerm: false},
		{name: "uppercase_running", status: LaneStatus("RUNNING"), valid: false, isTerm: false},
		{name: "trailing_space", status: LaneStatus("done "), valid: false, isTerm: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.IsValid(); got != tc.valid {
				t.Fatalf("LaneStatus(%q).IsValid() = %v, want %v", string(tc.status), got, tc.valid)
			}
			if got := tc.status.IsTerminal(); got != tc.isTerm {
				t.Fatalf("LaneStatus(%q).IsTerminal() = %v, want %v", string(tc.status), got, tc.isTerm)
			}
		})
	}
}

// TestLaneStatusStringRepresentation locks the wire-format spelling so a
// future refactor cannot silently rename a constant. The literals must
// match specs/lanes-protocol.md §3.1 verbatim.
func TestLaneStatusStringRepresentation(t *testing.T) {
	t.Parallel()
	cases := map[LaneStatus]string{
		LaneStatusPending:   "pending",
		LaneStatusRunning:   "running",
		LaneStatusBlocked:   "blocked",
		LaneStatusDone:      "done",
		LaneStatusErrored:   "errored",
		LaneStatusCancelled: "cancelled",
	}
	for s, want := range cases {
		if string(s) != want {
			t.Errorf("LaneStatus value %q does not match spec literal %q", string(s), want)
		}
	}
}
