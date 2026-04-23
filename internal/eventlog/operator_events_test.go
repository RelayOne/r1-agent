package eventlog

import "testing"

func TestIsOperatorEvent_Known(t *testing.T) {
	for _, k := range OperatorEventKinds {
		if !IsOperatorEvent(k) {
			t.Errorf("IsOperatorEvent(%q) = false, want true", k)
		}
	}
}

func TestIsOperatorEvent_Unknown(t *testing.T) {
	if IsOperatorEvent("task.dispatch") {
		t.Error("IsOperatorEvent(task.dispatch) = true, want false")
	}
}
