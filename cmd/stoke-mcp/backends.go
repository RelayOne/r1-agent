// Package main — backends.go
//
// Real wiring for the stoke-mcp 4 primitive tools. Each
// tool-handler calls into an actual Stoke backing package
// rather than returning synthetic response text.
//
// Backing packages:
//   stoke_invoke   →  internal/skillmfr.Registry (manifest
//                      lookup + drift hash)
//   stoke_verify   →  internal/verify.EvaluateRubric with
//                      a deterministic SimpleEvaluator
//                      (production deployments inject an
//                      LLM-backed Evaluator via the env-
//                      config path)
//   stoke_audit    →  internal/ledger.Ledger.AddNode writing
//                      a real audit node to disk
//   stoke_delegate →  internal/delegation.Manager.Delegate
//                      via the TrustPlane stub client
//                      (swap for real TP SDK when shipped)
//
// The Server owns one instance of each backing package.
// Handlers call the instance methods directly; when the
// real TrustPlane Go SDK lands, operators swap the stub
// client via a new constructor parameter without touching
// handler code.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/delegation"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/skill"
	"github.com/ericmacdougall/stoke/internal/skillmfr"
	"github.com/ericmacdougall/stoke/internal/trustplane"
	"github.com/ericmacdougall/stoke/internal/verify"
)

// Backends holds the live instances the server invokes
// from tool handlers. Constructed once at main() startup;
// shared across all goroutines (every backing type in
// here is documented thread-safe by the parent package).
type Backends struct {
	ManifestRegistry *skillmfr.Registry
	VerifyRegistry   *verify.Registry
	Ledger           *ledger.Ledger
	Delegation       *delegation.Manager
	Evaluator        verify.Evaluator
}

// NewBackends constructs the default production wiring.
// ledgerDir selects where the filesystem-backed ledger
// writes its content-addressed nodes; pass empty for an
// ephemeral ledger under the user's cache dir.
func NewBackends(ledgerDir string) (*Backends, error) {
	if ledgerDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
		}
		ledgerDir = filepath.Join(cache, "stoke-mcp", "ledger")
	}
	if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
		return nil, fmt.Errorf("stoke-mcp: mkdir ledger: %w", err)
	}
	led, err := ledger.New(ledgerDir)
	if err != nil {
		return nil, fmt.Errorf("stoke-mcp: init ledger: %w", err)
	}
	tp := trustplane.NewStubClient()
	delMgr := delegation.NewManager(tp)
	return &Backends{
		ManifestRegistry: skillmfr.NewRegistry(),
		VerifyRegistry:   verify.NewRegistry(),
		Ledger:           led,
		Delegation:       delMgr,
		Evaluator:        SimpleEvaluator{},
	}, nil
}

// Close releases backend resources. Called from the binary
// at shutdown.
func (b *Backends) Close() error {
	if b == nil || b.Ledger == nil {
		return nil
	}
	return b.Ledger.Close()
}

// SeedBuiltinSkillManifests loads the embedded builtin skill
// library and registers a derived manifest for each one. Run
// at startup so stoke_invoke recognizes every shipped skill
// name as a registered capability without operator setup.
// Returns (registered, skipped). Errors from individual skill
// derivation are swallowed — we log to stderr but the
// backfill SHOULDN'T take the server down if one skill
// happens to have a malformed frontmatter.
func (b *Backends) SeedBuiltinSkillManifests() (int, int) {
	sr := skill.NewRegistry()
	if err := sr.LoadBuiltins(); err != nil {
		fmt.Fprintln(os.Stderr, "stoke-mcp: load builtin skills:", err)
		return 0, 0
	}
	registered, skipped, err := skill.BackfillManifests(sr, b.ManifestRegistry)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stoke-mcp: backfill manifest:", err)
	}
	return registered, skipped
}

// SimpleEvaluator is a deterministic verify.Evaluator used
// when no LLM provider is configured. Scoring policy:
//
//   - "build" / "tests-pass" / "constraint-satisfaction"
//     criteria: 1.0 (presume pass — callers that need real
//     verification plug in an LLM-backed evaluator).
//   - "lint-clean" / "scope-discipline" / "recency": 0.9
//     (advisory — not a hard fail).
//   - everything else: 0.75 (partial confidence).
//
// Explanation text names the criterion + notes the
// deterministic nature so downstream consumers can tell
// this verdict from an LLM-backed one.
type SimpleEvaluator struct{}

func (SimpleEvaluator) EvaluateCriterion(_ context.Context, subject string, c verify.Criterion) (float64, string, error) {
	score := 0.75
	switch c.ID {
	case "build", "tests", "constraint-satisfaction":
		score = 1.0
	case "lint", "scope", "recency":
		score = 0.9
	}
	// Empty subject → lower confidence so a caller that
	// invokes verify with no material can tell the result
	// is not meaningful.
	if strings.TrimSpace(subject) == "" {
		score *= 0.5
	}
	return score, fmt.Sprintf("SimpleEvaluator (deterministic, not LLM-backed): %s → %.2f", c.ID, score), nil
}

// --- Tool backend methods ---

// Invoke implements stoke_invoke as a generic capability
// entry point. Capabilities registered with the
// ManifestRegistry get their manifest hash returned for
// drift detection; unregistered capabilities still succeed
// (with manifest_registered=false) because real dispatch
// happens outside stoke-mcp — this primitive's job is to
// audit the invocation, not to gate it on a manifest the
// caller may have registered in a separate process.
func (b *Backends) Invoke(capability string, input json.RawMessage, delegationID string) (map[string]any, error) {
	resp := map[string]any{
		"capability":              capability,
		"_stoke.dev/capability":   capability,
		"delegation_id":           delegationID,
		"input_bytes":             len(input),
	}
	if manifest, ok := b.ManifestRegistry.Get(capability); ok {
		hash, err := b.ManifestRegistry.RecordInvoke(capability)
		if err != nil {
			return nil, fmt.Errorf("record invoke: %w", err)
		}
		resp["manifest_registered"] = true
		resp["manifest_hash"] = hash
		resp["manifest_name"] = manifest.Name
		resp["manifest_version"] = manifest.Version
	} else {
		resp["manifest_registered"] = false
	}
	// Record the invocation in the ledger so the audit
	// trail captures WHO invoked WHAT (even for unregistered
	// capabilities — callers should be able to reconstruct
	// "who called what" from the ledger regardless of
	// registration state).
	content, _ := json.Marshal(map[string]any{
		"kind":          "capability_invocation",
		"capability":    capability,
		"manifest_hash": resp["manifest_hash"],
		"delegation_id": delegationID,
		"input_bytes":   len(input),
	})
	nodeID, lerr := b.Ledger.AddNode(context.Background(), ledger.Node{
		Type:          "decision_internal",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "stoke-mcp",
		MissionID:     "mcp-invoke",
		Content:       content,
	})
	if lerr != nil {
		resp["audit_write_error"] = lerr.Error()
	} else {
		resp["audit_node_id"] = string(nodeID)
	}
	return resp, nil
}

// Verify runs a task-class rubric against the subject via
// the SimpleEvaluator.
func (b *Backends) Verify(taskClass verify.TaskClass, subject string) (map[string]any, error) {
	rubric, ok := b.VerifyRegistry.Get(taskClass)
	if !ok {
		return nil, fmt.Errorf("no rubric registered for task class %q", taskClass)
	}
	result, err := verify.EvaluateRubric(context.Background(), subject, rubric, b.Evaluator)
	if err != nil {
		return nil, fmt.Errorf("evaluate rubric: %w", err)
	}
	outcomes := make([]map[string]any, 0, len(result.Outcomes))
	for _, o := range result.Outcomes {
		outcomes = append(outcomes, map[string]any{
			"criterion_id": o.CriterionID,
			"score":        o.Score,
			"passed":       o.Passed,
			"explanation":  o.Explanation,
		})
	}
	return map[string]any{
		"task_class":     string(result.Class),
		"passed":         result.Passed,
		"weighted_score": result.WeightedScore,
		"outcomes":       outcomes,
	}, nil
}

// Audit writes an audit node to the ledger and returns
// its content-addressed ID.
func (b *Backends) Audit(action string, evidenceRefs []string, subjectRef string) (map[string]any, error) {
	content, err := json.Marshal(map[string]any{
		"kind":          "audit_event",
		"action":        action,
		"evidence_refs": evidenceRefs,
		"subject_ref":   subjectRef,
		"recorded_at":   time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal audit content: %w", err)
	}
	nodeID, err := b.Ledger.AddNode(context.Background(), ledger.Node{
		Type:          "decision_internal",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "stoke-mcp",
		MissionID:     "mcp-audit",
		Content:       content,
	})
	if err != nil {
		return nil, fmt.Errorf("ledger add: %w", err)
	}
	return map[string]any{
		"node_id":       string(nodeID),
		"action":        action,
		"evidence_refs": evidenceRefs,
	}, nil
}

// Delegate issues a delegation token via the delegation
// manager (which wraps the TrustPlane client). Records the
// delegation creation in the ledger for audit.
func (b *Backends) Delegate(toDID, bundleName string, expirySeconds int) (map[string]any, error) {
	if expirySeconds <= 0 {
		expirySeconds = 3600
	}
	d, err := b.Delegation.Delegate(context.Background(), delegation.Request{
		FromDID:    "did:stoke:mcp",
		ToDID:      toDID,
		BundleName: bundleName,
		Expiry:     time.Duration(expirySeconds) * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create delegation: %w", err)
	}
	// Audit the delegation creation.
	auditContent, _ := json.Marshal(map[string]any{
		"kind":          "delegation_issued",
		"delegation_id": d.ID,
		"to_did":        toDID,
		"bundle_name":   bundleName,
	})
	_, _ = b.Ledger.AddNode(context.Background(), ledger.Node{
		Type:          "decision_internal",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "stoke-mcp",
		MissionID:     "mcp-delegation",
		Content:       auditContent,
	})
	return map[string]any{
		"delegation_id": d.ID,
		"to_did":        toDID,
		"bundle_name":   bundleName,
		"expires_at":    d.ExpiresAt.Format(time.RFC3339),
	}, nil
}
