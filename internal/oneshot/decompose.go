// decompose.go — one-shot decompose verb.
//
// Thin wrapper around internal/plan.Decompose / DecomposeWithAspects.
// CloudSwarm supervisor posts {task, context?, max_depth?} as the
// payload; this file turns that into a plan.Task, runs the decomposer
// with a selectable strategy, and shapes the output as a minimal SOW-
// shaped plan so the supervisor sees the same structural contract as
// the real R1 SOW generator (id / sessions / tasks / acceptance).
//
// Strategy selection:
//
//	default                   — "aspect" via DecomposeWithAspects
//	context.strategy="basic"  — Decompose (numbered/bullets/conjunction
//	                            /semicolons auto-detect; no aspects)
//	context.strategy="aspect" — DecomposeWithAspects with tests+docs
//
// Legacy-compat: when the caller posts a nil / empty payload OR a
// too-short probe task with no structural markers, the handler
// returns the pre-wiring scaffold shape (Status="scaffold", plan
// with empty subTasks/dependencies/acceptanceCriteria). This keeps
// the CloudSwarm supervisor's existing probe path working while
// real payloads unlock the new {status:"ok", plan:<SOW>,
// strategy_used:<string>} contract.
package oneshot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/plan"
)

// decomposeRequest is the verb-specific payload shape.
type decomposeRequest struct {
	Task     string                 `json:"task"`
	Context  map[string]interface{} `json:"context,omitempty"`
	MaxDepth int                    `json:"max_depth,omitempty"`
}

// decomposeOKResponse is the {verb,status,plan,strategy_used} shape
// emitted when the decomposer produced a real plan (spec §5.6.1).
type decomposeOKResponse struct {
	Verb         string    `json:"verb"`
	Status       string    `json:"status"`
	Plan         *plan.SOW `json:"plan"`
	StrategyUsed string    `json:"strategy_used"`
}

// decomposeLegacyPlan mirrors the pre-wiring scaffold plan shape
// (subTasks / dependencies / acceptanceCriteria triad). Retained
// verbatim so the CloudSwarm supervisor's existing probe parser
// keeps working for legacy / insufficient inputs.
type decomposeLegacyPlan struct {
	SubTasks           []string `json:"subTasks"`
	Dependencies       []string `json:"dependencies"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
}

// handleDecompose is invoked by Dispatch when verb=="decompose".
// Returns a Response whose Data is a decomposeOKResponse on success
// or the legacy scaffold shape when the input is insufficient.
func handleDecompose(payload json.RawMessage) (Response, error) {
	req := decomposeRequest{}
	if len(payload) > 0 {
		// Malformed JSON input deliberately returns a legacy-shape
		// scaffold response (with err surfaced via Data) rather than a
		// Go-level error, so CloudSwarm sees a stable shape rather
		// than exit-code noise. The unmarshal error is intentionally
		// routed into the response body.
		unmarshalErr := json.Unmarshal(payload, &req)
		if unmarshalErr != nil {
			msg := "invalid request payload: " + unmarshalErr.Error()
			resp := decomposeScaffoldResponse(msg)
			return resp, nil
		}
	}

	task := strings.TrimSpace(req.Task)
	// Legacy / insufficient-input path — preserve the pre-wiring
	// scaffold response so long-standing CloudSwarm probes still
	// parse. A real-world decomposition needs at least a short
	// phrase; anything less is treated as a scaffold probe.
	if task == "" {
		return decomposeScaffoldResponse("scaffold — decompose called without task field"), nil
	}

	// Pick strategy: context.strategy overrides the default.
	strategy := "aspect"
	if req.Context != nil {
		if v, ok := req.Context["strategy"].(string); ok && v != "" {
			strategy = strings.ToLower(strings.TrimSpace(v))
		}
	}

	planTask := plan.Task{
		ID:          "oneshot-decompose",
		Description: req.Task,
	}

	var result plan.DecompositionResult
	switch strategy {
	case "basic":
		result = plan.Decompose(planTask)
	case "aspect":
		// Add test+docs aspects when the task is atomic, or attach
		// them to each sub-piece otherwise.
		result = plan.DecomposeWithAspects(planTask, true, true)
	default:
		return decomposeScaffoldResponse(fmt.Sprintf(
			"scaffold — unknown strategy %q (supported: basic, aspect)", strategy)), nil
	}

	// Legacy-probe detection: a 1-2 word task with no structural
	// markers routed through the "basic" strategy produces no
	// subtasks. Surface as scaffold rather than an error so the
	// CloudSwarm probe path keeps working with minimal inputs.
	if len(result.Subtasks) == 0 {
		return decomposeScaffoldResponse(fmt.Sprintf(
			"scaffold — decomposer found no structure (strategy=%s)", result.Strategy)), nil
	}

	// Shape the output as a minimal SOW: one session holding every
	// subtask. Keeps the contract aligned with the real R1 SOW output
	// so CloudSwarm doesn't have to branch on verb shape.
	sow := &plan.SOW{
		ID:          "oneshot-decompose-sow",
		Name:        truncate(req.Task, 80),
		Description: req.Task,
		Sessions: []plan.Session{
			{
				ID:                 "S1",
				Title:              "decomposed tasks",
				Tasks:              result.Subtasks,
				AcceptanceCriteria: buildAspectACs(result.Subtasks),
			},
		},
	}

	body := decomposeOKResponse{
		Verb:         "decompose",
		Status:       StatusOK,
		Plan:         sow,
		StrategyUsed: result.Strategy,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("oneshot: marshal decompose: %w", err)
	}
	return Response{Verb: "decompose", Status: StatusOK, Data: data}, nil
}

// decomposeScaffoldResponse emits the legacy scaffold shape that
// the supervisor's original probe parser expects. Stable across the
// scaffold→wired transition so integration tests on either side of
// the boundary keep parsing.
func decomposeScaffoldResponse(note string) Response {
	legacy := decomposeLegacyPlan{
		SubTasks:           []string{},
		Dependencies:       []string{},
		AcceptanceCriteria: []string{},
	}
	wrapped := map[string]any{"plan": legacy}
	data, _ := json.Marshal(wrapped)
	return Response{
		Verb:   "decompose",
		Status: StatusScaffold,
		Data:   data,
		Note:   note,
	}
}

// buildAspectACs synthesizes a minimal AC per subtask — description
// only, no command. Downstream consumers use this to confirm that
// every subtask has a gate of some kind. The real R1 SOW pipeline
// replaces these with runnable checks.
func buildAspectACs(tasks []plan.Task) []plan.AcceptanceCriterion {
	acs := make([]plan.AcceptanceCriterion, 0, len(tasks))
	for _, t := range tasks {
		acs = append(acs, plan.AcceptanceCriterion{
			ID:          "AC-" + t.ID,
			Description: "satisfy: " + t.Description,
		})
	}
	return acs
}

// truncate shortens s to at most n runes (plus an ellipsis) for use as
// the SOW.Name. Keeps the output compact when the task text is long.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
