package nodes

import (
	"strings"
	"testing"
	"time"
)

// ─── Artifact ────────────────────────────────────────────────────────

func TestArtifact_NodeTyper(t *testing.T) {
	got, err := New("artifact")
	if err != nil {
		t.Fatalf("New(artifact): %v", err)
	}
	a, ok := got.(*Artifact)
	if !ok {
		t.Fatalf("expected *Artifact, got %T", got)
	}
	if a.NodeType() != "artifact" {
		t.Errorf("NodeType() = %q, want %q", a.NodeType(), "artifact")
	}
	if a.SchemaVersion() != 1 {
		t.Errorf("SchemaVersion() = %d, want 1", a.SchemaVersion())
	}
}

func validArtifact() *Artifact {
	return &Artifact{
		ArtifactKind: "plan",
		Title:        "Migrate JWT to ed25519",
		ContentRef:   "sha256:" + strings.Repeat("a", 64),
		ContentType:  "application/json",
		SizeBytes:    1024,
		StanceID:     "worker-MISSION-abc-1",
		When:         time.Now(),
		Version:      1,
	}
}

func TestArtifact_Validate_Happy(t *testing.T) {
	if err := validArtifact().Validate(); err != nil {
		t.Errorf("validArtifact().Validate() = %v", err)
	}
}

func TestArtifact_Validate_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Artifact)
		wantSub string
	}{
		{"missing kind", func(a *Artifact) { a.ArtifactKind = "" }, "artifact_kind"},
		{"unknown kind", func(a *Artifact) { a.ArtifactKind = "video-game" }, "unknown artifact_kind"},
		{"missing title", func(a *Artifact) { a.Title = "" }, "title"},
		{"missing content_ref", func(a *Artifact) { a.ContentRef = "" }, "content_ref"},
		{"bad content_ref prefix", func(a *Artifact) { a.ContentRef = "md5:abc" }, "must start with"},
		{"short content_ref hex", func(a *Artifact) { a.ContentRef = "sha256:abc" }, "64 chars"},
		{"missing content_type", func(a *Artifact) { a.ContentType = "" }, "content_type"},
		{"kind/type mismatch", func(a *Artifact) { a.ContentType = "image/png" }, "does not accept"},
		{"negative size", func(a *Artifact) { a.SizeBytes = -1 }, "size_bytes"},
		{"missing stance", func(a *Artifact) { a.StanceID = "" }, "stance_id"},
		{"zero when", func(a *Artifact) { a.When = time.Time{} }, "when"},
		{"zero version", func(a *Artifact) { a.Version = 0 }, "schema_version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validArtifact()
			tc.mut(a)
			err := a.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestArtifact_CustomKindAcceptsAnyContentType(t *testing.T) {
	a := validArtifact()
	a.ArtifactKind = "custom"
	a.ContentType = "application/x-some-weird-format"
	if err := a.Validate(); err != nil {
		t.Errorf("custom kind should accept any content_type, got: %v", err)
	}
}

func TestArtifact_KindContentTypeMatrix(t *testing.T) {
	cases := []struct {
		kind        string
		contentType string
		wantValid   bool
	}{
		{"plan", "application/json", true},
		{"plan", "text/markdown", true},
		{"plan", "image/png", false},
		{"screenshot", "image/png", true},
		{"screenshot", "image/jpeg", true},
		{"screenshot", "video/mp4", false},
		{"recording", "video/mp4", true},
		{"recording", "image/png", false},
		{"diff", "text/x-diff", true},
		{"diff", "text/x-patch", true},
		{"wiki", "text/markdown", true},
		{"test_output", "application/json", true},
		{"test_output", "application/x-junit-xml", true},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"_"+tc.contentType, func(t *testing.T) {
			a := validArtifact()
			a.ArtifactKind = tc.kind
			a.ContentType = tc.contentType
			err := a.Validate()
			if tc.wantValid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tc.wantValid && err == nil {
				t.Errorf("expected invalid, got no error")
			}
		})
	}
}

// ─── ArtifactAnnotation ──────────────────────────────────────────────

func TestArtifactAnnotation_NodeTyper(t *testing.T) {
	got, err := New("artifact_annotation")
	if err != nil {
		t.Fatalf("New(artifact_annotation): %v", err)
	}
	a, ok := got.(*ArtifactAnnotation)
	if !ok {
		t.Fatalf("expected *ArtifactAnnotation, got %T", got)
	}
	if a.NodeType() != "artifact_annotation" {
		t.Errorf("NodeType() = %q", a.NodeType())
	}
}

func validAnnotation() *ArtifactAnnotation {
	return &ArtifactAnnotation{
		ArtifactRef:   "artifact-3a2b8d10",
		AnnotatorID:   "operator:eric",
		AnnotatorRole: "operator",
		Action:        "comment",
		Body:          "Use existing rotation primitive at internal/secrets/rotation.go",
		When:          time.Now(),
		Version:       1,
	}
}

func TestArtifactAnnotation_Validate_Happy(t *testing.T) {
	if err := validAnnotation().Validate(); err != nil {
		t.Errorf("validAnnotation().Validate() = %v", err)
	}
}

func TestArtifactAnnotation_Validate_RequiresBodyForCommentReject(t *testing.T) {
	for _, action := range []string{"comment", "reject"} {
		t.Run(action, func(t *testing.T) {
			a := validAnnotation()
			a.Action = action
			a.Body = ""
			err := a.Validate()
			if err == nil {
				t.Fatalf("expected error for empty body with action=%s", action)
			}
			if !strings.Contains(err.Error(), "body is required") {
				t.Errorf("error %q does not mention body requirement", err.Error())
			}
		})
	}
}

func TestArtifactAnnotation_Validate_AcceptDoesNotRequireBody(t *testing.T) {
	a := validAnnotation()
	a.Action = "accept"
	a.Body = ""
	if err := a.Validate(); err != nil {
		t.Errorf("accept without body should be valid, got: %v", err)
	}
}

func TestArtifactAnnotation_Validate_AmendRequiresAmendmentRef(t *testing.T) {
	a := validAnnotation()
	a.Action = "amend"
	a.Body = ""         // amend can have empty body
	a.AmendmentRef = "" // but must have ref
	err := a.Validate()
	if err == nil {
		t.Fatal("expected error for amend without amendment_ref")
	}
	if !strings.Contains(err.Error(), "amendment_ref") {
		t.Errorf("error %q does not mention amendment_ref", err.Error())
	}
	a.AmendmentRef = "artifact-7f9e2c41"
	if err := a.Validate(); err != nil {
		t.Errorf("amend with amendment_ref should be valid, got: %v", err)
	}
}

func TestArtifactAnnotation_Validate_RegionWellFormed(t *testing.T) {
	t.Run("bbox valid", func(t *testing.T) {
		a := validAnnotation()
		a.Region = &AnnotationRegion{BBox: [4]int{10, 20, 100, 50}}
		if err := a.Validate(); err != nil {
			t.Errorf("expected valid bbox, got: %v", err)
		}
	})
	t.Run("bbox negative coords", func(t *testing.T) {
		a := validAnnotation()
		a.Region = &AnnotationRegion{BBox: [4]int{-1, 20, 100, 50}}
		if err := a.Validate(); err == nil {
			t.Error("expected error for negative bbox coords")
		}
	})
	t.Run("line range valid", func(t *testing.T) {
		a := validAnnotation()
		a.Region = &AnnotationRegion{
			File:      "internal/auth/login.go",
			LineStart: 10,
			LineEnd:   15,
		}
		if err := a.Validate(); err != nil {
			t.Errorf("expected valid line range, got: %v", err)
		}
	})
	t.Run("inverted line range", func(t *testing.T) {
		a := validAnnotation()
		a.Region = &AnnotationRegion{
			File:      "internal/auth/login.go",
			LineStart: 20,
			LineEnd:   10,
		}
		if err := a.Validate(); err == nil {
			t.Error("expected error for inverted line range")
		}
	})
	t.Run("empty region", func(t *testing.T) {
		a := validAnnotation()
		a.Region = &AnnotationRegion{} // neither bbox nor lines
		if err := a.Validate(); err == nil {
			t.Error("expected error for empty region")
		}
	})
}

func TestArtifactAnnotation_RejectsUnknownActionAndRole(t *testing.T) {
	t.Run("unknown action", func(t *testing.T) {
		a := validAnnotation()
		a.Action = "lol"
		err := a.Validate()
		if err == nil || !strings.Contains(err.Error(), "unknown action") {
			t.Errorf("expected unknown-action error, got: %v", err)
		}
	})
	t.Run("unknown role", func(t *testing.T) {
		a := validAnnotation()
		a.AnnotatorRole = "ceo"
		err := a.Validate()
		if err == nil || !strings.Contains(err.Error(), "unknown annotator_role") {
			t.Errorf("expected unknown-role error, got: %v", err)
		}
	})
}
