// verify.go — one-shot verify verb.
//
// Wires into internal/plan.VerificationDescent with a minimal
// synthetic session. CloudSwarm posts {artifact, acceptance_criteria[],
// context?} and gets back {result, tier, classification?, message?}.
//
// Integration shape:
//
//	artifact   — either a file path (if it exists on disk) OR raw
//	             text that we drop into a temp file for ContentMatch
//	             checks. context.artifact_path overrides detection.
//	acceptance_criteria — each criterion string is treated as a
//	             substring that must appear in the artifact. For
//	             callers that have real runnable ACs, pass them via
//	             context.criteria (advanced) — not wired yet.
//
// VerificationDescent is called with Provider=nil so the
// multi-analyst/LLM-backed tiers (T3/T4/T5/T6/T7) fall through to the
// deterministic stderr classifier. That keeps the one-shot cheap and
// deterministic: T2 runs the check, T3 classifies the failure, and if
// all criteria pass we report T2-pass; if any fail we report whatever
// tier produced the verdict (typically T3 for a classified failure).
//
// The descent engine operates on ONE AC at a time — we iterate the
// criteria and surface the worst-outcome AC as the verb's result.
package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/plan"
)

// verifyRequest is the {artifact, acceptance_criteria, context?}
// payload shape from spec §5.6.1.
type verifyRequest struct {
	Artifact           string                 `json:"artifact"`
	AcceptanceCriteria []string               `json:"acceptance_criteria"`
	Context            map[string]interface{} `json:"context,omitempty"`
}

// verifyResponse is the verb's output shape.
type verifyResponse struct {
	Verb           string `json:"verb"`
	Status         string `json:"status"`
	Result         string `json:"result,omitempty"`         // "pass" | "soft_pass" | "fail"
	Tier           string `json:"tier,omitempty"`           // "T1".."T8"
	Classification string `json:"classification,omitempty"` // "code_bug" | "ac_bug" | "environment" | ""
	Message        string `json:"message,omitempty"`
	Error          string `json:"error,omitempty"`
}

// handleVerify is invoked by Dispatch when verb=="verify".
func handleVerify(payload json.RawMessage) (Response, error) {
	req := verifyRequest{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return verifyScaffoldResponse(fmt.Sprintf("invalid request payload: %v", err)), nil
		}
	}
	// Legacy probe path: a nil / empty payload or a payload missing
	// the required fields falls back to the pre-wiring scaffold shape
	// ({result:"soft-pass", tier:"T1"}) so CloudSwarm's long-standing
	// supervisor probe keeps parsing.
	if strings.TrimSpace(req.Artifact) == "" || len(req.AcceptanceCriteria) == 0 {
		return verifyScaffoldResponse(
			"scaffold — verify called without required artifact/acceptance_criteria fields"), nil
	}

	// Materialize artifact → file on disk. If the artifact string
	// names an existing file, use it directly; otherwise stage its
	// contents under a temp dir so ContentMatch has something to
	// read. The temp dir is also the descent's RepoRoot.
	repoRoot, artifactPath, cleanup, err := stageArtifact(req.Artifact)
	if err != nil {
		return errorResponse("verify", fmt.Sprintf("stage artifact: %v", err))
	}
	defer cleanup()

	// Build synthetic ACs. Each criterion becomes a ContentMatch
	// check: the criterion text must appear as a substring of the
	// artifact. This is the minimal deterministic verification the
	// descent engine can act on without a live LLM / runnable test
	// infrastructure.
	relPath, err := filepath.Rel(repoRoot, artifactPath)
	if err != nil {
		relPath = artifactPath
	}
	acs := make([]plan.AcceptanceCriterion, 0, len(req.AcceptanceCriteria))
	for i, c := range req.AcceptanceCriteria {
		acs = append(acs, plan.AcceptanceCriterion{
			ID:          fmt.Sprintf("AC%d", i+1),
			Description: c,
			ContentMatch: &plan.ContentMatchCriterion{
				File:    relPath,
				Pattern: c,
			},
		})
	}

	session := plan.Session{
		ID:                 "S1",
		Title:              "one-shot verify",
		Tasks:              nil,
		AcceptanceCriteria: acs,
	}

	cfg := plan.DescentConfig{
		Provider: nil, // nil → deterministic-only descent
		RepoRoot: repoRoot,
		Session:  session,
	}

	// Run descent per AC and aggregate. Worst outcome wins —
	// DescentFail > DescentSoftPass > DescentPass. The tier + reason
	// reported are those of the worst AC.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var worst *plan.DescentResult
	for _, ac := range acs {
		// runACCommand is called INSIDE VerificationDescent at T2.
		// Supply an empty initialOutput so the descent re-runs it.
		r := plan.VerificationDescent(ctx, ac, "", cfg)
		if worst == nil || outcomeRank(r.Outcome) > outcomeRank(worst.Outcome) {
			rr := r
			worst = &rr
		}
	}

	// Should never be nil given len(acs) >= 1, but guard defensively.
	if worst == nil {
		return errorResponse("verify", "descent produced no result")
	}

	body := verifyResponse{
		Verb:           "verify",
		Status:         "ok",
		Result:         outcomeToResult(worst.Outcome),
		Tier:           tierToString(worst.ResolvedAtTier),
		Classification: worst.Category,
		Message:        worst.Reason,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("oneshot: marshal verify: %w", err)
	}
	return Response{Verb: "verify", Status: "ok", Data: data}, nil
}

// verifyScaffoldResponse returns the legacy pre-wiring scaffold shape
// ({result:"soft-pass", tier:"T1"}) so CloudSwarm's long-standing probe
// parser keeps working. Used when the caller posts a nil / empty / or
// field-incomplete payload, analogous to decomposeScaffoldResponse.
func verifyScaffoldResponse(note string) Response {
	body := struct {
		Result string `json:"result"`
		Tier   string `json:"tier"`
	}{
		Result: "soft-pass",
		Tier:   "T1",
	}
	data, _ := json.Marshal(body)
	return Response{
		Verb:   "verify",
		Status: StatusScaffold,
		Data:   data,
		Note:   note,
	}
}

// stageArtifact resolves the artifact payload. If the string names an
// existing readable file, use it in place and point RepoRoot at its
// directory. Otherwise write the content into a fresh temp dir.
//
// Returns (repoRoot, artifactPath, cleanupFn, err). The cleanup
// removes any temp dir we created; it's a no-op when we pointed at an
// existing file.
func stageArtifact(artifact string) (string, string, func(), error) {
	// Treat as path only if it looks like one: must exist as a file.
	// (An arbitrary text blob can contain slashes so we can't check
	// separators alone.)
	if info, err := os.Stat(artifact); err == nil && !info.IsDir() {
		abs, err := filepath.Abs(artifact)
		if err != nil {
			return "", "", func() {}, err
		}
		return filepath.Dir(abs), abs, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "stoke-oneshot-verify-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	artifactPath := filepath.Join(tmp, "artifact.txt")
	if err := os.WriteFile(artifactPath, []byte(artifact), 0o644); err != nil { // #nosec G306 -- verification artefact; user-readable.
		cleanup()
		return "", "", func() {}, err
	}
	return tmp, artifactPath, cleanup, nil
}

// outcomeRank maps descent outcomes to a total ordering used to pick
// the worst AC result. Higher = worse.
func outcomeRank(o plan.DescentOutcome) int {
	switch o {
	case plan.DescentPass:
		return 0
	case plan.DescentSoftPass:
		return 1
	case plan.DescentFail:
		return 2
	default:
		return 3
	}
}

// outcomeToResult maps the internal descent enum to the external
// string shape expected by the spec: "pass" | "soft_pass" | "fail".
func outcomeToResult(o plan.DescentOutcome) string {
	switch o {
	case plan.DescentPass:
		return "pass"
	case plan.DescentSoftPass:
		return "soft_pass"
	case plan.DescentFail:
		return "fail"
	default:
		return "fail"
	}
}

// tierToString surfaces the descent tier as a short label ("T1".."T8").
// Descent's own String() is "T1-intent-match" etc; the spec wants the
// bare tier id so we strip the suffix.
func tierToString(t plan.DescentTier) string {
	full := t.String()
	if i := strings.Index(full, "-"); i > 0 {
		return full[:i]
	}
	return full
}
