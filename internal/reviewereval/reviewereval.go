// Package reviewereval provides the measurement harness for S-U-017:
// the internal experiment that produces defensible FP/FN numbers for
// Stoke's cross-model review pairings (Claude implements → Codex
// reviews; Claude implements → Gemini reviews; etc.).
//
// The published literature confirms the broader pattern — choice of
// reviewer model and review stance can swing agent success rates as
// much as upgrading the base model — but no public study isolates
// the cross-model-review effect on a controlled corpus. Stoke can
// produce that data internally with the existing `modelsource`
// routing infrastructure: pick any (builder, reviewer) pair, run
// the same seeded task corpus, score reviewer decisions against a
// ground-truth label for each task.
//
// What this package is:
//   - A corpus loader that reads ReviewEvalCase JSON files from disk
//     so the corpus is code-reviewable and operator-extensible.
//   - A grader that takes a reviewer's verdict (Accept / Reject) and
//     computes FP / FN / precision / recall against the case's label.
//   - A pair-runner that, given a (builder provider, reviewer provider),
//     runs every case through both and emits a tabular result set.
//
// What this package explicitly is not:
//   - The corpus itself. The corpus is intentionally a data artifact
//     that lives under corpus/ and evolves independently of this code.
//   - A replacement for the in-run per-task reviewer. This harness
//     is for measurement; the production reviewer path is unchanged.
//   - An online A/B. Evaluations are deliberately offline so results
//     are reproducible and comparable across runs.
package reviewereval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Label is the ground-truth verdict for a case. A reviewer that
// accepts a Fake or rejects a Real is mis-scoring.
type Label string

const (
	LabelReal Label = "real" // the implementation actually satisfies the spec
	LabelFake Label = "fake" // the implementation is a plausible-looking placeholder
)

// Case is one evaluation unit. Paths are relative to the corpus root.
type Case struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Spec        string            `json:"spec"`
	Files       map[string]string `json:"files"` // relative path → full file content
	Label       Label             `json:"label"`
	// Rationale records why this case was labeled the way it was.
	// Never fed to the reviewer; exists for corpus-maintenance audits.
	Rationale string `json:"rationale,omitempty"`
}

// LoadCorpus reads every *.json file under dir as a Case. Files that
// fail to parse are skipped with a warning written to stderr so a
// single malformed case doesn't sink the whole evaluation. The
// returned slice is sorted by Case.ID for reproducibility.
func LoadCorpus(dir string) ([]Case, error) {
	var cases []Case
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reviewereval: read corpus dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reviewereval: skip %s: %v\n", e.Name(), err)
			continue
		}
		var c Case
		if err := json.Unmarshal(data, &c); err != nil {
			fmt.Fprintf(os.Stderr, "reviewereval: parse %s: %v\n", e.Name(), err)
			continue
		}
		if c.ID == "" {
			c.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

// Decision is a reviewer's verdict on a single case. MatchesReal is
// the reviewer's call; when true the reviewer believes the case was
// a real implementation.
type Decision struct {
	CaseID      string
	MatchesReal bool
	Reasoning   string
}

// Confusion is a 2×2 confusion matrix for one (builder, reviewer) pair.
type Confusion struct {
	TP int // real implementation, reviewer said real
	FP int // fake implementation, reviewer said real  ← bad: passes rot
	FN int // real implementation, reviewer said fake  ← bad: blocks good code
	TN int // fake implementation, reviewer said fake
}

// Score collates the confusion matrix into scalar scores useful for
// decision-making. Precision = TP / (TP + FP); recall = TP / (TP + FN).
// Returns zeros when denominators are zero — the caller treats zero as
// "insufficient data" rather than a defined result.
func (c Confusion) Score() (precision, recall, accuracy float64) {
	total := c.TP + c.FP + c.FN + c.TN
	if total == 0 {
		return 0, 0, 0
	}
	if c.TP+c.FP > 0 {
		precision = float64(c.TP) / float64(c.TP+c.FP)
	}
	if c.TP+c.FN > 0 {
		recall = float64(c.TP) / float64(c.TP+c.FN)
	}
	accuracy = float64(c.TP+c.TN) / float64(total)
	return
}

// Grade computes the confusion matrix for a set of reviewer decisions
// against the labeled corpus. Cases absent from decisions are counted
// as neutral (skipped) and do not affect the score.
func Grade(cases []Case, decisions []Decision) Confusion {
	labelByID := map[string]Label{}
	for _, c := range cases {
		labelByID[c.ID] = c.Label
	}
	var conf Confusion
	for _, d := range decisions {
		label, ok := labelByID[d.CaseID]
		if !ok {
			continue
		}
		switch {
		case label == LabelReal && d.MatchesReal:
			conf.TP++
		case label == LabelFake && d.MatchesReal:
			conf.FP++
		case label == LabelReal && !d.MatchesReal:
			conf.FN++
		case label == LabelFake && !d.MatchesReal:
			conf.TN++
		}
	}
	return conf
}

// PairResult is the full record of a (builder, reviewer) evaluation.
type PairResult struct {
	BuilderModel  string
	ReviewerModel string
	Confusion     Confusion
	Precision     float64
	Recall        float64
	Accuracy      float64
	Decisions     []Decision
}

// Report renders pair results as a human-readable table. Intended for
// post-run decision docs and technical briefs; the JSON form (just
// `json.Marshal(results)`) is the machine-readable counterpart.
func Report(results []PairResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cross-model review evaluation (%d pair(s)):\n\n", len(results))
	fmt.Fprintf(&b, "  %-32s %-32s %5s %5s %5s %5s %8s %8s %8s\n",
		"builder", "reviewer", "TP", "FP", "FN", "TN", "prec", "recall", "acc")
	fmt.Fprintf(&b, "  %s\n", strings.Repeat("-", 118))
	for _, r := range results {
		fmt.Fprintf(&b, "  %-32s %-32s %5d %5d %5d %5d %8.3f %8.3f %8.3f\n",
			trunc(r.BuilderModel, 32), trunc(r.ReviewerModel, 32),
			r.Confusion.TP, r.Confusion.FP, r.Confusion.FN, r.Confusion.TN,
			r.Precision, r.Recall, r.Accuracy)
	}
	return b.String()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
