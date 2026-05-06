package cortex

import (
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// TestLaneStruct asserts Lane's value-type semantics: zero-value, field
// assignment, copy semantics, and the Clone helper. The struct shape is
// fixed by specs/lanes-protocol.md §5 — adding a field is fine, renaming
// or removing one breaks wire format.
func TestLaneStruct(t *testing.T) {
	t.Parallel()

	t.Run("zero_value", func(t *testing.T) {
		var l Lane
		if l.ID != "" || l.Kind != "" || l.ParentID != "" || l.Label != "" {
			t.Errorf("zero Lane has unexpected non-empty string fields: %+v", l)
		}
		if l.Status != "" {
			t.Errorf("zero Lane has non-empty Status: %q", l.Status)
		}
		if l.Pinned {
			t.Errorf("zero Lane should have Pinned=false")
		}
		if !l.StartedAt.IsZero() || !l.EndedAt.IsZero() {
			t.Errorf("zero Lane should have zero StartedAt/EndedAt; got %v / %v", l.StartedAt, l.EndedAt)
		}
		if l.LastSeq != 0 {
			t.Errorf("zero Lane should have LastSeq=0; got %d", l.LastSeq)
		}
		if l.IsTerminal() {
			t.Errorf("zero Lane should not be terminal")
		}
	})

	t.Run("fields_set", func(t *testing.T) {
		started := time.Date(2026, 5, 2, 18, 33, 21, 0, time.UTC)
		ended := started.Add(5 * time.Second)
		l := Lane{
			ID:        "lane_01J0K3M4",
			Kind:      hub.LaneKindLobe,
			ParentID:  "lane_01J0K3M3",
			Label:     "Recalling memories",
			Status:    hub.LaneStatusDone,
			Pinned:    true,
			StartedAt: started,
			EndedAt:   ended,
			LastSeq:   142,
		}
		if l.Kind != hub.LaneKindLobe {
			t.Errorf("Kind not retained: %q", l.Kind)
		}
		if !l.IsTerminal() {
			t.Errorf("done lane should report IsTerminal=true")
		}
	})

	t.Run("clone_is_independent", func(t *testing.T) {
		l := Lane{
			ID:      "lane_a",
			Kind:    hub.LaneKindMain,
			Status:  hub.LaneStatusRunning,
			LastSeq: 1,
		}
		copy := l.Clone()
		copy.Status = hub.LaneStatusDone
		copy.LastSeq = 999
		if l.Status != hub.LaneStatusRunning {
			t.Errorf("clone mutation leaked into original: Status=%q", l.Status)
		}
		if l.LastSeq != 1 {
			t.Errorf("clone mutation leaked into original: LastSeq=%d", l.LastSeq)
		}
	})
}
