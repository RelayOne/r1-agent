package antitrunclobe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDetector_NoFindings(t *testing.T) {
	d := &Detector{
		History: []string{"build is green; tests pass"},
	}
	got := d.Run(context.Background())
	if len(got) != 0 {
		t.Errorf("expected no findings, got %d: %+v", len(got), got)
	}
}

func TestDetector_TruncationPhrase(t *testing.T) {
	d := &Detector{
		History: []string{"i'll stop here for now"},
	}
	got := d.Run(context.Background())
	if len(got) == 0 {
		t.Fatal("expected at least one finding")
	}
	found := false
	for _, f := range got {
		if f.Source == "assistant_output" && f.PhraseID == "premature_stop_let_me" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing assistant_output/premature_stop_let_me finding: %+v", got)
	}
}

func TestDetector_UncheckedPlan(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("- [x] one\n- [ ] two\n- [ ] three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Detector{
		History:  []string{"clean text"},
		PlanPath: plan,
	}
	got := d.Run(context.Background())
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].Source != "plan_unchecked" {
		t.Errorf("Source = %q, want plan_unchecked", got[0].Source)
	}
}

func TestDetector_FalseCompletionInRecentCommitBody(t *testing.T) {
	d := &Detector{
		History: []string{"clean"},
		GitLog: func(n int) ([]string, error) {
			return []string{"spec 12 done"}, nil
		},
	}
	got := d.Run(context.Background())
	if len(got) == 0 {
		t.Fatal("expected commit finding")
	}
	if got[0].Source != "commit_body" {
		t.Errorf("Source = %q, want commit_body", got[0].Source)
	}
	if got[0].PhraseID != "false_completion_spec_done" {
		t.Errorf("PhraseID = %q, want false_completion_spec_done", got[0].PhraseID)
	}
	if got[0].Snippet == "" {
		t.Error("Snippet must not be empty for commit_body finding")
	}
}

func TestDetector_SpecGlob_InProgressUnchecked(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "feature.md")
	body := "<!-- STATUS: in-progress -->\n- [ ] x\n"
	if err := os.WriteFile(sp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Detector{
		SpecGlob: filepath.Join(dir, "*.md"),
	}
	got := d.Run(context.Background())
	if len(got) != 1 {
		t.Fatalf("expected 1 spec finding, got %d", len(got))
	}
	if got[0].Source != "spec_unchecked" {
		t.Errorf("Source = %q", got[0].Source)
	}
}

func TestDetector_SpecGlob_DoneIgnored(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "shipped.md")
	body := "<!-- STATUS: done -->\n- [ ] residual\n"
	if err := os.WriteFile(sp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Detector{
		SpecGlob: filepath.Join(dir, "*.md"),
	}
	got := d.Run(context.Background())
	if len(got) != 0 {
		t.Errorf("done spec should be ignored, got %d findings", len(got))
	}
}

func TestDetector_AllFourSignals(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	os.WriteFile(plan, []byte("- [ ] open\n"), 0o644)
	sp := filepath.Join(dir, "feat.md")
	os.WriteFile(sp, []byte("<!-- STATUS: in-progress -->\n- [ ] go\n"), 0o644)

	d := &Detector{
		History:  []string{"i'll stop here", "good enough to merge"},
		PlanPath: plan,
		SpecGlob: filepath.Join(dir, "feat*.md"),
		GitLog: func(n int) ([]string, error) {
			return []string{"spec 1 done"}, nil
		},
	}
	got := d.Run(context.Background())
	sources := map[string]int{}
	for _, f := range got {
		sources[f.Source]++
	}
	for _, want := range []string{"assistant_output", "plan_unchecked", "spec_unchecked", "commit_body"} {
		if sources[want] == 0 {
			t.Errorf("missing %q finding; got sources=%v", want, sources)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 12: "12", 305: "305", -3: "0"}
	for n, want := range cases {
		if got := itoa(n); got != want {
			t.Errorf("itoa(%d) = %q, want %q", n, got, want)
		}
	}
}
