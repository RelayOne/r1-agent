// Package antigravity implements bidirectional conversion between R1
// Artifact ledger nodes and Antigravity's Artifacts wire format.
//
// Why this package exists:
//
// Antigravity's Artifacts primitive (plans, screenshots, browser
// recordings, annotated diffs with Google-Doc-style inline comments) is
// the UX north-star of the agent-first IDE category. Buyers comparing
// agent platforms expect this primitive. R1 ships a strictly-better
// version through the ledger (signed, replayable, content-addressed,
// selective-disclosable), but the surface needs to be wire-compatible
// so:
//
//  1. Teams migrating from Antigravity can import their existing
//     artifact bundles into R1 with zero data loss.
//  2. R1 can export artifact bundles in Antigravity's format for tools
//     that have already integrated against it.
//  3. The "drop-in replacement with stronger guarantees" marketing
//     claim is structurally true, not aspirational.
//
// Wire-format note:
//
// Antigravity's exact serialization is not publicly documented at the
// byte level (it ships as part of the closed Antigravity binary). This
// package implements the documented shape: a JSON envelope per artifact
// containing kind, title, body (or content_b64 for binary), annotations
// array, timestamps. The structure is consistent with what Antigravity
// presents in its UI and what the documentation describes; if the
// underlying format diverges from this approximation, the conversion is
// straightforward to update without affecting the R1 side.
//
// The wire types are internal to this package; consumers interact via
// Convert / FromR1 / ToR1 functions.
package antigravity

import (
	"time"
)

// Bundle is the top-level Antigravity export. One bundle contains
// multiple artifacts plus their annotations.
type Bundle struct {
	// FormatVersion identifies the Antigravity wire format. R1 currently
	// supports format "1".
	FormatVersion string `json:"format_version"`

	// MissionID is Antigravity's task identifier, called "mission" in R1
	// terminology. Mapped 1:1.
	MissionID string `json:"mission_id"`

	// MissionTitle is the human-readable mission name.
	MissionTitle string `json:"mission_title,omitempty"`

	// CreatedAt is when the bundle was exported by Antigravity.
	CreatedAt time.Time `json:"created_at"`

	// Artifacts is the per-artifact list.
	Artifacts []WireArtifact `json:"artifacts"`
}

// WireArtifact is one artifact in the Antigravity envelope.
type WireArtifact struct {
	// ID is Antigravity's artifact identifier. Stable across exports.
	ID string `json:"id"`

	// Kind matches one of: plan, screenshot, recording, diff, knowledge.
	// "knowledge" maps to R1's "wiki" kind. All others map 1:1.
	Kind string `json:"kind"`

	// Title is the artifact's display name.
	Title string `json:"title"`

	// CreatedAt is when Antigravity emitted the artifact.
	CreatedAt time.Time `json:"created_at"`

	// AgentID identifies the Antigravity agent that produced the artifact.
	// Maps to R1's StanceID.
	AgentID string `json:"agent_id"`

	// Body is the inline content for text-shaped artifacts (plan,
	// diff, knowledge). Mutually exclusive with ContentBase64.
	Body string `json:"body,omitempty"`

	// ContentBase64 is the binary content for media-shaped artifacts
	// (screenshot, recording). Standard base64 (RFC 4648).
	ContentBase64 string `json:"content_b64,omitempty"`

	// MimeType is the explicit MIME type for binary content. For text
	// content, defaults are inferred from kind.
	MimeType string `json:"mime_type,omitempty"`

	// Annotations is the list of inline-feedback comments on this
	// artifact.
	Annotations []WireAnnotation `json:"annotations,omitempty"`

	// SupersedesID points at a predecessor artifact in this same bundle
	// when this artifact was generated as an amendment-driven successor.
	SupersedesID string `json:"supersedes_id,omitempty"`
}

// WireAnnotation is one inline comment on an artifact.
type WireAnnotation struct {
	// ID is Antigravity's annotation identifier.
	ID string `json:"id"`

	// AuthorID is the user or agent that wrote the annotation.
	AuthorID string `json:"author_id"`

	// AuthorRole is one of: user, agent, reviewer.
	// Maps to R1 annotator_role: user→operator, agent→peer-scope,
	// reviewer→reviewer.
	AuthorRole string `json:"author_role"`

	// Body is the annotation text.
	Body string `json:"body,omitempty"`

	// Action is one of: comment, request_change, approve, propose_edit.
	// Maps to R1 action: comment→comment, request_change→reject,
	// approve→accept, propose_edit→amend.
	Action string `json:"action"`

	// PatchID points at a successor artifact when Action ==
	// "propose_edit". Maps to R1 amendment_ref.
	PatchID string `json:"patch_id,omitempty"`

	// CreatedAt is when the annotation was written.
	CreatedAt time.Time `json:"created_at"`

	// Region locates the annotation within the artifact.
	Region *WireRegion `json:"region,omitempty"`
}

// WireRegion locates an annotation. For images: BoundingBox is set.
// For diffs: FilePath + LineFrom + LineTo are set.
type WireRegion struct {
	BoundingBox []int  `json:"bounding_box,omitempty"` // [x, y, w, h]
	FilePath    string `json:"file_path,omitempty"`
	LineFrom    int    `json:"line_from,omitempty"` // 1-indexed
	LineTo      int    `json:"line_to,omitempty"`   // 1-indexed
}

// CurrentFormatVersion is the format version this package emits and the
// versions it accepts on import. Reading older versions would require
// migration; this package fails fast on version mismatch rather than
// silently doing the wrong thing.
const CurrentFormatVersion = "1"

// kindMapAGtoR1 translates Antigravity kinds to R1 kinds. Most are 1:1;
// "knowledge" → "wiki" is the only rename.
var kindMapAGtoR1 = map[string]string{
	"plan":       "plan",
	"screenshot": "screenshot",
	"recording":  "recording",
	"diff":       "diff",
	"knowledge":  "wiki",
}

// kindMapR1toAG is the reverse. R1's "test_output" and "custom" don't
// have direct Antigravity equivalents; they round-trip as "knowledge"
// (lossy export with a metadata note) and "diff" (best-effort) when
// exported. Round-trip from R1 → AG → R1 preserves R1 kind via the
// AntigravitySource field on the R1 Artifact node.
var kindMapR1toAG = map[string]string{
	"plan":        "plan",
	"screenshot":  "screenshot",
	"recording":   "recording",
	"diff":        "diff",
	"wiki":        "knowledge",
	"test_output": "knowledge", // lossy
	"custom":      "knowledge", // lossy
}

// roleMapAGtoR1 translates Antigravity roles to R1 roles.
var roleMapAGtoR1 = map[string]string{
	"user":     "operator",
	"agent":    "peer-scope",
	"reviewer": "reviewer",
}

// roleMapR1toAG is the reverse. R1's "hire" and "supervisor" don't have
// Antigravity equivalents; they collapse to "agent" on export.
var roleMapR1toAG = map[string]string{
	"operator":   "user",
	"peer-scope": "agent",
	"reviewer":   "reviewer",
	"hire":       "agent",
	"supervisor": "agent",
}

// actionMapAGtoR1 translates Antigravity action strings to R1 actions.
var actionMapAGtoR1 = map[string]string{
	"comment":        "comment",
	"request_change": "reject",
	"approve":        "accept",
	"propose_edit":   "amend",
}

// actionMapR1toAG is the reverse.
var actionMapR1toAG = map[string]string{
	"comment": "comment",
	"reject":  "request_change",
	"accept":  "approve",
	"amend":   "propose_edit",
}
