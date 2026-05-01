package failure

import "time"

// RecoveryEvent is the WAL subset needed to reconstruct interrupted task state.
type RecoveryEvent struct {
	TS       time.Time
	Type     string
	TaskID   string
	WorkerID string
	Message  string
	Evidence map[string]string
}

// RecoveryCheckpoint is the restart-safe recovery hint for a task.
type RecoveryCheckpoint struct {
	TaskID        string
	LastEventType string
	LastWorkerID  string
	ResumeFrom    string
	Attempt       int
}

// DetectPartialState returns the latest non-terminal recovery point for each task.
func DetectPartialState(events []RecoveryEvent) map[string]RecoveryCheckpoint {
	checkpoints := map[string]RecoveryCheckpoint{}
	for _, ev := range events {
		if ev.TaskID == "" {
			continue
		}
		if isTerminalRecoveryEvent(ev.Type) {
			delete(checkpoints, ev.TaskID)
			continue
		}
		cp := RecoveryCheckpoint{
			TaskID:        ev.TaskID,
			LastEventType: ev.Type,
			LastWorkerID:  ev.WorkerID,
			ResumeFrom:    resumeFromEvent(ev),
			Attempt:       parseInt(ev.Evidence["attempt"]),
		}
		if cp.Attempt == 0 {
			cp.Attempt = 1
		}
		checkpoints[ev.TaskID] = cp
	}
	return checkpoints
}

func isTerminalRecoveryEvent(eventType string) bool {
	switch eventType {
	case "done", "complete", "fail", "task.completed", "task.failed", "task.cancelled":
		return true
	default:
		return false
	}
}

func resumeFromEvent(ev RecoveryEvent) string {
	if ev.Evidence != nil {
		if checkpoint := ev.Evidence["resume_checkpoint"]; checkpoint != "" {
			return checkpoint
		}
	}
	if ev.Message != "" {
		return ev.Message
	}
	return "resume after " + ev.Type
}

func parseInt(in string) int {
	n := 0
	for _, ch := range in {
		if ch < '0' || ch > '9' {
			continue
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
