package metrics

import (
	"strings"
)

// DiffMetrics captures the size and scope of a code change relative to
// a reference patch.
type DiffMetrics struct {
	// DiffSize is the total number of added + deleted lines.
	DiffSize int `json:"diff_size"`

	// FilesTouched is the number of distinct files modified.
	FilesTouched int `json:"files_touched"`

	// ScopeCreepScore measures how much the actual change deviates from the
	// reference patch. 0.0 means a perfect match in scope; higher values
	// indicate more extraneous changes. Computed as:
	//   (extra files + extra lines) / (reference files + reference lines)
	// Clamped to [0, +inf). Undefined (0) if there is no reference.
	ScopeCreepScore float64 `json:"scope_creep_score"`
}

// DiffSummary describes a unified diff in terms of touched files and line counts.
type DiffSummary struct {
	// Files is the set of file paths that appear in the diff.
	Files []string

	// Additions is the number of added lines.
	Additions int

	// Deletions is the number of deleted lines.
	Deletions int
}

// ParseDiffSummary extracts a DiffSummary from a unified diff string.
// It counts lines starting with '+' or '-' (excluding the +++ / --- headers).
func ParseDiffSummary(diff string) DiffSummary {
	var ds DiffSummary
	fileSet := make(map[string]bool)

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ b/") {
			f := strings.TrimPrefix(line, "+++ b/")
			fileSet[f] = true
			continue
		}
		if strings.HasPrefix(line, "--- a/") {
			f := strings.TrimPrefix(line, "--- a/")
			fileSet[f] = true
			continue
		}
		// Skip diff headers.
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			ds.Additions++
		} else if strings.HasPrefix(line, "-") {
			ds.Deletions++
		}
	}

	for f := range fileSet {
		if f != "/dev/null" {
			ds.Files = append(ds.Files, f)
		}
	}
	return ds
}

// ComputeDiffMetrics calculates diff metrics for an actual diff compared to
// an optional reference diff. If referenceDiff is empty, ScopeCreepScore is 0.
func ComputeDiffMetrics(actualDiff, referenceDiff string) DiffMetrics {
	actual := ParseDiffSummary(actualDiff)

	m := DiffMetrics{
		DiffSize:     actual.Additions + actual.Deletions,
		FilesTouched: len(actual.Files),
	}

	if referenceDiff == "" {
		return m
	}

	ref := ParseDiffSummary(referenceDiff)
	refFiles := make(map[string]bool, len(ref.Files))
	for _, f := range ref.Files {
		refFiles[f] = true
	}

	// Count extra files not in the reference.
	extraFiles := 0
	for _, f := range actual.Files {
		if !refFiles[f] {
			extraFiles++
		}
	}

	refSize := len(ref.Files) + ref.Additions + ref.Deletions
	if refSize == 0 {
		// Reference is empty; any change is scope creep but we can't
		// normalize, so report raw extra count.
		m.ScopeCreepScore = float64(m.DiffSize + len(actual.Files))
		return m
	}

	extraLines := 0
	if actual.Additions+actual.Deletions > ref.Additions+ref.Deletions {
		extraLines = (actual.Additions + actual.Deletions) - (ref.Additions + ref.Deletions)
	}

	m.ScopeCreepScore = float64(extraFiles+extraLines) / float64(refSize)
	return m
}
