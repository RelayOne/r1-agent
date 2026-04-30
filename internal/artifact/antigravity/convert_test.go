package antigravity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// ─── round-trip determinism ────────────────────────────────────────

func TestRoundtripPlanArtifact(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	original := R1Bundle{
		MissionID:    "MISSION-abc123",
		MissionTitle: "Migrate JWT to ed25519",
		CreatedAt:    now,
		Items: []R1Item{
			{
				ArtifactID: "artifact-3a2b8d10",
				Artifact: nodes.Artifact{
					ArtifactKind: "plan",
					Title:        "Migration plan",
					ContentType:  "application/json",
					StanceID:     "worker-MISSION-abc-1",
					When:         now,
					Version:      1,
				},
				Bytes: []byte(`{"steps": ["read login.go", "update verifier"]}`),
				Annotations: []nodes.ArtifactAnnotation{
					{
						AnnotatorID:   "operator:eric",
						AnnotatorRole: "operator",
						Action:        "comment",
						Body:          "Use existing rotation primitive",
						When:          now.Add(time.Minute),
						Version:       1,
					},
				},
			},
		},
	}

	wire, err := ToAntigravity(original)
	if err != nil {
		t.Fatalf("ToAntigravity: %v", err)
	}
	if wire.FormatVersion != CurrentFormatVersion {
		t.Errorf("FormatVersion = %q, want %q", wire.FormatVersion, CurrentFormatVersion)
	}
	if len(wire.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(wire.Artifacts))
	}
	wa := wire.Artifacts[0]
	if wa.Kind != "plan" {
		t.Errorf("Kind = %q, want plan", wa.Kind)
	}
	if !strings.Contains(wa.Body, "read login.go") {
		t.Errorf("Body missing original content: %q", wa.Body)
	}
	if len(wa.Annotations) != 1 {
		t.Fatalf("expected 1 annotation, got %d", len(wa.Annotations))
	}
	if wa.Annotations[0].AuthorRole != "user" {
		t.Errorf("AuthorRole mapping wrong: %q", wa.Annotations[0].AuthorRole)
	}

	// Round-trip back
	r1, err := FromAntigravity(wire)
	if err != nil {
		t.Fatalf("FromAntigravity: %v", err)
	}
	if r1.MissionID != original.MissionID {
		t.Errorf("MissionID changed: %q vs %q", r1.MissionID, original.MissionID)
	}
	if len(r1.Items) != 1 {
		t.Fatalf("expected 1 item back, got %d", len(r1.Items))
	}
	got := r1.Items[0].Artifact
	if got.ArtifactKind != "plan" {
		t.Errorf("kind round-trip lost: %q", got.ArtifactKind)
	}
	if got.Title != "Migration plan" {
		t.Errorf("title round-trip: %q", got.Title)
	}
	if string(r1.Items[0].Bytes) != string(original.Items[0].Bytes) {
		t.Errorf("bytes round-trip lost")
	}
}

func TestRoundtripScreenshot(t *testing.T) {
	now := time.Now().UTC()
	pngBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG header
	original := R1Bundle{
		MissionID: "MISSION-png",
		CreatedAt: now,
		Items: []R1Item{
			{
				ArtifactID: "artifact-7f9e2c41",
				Artifact: nodes.Artifact{
					ArtifactKind: "screenshot",
					Title:        "Login page after change",
					ContentType:  "image/png",
					StanceID:     "browser-agent-1",
					When:         now,
					Version:      1,
				},
				Bytes: pngBytes,
			},
		},
	}
	wire, err := ToAntigravity(original)
	if err != nil {
		t.Fatalf("ToAntigravity: %v", err)
	}
	wa := wire.Artifacts[0]
	if wa.ContentBase64 == "" {
		t.Error("expected base64 content for binary")
	}
	if wa.Body != "" {
		t.Error("Body should be empty for binary")
	}
	decoded, err := base64.StdEncoding.DecodeString(wa.ContentBase64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(decoded, pngBytes) {
		t.Error("base64 roundtrip lost bytes")
	}

	// Reverse
	back, err := FromAntigravity(wire)
	if err != nil {
		t.Fatalf("FromAntigravity: %v", err)
	}
	if !bytes.Equal(back.Items[0].Bytes, pngBytes) {
		t.Error("full roundtrip lost bytes")
	}
	if back.Items[0].Artifact.ArtifactKind != "screenshot" {
		t.Errorf("kind lost: %q", back.Items[0].Artifact.ArtifactKind)
	}
}

func TestRoundtripWiki_KindRenamed(t *testing.T) {
	original := R1Bundle{
		MissionID: "MISSION-wiki",
		CreatedAt: time.Now().UTC(),
		Items: []R1Item{
			{
				ArtifactID: "artifact-wiki-1",
				Artifact: nodes.Artifact{
					ArtifactKind: "wiki",
					Title:        "Auth Module Architecture",
					ContentType:  "text/markdown",
					StanceID:     "wiki-builder",
					When:         time.Now().UTC(),
					Version:      1,
				},
				Bytes: []byte("# Auth Module\n\nedge-cases described here."),
			},
		},
	}
	wire, err := ToAntigravity(original)
	if err != nil {
		t.Fatalf("ToAntigravity: %v", err)
	}
	if wire.Artifacts[0].Kind != "knowledge" {
		t.Errorf("wiki should map to knowledge, got %q", wire.Artifacts[0].Kind)
	}
	back, err := FromAntigravity(wire)
	if err != nil {
		t.Fatalf("FromAntigravity: %v", err)
	}
	if back.Items[0].Artifact.ArtifactKind != "wiki" {
		t.Errorf("knowledge should map back to wiki, got %q", back.Items[0].Artifact.ArtifactKind)
	}
}

func TestRoundtripCustom_KindPreservedViaMarker(t *testing.T) {
	original := R1Bundle{
		MissionID: "MISSION-custom",
		CreatedAt: time.Now().UTC(),
		Items: []R1Item{
			{
				ArtifactID: "artifact-test-output",
				Artifact: nodes.Artifact{
					ArtifactKind: "test_output",
					Title:        "go test ./internal/auth/...",
					ContentType:  "text/plain",
					StanceID:     "test-runner",
					When:         time.Now().UTC(),
					Version:      1,
				},
				Bytes: []byte("PASS\nok internal/auth 0.123s\n"),
			},
		},
	}
	wire, err := ToAntigravity(original)
	if err != nil {
		t.Fatalf("ToAntigravity: %v", err)
	}
	if wire.Artifacts[0].Kind != "knowledge" {
		t.Errorf("test_output should map to knowledge for AG export")
	}
	if !strings.HasPrefix(wire.Artifacts[0].Body, "# r1:original_kind=test_output\n") {
		t.Errorf("expected lossy-marker prefix in body, got: %q", wire.Artifacts[0].Body)
	}
	back, err := FromAntigravity(wire)
	if err != nil {
		t.Fatalf("FromAntigravity: %v", err)
	}
	if back.Items[0].Artifact.ArtifactKind != "test_output" {
		t.Errorf("kind should restore from marker, got %q", back.Items[0].Artifact.ArtifactKind)
	}
	// Verify the marker was stripped from the body
	if strings.Contains(string(back.Items[0].Bytes), "r1:original_kind") {
		t.Errorf("marker not stripped from imported body: %q", back.Items[0].Bytes)
	}
}

// ─── action / role mapping ───────────────────────────────────────────

func TestAnnotationActionMapping(t *testing.T) {
	cases := []struct {
		r1Action string
		agAction string
	}{
		{"comment", "comment"},
		{"reject", "request_change"},
		{"accept", "approve"},
		{"amend", "propose_edit"},
	}
	for _, tc := range cases {
		t.Run(tc.r1Action+"_"+tc.agAction, func(t *testing.T) {
			ann := nodes.ArtifactAnnotation{
				AnnotatorID:   "u",
				AnnotatorRole: "operator",
				Action:        tc.r1Action,
				When:          time.Now().UTC(),
				Version:       1,
			}
			if tc.r1Action == "comment" || tc.r1Action == "reject" {
				ann.Body = "x"
			}
			if tc.r1Action == "amend" {
				ann.AmendmentRef = "artifact-9d8e7f6a"
			}
			w, err := r1AnnotationToWire(ann)
			if err != nil {
				t.Fatalf("r1AnnotationToWire: %v", err)
			}
			if w.Action != tc.agAction {
				t.Errorf("R1->AG action: got %q, want %q", w.Action, tc.agAction)
			}
			back, err := wireAnnotationToR1(w)
			if err != nil {
				t.Fatalf("wireAnnotationToR1: %v", err)
			}
			if back.Action != tc.r1Action {
				t.Errorf("round-trip action: got %q, want %q", back.Action, tc.r1Action)
			}
		})
	}
}

func TestRoleMapping(t *testing.T) {
	cases := []struct {
		r1, ag string
	}{
		{"operator", "user"},
		{"peer-scope", "agent"},
		{"reviewer", "reviewer"},
	}
	for _, tc := range cases {
		t.Run(tc.r1+"_"+tc.ag, func(t *testing.T) {
			ann := nodes.ArtifactAnnotation{
				AnnotatorID:   "u",
				AnnotatorRole: tc.r1,
				Action:        "comment",
				Body:          "x",
				When:          time.Now().UTC(),
				Version:       1,
			}
			w, _ := r1AnnotationToWire(ann)
			if w.AuthorRole != tc.ag {
				t.Errorf("R1->AG role: %q -> %q, want %q", tc.r1, w.AuthorRole, tc.ag)
			}
			back, _ := wireAnnotationToR1(w)
			if back.AnnotatorRole != tc.r1 {
				t.Errorf("round-trip: %q -> %q -> %q", tc.r1, w.AuthorRole, back.AnnotatorRole)
			}
		})
	}
}

// ─── format-version strict mode ──────────────────────────────────────

func TestFromAntigravity_RejectsUnknownFormatVersion(t *testing.T) {
	b := Bundle{
		FormatVersion: "999",
		MissionID:     "MISSION-future",
		CreatedAt:     time.Now().UTC(),
	}
	_, err := FromAntigravity(b)
	if err == nil {
		t.Fatal("expected error for unknown format_version")
	}
	if !strings.Contains(err.Error(), "unsupported format_version") {
		t.Errorf("error should mention version: %v", err)
	}
}

func TestToAntigravity_RejectsEmptyMissionID(t *testing.T) {
	r := R1Bundle{}
	_, err := ToAntigravity(r)
	if err == nil {
		t.Error("expected error for empty mission_id")
	}
}

// ─── region preservation ─────────────────────────────────────────────

func TestRegionRoundtrip_LineRange(t *testing.T) {
	ann := nodes.ArtifactAnnotation{
		ArtifactRef:   "artifact-x",
		AnnotatorID:   "u",
		AnnotatorRole: "operator",
		Action:        "comment",
		Body:          "x",
		Region: &nodes.AnnotationRegion{
			File:      "internal/auth/login.go",
			LineStart: 42,
			LineEnd:   58,
		},
		When:    time.Now().UTC(),
		Version: 1,
	}
	w, _ := r1AnnotationToWire(ann)
	if w.Region == nil {
		t.Fatal("region lost on R1->AG")
	}
	if w.Region.LineFrom != 42 || w.Region.LineTo != 58 {
		t.Errorf("line range mismapped: %d-%d", w.Region.LineFrom, w.Region.LineTo)
	}
	back, _ := wireAnnotationToR1(w)
	if back.Region.LineStart != 42 || back.Region.LineEnd != 58 {
		t.Errorf("line range round-trip: %d-%d", back.Region.LineStart, back.Region.LineEnd)
	}
}

func TestRegionRoundtrip_BBox(t *testing.T) {
	ann := nodes.ArtifactAnnotation{
		ArtifactRef:   "artifact-x",
		AnnotatorID:   "u",
		AnnotatorRole: "operator",
		Action:        "comment",
		Body:          "x",
		Region: &nodes.AnnotationRegion{
			BBox: [4]int{10, 20, 100, 50},
		},
		When:    time.Now().UTC(),
		Version: 1,
	}
	w, _ := r1AnnotationToWire(ann)
	if len(w.Region.BoundingBox) != 4 {
		t.Fatal("bbox lost on R1->AG")
	}
	if w.Region.BoundingBox[0] != 10 || w.Region.BoundingBox[2] != 100 {
		t.Errorf("bbox mismapped: %v", w.Region.BoundingBox)
	}
	back, _ := wireAnnotationToR1(w)
	if back.Region.BBox != [4]int{10, 20, 100, 50} {
		t.Errorf("bbox round-trip: %v", back.Region.BBox)
	}
}

// ─── JSON serialization (golden test pattern) ────────────────────────

func TestBundleJSONIsParseable(t *testing.T) {
	b := Bundle{
		FormatVersion: "1",
		MissionID:     "MISSION-json",
		MissionTitle:  "test",
		CreatedAt:     time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
		Artifacts: []WireArtifact{
			{
				ID:        "artifact-1",
				Kind:      "plan",
				Title:     "the plan",
				CreatedAt: time.Date(2026, 4, 29, 0, 1, 0, 0, time.UTC),
				AgentID:   "worker-1",
				Body:      `{"steps":["a","b"]}`,
				Annotations: []WireAnnotation{
					{
						ID:         "annot-1",
						AuthorID:   "eric",
						AuthorRole: "user",
						Action:     "comment",
						Body:       "looks good",
						CreatedAt:  time.Date(2026, 4, 29, 0, 2, 0, 0, time.UTC),
					},
				},
			},
		},
	}
	bs, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed Bundle
	if err := json.Unmarshal(bs, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.MissionID != b.MissionID {
		t.Errorf("mission_id lost in JSON")
	}
	if len(parsed.Artifacts) != 1 || parsed.Artifacts[0].Annotations[0].Body != "looks good" {
		t.Errorf("annotation lost in JSON")
	}
}
