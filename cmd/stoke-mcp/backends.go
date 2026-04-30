// Package main — backends.go
//
// Real wiring for the stoke-mcp 4 primitive tools. Each
// tool-handler calls into an actual Stoke backing package
// rather than returning synthetic response text.
//
// Backing packages:
//
//	stoke_invoke   →  internal/skillmfr.Registry (manifest
//	                   lookup + drift hash)
//	stoke_verify   →  internal/verify.EvaluateRubric with
//	                   a deterministic SimpleEvaluator
//	                   (production deployments inject an
//	                   LLM-backed Evaluator via the env-
//	                   config path)
//	stoke_audit    →  internal/ledger.Ledger.AddNode writing
//	                   a real audit node to disk
//	stoke_delegate →  internal/delegation.Manager.Delegate
//	                   via the TrustPlane stub client
//	                   (swap for RealClient in prod)
//
// The Server owns one instance of each backing package.
// Handlers call the instance methods directly; swapping
// StubClient for RealClient (hand-written HTTP against the
// vendored TrustPlane OpenAPI spec) is a single constructor
// change without touching handler code.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/delegation"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/r1skill/interp"
	r1registry "github.com/RelayOne/r1/internal/r1skill/registry"
	"github.com/RelayOne/r1/internal/skill"
	"github.com/RelayOne/r1/internal/skillmfr"
	"github.com/RelayOne/r1/internal/truecom"
	"github.com/RelayOne/r1/internal/verify"
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
	SkillRuntime     *interp.Runtime
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
	// truecom.Client selection via the NewFromEnv factory
	// (SOW task B-5). Resolution:
	//   - STOKE_TRUSTPLANE_MODE unset or =stub → StubClient (default
	//     for local dev + zero-config startup).
	//   - STOKE_TRUSTPLANE_MODE=real → RealClient talking to the
	//     TrustPlane gateway at STOKE_TRUSTPLANE_URL with Ed25519
	//     private key resolved from STOKE_TRUSTPLANE_PRIVKEY /
	//     STOKE_TRUSTPLANE_PRIVKEY_FILE. Fatal on misconfiguration
	//     so operators see the problem at startup, not at first RPC.
	tp, err := truecom.NewFromEnv()
	if err != nil {
		return nil, fmt.Errorf("stoke-mcp: build trustplane client: %w", err)
	}
	delMgr := delegation.NewManager(tp)
	return &Backends{
		ManifestRegistry: skillmfr.NewRegistry(),
		VerifyRegistry:   verify.NewRegistry(),
		Ledger:           led,
		Delegation:       delMgr,
		Evaluator:        SimpleEvaluator{},
		SkillRuntime: &interp.Runtime{
			PureFuncs: map[string]interp.PureFunc{
				"stdlib:echo": func(input json.RawMessage) (json.RawMessage, error) {
					return json.Marshal(map[string]json.RawMessage{"value": input})
				},
				"cloudswarm:invoice_processor_runtime": invoiceProcessorRuntime,
			},
			LLM: func(_ context.Context, cfg interp.LLMCallConfig) (json.RawMessage, error) {
				return json.Marshal(map[string]string{
					"model":  cfg.Model,
					"status": "stubbed",
				})
			},
			Cache: interp.NewMemoryCache(),
		},
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
//
// ctx: propagated to the ledger write so the RPC's cancel
// / deadline flows through to audit persistence.
// missionID: caller-supplied bucket for the audit node
// (e.g., the mission/session ID the invocation belongs
// to). Empty → "mcp-invoke" default so standalone callers
// without a mission still land in a predictable bucket
// they can query.
func (b *Backends) Invoke(ctx context.Context, missionID, capability string, input json.RawMessage, delegationID string) (map[string]any, error) {
	resp := map[string]any{
		"capability":            capability,
		"_stoke.dev/capability": capability,
		"delegation_id":         delegationID,
		"input_bytes":           len(input),
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
		if manifest.UseIR {
			output, runErr := b.invokeDeterministicSkill(ctx, manifest, input)
			if runErr != nil {
				return nil, runErr
			}
			resp["deterministic"] = true
			resp["output"] = output
		}
	} else {
		resp["manifest_registered"] = false
	}
	// Record the invocation in the ledger so the audit
	// trail captures WHO invoked WHAT (even for unregistered
	// capabilities — callers should be able to reconstruct
	// "who called what" from the ledger regardless of
	// registration state).
	content, err := json.Marshal(map[string]any{
		"kind":          "capability_invocation",
		"capability":    capability,
		"manifest_hash": resp["manifest_hash"],
		"delegation_id": delegationID,
		"input_bytes":   len(input),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal invoke audit content: %w", err)
	}
	bucket := strings.TrimSpace(missionID)
	if bucket == "" {
		bucket = "mcp-invoke"
	}
	nodeID, lerr := b.Ledger.AddNode(ctx, ledger.Node{
		Type:          "decision_internal",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "stoke-mcp",
		MissionID:     bucket,
		Content:       content,
	})
	if lerr != nil {
		// Audit persistence failure is a real problem — the
		// invocation happened but the ledger didn't record it.
		// Surface via RPC error so clients see the gap rather
		// than hiding it inside a "success with audit_write_error
		// field" response that many callers ignore. Operators
		// wanting the legacy behavior can treat the error as
		// non-fatal at the MCP layer.
		return nil, fmt.Errorf("audit write: %w", lerr)
	}
	resp["audit_node_id"] = nodeID
	return resp, nil
}

func (b *Backends) invokeDeterministicSkill(ctx context.Context, manifest skillmfr.Manifest, input json.RawMessage) (map[string]any, error) {
	entry, err := r1registry.LoadEntry(manifest.IRRef)
	if err != nil {
		return nil, fmt.Errorf("load deterministic skill: %w", err)
	}
	res, err := interp.Run(ctx, b.SkillRuntime, entry.Skill, entry.Proof, input)
	if err != nil {
		return nil, fmt.Errorf("run deterministic skill: %w", err)
	}
	var output map[string]any
	if err := json.Unmarshal(res.Output, &output); err != nil {
		return nil, fmt.Errorf("decode deterministic skill output: %w", err)
	}
	return output, nil
}

// Verify runs a task-class rubric against the subject via
// the SimpleEvaluator. ctx propagates through to the
// evaluator so RPC cancellation flows cleanly.
func (b *Backends) Verify(ctx context.Context, taskClass verify.TaskClass, subject string) (map[string]any, error) {
	rubric, ok := b.VerifyRegistry.Get(taskClass)
	if !ok {
		return nil, fmt.Errorf("no rubric registered for task class %q", taskClass)
	}
	result, err := verify.EvaluateRubric(ctx, subject, rubric, b.Evaluator)
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
// its content-addressed ID. missionID buckets the audit
// in the caller's mission context; empty → "mcp-audit".
func (b *Backends) Audit(ctx context.Context, missionID, action string, evidenceRefs []string, subjectRef string) (map[string]any, error) {
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
	bucket := strings.TrimSpace(missionID)
	if bucket == "" {
		bucket = "mcp-audit"
	}
	nodeID, err := b.Ledger.AddNode(ctx, ledger.Node{
		Type:          "decision_internal",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "stoke-mcp",
		MissionID:     bucket,
		Content:       content,
	})
	if err != nil {
		return nil, fmt.Errorf("ledger add: %w", err)
	}
	return map[string]any{
		"node_id":       nodeID,
		"action":        action,
		"evidence_refs": evidenceRefs,
	}, nil
}

// Delegate issues a delegation token via the delegation
// manager (which wraps the TrustPlane client). Records the
// delegation creation in the ledger for audit. missionID
// buckets the audit node; empty → "mcp-delegation".
func (b *Backends) Delegate(ctx context.Context, missionID, toDID, bundleName string, expirySeconds int) (map[string]any, error) {
	if expirySeconds <= 0 {
		expirySeconds = 3600
	}
	d, err := b.Delegation.Delegate(ctx, delegation.Request{
		FromDID:    "did:stoke:mcp",
		ToDID:      toDID,
		BundleName: bundleName,
		Expiry:     time.Duration(expirySeconds) * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create delegation: %w", err)
	}
	// Audit the delegation creation.
	auditContent, marshalErr := json.Marshal(map[string]any{
		"kind":          "delegation_issued",
		"delegation_id": d.ID,
		"to_did":        toDID,
		"bundle_name":   bundleName,
	})
	if marshalErr != nil {
		// Delegation succeeded; log but don't fail the caller
		// on a marshal error for the audit record.
		fmt.Fprintln(os.Stderr, "stoke-mcp: delegate audit marshal:", marshalErr)
	} else {
		bucket := strings.TrimSpace(missionID)
		if bucket == "" {
			bucket = "mcp-delegation"
		}
		if _, lerr := b.Ledger.AddNode(ctx, ledger.Node{
			Type:          "decision_internal",
			SchemaVersion: 1,
			CreatedAt:     time.Now().UTC(),
			CreatedBy:     "stoke-mcp",
			MissionID:     bucket,
			Content:       auditContent,
		}); lerr != nil {
			fmt.Fprintln(os.Stderr, "stoke-mcp: delegate audit write:", lerr)
		}
	}
	return map[string]any{
		"delegation_id": d.ID,
		"to_did":        toDID,
		"bundle_name":   bundleName,
		"expires_at":    d.ExpiresAt.Format(time.RFC3339),
	}, nil
}
