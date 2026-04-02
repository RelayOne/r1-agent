package plan

import (
	"testing"
)

func TestROIHighSecurity(t *testing.T) {
	task := Task{ID: "T1", Description: "Fix SQL injection vulnerability in user login"}
	roi := ClassifyROI(task)
	if roi.Class != ROIHigh { t.Errorf("class=%v, want high", roi.Class) }
}

func TestROIHighReliability(t *testing.T) {
	roi := ClassifyROI(Task{Description: "Fix race condition in session handler"})
	if roi.Class != ROIHigh { t.Errorf("class=%v", roi.Class) }
}

func TestROIMediumRefactor(t *testing.T) {
	roi := ClassifyROI(Task{Description: "Refactor the handler to use a cleaner pattern"})
	if roi.Class != ROIMedium { t.Errorf("class=%v, want medium", roi.Class) }
}

func TestROILowDocs(t *testing.T) {
	roi := ClassifyROI(Task{Description: "Update README documentation"})
	if roi.Class != ROILow { t.Errorf("class=%v, want low", roi.Class) }
}

func TestROISkipFormatting(t *testing.T) {
	roi := ClassifyROI(Task{Description: "Fix trailing whitespace in all files"})
	if roi.Class != ROISkip { t.Errorf("class=%v, want skip", roi.Class) }
}

func TestFilterByROI(t *testing.T) {
	tasks := []Task{
		{ID: "A", Description: "Fix XSS vulnerability"},       // High
		{ID: "B", Description: "Fix trailing whitespace"},     // Skip
		{ID: "C", Description: "Add rate limiting"},           // Medium (contains "add")
		{ID: "D", Description: "Update README documentation"}, // Low (contains "document")
	}
	// minClass=ROIMedium keeps High (0) and Medium (1), filters Low (2) and Skip (3)
	kept, filtered := FilterByROI(tasks, ROIMedium)
	if len(kept) != 2 {
		t.Errorf("kept=%d, want 2 (A=high, C=medium)", len(kept))
	}
	if len(filtered) != 2 {
		t.Errorf("filtered=%d, want 2 (B=skip, D=low)", len(filtered))
	}
}

func TestFilterByROIHighOnly(t *testing.T) {
	tasks := []Task{
		{ID: "A", Description: "Fix SQL injection"},
		{ID: "B", Description: "Refactor utils"},
		{ID: "C", Description: "Update docs"},
	}
	kept, _ := FilterByROI(tasks, ROIHigh)
	if len(kept) != 1 { t.Errorf("kept=%d, want 1 (only security)", len(kept)) }
}
