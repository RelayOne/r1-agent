package honesty

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

type DecisionKind string

const (
	KindRefused DecisionKind = "refused"
	KindWhyNot  DecisionKind = "why_not"
)

type Decision struct {
	Kind       DecisionKind `json:"kind"`
	TaskID     string       `json:"task_id,omitempty"`
	Action     string       `json:"action,omitempty"`
	Claim      string       `json:"claim,omitempty"`
	Reason     string       `json:"reason"`
	Evidence   []string     `json:"evidence,omitempty"`
	OverrideBy string       `json:"override_by,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

func (d Decision) Validate() error {
	if d.Kind != KindRefused && d.Kind != KindWhyNot {
		return fmt.Errorf("honesty: invalid kind %q", d.Kind)
	}
	if d.Reason == "" {
		return fmt.Errorf("honesty: reason is required")
	}
	if d.CreatedAt.IsZero() {
		return fmt.Errorf("honesty: created_at is required")
	}
	if d.Kind == KindRefused && d.Claim == "" {
		return fmt.Errorf("honesty: claim is required for refused decisions")
	}
	if d.Kind == KindWhyNot && d.Action == "" {
		return fmt.Errorf("honesty: action is required for why_not decisions")
	}
	return nil
}

func Record(lg *ledger.Ledger, createdBy, missionID string, d Decision) (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("honesty: marshal: %w", err)
	}
	return lg.AddNode(nil, ledger.Node{
		Type:          "honesty_decision",
		SchemaVersion: 1,
		CreatedAt:     d.CreatedAt.UTC(),
		CreatedBy:     createdBy,
		MissionID:     missionID,
		Content:       body,
	})
}

func Query(lg *ledger.Ledger, taskID string) ([]Decision, error) {
	nodes, err := lg.Query(nil, ledger.QueryFilter{Type: "honesty_decision"})
	if err != nil {
		return nil, err
	}
	out := make([]Decision, 0, len(nodes))
	for _, n := range nodes {
		var d Decision
		if err := json.Unmarshal(n.Content, &d); err != nil {
			continue
		}
		if taskID != "" && d.TaskID != taskID {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}
