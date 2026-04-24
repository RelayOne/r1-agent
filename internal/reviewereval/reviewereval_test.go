package reviewereval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCase(t *testing.T, dir string, c Case) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, c.ID+".json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCorpusSortsAndSkipsBadFiles(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, Case{ID: "b", Label: LabelReal, Spec: "s"})
	writeCase(t, dir, Case{ID: "a", Label: LabelFake, Spec: "s"})
	// bogus file with invalid JSON
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o600)
	// non-json file ignored
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o600)
	got, err := LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 loaded cases, got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("cases must be sorted by ID; got %+v", got)
	}
}

func TestGradeCountsCorrectly(t *testing.T) {
	cases := []Case{
		{ID: "c1", Label: LabelReal},
		{ID: "c2", Label: LabelReal},
		{ID: "c3", Label: LabelFake},
		{ID: "c4", Label: LabelFake},
	}
	decisions := []Decision{
		{CaseID: "c1", MatchesReal: true},  // TP
		{CaseID: "c2", MatchesReal: false}, // FN (real, rejected)
		{CaseID: "c3", MatchesReal: true},  // FP (fake, accepted)
		{CaseID: "c4", MatchesReal: false}, // TN
	}
	conf := Grade(cases, decisions)
	if conf.TP != 1 || conf.FP != 1 || conf.FN != 1 || conf.TN != 1 {
		t.Fatalf("confusion wrong: %+v", conf)
	}
	p, r, a := conf.Score()
	if !approx(p, 0.5) || !approx(r, 0.5) || !approx(a, 0.5) {
		t.Fatalf("scores wrong: p=%v r=%v a=%v", p, r, a)
	}
}

func TestGradeEmptyReturnsZeroScores(t *testing.T) {
	conf := Grade(nil, nil)
	p, r, a := conf.Score()
	if p != 0 || r != 0 || a != 0 {
		t.Fatalf("empty grade must score zero; got p=%v r=%v a=%v", p, r, a)
	}
}

func TestGradeSkipsUnknownDecisions(t *testing.T) {
	cases := []Case{{ID: "c1", Label: LabelReal}}
	decisions := []Decision{
		{CaseID: "c1", MatchesReal: true},
		{CaseID: "not-in-corpus", MatchesReal: false},
	}
	conf := Grade(cases, decisions)
	if conf.TP+conf.FP+conf.FN+conf.TN != 1 {
		t.Fatalf("decisions for unknown cases must be ignored: %+v", conf)
	}
}

func TestReportTableShapeIsStable(t *testing.T) {
	got := Report([]PairResult{{
		BuilderModel:  "claude-sonnet-4-6",
		ReviewerModel: "claude-opus-4-6",
		Confusion:     Confusion{TP: 10, FP: 1, FN: 2, TN: 9},
		Precision:     0.909,
		Recall:        0.833,
		Accuracy:      0.864,
	}})
	// Header should mention every column the operator needs to read
	// the delta at a glance.
	for _, col := range []string{"builder", "reviewer", "TP", "FP", "FN", "TN", "prec", "recall", "acc"} {
		if !strings.Contains(got, col) {
			t.Fatalf("column %q missing from report:\n%s", col, got)
		}
	}
}

func approx(a, b float64) bool {
	if a > b {
		return a-b < 1e-6
	}
	return b-a < 1e-6
}
