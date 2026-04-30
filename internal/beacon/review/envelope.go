package review

import (
	"encoding/json"
	"errors"
	"time"
)

type Envelope struct {
	BeaconID    string    `json:"beacon_id"`
	SessionID   string    `json:"session_id"`
	ArtifactRef string    `json:"artifact_ref"`
	RequestedAt time.Time `json:"requested_at"`
	Reason      string    `json:"reason"`
	RequestedBy string    `json:"requested_by,omitempty"`
}

func (e Envelope) Validate() error {
	if e.BeaconID == "" || e.SessionID == "" || e.ArtifactRef == "" || e.RequestedAt.IsZero() || e.Reason == "" {
		return errors.New("review: beacon_id, session_id, artifact_ref, requested_at, and reason are required")
	}
	return nil
}

func (e Envelope) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}
