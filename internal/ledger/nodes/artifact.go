// Package nodes — artifact.go
//
// Artifact ledger node (Parity-2 from r1-parity-and-superiority.md).
//
// An Artifact is a structured deliverable produced by a worker during a
// mission: a plan, a screenshot, a browser recording, an annotated diff,
// a wiki excerpt, a test output. It is the R1 answer to Antigravity's
// Artifacts primitive.
//
// Wire-compatible with Antigravity via internal/artifact/antigravity, but
// every Artifact in R1 carries the structural superiorities Antigravity
// cannot:
//
//   - content is content-addressed (sha256 stored in ContentRef)
//   - the Artifact node itself goes through ledger.AddNode which produces
//     a salt-blinded ContentCommitment in the chain tier (see
//     internal/ledger/ledger.go:47-58); the chain tier survives crypto-
//     shred of the bytes
//   - annotations are first-class siblings (see artifact_annotation.go)
//     that the worker reads at safe points to incorporate operator
//     feedback without restarting the mission
//   - SupersedesRef forms a version chain so an annotation-driven amendment
//     produces a new Artifact whose chain back to the original is
//     verifiable
//
// Storage layout:
//
//   .r1/artifacts/<sha256-of-content>          # raw bytes, content-addressed
//   ledger nodes (ID prefix "artifact-")        # references ContentRef
//
// Deletion of the bytes (crypto-shred) leaves the chain intact: the
// ContentCommitment in the ledger's chain tier still verifies the chain,
// and the Artifact node still references a now-missing ContentRef. This
// is the same redaction model the ledger uses generally.

package nodes

import (
	"fmt"
	"strings"
	"time"
)

// Artifact is a structured deliverable produced during a mission.
// ID prefix: artifact-
type Artifact struct {
	// ArtifactKind classifies the deliverable. One of: plan, screenshot,
	// recording, diff, wiki, test_output, custom.
	ArtifactKind string `json:"artifact_kind"`

	// Title is a short human-readable label.
	Title string `json:"title"`

	// ContentRef is the content-addressed reference to the bytes.
	// Format: "sha256:<hex>". Bytes live at .r1/artifacts/<hex>.
	ContentRef string `json:"content_ref"`

	// ContentType is the MIME type of the content. For Antigravity-compat
	// this is one of: application/json (plan), image/png (screenshot),
	// video/mp4 (recording), text/x-diff (diff), text/markdown (wiki),
	// text/plain (test_output), application/octet-stream (custom).
	ContentType string `json:"content_type"`

	// SizeBytes is the size of the referenced content in bytes.
	SizeBytes int64 `json:"size_bytes"`

	// ScopeID links this artifact to a team scope (Parity-1) when applicable.
	ScopeID string `json:"scope_id,omitempty"`

	// StanceID is the producing stance (worker, reviewer, hire). Required
	// because every artifact has provenance to a stance.
	StanceID string `json:"stance_id"`

	// AnnotationRefs lists child annotation node IDs that target this
	// artifact. Maintained eventual-consistent: an annotation node lists
	// its parent via ArtifactAnnotation.ArtifactRef; this field is the
	// reverse index updated lazily by the artifact builder when annotations
	// arrive.
	AnnotationRefs []string `json:"annotation_refs,omitempty"`

	// SupersedesRef points at the previous version's artifact NodeID when
	// this artifact replaces an earlier one (e.g. after an amendment).
	// Forms a version chain. First version has SupersedesRef == "".
	SupersedesRef string `json:"supersedes_ref,omitempty"`

	// AntigravitySource is set when this artifact was imported from an
	// Antigravity bundle. Stores the original Antigravity artifact ID
	// for round-trip fidelity. Empty for native R1 artifacts.
	AntigravitySource string `json:"antigravity_source,omitempty"`

	// When records the producer's wall-clock time at emission.
	When time.Time `json:"when"`

	Version int `json:"schema_version"`
}

// ValidArtifactKinds enumerates the supported artifact kinds. The set
// matches Antigravity's vocabulary plus "custom" for the long tail.
var ValidArtifactKinds = map[string]bool{
	"plan":        true,
	"screenshot":  true,
	"recording":   true,
	"diff":        true,
	"wiki":        true,
	"test_output": true,
	"custom":      true,
}

// validContentTypes enumerates the recommended MIME types per kind. The
// validator enforces match between kind and ContentType so that downstream
// consumers (the dashboard, the Antigravity exporter) can dispatch by kind.
var validContentTypes = map[string]map[string]bool{
	"plan":        {"application/json": true, "text/markdown": true, "text/plain": true},
	"screenshot":  {"image/png": true, "image/jpeg": true, "image/webp": true},
	"recording":   {"video/mp4": true, "video/webm": true, "application/x-r1-recording": true},
	"diff":        {"text/x-diff": true, "text/x-patch": true, "application/json": true},
	"wiki":        {"text/markdown": true, "text/html": true},
	"test_output": {"text/plain": true, "application/json": true, "application/x-junit-xml": true},
	"custom":      {}, // any
}

// NodeType implements NodeTyper.
func (a *Artifact) NodeType() string { return "artifact" }

// SchemaVersion implements NodeTyper.
func (a *Artifact) SchemaVersion() int { return a.Version }

// Validate implements NodeTyper. Rejects ill-formed artifacts at AddNode
// time so the ledger never persists an artifact whose ContentRef cannot
// resolve, whose kind is unknown, or whose ContentType disagrees with its
// kind.
func (a *Artifact) Validate() error {
	if a.ArtifactKind == "" {
		return fmt.Errorf("artifact: artifact_kind is required")
	}
	if !ValidArtifactKinds[a.ArtifactKind] {
		return fmt.Errorf("artifact: unknown artifact_kind %q (valid: %v)",
			a.ArtifactKind, sortedKeys(ValidArtifactKinds))
	}
	if a.Title == "" {
		return fmt.Errorf("artifact: title is required")
	}
	if a.ContentRef == "" {
		return fmt.Errorf("artifact: content_ref is required")
	}
	if !strings.HasPrefix(a.ContentRef, "sha256:") {
		return fmt.Errorf("artifact: content_ref must start with %q (got %q)",
			"sha256:", a.ContentRef)
	}
	if hex := strings.TrimPrefix(a.ContentRef, "sha256:"); len(hex) != 64 {
		return fmt.Errorf("artifact: content_ref hex must be 64 chars (got %d)", len(hex))
	}
	if a.ContentType == "" {
		return fmt.Errorf("artifact: content_type is required")
	}
	// Custom kind accepts any content type. Other kinds require the
	// content type to match the recommended set.
	if a.ArtifactKind != "custom" {
		allowed := validContentTypes[a.ArtifactKind]
		if !allowed[a.ContentType] {
			return fmt.Errorf("artifact: kind=%q does not accept content_type=%q (valid: %v)",
				a.ArtifactKind, a.ContentType, sortedKeys(allowed))
		}
	}
	if a.SizeBytes < 0 {
		return fmt.Errorf("artifact: size_bytes cannot be negative")
	}
	if a.StanceID == "" {
		return fmt.Errorf("artifact: stance_id is required")
	}
	if a.When.IsZero() {
		return fmt.Errorf("artifact: when is required")
	}
	if a.Version < 1 {
		return fmt.Errorf("artifact: schema_version must be >= 1")
	}
	return nil
}

// sortedKeys returns the keys of a string-keyed bool map sorted for stable
// error messages. Local helper rather than pulling in golang.org/x/exp/maps
// because the nodes package keeps a minimal dependency surface.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion sort: small N, no allocation
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func init() {
	Register("artifact", func() NodeTyper { return &Artifact{Version: 1} })
}
