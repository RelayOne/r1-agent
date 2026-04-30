// Package antigravity — convert.go
//
// Bidirectional conversion between R1 ledger Artifacts and Antigravity
// Bundles. Two top-level functions:
//
//   ToAntigravity(r1Bundle) → Bundle    — export R1 → Antigravity wire
//   FromAntigravity(Bundle) → r1Bundle  — import Antigravity → R1 wire
//
// Where r1Bundle is an in-memory representation matching R1's ledger
// node shapes; the importing caller is responsible for actually
// persisting via *artifact.Builder.

package antigravity

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// R1Bundle is the in-memory representation of an export-ready set of
// R1 artifacts and annotations. It is the input to ToAntigravity and
// the output of FromAntigravity.
//
// Bytes are kept inline (rather than as content-refs) because the
// Antigravity wire format inlines bytes via base64. A consumer of
// FromAntigravity is responsible for putting these bytes into an R1
// content store via the artifact.Builder.
type R1Bundle struct {
	MissionID    string
	MissionTitle string
	CreatedAt    time.Time

	// Items pairs each artifact with the actual bytes (content) and the
	// annotations targeting that artifact. The bytes are NOT stored in
	// the ledger node directly; the importer uses Builder.Emit which
	// places bytes into the content-addressed Store.
	Items []R1Item
}

// R1Item is one artifact-plus-bytes-plus-annotations triple.
type R1Item struct {
	// ArtifactID is the original R1 NodeID (for export) or empty (for
	// import: the importer assigns IDs at AddNode time).
	ArtifactID string

	// Artifact is the R1 ledger node form with content_ref, when, etc.
	// On export, ContentRef is left blank (Antigravity wire inlines).
	// On import, ContentRef is left blank (importer fills after Put).
	Artifact nodes.Artifact

	// Bytes is the actual content. Populated on both export and import.
	Bytes []byte

	// Annotations targets this artifact.
	Annotations []nodes.ArtifactAnnotation
}

// ToAntigravity converts an R1Bundle to the Antigravity wire format.
// Lossy on R1 kinds without direct AG equivalents (test_output, custom)
// — those are mapped to "knowledge" with a note in the body. The
// AntigravitySource field on each R1 Artifact preserves round-trip
// fidelity so that re-importing the exported bundle restores the R1
// kind.
func ToAntigravity(r R1Bundle) (Bundle, error) {
	if r.MissionID == "" {
		return Bundle{}, errors.New("antigravity: ToAntigravity: mission_id required")
	}

	out := Bundle{
		FormatVersion: CurrentFormatVersion,
		MissionID:     r.MissionID,
		MissionTitle:  r.MissionTitle,
		CreatedAt:     r.CreatedAt,
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now().UTC()
	}

	for _, item := range r.Items {
		wa, err := r1ItemToWire(item)
		if err != nil {
			return Bundle{}, fmt.Errorf("antigravity: convert artifact %q: %w", item.ArtifactID, err)
		}
		out.Artifacts = append(out.Artifacts, wa)
	}
	return out, nil
}

// FromAntigravity converts an Antigravity Bundle to an R1Bundle. The
// importer caller persists the result via *artifact.Builder.
//
// Strict on FormatVersion: an unrecognized version returns an explicit
// error rather than attempting best-effort conversion. Migrations
// happen in a separate package (when needed).
func FromAntigravity(b Bundle) (R1Bundle, error) {
	if b.FormatVersion != CurrentFormatVersion {
		return R1Bundle{}, fmt.Errorf("antigravity: unsupported format_version %q (expected %q)",
			b.FormatVersion, CurrentFormatVersion)
	}
	if b.MissionID == "" {
		return R1Bundle{}, errors.New("antigravity: FromAntigravity: mission_id required")
	}

	out := R1Bundle{
		MissionID:    b.MissionID,
		MissionTitle: b.MissionTitle,
		CreatedAt:    b.CreatedAt,
	}

	for _, wa := range b.Artifacts {
		item, err := wireToR1Item(wa, b.MissionID)
		if err != nil {
			return R1Bundle{}, fmt.Errorf("antigravity: convert wire %q: %w", wa.ID, err)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ─── R1 → Antigravity conversion ────────────────────────────────────

func r1ItemToWire(item R1Item) (WireArtifact, error) {
	a := item.Artifact

	// Map R1 kind → AG kind. Unknown kinds (custom) collapse to "knowledge"
	// with a metadata-style note in the body.
	agKind, ok := kindMapR1toAG[a.ArtifactKind]
	if !ok {
		// Defensive default; Validate would have rejected unknown kinds.
		agKind = "knowledge"
	}

	wa := WireArtifact{
		ID:           item.ArtifactID,
		Kind:         agKind,
		Title:        a.Title,
		CreatedAt:    a.When,
		AgentID:      a.StanceID,
		MimeType:     a.ContentType,
		SupersedesID: a.SupersedesRef,
	}

	// Place bytes into Body (text) or ContentBase64 (binary) based on
	// content type. The discriminator is whether ContentType is a text
	// MIME.
	if isTextContent(a.ContentType) {
		wa.Body = string(item.Bytes)
	} else {
		wa.ContentBase64 = base64.StdEncoding.EncodeToString(item.Bytes)
	}

	// Lossy-kind note: when R1 kind didn't have a direct AG kind, prepend
	// a marker so a future re-import knows the original kind. This is
	// also what AntigravitySource field carries via wire.
	if a.ArtifactKind == "test_output" || a.ArtifactKind == "custom" {
		marker := fmt.Sprintf("# r1:original_kind=%s\n", a.ArtifactKind)
		if wa.Body != "" {
			wa.Body = marker + wa.Body
		} else {
			// Binary: encode the marker as part of MimeType prefix
			wa.MimeType = "x-r1-original-kind/" + a.ArtifactKind + "+" + a.ContentType
		}
	}

	for _, ann := range item.Annotations {
		w, err := r1AnnotationToWire(ann)
		if err != nil {
			return WireArtifact{}, fmt.Errorf("annotation: %w", err)
		}
		wa.Annotations = append(wa.Annotations, w)
	}
	return wa, nil
}

func r1AnnotationToWire(a nodes.ArtifactAnnotation) (WireAnnotation, error) {
	role, ok := roleMapR1toAG[a.AnnotatorRole]
	if !ok {
		role = "agent" // safe default
	}
	action, ok := actionMapR1toAG[a.Action]
	if !ok {
		return WireAnnotation{}, fmt.Errorf("unknown R1 action %q", a.Action)
	}
	w := WireAnnotation{
		AuthorID:   a.AnnotatorID,
		AuthorRole: role,
		Action:     action,
		Body:       a.Body,
		PatchID:    a.AmendmentRef,
		CreatedAt:  a.When,
	}
	if a.Region != nil {
		w.Region = &WireRegion{
			FilePath: a.Region.File,
			LineFrom: a.Region.LineStart,
			LineTo:   a.Region.LineEnd,
		}
		if a.Region.BBox[2] > 0 && a.Region.BBox[3] > 0 {
			w.Region.BoundingBox = []int{
				a.Region.BBox[0], a.Region.BBox[1], a.Region.BBox[2], a.Region.BBox[3],
			}
		}
	}
	return w, nil
}

// ─── Antigravity → R1 conversion ────────────────────────────────────

func wireToR1Item(wa WireArtifact, missionID string) (R1Item, error) {
	// Determine the R1 kind. If the body has a r1:original_kind marker
	// (we wrote on export), restore the original kind for round-trip
	// fidelity. Otherwise look up the AG kind in the standard map.
	r1Kind := ""
	body := wa.Body
	mimeType := wa.MimeType

	if marker := extractOriginalKindMarker(body); marker != "" {
		r1Kind = marker
		body = trimOriginalKindMarker(body)
	} else if origKindFromMime, rest := extractOriginalKindFromMime(mimeType); origKindFromMime != "" {
		r1Kind = origKindFromMime
		mimeType = rest
	} else {
		mapped, ok := kindMapAGtoR1[wa.Kind]
		if !ok {
			return R1Item{}, fmt.Errorf("unknown Antigravity kind %q", wa.Kind)
		}
		r1Kind = mapped
	}

	// Reconstruct bytes
	var raw []byte
	if body != "" {
		raw = []byte(body)
	} else if wa.ContentBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(wa.ContentBase64)
		if err != nil {
			return R1Item{}, fmt.Errorf("decode base64: %w", err)
		}
		raw = decoded
	} else {
		// Empty body and empty base64: a zero-byte artifact is valid (e.g.
		// an empty test_output). Allow but note it.
		raw = []byte{}
	}

	contentType := mimeType
	if contentType == "" {
		contentType = inferContentTypeForKind(r1Kind, len(raw) > 0 && body != "")
	}

	a := nodes.Artifact{
		ArtifactKind:      r1Kind,
		Title:             wa.Title,
		ContentType:       contentType,
		StanceID:          wa.AgentID,
		SupersedesRef:     wa.SupersedesID,
		AntigravitySource: wa.ID,
		When:              wa.CreatedAt,
		Version:           1,
		// ContentRef + SizeBytes filled by the importer's Builder.Emit
	}
	if a.When.IsZero() {
		a.When = time.Now().UTC()
	}
	if a.StanceID == "" {
		// AG bundles may omit agent_id for some artifacts; mark them
		// with a generic import label so the R1 Validate doesn't reject.
		a.StanceID = "antigravity-import"
	}

	item := R1Item{
		Artifact: a,
		Bytes:    raw,
	}

	for _, w := range wa.Annotations {
		ann, err := wireAnnotationToR1(w)
		if err != nil {
			return R1Item{}, fmt.Errorf("annotation: %w", err)
		}
		item.Annotations = append(item.Annotations, ann)
	}
	return item, nil
}

func wireAnnotationToR1(w WireAnnotation) (nodes.ArtifactAnnotation, error) {
	role, ok := roleMapAGtoR1[w.AuthorRole]
	if !ok {
		return nodes.ArtifactAnnotation{}, fmt.Errorf("unknown AG role %q", w.AuthorRole)
	}
	action, ok := actionMapAGtoR1[w.Action]
	if !ok {
		return nodes.ArtifactAnnotation{}, fmt.Errorf("unknown AG action %q", w.Action)
	}
	a := nodes.ArtifactAnnotation{
		AnnotatorID:   w.AuthorID,
		AnnotatorRole: role,
		Action:        action,
		Body:          w.Body,
		AmendmentRef:  w.PatchID,
		When:          w.CreatedAt,
		Version:       1,
	}
	if a.When.IsZero() {
		a.When = time.Now().UTC()
	}
	if w.Region != nil {
		r := &nodes.AnnotationRegion{
			File:      w.Region.FilePath,
			LineStart: w.Region.LineFrom,
			LineEnd:   w.Region.LineTo,
		}
		if len(w.Region.BoundingBox) == 4 {
			r.BBox = [4]int{
				w.Region.BoundingBox[0],
				w.Region.BoundingBox[1],
				w.Region.BoundingBox[2],
				w.Region.BoundingBox[3],
			}
		}
		a.Region = r
	}
	return a, nil
}

// ─── helpers ────────────────────────────────────────────────────────

func isTextContent(mimeType string) bool {
	if mimeType == "" {
		return true
	}
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "application/json", "application/x-junit-xml", "application/xml":
		return true
	}
	return false
}

// extractOriginalKindMarker peels off the "# r1:original_kind=..." marker
// from a body string when present. Returns the original kind or empty.
func extractOriginalKindMarker(body string) string {
	const prefix = "# r1:original_kind="
	if !strings.HasPrefix(body, prefix) {
		return ""
	}
	rest := body[len(prefix):]
	newline := strings.Index(rest, "\n")
	if newline < 0 {
		return rest
	}
	return rest[:newline]
}

// trimOriginalKindMarker removes the marker line from body.
func trimOriginalKindMarker(body string) string {
	const prefix = "# r1:original_kind="
	if !strings.HasPrefix(body, prefix) {
		return body
	}
	newline := strings.Index(body, "\n")
	if newline < 0 {
		return ""
	}
	return body[newline+1:]
}

// extractOriginalKindFromMime decodes the AntigravitySource hint we
// embedded in the MIME type for binary lossy round-trips.
func extractOriginalKindFromMime(mt string) (origKind, rest string) {
	const prefix = "x-r1-original-kind/"
	if !strings.HasPrefix(mt, prefix) {
		return "", ""
	}
	body := mt[len(prefix):]
	plus := strings.Index(body, "+")
	if plus < 0 {
		return "", ""
	}
	return body[:plus], body[plus+1:]
}

// inferContentTypeForKind chooses a default content type when the wire
// envelope didn't carry an explicit MIME. The hint indicates whether the
// content originally came from Body (text) vs ContentBase64 (binary),
// which lets us pick a text MIME when appropriate.
func inferContentTypeForKind(kind string, hasTextBody bool) string {
	if hasTextBody {
		switch kind {
		case "plan":
			return "application/json"
		case "diff":
			return "text/x-diff"
		case "wiki":
			return "text/markdown"
		case "test_output":
			return "text/plain"
		default:
			return "text/plain"
		}
	}
	switch kind {
	case "screenshot":
		return "image/png"
	case "recording":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}
