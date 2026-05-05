package hub

import "testing"

// TestLaneKindValidValues table-tests every LaneKind value plus the
// rejection paths (empty string, unknown literal). The set is closed;
// a sixth kind requires a wire-version bump per specs/lanes-protocol.md
// §5.6.
func TestLaneKindValidValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		kind  LaneKind
		valid bool
	}{
		// Accepted values.
		{name: "main", kind: LaneKindMain, valid: true},
		{name: "lobe", kind: LaneKindLobe, valid: true},
		{name: "tool", kind: LaneKindTool, valid: true},
		{name: "mission_task", kind: LaneKindMissionTask, valid: true},
		{name: "router", kind: LaneKindRouter, valid: true},

		// Rejection paths.
		{name: "empty", kind: LaneKind(""), valid: false},
		{name: "unknown", kind: LaneKind("worker"), valid: false},
		{name: "uppercase", kind: LaneKind("MAIN"), valid: false},
		{name: "missiontask_no_underscore", kind: LaneKind("missiontask"), valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.kind.IsValid(); got != tc.valid {
				t.Fatalf("LaneKind(%q).IsValid() = %v, want %v", string(tc.kind), got, tc.valid)
			}
		})
	}
}

// TestLaneKindStringRepresentation locks the wire-format spelling so a
// future refactor cannot silently rename a constant. The literals must
// match specs/lanes-protocol.md §4.1 verbatim.
func TestLaneKindStringRepresentation(t *testing.T) {
	t.Parallel()
	cases := map[LaneKind]string{
		LaneKindMain:        "main",
		LaneKindLobe:        "lobe",
		LaneKindTool:        "tool",
		LaneKindMissionTask: "mission_task",
		LaneKindRouter:      "router",
	}
	for k, want := range cases {
		if string(k) != want {
			t.Errorf("LaneKind value %q does not match spec literal %q", string(k), want)
		}
	}
}
