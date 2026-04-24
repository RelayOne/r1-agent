package plan

import (
	"strings"
)

// ROIClass categorizes the expected value of a task.
type ROIClass int

// ROI tiers for plan filtering. Tasks marked ROISkip are dropped
// before execution; the other tiers influence scheduling priority.
const (
	ROIHigh   ROIClass = iota // correctness, security, reliability
	ROIMedium                 // refactoring, type safety, tests
	ROILow                    // cosmetic, formatting, trivial docs
	ROISkip                   // not worth agent time
)

// ROIResult is the ROI classification for a task.
type ROIResult struct {
	Class  ROIClass
	Reason string
}

// ClassifyROI evaluates whether a task is worth executing.
func ClassifyROI(task Task) ROIResult {
	desc := strings.ToLower(task.Description)

	// High ROI: security, correctness, reliability
	if containsAny(desc, "security", "auth", "vulnerability", "injection", "xss", "csrf", "encrypt") {
		return ROIResult{ROIHigh, "security impact"}
	}
	if containsAny(desc, "crash", "data loss", "race condition", "deadlock", "memory leak") {
		return ROIResult{ROIHigh, "reliability/correctness"}
	}
	if containsAny(desc, "test", "coverage", "regression") {
		return ROIResult{ROIHigh, "test coverage"}
	}

	// Medium ROI: refactoring, features, type safety
	if containsAny(desc, "refactor", "extract", "simplify", "optimize", "performance") {
		return ROIResult{ROIMedium, "code quality improvement"}
	}
	if containsAny(desc, "type", "interface", "generic", "typescript") {
		return ROIResult{ROIMedium, "type safety"}
	}
	if containsAny(desc, "add", "implement", "create", "build", "feature") {
		return ROIResult{ROIMedium, "new functionality"}
	}

	// Low ROI: docs, formatting, cosmetic
	if containsAny(desc, "document", "readme", "comment", "jsdoc", "godoc") {
		return ROIResult{ROILow, "documentation"}
	}
	if containsAny(desc, "format", "indent", "whitespace", "newline", "trailing") {
		return ROIResult{ROISkip, "formatting (use a formatter instead)"}
	}
	if containsAny(desc, "rename variable", "rename function") && !containsAny(desc, "api", "public", "export") {
		return ROIResult{ROILow, "cosmetic rename"}
	}

	// Default: medium
	return ROIResult{ROIMedium, "general task"}
}

// FilterByROI removes tasks below the minimum ROI threshold.
// Returns kept tasks and filtered-out tasks with reasons.
func FilterByROI(tasks []Task, minClass ROIClass) (kept []Task, filtered []FilteredTask) {
	for _, t := range tasks {
		roi := ClassifyROI(t)
		if roi.Class <= minClass {
			kept = append(kept, t)
		} else {
			filtered = append(filtered, FilteredTask{Task: t, ROI: roi})
		}
	}
	return
}

// FilteredTask is a task that was removed by the ROI filter.
type FilteredTask struct {
	Task Task
	ROI  ROIResult
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) { return true }
	}
	return false
}

func (c ROIClass) String() string {
	switch c {
	case ROIHigh:   return "high"
	case ROIMedium: return "medium"
	case ROILow:    return "low"
	case ROISkip:   return "skip"
	default:        return "unknown"
	}
}
