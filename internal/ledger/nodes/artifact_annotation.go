// Package nodes — artifact_annotation.go
//
// ArtifactAnnotation is the operator's (or another agent's) feedback on
// an Artifact. This is the substrate behind Antigravity's "Google-Doc-style
// inline feedback that gets incorporated live without restarting the run."
//
// The annotation is a sibling node, not embedded in the Artifact, so:
//
//   - the worker reads new annotations at every safe point (next tool call
//     boundary) without re-fetching the parent Artifact
//   - annotations are independently queryable by author, by region, by
//     action; Latitude-style issue-lifecycle queries (Parity-10) work
//     without joins
//   - annotations are independently signed via the ledger's normal salt-
//     blinded ContentCommitment, so deleting an annotation crypto-shreds
//     just that comment without touching the artifact
//
// Action semantics:
//
//   "comment" — informational; worker may consider but is not required to
//   "reject" — worker MUST stop the current step and either revise or
//              escalate via HITL
//   "accept" — affirmative seal on the artifact; downstream gates can
//              treat the artifact as approved
//   "amend"  — proposes a replacement; AmendmentRef points at the new
//              artifact; worker reads and decides whether to supersede
//              the original (creating a new Artifact whose SupersedesRef
//              points at the original)

package nodes

import (
	"fmt"
	"time"
)

// ArtifactAnnotation is feedback on an Artifact.
// ID prefix: annot-
type ArtifactAnnotation struct {
	// ArtifactRef is the parent artifact's NodeID. Required.
	ArtifactRef string `json:"artifact_ref"`

	// AnnotatorID is the user/agent that created the annotation. For human
	// operators this is typically the username; for agents this is the
	// stance ID.
	AnnotatorID string `json:"annotator_id"`

	// AnnotatorRole classifies the annotator: one of operator, reviewer,
	// peer-scope, hire, supervisor.
	AnnotatorRole string `json:"annotator_role"`

	// Region locates the annotation within the artifact (a screenshot's
	// bbox, a diff's line range). Optional: artifact-level annotations
	// have nil Region.
	Region *AnnotationRegion `json:"region,omitempty"`

	// Body is the annotation text. Required for comment/reject; optional
	// for accept (a bare seal); optional for amend (the AmendmentRef
	// carries the substantive content).
	Body string `json:"body,omitempty"`

	// Action is one of: comment, reject, accept, amend.
	Action string `json:"action"`

	// AmendmentRef points at the proposed replacement artifact when
	// Action == "amend". Required for amend, ignored otherwise.
	AmendmentRef string `json:"amendment_ref,omitempty"`

	// ConsumedByStanceID records the worker stance that read this
	// annotation and acted on it. Empty until consumed. Once set, an
	// annotation is closed; subsequent reads ignore it. The supervisor's
	// poll loop sets this via a follow-up annotation node (annotations
	// are immutable; a "consumed" annotation is itself a new node with
	// type artifact_annotation_consumed; see consume.go).
	//
	// We track it on the original annotation as well via lazy copy when
	// the consume marker arrives, so a single read of the parent gives
	// the consumer's identity without a follow-up join. Optional.
	ConsumedByStanceID string `json:"consumed_by_stance_id,omitempty"`

	When    time.Time `json:"when"`
	Version int       `json:"schema_version"`
}

// AnnotationRegion locates an annotation within its parent artifact.
//
// For screenshots, BBox is the bounding box (x, y, width, height) in the
// image's pixel coordinates. File and line fields are unused.
//
// For diffs, File + LineStart + LineEnd locate the annotation. BBox is
// unused.
//
// Both can be set when the artifact contains both spatial and textual
// regions (e.g. an annotated screenshot of a diff).
type AnnotationRegion struct {
	BBox [4]int `json:"bbox,omitempty"` // [x, y, w, h]

	File      string `json:"file,omitempty"`
	LineStart int    `json:"line_start,omitempty"` // 1-indexed
	LineEnd   int    `json:"line_end,omitempty"`   // 1-indexed, inclusive
}

// ValidAnnotationActions enumerates supported actions.
var ValidAnnotationActions = map[string]bool{
	"comment": true,
	"reject":  true,
	"accept":  true,
	"amend":   true,
}

// ValidAnnotatorRoles enumerates supported roles.
var ValidAnnotatorRoles = map[string]bool{
	"operator":   true,
	"reviewer":   true,
	"peer-scope": true,
	"hire":       true,
	"supervisor": true,
}

// NodeType implements NodeTyper.
func (a *ArtifactAnnotation) NodeType() string { return "artifact_annotation" }

// SchemaVersion implements NodeTyper.
func (a *ArtifactAnnotation) SchemaVersion() int { return a.Version }

// Validate implements NodeTyper.
func (a *ArtifactAnnotation) Validate() error {
	if a.ArtifactRef == "" {
		return fmt.Errorf("artifact_annotation: artifact_ref is required")
	}
	if a.AnnotatorID == "" {
		return fmt.Errorf("artifact_annotation: annotator_id is required")
	}
	if a.AnnotatorRole == "" {
		return fmt.Errorf("artifact_annotation: annotator_role is required")
	}
	if !ValidAnnotatorRoles[a.AnnotatorRole] {
		return fmt.Errorf("artifact_annotation: unknown annotator_role %q (valid: %v)",
			a.AnnotatorRole, sortedKeys(ValidAnnotatorRoles))
	}
	if a.Action == "" {
		return fmt.Errorf("artifact_annotation: action is required")
	}
	if !ValidAnnotationActions[a.Action] {
		return fmt.Errorf("artifact_annotation: unknown action %q (valid: %v)",
			a.Action, sortedKeys(ValidAnnotationActions))
	}
	switch a.Action {
	case "comment", "reject":
		if a.Body == "" {
			return fmt.Errorf("artifact_annotation: body is required for action=%q", a.Action)
		}
	case "amend":
		if a.AmendmentRef == "" {
			return fmt.Errorf("artifact_annotation: amendment_ref is required for action=amend")
		}
	}
	if a.Region != nil {
		if err := a.Region.validate(); err != nil {
			return fmt.Errorf("artifact_annotation: %w", err)
		}
	}
	if a.When.IsZero() {
		return fmt.Errorf("artifact_annotation: when is required")
	}
	if a.Version < 1 {
		return fmt.Errorf("artifact_annotation: schema_version must be >= 1")
	}
	return nil
}

// validate checks an AnnotationRegion is well-formed. A region must have
// either a BBox or File+LineStart+LineEnd populated; mixed is allowed.
func (r *AnnotationRegion) validate() error {
	hasBBox := r.BBox[2] > 0 && r.BBox[3] > 0 // width and height positive
	hasLines := r.File != "" && r.LineStart > 0 && r.LineEnd >= r.LineStart
	if !hasBBox && !hasLines {
		return fmt.Errorf("region: must specify either bbox (w/h positive) or file+line range")
	}
	if hasBBox {
		if r.BBox[0] < 0 || r.BBox[1] < 0 {
			return fmt.Errorf("region: bbox coordinates cannot be negative")
		}
	}
	return nil
}

func init() {
	Register("artifact_annotation", func() NodeTyper { return &ArtifactAnnotation{Version: 1} })
}
