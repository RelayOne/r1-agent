// Package plan's meta_reasoner.go implements a RUN-LEVEL learning pass
// that fires once after a SOW run completes.
//
// Why this exists:
//
// Stoke already reflects per-session through sow_reason.go (the multi-
// analyst + judge pass that fires when a single criterion gets stuck).
// That loop answers "why is THIS AC still failing?" at the narrowest
// scope, and it's good at it.
//
// The gap that motivates this file: nothing looks at the WHOLE run at
// the end and asks "across all the pain we hit this run, what classes
// of root cause actually showed up, and how often?" Operators can see
// that answer by reading the SOW log, but (a) it isn't structured and
// (b) it doesn't feed forward. The next run starts blind to what
// already bit us on this repo yesterday.
//
// The meta-reasoner closes that loop. After the SOW finishes, it takes
// the deterministic fact-set the runner already produced (AC results,
// repair loop attempts, telemetry counters) and asks an LLM ONE thing:
// "classify these failures into known root-cause categories and emit
// one machine-actionable prevention rule per category." The output
// lands in .stoke/meta-reports/<run-id>.json and prior reports are
// injected into the next run's lead-dev briefing so the orchestration
// layer preempts failure classes that already burned us.
//
// Division of labour:
//   - Facts are gathered deterministically by the caller (integer
//     counters, AC pass/fail, elapsed time). The LLM never counts.
//   - The LLM classifies — given these facts, what PATTERN do they
//     fit? It can propose new classes, but those go to a separate
//     NewClassCandidates field and require operator promotion before
//     they affect future-run orchestration.
//   - Prevention rules are stored as text, but the prompt instructs
//     the LLM to start each rule with a machine-actionable verb
//     (run, add, skip, check) so downstream consumers can keyword-
//     match without re-parsing prose.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// RootCauseClass is one observed pattern of failure within a single
// SOW run. The meta-reasoner emits one of these per class it finds.
type RootCauseClass struct {
	// Name is the short canonical identifier (snake_case). When the
	// class matches a seeded category (e.g. "missing_devdep") this
	// field reuses that exact name; when the LLM proposes a new
	// class, the name is its own coinage and the entry lands in
	// MetaReport.NewClassCandidates instead of MetaReport.Classes.
	Name string `json:"name"`

	// Description is one sentence on what the class represents.
	Description string `json:"description"`

	// Occurrences is how many distinct findings this run fell into
	// this class. Classes with zero occurrences are omitted from the
	// report entirely.
	Occurrences int `json:"occurrences"`

	// ExampleFindings is up to 3 concrete findings (short strings)
	// that fell into this class. Useful for the operator reading
	// the printed summary.
	ExampleFindings []string `json:"example_findings"`

	// PreventionRule is a machine-actionable hint a future run's
	// orchestration layer could consume. Always one sentence,
	// specific, and begins with an action verb (run, add, skip,
	// check, move, split). Never "be more careful".
	PreventionRule string `json:"prevention_rule"`
}

// MetaReport is the full meta-reasoner output for one SOW run.
type MetaReport struct {
	// RunID matches the stoke run identifier (SOW ID). Used as the
	// filename when the report is persisted.
	RunID string `json:"run_id"`

	// GeneratedAt is the wall-clock time the report was produced.
	// Used by LoadRecentMetaReports to sort reports newest-first.
	GeneratedAt time.Time `json:"generated_at"`

	// OverallVerdict is one paragraph: did this run converge,
	// plateau, or escalate? Cites concrete numbers from Telemetry.
	OverallVerdict string `json:"overall_verdict"`

	// Classes is the set of root-cause classes found this run,
	// sorted by Occurrences descending.
	Classes []RootCauseClass `json:"classes"`

	// NewClassCandidates is the list of classes the LLM proposed
	// that don't match any of our seeded categories. Empty when
	// the run's failures all fit known classes. Each entry is a
	// complete RootCauseClass so an operator can promote it to a
	// seeded category on review.
	NewClassCandidates []RootCauseClass `json:"new_class_candidates"`
}

// MetaReasonInput bundles everything the meta-reasoner needs. The
// caller populates whichever fields it has cheaply to hand — missing
// fields degrade report quality gracefully but never cause an error.
type MetaReasonInput struct {
	// RunID is the SOW ID for this run.
	RunID string

	// Telemetry is the deterministic fact-set gathered by the
	// runner. See MetaRunTelemetry for which fields are populated
	// today.
	Telemetry MetaRunTelemetry

	// ACResults is the flat list of every acceptance criterion's
	// final result across every session in the run. The LLM reads
	// these to identify WHICH specific criteria failed and what
	// their failure output looked like — that's the raw material
	// for pattern classification.
	ACResults []AcceptanceResult

	// SessionSummaries is a short per-session summary (title, ACs
	// passed vs failed, attempts). The LLM uses this to detect
	// "S7 was fine, S8 and S9 both plateaued on the same pattern"
	// — the shape of the run matters for classification.
	SessionSummaries []MetaSessionSummary
}

// MetaRunTelemetry is the deterministic fact-set for one SOW run.
// Every field here is an integer counter or duration the runner can
// produce without LLM inference. Fields that would require additional
// instrumentation to populate today are deliberately absent — the
// struct lists only what the caller can supply cheaply as of now.
type MetaRunTelemetry struct {
	// Sessions is the total number of sessions the scheduler ran
	// (including any continuations appended during the run).
	Sessions int

	// TasksDispatched is the sum of all per-session task counts.
	TasksDispatched int

	// TasksCompleted is the count of tasks that returned Success.
	TasksCompleted int

	// SessionsPassed / SessionsFailed / SessionsSkipped together
	// partition Sessions. Populated from SessionResult.AcceptanceMet
	// plus SessionResult.Error plus SessionResult.Skipped.
	SessionsPassed  int
	SessionsFailed  int
	SessionsSkipped int

	// TotalAttempts is the sum of SessionResult.Attempts across
	// every session. A value well above Sessions indicates the
	// repair loop was busy; equal to Sessions means everything
	// passed first try.
	TotalAttempts int

	// TotalElapsed is wall-clock duration of the whole run, measured
	// by the caller around ss.Run.
	TotalElapsed time.Duration

	// TotalCostUSD is cumulative LLM spend for the run, gathered
	// from the shared cost pointer the runner threads through every
	// session.
	TotalCostUSD float64
}

// MetaSessionSummary is a compact per-session snapshot for the LLM.
type MetaSessionSummary struct {
	SessionID     string `json:"session_id"`
	Title         string `json:"title"`
	Attempts      int    `json:"attempts"`
	AcceptanceMet bool   `json:"acceptance_met"`
	Skipped       bool   `json:"skipped"`
	Error         string `json:"error,omitempty"`
	ACsTotal      int    `json:"acs_total"`
	ACsPassed     int    `json:"acs_passed"`
}

// RunMetaReasoning consults the LLM to classify this run's failures
// into root-cause categories and emit prevention rules. Deterministic
// fact gathering happens in the caller — this function consumes facts
// and produces classification.
//
// Returns (nil, nil) when prov is nil. A best-effort function: if the
// LLM call fails or the output doesn't parse, returns an error but
// does not panic. Callers should log and continue.
func RunMetaReasoning(ctx context.Context, prov provider.Provider, model string, in MetaReasonInput) (*MetaReport, error) {
	if prov == nil {
		return nil, nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	var b strings.Builder
	b.WriteString(metaReasonerPrompt)
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "RUN ID: %s\n\n", in.RunID)

	b.WriteString("RUN TELEMETRY (deterministic counters — do NOT recount, classify only):\n")
	fmt.Fprintf(&b, "  sessions: %d (passed=%d failed=%d skipped=%d)\n", in.Telemetry.Sessions, in.Telemetry.SessionsPassed, in.Telemetry.SessionsFailed, in.Telemetry.SessionsSkipped)
	fmt.Fprintf(&b, "  tasks: dispatched=%d completed=%d\n", in.Telemetry.TasksDispatched, in.Telemetry.TasksCompleted)
	fmt.Fprintf(&b, "  session attempts total: %d (above session count = repair loop activity)\n", in.Telemetry.TotalAttempts)
	fmt.Fprintf(&b, "  wall clock: %s\n", in.Telemetry.TotalElapsed.Round(time.Second))
	fmt.Fprintf(&b, "  cost: $%.2f\n", in.Telemetry.TotalCostUSD)
	b.WriteString("\n")

	if len(in.SessionSummaries) > 0 {
		b.WriteString("PER-SESSION SUMMARY:\n")
		for _, s := range in.SessionSummaries {
			status := "pass"
			switch {
			case s.Skipped:
				status = "skip"
			case !s.AcceptanceMet:
				status = "fail"
			case s.Error != "":
				status = "fail"
			}
			fmt.Fprintf(&b, "  %s [%s] %q attempts=%d acs=%d/%d",
				s.SessionID, status, s.Title, s.Attempts, s.ACsPassed, s.ACsTotal)
			if s.Error != "" {
				fmt.Fprintf(&b, " err=%q", truncateForReasoning(s.Error, 140))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Gather failed criteria with their output — that's the raw
	// signal the LLM actually classifies.
	failed := 0
	for _, ac := range in.ACResults {
		if !ac.Passed {
			failed++
		}
	}
	if failed > 0 {
		b.WriteString("FAILED ACCEPTANCE CRITERIA (what the model should classify):\n")
		shown := 0
		for _, ac := range in.ACResults {
			if ac.Passed {
				continue
			}
			fmt.Fprintf(&b, "\n  [%s] %s\n", ac.CriterionID, ac.Description)
			if ac.JudgeReasoning != "" {
				fmt.Fprintf(&b, "    judge said: %s\n", truncateForReasoning(ac.JudgeReasoning, 400))
			}
			if ac.Output != "" {
				fmt.Fprintf(&b, "    output:\n")
				for _, line := range strings.Split(strings.TrimSpace(truncateForReasoning(ac.Output, 1500)), "\n") {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
			shown++
			// Cap to avoid blowing the prompt; rely on telemetry for
			// overall counts.
			if shown >= 25 {
				fmt.Fprintf(&b, "\n  ... and %d more failing criteria not shown\n", failed-shown)
				break
			}
		}
		b.WriteString("\n")
	} else {
		b.WriteString("FAILED ACCEPTANCE CRITERIA: none. The run closed all ACs cleanly.\n\n")
	}

	b.WriteString("Output the JSON MetaReport object described above. No prose, no backticks.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 12000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("meta-reasoner chat: %w", err)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("meta-reasoner returned no content")
	}

	var report MetaReport
	if _, err := jsonutil.ExtractJSONInto(raw, &report); err != nil {
		return nil, fmt.Errorf("parse meta-report: %w", err)
	}

	// Fill in fields the LLM isn't responsible for.
	report.RunID = in.RunID
	report.GeneratedAt = time.Now().UTC()

	// Drop any class with zero occurrences — the prompt says to omit
	// them but we enforce defensively.
	report.Classes = filterNonEmptyClasses(report.Classes)
	report.NewClassCandidates = filterNonEmptyClasses(report.NewClassCandidates)

	// Sort classes by Occurrences descending for stable printing.
	sort.SliceStable(report.Classes, func(i, j int) bool {
		return report.Classes[i].Occurrences > report.Classes[j].Occurrences
	})
	sort.SliceStable(report.NewClassCandidates, func(i, j int) bool {
		return report.NewClassCandidates[i].Occurrences > report.NewClassCandidates[j].Occurrences
	})

	return &report, nil
}

// filterNonEmptyClasses drops classes with zero occurrences and clips
// ExampleFindings to at most 3 entries each.
func filterNonEmptyClasses(in []RootCauseClass) []RootCauseClass {
	out := make([]RootCauseClass, 0, len(in))
	for _, c := range in {
		if c.Occurrences <= 0 {
			continue
		}
		if len(c.ExampleFindings) > 3 {
			c.ExampleFindings = c.ExampleFindings[:3]
		}
		out = append(out, c)
	}
	return out
}

// MetaReportsDir returns the absolute path to the directory where
// meta-reports are persisted for a given repo root. Callers should
// create it with os.MkdirAll before writing.
func MetaReportsDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".stoke", "meta-reports")
}

// SaveMetaReport writes report to <repoRoot>/.stoke/meta-reports/<RunID>.json
// using pretty-printed JSON. Creates the parent directory if missing.
//
// Returns an error when the report is nil, the RunID is empty (so we
// can't construct a filename), or the filesystem write fails.
func SaveMetaReport(repoRoot string, report *MetaReport) error {
	if report == nil {
		return fmt.Errorf("nil meta-report")
	}
	if strings.TrimSpace(report.RunID) == "" {
		return fmt.Errorf("meta-report has empty RunID")
	}
	dir := MetaReportsDir(repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create meta-reports dir: %w", err)
	}
	path := filepath.Join(dir, sanitizeRunID(report.RunID)+".json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta-report: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
		return fmt.Errorf("write meta-report: %w", err)
	}
	return nil
}

// sanitizeRunID strips characters that would be unsafe in a filename.
// Run IDs are usually already safe, but we defend against SOW IDs that
// snuck through with slashes or whitespace.
func sanitizeRunID(id string) string {
	id = strings.TrimSpace(id)
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		" ", "_",
		":", "_",
	)
	id = replacer.Replace(id)
	if id == "" {
		id = "unknown"
	}
	return id
}

// LoadRecentMetaReports reads up to maxReports newest meta-reports from
// <repoRoot>/.stoke/meta-reports/ and returns them sorted newest-first.
// Missing directory is treated as "no reports" (returns nil, nil) so the
// first run on a repo doesn't error.
//
// Malformed JSON files are skipped with no error — one bad report can't
// block future-run briefings from seeing the rest.
func LoadRecentMetaReports(repoRoot string, maxReports int) ([]MetaReport, error) {
	if maxReports <= 0 {
		return nil, nil
	}
	dir := MetaReportsDir(repoRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read meta-reports dir: %w", err)
	}
	type candidate struct {
		report MetaReport
		when   time.Time
	}
	var cands []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			continue
		}
		var r MetaReport
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		when := r.GeneratedAt
		if when.IsZero() {
			if info, ierr := e.Info(); ierr == nil {
				when = info.ModTime()
			}
		}
		cands = append(cands, candidate{report: r, when: when})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].when.After(cands[j].when)
	})
	if len(cands) > maxReports {
		cands = cands[:maxReports]
	}
	out := make([]MetaReport, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.report)
	}
	return out, nil
}

// FormatMetaReportForOperator renders a MetaReport as the compact
// human-readable summary the SOW runner prints at end-of-run. Format
// stays narrow so it fits in a terminal without wrapping.
func FormatMetaReportForOperator(r *MetaReport) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "meta-reasoner — run %s\n", r.RunID)
	if strings.TrimSpace(r.OverallVerdict) != "" {
		fmt.Fprintf(&b, "  verdict: %s\n", r.OverallVerdict)
	}
	if len(r.Classes) > 0 {
		b.WriteString("  root-cause classes:\n")
		for i, c := range r.Classes {
			fmt.Fprintf(&b, "    %d. %s (%d occurrences) — %s\n", i+1, c.Name, c.Occurrences, c.Description)
		}
		b.WriteString("  prevention rules:\n")
		for _, c := range r.Classes {
			if strings.TrimSpace(c.PreventionRule) != "" {
				fmt.Fprintf(&b, "    - %s\n", c.PreventionRule)
			}
		}
	} else {
		b.WriteString("  no classifiable root-cause classes this run\n")
	}
	if len(r.NewClassCandidates) > 0 {
		b.WriteString("\n  new class candidates (review and promote if valid):\n")
		for _, c := range r.NewClassCandidates {
			fmt.Fprintf(&b, "    - %s (%d occurrences) — %s\n", c.Name, c.Occurrences, c.Description)
		}
	} else {
		b.WriteString("  new class candidates: (none this run)\n")
	}
	return b.String()
}

// FormatPriorLearningsForBriefing renders up to len(reports) prior
// meta-reports as a block suitable for injecting into the lead-dev
// briefing system prompt. Only prevention rules are included — the
// briefing doesn't need the full class descriptions, just the
// actionable rules from prior pain.
//
// Returns empty string when no reports carry any prevention rules, so
// the caller can safely write the result unconditionally.
func FormatPriorLearningsForBriefing(reports []MetaReport) string {
	if len(reports) == 0 {
		return ""
	}
	// Deduplicate rules by text so three runs that all hit the same
	// class don't print the same rule three times.
	seen := map[string]bool{}
	var lines []string
	for _, r := range reports {
		for _, c := range r.Classes {
			rule := strings.TrimSpace(c.PreventionRule)
			if rule == "" || seen[rule] {
				continue
			}
			seen[rule] = true
			lines = append(lines, fmt.Sprintf("  - [%s] %s", c.Name, rule))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("LEARNINGS FROM PRIOR RUNS ON THIS REPO (each rule was synthesized from a real failure class — preempt these BEFORE dispatching work):\n")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// metaReasonerPrompt is the system prompt for the meta-reasoner.
// Emphasizes: (1) facts come from telemetry/results, don't invent or
// recount; (2) use seeded class names when they fit; (3) new classes
// go to NewClassCandidates; (4) rules must begin with an action verb.
const metaReasonerPrompt = `You are the META-REASONER for Stoke. You run once at the end of a SOW run. Your job is NOT to count or to diagnose a single failure — it's to step back and classify the entire run's pain into structured root-cause classes so the next run can preempt them.

DIVISION OF LABOUR:
  - Facts about the run (session counts, AC pass/fail, attempts, cost, elapsed) are given to you as deterministic telemetry. TRUST the counters. Do NOT re-count from the per-session summary text. If the telemetry says 3 sessions failed, 3 sessions failed.
  - Your job is CLASSIFICATION. Given the failing AC output excerpts and the per-session summary, group the pain into known categories. Emit one class per category with a concrete, machine-actionable prevention rule.

SEEDED ROOT-CAUSE CLASSES (use these names verbatim when they fit; only coin new names when a genuine novel pattern appears that none of these describe):

  missing_devdep             — script references a binary not declared in the package's devDependencies (e.g. "tsc: not found")
  missing_script_target      — script references a helper file or path not present on disk
  cross_package_contract     — code imports a symbol another package doesn't export, or exports it under a different name
  empty_tsconfig_include     — TypeScript TS18003 — tsconfig.include matched zero files
  package_exports_mismatch   — package.json exports map points to a file that doesn't exist
  turbo_pipeline_drift       — turbo.json (or equivalent) references a task script a package doesn't define
  install_hang               — pnpm/npm/cargo/go install stalled or deadlocked
  spec_ambiguity             — the SOW didn't tell the worker which version / library / shape to use. Not a code bug — a prompt bug.
  reviewer_over_dispatch     — per-task reviewer flagged polish beyond the task's scope, spawning follow-ups that added cost without closing an AC
  reviewer_under_dispatch    — per-task reviewer missed a real gap that later failed a downstream AC

RULES FOR CLASSES:
  - Only emit a class when Occurrences >= 1 — you observed at least one failing AC / session that fits the pattern. Don't emit speculative classes.
  - ExampleFindings is up to 3 short strings (one sentence each). Quote enough of the actual failure output that the operator can see why you classified it this way.
  - PreventionRule is ONE sentence. Start with an action verb: run, add, skip, check, move, split, merge, pin, enable, require. Never "be more careful", never "consider", never "maybe". The rule must be specific enough that a future-run configuration could act on it — pointing at a concrete phase, component, or gate of Stoke.
  - If a class doesn't match any seeded name, put it in new_class_candidates instead of classes. Give it a snake_case name and a one-sentence description that another engineer could evaluate for promotion to a seeded category.

OVERALL_VERDICT:
  - One short paragraph. State whether the run CONVERGED (all sessions passed), PLATEAUED (some failed but made measurable progress), or ESCALATED (failures compounded / sessions had to escalate to override / the repair loop flailed).
  - Cite the concrete numbers from telemetry. "Plateaued at 82% AC pass (14/17 sessions)." Not "the run was mostly good."

OUTPUT SCHEMA — return ONLY this JSON object, no prose, no backticks:

{
  "overall_verdict": "one paragraph citing telemetry numbers",
  "classes": [
    {
      "name": "missing_devdep",
      "description": "script references a binary not declared in the package's devDependencies",
      "occurrences": 4,
      "example_findings": ["S3 AC1: 'tsc: command not found' in packages/ui", "..."],
      "prevention_rule": "Run hygiene.ScanAndAutoFix BEFORE Phase 1 task dispatch (currently runs Phase 1.75) so missing devDeps are injected before workers start writing code against them."
    }
  ],
  "new_class_candidates": []
}
`
