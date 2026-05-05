package antitrunc

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureBuildPlan is a representative build-plan.md exercising:
//   - mixed checked / unchecked items
//   - upper-case X
//   - bullet variants (* and -)
//   - intervening prose (which must NOT be counted)
const fixtureBuildPlan = `<!-- STATUS: in-progress -->
# Build Plan — Anti-Truncation

Top-of-plan prose that should not be counted as a checklist item.

## Layer 1

- [x] phrases.go ships
- [x] phrases_test.go passes
- [ ] gate wires into agentloop

## Layer 2

* [X] scope-completion gate
* [ ] CommitLookback wired to git
* [ ] cortex Lobe published

Inline mention of "[ ] sample" inside a code block — do not count.
`

func TestCountChecklist_Fixture(t *testing.T) {
	done, total := CountChecklist(fixtureBuildPlan)
	if total != 6 {
		t.Errorf("total = %d, want 6", total)
	}
	if done != 3 {
		t.Errorf("done = %d, want 3", done)
	}
}

func TestChecklistItems_OrderAndIndex(t *testing.T) {
	items := ChecklistItems(fixtureBuildPlan)
	if len(items) != 6 {
		t.Fatalf("len = %d, want 6", len(items))
	}
	if items[0].Index != 1 || !items[0].Checked || items[0].Text != "phrases.go ships" {
		t.Errorf("item 1 mismatch: %+v", items[0])
	}
	if items[2].Checked {
		t.Errorf("item 3 (gate wires) should be unchecked: %+v", items[2])
	}
	if items[3].Index != 4 || !items[3].Checked {
		t.Errorf("item 4 (X uppercase) should be checked: %+v", items[3])
	}
}

func TestUncheckedItems_OnlyUnchecked(t *testing.T) {
	un := UncheckedItems(fixtureBuildPlan)
	if len(un) != 3 {
		t.Fatalf("len = %d, want 3", len(un))
	}
	for _, it := range un {
		if it.Checked {
			t.Errorf("unchecked subset includes checked item: %+v", it)
		}
	}
}

func TestCheckedItems_OnlyChecked(t *testing.T) {
	ch := CheckedItems(fixtureBuildPlan)
	if len(ch) != 3 {
		t.Fatalf("len = %d, want 3", len(ch))
	}
	for _, it := range ch {
		if !it.Checked {
			t.Errorf("checked subset includes unchecked item: %+v", it)
		}
	}
}

func TestSpecStatus_HtmlComment(t *testing.T) {
	if got := SpecStatus(fixtureBuildPlan); got != "in-progress" {
		t.Errorf("SpecStatus = %q, want in-progress", got)
	}
}

func TestSpecStatus_BareLine(t *testing.T) {
	text := "STATUS: ready\n# foo\n"
	if got := SpecStatus(text); got != "ready" {
		t.Errorf("SpecStatus = %q, want ready", got)
	}
}

func TestSpecStatus_Absent(t *testing.T) {
	text := "no header here\n- [ ] open\n"
	if got := SpecStatus(text); got != "" {
		t.Errorf("SpecStatus = %q, want empty", got)
	}
}

func TestScopeReport_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(fixtureBuildPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ScopeReportFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Path != path {
		t.Errorf("Path = %q, want %q", rep.Path, path)
	}
	if rep.Status != "in-progress" {
		t.Errorf("Status = %q, want in-progress", rep.Status)
	}
	if rep.Total != 6 || rep.Done != 3 {
		t.Errorf("counts = %d/%d, want 3/6", rep.Done, rep.Total)
	}
	if len(rep.Unchecked) != 3 {
		t.Errorf("unchecked = %d, want 3", len(rep.Unchecked))
	}
	if rep.IsComplete() {
		t.Error("IsComplete() = true on partial plan")
	}
	if got := rep.PercentDone(); got < 0.49 || got > 0.51 {
		t.Errorf("PercentDone() = %f, want ~0.5", got)
	}
}

func TestScopeReport_FullyChecked(t *testing.T) {
	rep := ScopeReportFromText("x", "- [x] one\n- [x] two\n")
	if !rep.IsComplete() {
		t.Errorf("expected complete: %+v", rep)
	}
	if rep.PercentDone() != 1.0 {
		t.Errorf("PercentDone = %f, want 1.0", rep.PercentDone())
	}
}

func TestScopeReport_NoChecklist(t *testing.T) {
	rep := ScopeReportFromText("x", "# title\n\nprose only\n")
	if rep.Total != 0 {
		t.Errorf("Total = %d, want 0", rep.Total)
	}
	if rep.IsComplete() {
		t.Error("IsComplete() must be false on empty checklist")
	}
}

func TestScopeReport_FromFile_NotFound(t *testing.T) {
	_, err := ScopeReportFromFile("/no/such/file/here.md")
	if err == nil {
		t.Fatal("expected error on missing file")
	}
}
