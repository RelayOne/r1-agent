// Tether — any-agent wrapped with R1's anti-truncation middleware.
//
// TetherDispatcher takes ANY other Dispatcher (inner) and post-processes
// its Trace through R1's antitrunc primitives. If the inner agent
// claims completion but the gate flags truncation phrases or unchecked
// plan items, Tether overrides:
//
//   - CompletionAttempted stays true (the inner agent did try)
//   - ExitReason becomes ExitReasonTetherRefused
//   - LastAssistantText is annotated with the gate findings so the
//     verdict scorer (and the leaderboard) can show WHY the gate
//     refused.
//
// This is the differentiator-transfers measurement: even agents that
// ship no completion gate get one when wrapped by R1.
//
// Spec: specs/truthful-completion-benchmark.md §T4.10 (items 37-40).
package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/bench"
)

// TetherDispatcher wraps Inner and gates its completion via antitrunc.
type TetherDispatcher struct {
	Inner Dispatcher
}

func (d *TetherDispatcher) Agent() Agent {
	if d.Inner == nil {
		return Agent{ID: "tether(unwired)", DisplayName: "Tether (no inner)", Version: "dev"}
	}
	inner := d.Inner.Agent()
	return Agent{
		ID:          "tether+" + inner.ID,
		DisplayName: "Tether(" + inner.DisplayName + ")",
		Version:     "dev",
	}
}

// tetherDecision is the outcome of running the antitrunc primitives
// over an inner trace + the mission plan. Exposed for unit tests.
type tetherDecision struct {
	Refused    bool
	Reasons    []string
}

// evaluateTether is the pure function: given the inner trace and the
// mission plan, decide whether the antitrunc gate refuses completion.
// Detection signals:
//   - Truncation phrases in LastAssistantText (catalog from phrases.go)
//   - Plan items not touched by the diff/text (cheap heuristic)
func evaluateTether(trace Trace, plan []bench.PlanItem) tetherDecision {
	var reasons []string

	// 1. Phrase scan over the inner agent's final assistant text.
	for _, m := range antitrunc.MatchTruncation(trace.LastAssistantText) {
		reasons = append(reasons, fmt.Sprintf("truncation phrase %q matched: %q", m.PhraseID, truncSnippet(m.Snippet)))
	}

	// 2. Plan-coverage scan: build a synthetic checklist where each
	//    PlanItem is checked iff the diff or text mentions a required
	//    symbol or expected changed file. Then ask antitrunc.CountChecklist
	//    how many remain unchecked.
	var cb strings.Builder
	for _, item := range plan {
		marker := "[ ]"
		if planItemTouched(item, trace) {
			marker = "[x]"
		}
		fmt.Fprintf(&cb, "- %s %s: %s\n", marker, item.ID, item.Description)
	}
	done, total := antitrunc.CountChecklist(cb.String())
	if total > 0 && done < total {
		reasons = append(reasons, fmt.Sprintf("%d/%d plan items unchecked", total-done, total))
	}

	return tetherDecision{Refused: len(reasons) > 0, Reasons: reasons}
}

// planItemTouched returns true iff the inner trace's diff or last
// assistant text mentions the item's required symbols / changed files.
// Cheap heuristic — verdict.go does the rigorous post-run check.
func planItemTouched(item bench.PlanItem, trace Trace) bool {
	for _, sym := range item.RequiredSymbols {
		if sym == "" {
			continue
		}
		if strings.Contains(trace.UnifiedDiff, sym) || strings.Contains(trace.LastAssistantText, sym) {
			return true
		}
	}
	for _, f := range item.ChangedFiles {
		if f == "" {
			continue
		}
		if strings.Contains(trace.UnifiedDiff, f) {
			return true
		}
	}
	return false
}

func truncSnippet(s string) string {
	const max = 80
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func (d *TetherDispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if d.Inner == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("TetherDispatcher.Run: Inner dispatcher is nil")
	}
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, errors.New("TetherDispatcher.Run: mission is nil")
	}
	trace, err := d.Inner.Run(ctx, mission, workDir, timeout)
	if err != nil {
		return trace, err
	}
	// Only gate if the inner agent claimed completion. Silent
	// failures stay silent — Tether polices claims, not absences.
	if !trace.CompletionAttempted {
		return trace, nil
	}
	decision := evaluateTether(trace, mission.Plan)
	if !decision.Refused {
		return trace, nil
	}
	var note strings.Builder
	note.WriteString("\n\n[tether] anti-truncation gate refused completion:\n")
	for _, r := range decision.Reasons {
		fmt.Fprintf(&note, "- %s\n", r)
	}
	trace.LastAssistantText = trace.LastAssistantText + note.String()
	trace.ExitReason = ExitReasonTetherRefused
	return trace, nil
}

func init() {
	// Wire canonical tether combos. Go's init order within a package
	// is file-name alphabetical, so by the time tether.go init() runs
	// the inner dispatchers (aider.go, cline.go, codex.go, cursor.go,
	// claude_code.go) have already registered themselves in Registry.
	for _, innerID := range []string{"aider", "cline", "cursor", "codex-cli", "claude-code-default"} {
		inner := Lookup(innerID)
		if inner == nil {
			continue // dispatcher unavailable in this build
		}
		RegisterDispatcher("tether+"+innerID, &TetherDispatcher{Inner: inner})
	}
}

