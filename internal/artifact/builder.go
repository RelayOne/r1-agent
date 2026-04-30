// Package artifact — builder.go
//
// The Builder is the worker-side API for emitting Artifacts coherently.
// It wraps three operations into one call:
//
//   1. Put bytes into the content-addressed Store
//   2. Construct the Artifact ledger node with content_ref + size_bytes
//   3. Persist via Ledger.AddNode (which produces salt + ContentCommitment
//      automatically per internal/ledger/ledger.go:266-289)
//
// Coherence guarantees the caller cannot easily get wrong if implementing
// against the raw store + raw ledger:
//
//   - SizeBytes always matches the persisted bytes
//   - ContentRef always points at bytes that exist in the store
//   - When is set to wall-clock if caller leaves it zero
//   - Validation runs before persistence; partial states never appear
//   - Concurrent emission from the same worker is safe (Store.Put is
//     synchronized; Ledger.AddNode is synchronized internally)
//
// The Builder also handles the supersedes case: when an annotation's
// amend action triggers a new Artifact, the worker calls Builder.Amend
// with the amendment bytes and the original Artifact's NodeID; the
// resulting node has SupersedesRef populated and an EdgeReferences edge
// drawn back to the predecessor.

package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// Ledger is the subset of *ledger.Ledger that Builder needs. Defining it
// as an interface keeps the test surface small (no need to instantiate a
// full Ledger for unit tests of the Builder; a fake implementing this
// interface is enough).
type Ledger interface {
	AddNode(ctx context.Context, node ledger.Node) (ledger.NodeID, error)
	AddEdge(ctx context.Context, edge ledger.Edge) error
}

// Builder produces ledger-persisted Artifacts.
type Builder struct {
	store  *Store
	ledger Ledger
}

// NewBuilder constructs a Builder from a content store and a ledger.
// Both arguments are required. A nil store or nil ledger panics at
// construction time so misconfigurations are caught at startup, not on
// the first artifact emission deep in a mission.
func NewBuilder(store *Store, l Ledger) *Builder {
	if store == nil {
		panic("artifact: NewBuilder: store is nil")
	}
	if l == nil {
		panic("artifact: NewBuilder: ledger is nil")
	}
	return &Builder{store: store, ledger: l}
}

// EmitParams carries everything the caller needs to specify per emission.
// The kind, content, and StanceID are required; other fields have sensible
// defaults.
type EmitParams struct {
	// Kind is the artifact kind: plan, screenshot, recording, diff, wiki,
	// test_output, custom.
	Kind string

	// Title is a human-readable label.
	Title string

	// Content is the raw bytes of the artifact. Required for native emit.
	// Mutually exclusive with ContentRef (use Reattach for the case where
	// bytes are already in the store from a prior Put).
	Content []byte

	// ContentRef is the precomputed content reference, used when the
	// caller has already stored bytes via Store.Put or PutFromReader.
	// Mutually exclusive with Content.
	ContentRef string

	// ContentType is the MIME type. Defaults to a kind-appropriate type
	// (application/json for plan, image/png for screenshot, etc.) if
	// empty.
	ContentType string

	// MissionID associates the artifact with a mission. Required because
	// every R1 artifact has mission provenance.
	MissionID string

	// StanceID is the producing stance. Required.
	StanceID string

	// ScopeID associates the artifact with a team scope (Parity-1).
	// Optional.
	ScopeID string

	// SupersedesRef points at a predecessor artifact NodeID; sets up the
	// version chain and produces an EdgeReferences edge from the new
	// artifact to the predecessor. Optional.
	SupersedesRef string

	// AntigravitySource records the original Antigravity artifact ID
	// when this artifact is being imported. Optional.
	AntigravitySource string

	// When defaults to time.Now().UTC() if zero.
	When time.Time
}

// Emit creates an Artifact ledger node from the given parameters.
// Returns the new NodeID on success.
func (b *Builder) Emit(ctx context.Context, p EmitParams) (ledger.NodeID, error) {
	if err := validateEmitParams(p); err != nil {
		return "", err
	}

	// Resolve content and size. Either we put fresh bytes, or we trust an
	// existing ref. We always Stat the ref afterwards to get authoritative
	// SizeBytes — the caller should not be trusted to compute size
	// because the on-disk representation might differ from the in-memory
	// length (e.g. after future compression hooks).
	contentRef := p.ContentRef
	if p.Content != nil {
		ref, err := b.store.Put(p.Content)
		if err != nil {
			return "", fmt.Errorf("artifact: store put: %w", err)
		}
		contentRef = ref
	}

	size, exists, err := b.store.Stat(contentRef)
	if err != nil {
		return "", fmt.Errorf("artifact: stat content: %w", err)
	}
	if !exists {
		return "", fmt.Errorf("artifact: content_ref %q does not exist in store", contentRef)
	}

	// Resolve content type if defaulted.
	contentType := p.ContentType
	if contentType == "" {
		contentType = defaultContentTypeForKind(p.Kind)
	}

	when := p.When
	if when.IsZero() {
		when = time.Now().UTC()
	}

	artifact := &nodes.Artifact{
		ArtifactKind:      p.Kind,
		Title:             p.Title,
		ContentRef:        contentRef,
		ContentType:       contentType,
		SizeBytes:         size,
		ScopeID:           p.ScopeID,
		StanceID:          p.StanceID,
		SupersedesRef:     p.SupersedesRef,
		AntigravitySource: p.AntigravitySource,
		When:              when,
		Version:           1,
	}
	if err := artifact.Validate(); err != nil {
		return "", fmt.Errorf("artifact: validate: %w", err)
	}

	body, err := json.Marshal(artifact)
	if err != nil {
		return "", fmt.Errorf("artifact: marshal: %w", err)
	}

	id, err := b.ledger.AddNode(ctx, ledger.Node{
		Type:          "artifact",
		SchemaVersion: 1,
		CreatedBy:     p.StanceID,
		MissionID:     p.MissionID,
		Content:       json.RawMessage(body),
	})
	if err != nil {
		return "", fmt.Errorf("artifact: ledger add: %w", err)
	}

	// If this artifact supersedes a predecessor, draw the edge so a
	// graph walker can find the version chain by traversal alone (no
	// content scan needed).
	if p.SupersedesRef != "" {
		if err := b.ledger.AddEdge(ctx, ledger.Edge{
			From: id,
			To:   p.SupersedesRef,
			Type: ledger.EdgeReferences,
			Metadata: map[string]string{
				"reason": "artifact_supersedes",
			},
		}); err != nil {
			return id, fmt.Errorf("artifact: add supersedes edge (artifact persisted): %w", err)
		}
	}
	return id, nil
}

// Amend is a convenience wrapper around Emit for the supersedes case.
// Given an annotation with Action="amend" and AmendmentRef set, this
// produces the new artifact and links it via SupersedesRef.
func (b *Builder) Amend(ctx context.Context, predecessorID string, p EmitParams) (ledger.NodeID, error) {
	if predecessorID == "" {
		return "", errors.New("artifact: Amend: predecessorID required")
	}
	p.SupersedesRef = predecessorID
	return b.Emit(ctx, p)
}

// EmitAnnotation creates an ArtifactAnnotation ledger node. Used by the
// CLI / web UI to record operator feedback on an artifact.
func (b *Builder) EmitAnnotation(ctx context.Context, missionID string, ann nodes.ArtifactAnnotation) (ledger.NodeID, error) {
	ann.Version = 1
	if ann.When.IsZero() {
		ann.When = time.Now().UTC()
	}
	if err := ann.Validate(); err != nil {
		return "", fmt.Errorf("artifact: annotation validate: %w", err)
	}
	body, err := json.Marshal(&ann)
	if err != nil {
		return "", fmt.Errorf("artifact: annotation marshal: %w", err)
	}
	id, err := b.ledger.AddNode(ctx, ledger.Node{
		Type:          "artifact_annotation",
		SchemaVersion: 1,
		CreatedBy:     ann.AnnotatorID,
		MissionID:     missionID,
		Content:       json.RawMessage(body),
	})
	if err != nil {
		return "", fmt.Errorf("artifact: annotation ledger add: %w", err)
	}
	// Edge from annotation to its target artifact: supports reverse-index
	// queries ("find all annotations for artifact X") via graph walk.
	if err := b.ledger.AddEdge(ctx, ledger.Edge{
		From: id,
		To:   ann.ArtifactRef,
		Type: ledger.EdgeReferences,
		Metadata: map[string]string{
			"reason": "annotation_target",
			"action": ann.Action,
		},
	}); err != nil {
		return id, fmt.Errorf("artifact: annotation edge (annotation persisted): %w", err)
	}
	return id, nil
}

// validateEmitParams is the pre-store sanity check. Catches obviously
// wrong inputs before we touch disk.
func validateEmitParams(p EmitParams) error {
	if p.Kind == "" {
		return errors.New("artifact: emit: kind is required")
	}
	if !nodes.ValidArtifactKinds[p.Kind] {
		return fmt.Errorf("artifact: emit: unknown kind %q", p.Kind)
	}
	if p.Title == "" {
		return errors.New("artifact: emit: title is required")
	}
	if p.MissionID == "" {
		return errors.New("artifact: emit: mission_id is required")
	}
	if p.StanceID == "" {
		return errors.New("artifact: emit: stance_id is required")
	}
	hasContent := p.Content != nil
	hasRef := p.ContentRef != ""
	if hasContent && hasRef {
		return errors.New("artifact: emit: pass either Content or ContentRef, not both")
	}
	if !hasContent && !hasRef {
		return errors.New("artifact: emit: pass either Content or ContentRef")
	}
	return nil
}

// defaultContentTypeForKind returns the canonical default content_type
// for a kind. Mirrors validContentTypes in nodes/artifact.go but picks a
// single default per kind.
func defaultContentTypeForKind(kind string) string {
	switch kind {
	case "plan":
		return "application/json"
	case "screenshot":
		return "image/png"
	case "recording":
		return "video/mp4"
	case "diff":
		return "text/x-diff"
	case "wiki":
		return "text/markdown"
	case "test_output":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}
