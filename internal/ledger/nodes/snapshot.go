package nodes

import (
	"fmt"
	"time"
)

// SnapshotAnnotation is the CTO's structured notes on the protected baseline.
// ID prefix: snap-anno-
type SnapshotAnnotation struct {
	Target         string    `json:"target"`
	AnnotationType string    `json:"annotation_type"` // intentional_pattern, accidental_pattern, load_bearing_area, known_footgun, convention, out_of_scope
	Description    string    `json:"description"`
	Evidence       string    `json:"evidence"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedBy      string    `json:"created_by"`

	// Optional fields.
	OriginatingConsultationRef string `json:"originating_consultation_ref,omitempty"`
	SupersededBy               string `json:"superseded_by,omitempty"`

	Version int `json:"schema_version"`
}

var validAnnotationTypes = map[string]bool{
	"intentional_pattern": true, "accidental_pattern": true,
	"load_bearing_area": true, "known_footgun": true,
	"convention": true, "out_of_scope": true,
}

func (s *SnapshotAnnotation) NodeType() string     { return "snapshot_annotation" }
func (s *SnapshotAnnotation) SchemaVersion() int   { return s.Version }

func (s *SnapshotAnnotation) Validate() error {
	if s.Target == "" {
		return fmt.Errorf("snapshot_annotation: target is required")
	}
	if s.AnnotationType == "" {
		return fmt.Errorf("snapshot_annotation: annotation_type is required")
	}
	if !validAnnotationTypes[s.AnnotationType] {
		return fmt.Errorf("snapshot_annotation: invalid annotation_type %q", s.AnnotationType)
	}
	if s.Description == "" {
		return fmt.Errorf("snapshot_annotation: description is required")
	}
	if s.Evidence == "" {
		return fmt.Errorf("snapshot_annotation: evidence is required")
	}
	if s.CreatedAt.IsZero() {
		return fmt.Errorf("snapshot_annotation: created_at is required")
	}
	if s.CreatedBy == "" {
		return fmt.Errorf("snapshot_annotation: created_by is required")
	}
	return nil
}

func init() {
	Register("snapshot_annotation", func() NodeTyper { return &SnapshotAnnotation{Version: 1} })
}
