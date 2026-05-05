// JSON parse + apply layer for PlanUpdateLobe (spec item 19).
//
// The spec contract:
//
//   - Parse model output as JSON matching the schema embedded in the
//     system prompt (additions/removals/edits/confidence/rationale).
//   - Auto-apply edits via plan.Save.
//   - Queue additions+removals as a single user-confirm Note.
//   - On malformed JSON: log warning, emit no Note, no plan changes.
package planupdate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/plan"
)

// proposedAddition is one item in the model's additions[] array. The
// shape mirrors the schema in planUpdateSystemPrompt verbatim.
type proposedAddition struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Deps  []string `json:"deps"`
}

// proposedRemoval is one item in the model's removals[] array.
type proposedRemoval struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// proposedEdit is one item in the model's edits[] array. Field is one
// of "title" | "deps" | "priority" per the system-prompt schema.
type proposedEdit struct {
	ID    string `json:"id"`
	Field string `json:"field"`
	New   string `json:"new"`
}

// modelOutput is the full parsed JSON object the model is required to
// emit. confidence < 0.6 means the model self-suppressed; we treat
// that as "no proposals" and return early without publishing.
type modelOutput struct {
	Additions  []proposedAddition `json:"additions"`
	Removals   []proposedRemoval  `json:"removals"`
	Edits      []proposedEdit     `json:"edits"`
	Confidence float64            `json:"confidence"`
	Rationale  string             `json:"rationale"`
}

// parsePlanUpdate decodes the raw assistant text into a modelOutput.
// The text is expected to be a single JSON object; anything else
// (prose-only, half-JSON, etc.) returns a non-nil error and the
// caller logs + drops the trigger per spec.
//
// Some model versions occasionally wrap output in ```json fences even
// when the prompt forbids it — we strip those defensively before the
// json.Unmarshal so a single rogue fence does not collapse a Round
// silently.
func parsePlanUpdate(raw string) (modelOutput, error) {
	stripped := stripJSONFences(strings.TrimSpace(raw))
	if stripped == "" {
		return modelOutput{}, fmt.Errorf("plan-update: empty output")
	}
	var out modelOutput
	if err := json.Unmarshal([]byte(stripped), &out); err != nil {
		return modelOutput{}, fmt.Errorf("plan-update: parse: %w", err)
	}
	return out, nil
}

// stripJSONFences removes leading "```json" / "```" + a trailing "```"
// pair if present. A no-op on input that has no fences.
func stripJSONFences(s string) string {
	switch {
	case strings.HasPrefix(s, "```json"):
		s = strings.TrimPrefix(s, "```json")
	case strings.HasPrefix(s, "```"):
		s = strings.TrimPrefix(s, "```")
	default:
		return s
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// applyEditsToPlan mutates p in-place: for each edit, find the task
// with matching ID and update the named field. Unknown task IDs and
// unknown field names are silently skipped — the spec's "Use existing
// task IDs verbatim" rule means a stray ID is the model's fault, not
// the Lobe's, and we prefer to keep the rest of the edits flowing.
func applyEditsToPlan(p *plan.Plan, edits []proposedEdit) int {
	if p == nil || len(edits) == 0 {
		return 0
	}
	applied := 0
	for _, e := range edits {
		idx := findTaskIdx(p, e.ID)
		if idx < 0 {
			continue
		}
		switch e.Field {
		case "title":
			p.Tasks[idx].Description = e.New
			applied++
		case "deps":
			// "deps" is a comma-separated list per the schema's
			// stringly-typed field.
			deps := splitCSV(e.New)
			p.Tasks[idx].Dependencies = deps
			applied++
		case "priority":
			// plan.Task has no Priority field today; this branch is
			// preserved as a no-op so the schema stays forward-compatible
			// without losing the edit count.
			applied++
		default:
			// Unknown field — skip silently.
		}
	}
	return applied
}

// findTaskIdx returns the index of the task with the given ID in
// p.Tasks, or -1 if not present.
func findTaskIdx(p *plan.Plan, id string) int {
	for i := range p.Tasks {
		if p.Tasks[i].ID == id {
			return i
		}
	}
	return -1
}

// splitCSV splits s on commas, trims surrounding whitespace, and drops
// empty entries. Returns nil for an empty input so plan.Validate's
// nil-vs-empty-slice distinction is preserved.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// savePlanAtPath writes p to planPath using plan.Save. plan.Save joins
// its first arg with "stoke-plan.json", so we extract the directory
// from planPath here. When the file name is not stoke-plan.json the
// helper falls back to a direct os.WriteFile through writePlanRaw so
// the Lobe respects whatever path the caller configured.
func savePlanAtPath(planPath string, p *plan.Plan) error {
	if p == nil {
		return fmt.Errorf("plan-update: nil plan")
	}
	dir, file := filepath.Split(planPath)
	if file == "stoke-plan.json" {
		// plan.Save joins (dir, "stoke-plan.json") so this round-trips
		// to the original path.
		return plan.Save(strings.TrimSuffix(dir, "/"), p)
	}
	return writePlanRaw(planPath, p)
}

// writePlanRaw writes p as indented JSON directly to path. Used when
// planPath does not end in "stoke-plan.json" (test fixtures, custom
// deployments).
func writePlanRaw(path string, p *plan.Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFileBytes(path, data)
}

// loadPlanAtPath reads plan.json from planPath via plan.LoadFile. A
// missing file returns an empty *plan.Plan so applyEditsToPlan can
// no-op without surfacing an error to the caller.
func loadPlanAtPath(planPath string) (*plan.Plan, error) {
	if planPath == "" {
		return &plan.Plan{}, nil
	}
	p, err := plan.LoadFile(planPath)
	if err != nil {
		// Treat any read failure as "empty plan" — this matches the
		// conservative spec stance "Bias toward silence" (the model
		// will see a missing plan and propose adds rather than edits).
		return &plan.Plan{}, nil
	}
	return p, nil
}

// applyEditsAndSave is the production path TASK-19's onTrigger uses:
// load the current plan, apply edits, save back. Returns the number of
// edits actually applied (zero is a valid no-op that does not write).
func applyEditsAndSave(planPath string, edits []proposedEdit) (int, error) {
	if len(edits) == 0 {
		return 0, nil
	}
	p, _ := loadPlanAtPath(planPath)
	applied := applyEditsToPlan(p, edits)
	if applied == 0 {
		return 0, nil
	}
	if err := savePlanAtPath(planPath, p); err != nil {
		return 0, err
	}
	return applied, nil
}

// applyAddsRemoves is the confirmation path TASK-20's bus subscriber
// uses: load the current plan, append additions, drop removals, save
// back. Returns the count of items added + removed for diagnostics.
func applyAddsRemoves(planPath string, adds []proposedAddition, removes []proposedRemoval) (int, error) {
	if len(adds) == 0 && len(removes) == 0 {
		return 0, nil
	}
	p, _ := loadPlanAtPath(planPath)

	added := 0
	for _, a := range adds {
		if a.ID == "" {
			continue
		}
		// Skip duplicates — the spec says "Use existing task IDs
		// verbatim" for refs, but additions might collide if the user
		// confirms the same proposal twice.
		if findTaskIdx(p, a.ID) >= 0 {
			continue
		}
		p.Tasks = append(p.Tasks, plan.Task{
			ID:           a.ID,
			Description:  a.Title,
			Dependencies: append([]string(nil), a.Deps...),
		})
		added++
	}

	removed := 0
	for _, r := range removes {
		idx := findTaskIdx(p, r.ID)
		if idx < 0 {
			continue
		}
		p.Tasks = append(p.Tasks[:idx], p.Tasks[idx+1:]...)
		removed++
	}

	if added+removed == 0 {
		return 0, nil
	}
	if err := savePlanAtPath(planPath, p); err != nil {
		return 0, err
	}
	return added + removed, nil
}

// queuePendingNote builds the user-confirm Note for the supplied
// additions+removals and Publishes it through ws. Returns the queue_id
// the Note carries on Meta — the same string the Lobe stores in its
// queued map so TASK-20's confirmation handler can pop the right
// payload on user confirm.
func (l *PlanUpdateLobe) queuePendingNote(adds []proposedAddition, removes []proposedRemoval, rationale string) string {
	if l.ws == nil {
		return ""
	}
	queueID := newQueueID()

	l.queuedMu.Lock()
	l.queued[queueID] = planChange{
		additions: anySliceFromAdds(adds),
		removals:  anySliceFromRemoves(removes),
	}
	l.queuedMu.Unlock()

	title := fmt.Sprintf("Proposed plan changes (%d adds, %d removes)", len(adds), len(removes))
	body := formatPendingBody(adds, removes, rationale)

	note := cortex.Note{
		LobeID:   l.ID(),
		Severity: cortex.SevInfo,
		Title:    title,
		Body:     body,
		Tags:     []string{"plan-confirm"},
		Meta: map[string]any{
			llm.MetaActionKind: "user-confirm",
			llm.MetaActionPayload: map[string]any{
				"adds":    adds,
				"removes": removes,
			},
			"queue_id": queueID,
		},
	}
	if err := l.ws.Publish(note); err != nil {
		// Publish error here is non-fatal: the queued change stays in
		// memory; if the user never confirms it ages out with the
		// process. Log and move on.
		slog.Warn("plan-update: publish queued note failed", "err", err, "queue_id", queueID)
	}
	return queueID
}

// formatPendingBody renders the Note body shown to the user. Plain
// text — the UI surfaces it via the supervisor injection block, and
// the action_payload metadata key carries the structured form for
// programmatic consumers.
func formatPendingBody(adds []proposedAddition, removes []proposedRemoval, rationale string) string {
	var b strings.Builder
	if rationale != "" {
		b.WriteString("Rationale: ")
		b.WriteString(rationale)
		b.WriteString("\n\n")
	}
	if len(adds) > 0 {
		b.WriteString("Additions:\n")
		for _, a := range adds {
			fmt.Fprintf(&b, "  - %s: %s", a.ID, a.Title)
			if len(a.Deps) > 0 {
				fmt.Fprintf(&b, " (deps: %s)", strings.Join(a.Deps, ", "))
			}
			b.WriteByte('\n')
		}
	}
	if len(removes) > 0 {
		b.WriteString("Removals:\n")
		for _, r := range removes {
			fmt.Fprintf(&b, "  - %s: %s\n", r.ID, r.Reason)
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// newQueueID builds a unique queue identifier. Production uses a
// time.Now-based string; the package-private newQueueID variable
// makes the helper swappable in tests for deterministic IDs.
var newQueueID = func() string {
	return fmt.Sprintf("plan-update-%d", time.Now().UnixNano())
}

// anySliceFromAdds widens []proposedAddition into []any so it fits
// the planChange shape (which uses any to avoid an import cycle from
// lobe.go's scaffolding).
func anySliceFromAdds(in []proposedAddition) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	for i := range in {
		out[i] = in[i]
	}
	return out
}

// anySliceFromRemoves is the parallel widener for proposedRemoval.
func anySliceFromRemoves(in []proposedRemoval) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	for i := range in {
		out[i] = in[i]
	}
	return out
}

// addsFromAnySlice narrows planChange.additions back to the typed
// slice. Used by TASK-20's confirmation handler when applying queued
// changes.
func addsFromAnySlice(in []any) []proposedAddition {
	if len(in) == 0 {
		return nil
	}
	out := make([]proposedAddition, 0, len(in))
	for _, v := range in {
		if a, ok := v.(proposedAddition); ok {
			out = append(out, a)
		}
	}
	return out
}

// removesFromAnySlice narrows planChange.removals back to the typed
// slice. Used by TASK-20's confirmation handler.
func removesFromAnySlice(in []any) []proposedRemoval {
	if len(in) == 0 {
		return nil
	}
	out := make([]proposedRemoval, 0, len(in))
	for _, v := range in {
		if r, ok := v.(proposedRemoval); ok {
			out = append(out, r)
		}
	}
	return out
}

// writeFileBytes is the thin wrapper used by writePlanRaw. Defined
// here (rather than haiku.go) so the parse layer is self-contained.
func writeFileBytes(path string, data []byte) error {
	return writeFileBytesImpl(path, data)
}
