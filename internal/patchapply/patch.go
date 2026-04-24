// Package patchapply implements unified diff parsing and application.
// Inspired by Aider's edit format handling and SWE-agent's patch application:
//
// LLMs generate diffs and patches that need to be applied to code.
// This package handles:
// - Parsing unified diff format (--- a/file, +++ b/file, @@ hunks)
// - Applying patches with context matching and fuzzy offset
// - Validation before application (context lines must match)
// - Dry-run mode for preview
// - Reverse application (undo a patch)
package patchapply

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ericmacdougall/stoke/internal/hashline"
)

// Hunk represents a single diff hunk.
type Hunk struct {
	OldStart int      `json:"old_start"`
	OldCount int      `json:"old_count"`
	NewStart int      `json:"new_start"`
	NewCount int      `json:"new_count"`
	Lines    []Line   `json:"lines"`
	Header   string   `json:"header,omitempty"` // @@ line
}

// Line is a single line in a hunk.
type Line struct {
	Op   LineOp `json:"op"`
	Text string `json:"text"`
}

// LineOp classifies a diff line.
type LineOp string

const (
	OpContext LineOp = " "
	OpAdd     LineOp = "+"
	OpDelete  LineOp = "-"
)

// Sentinel paths used by unified diff format to mark file creation/deletion.
const (
	devNullAbs = "/dev/null"
	devNullRel = "dev/null"
)

// FilePatch represents changes to a single file.
type FilePatch struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Hunks   []Hunk `json:"hunks"`
	IsNew   bool   `json:"is_new,omitempty"`
	IsDelete bool  `json:"is_delete,omitempty"`
}

// Patch is a collection of file patches.
type Patch struct {
	Files []FilePatch `json:"files"`
}

// ApplyResult describes the outcome of patch application.
type ApplyResult struct {
	Applied  []string `json:"applied"`  // files successfully patched
	Failed   []string `json:"failed"`   // files that failed
	Skipped  []string `json:"skipped"`  // files already at target state
	Errors   []string `json:"errors"`   // error messages
}

// Parse parses a unified diff string into a Patch.
func Parse(diff string) (*Patch, error) {
	diff = strings.TrimRight(diff, "\n")
	lines := strings.Split(diff, "\n")
	patch := &Patch{}
	var current *FilePatch
	var currentHunk *Hunk

	i := 0
	for i < len(lines) {
		line := lines[i]

		// File header
		if strings.HasPrefix(line, "--- ") {
			if current != nil {
				if currentHunk != nil {
					current.Hunks = append(current.Hunks, *currentHunk)
					currentHunk = nil
				}
				patch.Files = append(patch.Files, *current)
			}
			current = &FilePatch{}
			oldPath := strings.TrimSpace(line[4:])
			current.OldPath = stripPrefix(oldPath)

			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
				newPath := strings.TrimSpace(lines[i+1][4:])
				current.NewPath = stripPrefix(newPath)
				if current.OldPath == devNullAbs || current.OldPath == devNullRel {
					current.IsNew = true
				}
				if current.NewPath == devNullAbs || current.NewPath == devNullRel {
					current.IsDelete = true
				}
				i += 2
				continue
			}
			i++
			continue
		}

		if strings.HasPrefix(line, "+++ ") && current != nil {
			newPath := strings.TrimSpace(line[4:])
			current.NewPath = stripPrefix(newPath)
			if current.NewPath == devNullAbs || current.NewPath == devNullRel {
				current.IsDelete = true
			}
			if current.OldPath == devNullAbs || current.OldPath == devNullRel {
				current.IsNew = true
			}
			i++
			continue
		}

		// Hunk header
		if strings.HasPrefix(line, "@@") && current != nil {
			hunk, err := parseHunkHeader(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			if currentHunk != nil {
				current.Hunks = append(current.Hunks, *currentHunk)
			}
			currentHunk = hunk
			i++
			continue
		}

		// Hunk content
		if currentHunk != nil {
			if len(line) == 0 {
				// Empty line in a hunk = empty context line
				currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpContext, Text: ""})
			} else {
				switch line[0] {
				case '+':
					currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpAdd, Text: line[1:]})
				case '-':
					currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpDelete, Text: line[1:]})
				case ' ':
					currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpContext, Text: line[1:]})
				default:
					currentHunk.Lines = append(currentHunk.Lines, Line{Op: OpContext, Text: line})
				}
			}
		}
		i++
	}

	// Flush last hunk and file
	if currentHunk != nil && current != nil {
		current.Hunks = append(current.Hunks, *currentHunk)
	}
	if current != nil {
		patch.Files = append(patch.Files, *current)
	}

	return patch, nil
}

// Apply applies the patch to files under the given root directory.
func Apply(patch *Patch, root string) *ApplyResult {
	return applyPatch(patch, root, false, false)
}

// ApplyReverse applies the patch in reverse (undo).
func ApplyReverse(patch *Patch, root string) *ApplyResult {
	return applyPatch(patch, root, true, false)
}

// DryRun checks if the patch can be applied without making changes.
func DryRun(patch *Patch, root string) *ApplyResult {
	return applyPatch(patch, root, false, true)
}

func applyPatch(patch *Patch, root string, reverse, dryRun bool) *ApplyResult {
	result := &ApplyResult{}

	for _, fp := range patch.Files {
		if fp.IsNew && !reverse {
			path := fp.NewPath
			fullPath := filepath.Join(root, path)
			if err := applyNewFile(fp, fullPath, dryRun); err != nil {
				result.Failed = append(result.Failed, path)
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
			} else {
				result.Applied = append(result.Applied, path)
			}
			continue
		}

		if fp.IsDelete && !reverse {
			path := fp.OldPath
			fullPath := filepath.Join(root, path)
			if !dryRun {
				if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
					result.Failed = append(result.Failed, path)
					result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
					continue
				}
			}
			result.Applied = append(result.Applied, path)
			continue
		}

		path := fp.NewPath
		if reverse {
			path = fp.OldPath
		}
		fullPath := filepath.Join(root, path)

		content, err := os.ReadFile(fullPath)
		if err != nil {
			result.Failed = append(result.Failed, path)
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
			continue
		}

		// Use hashline to verify context lines haven't been modified concurrently.
		// Tag each line with a content hash; if a hunk's context lines don't match
		// the current tags, the file was changed since the diff was generated.
		if tf, tagErr := hashline.TagFile(fullPath); tagErr == nil {
			for _, h := range fp.Hunks {
				for i, l := range h.Lines {
					if l.Op != OpContext && l.Op != OpDelete {
						continue
					}
					lineNum := h.OldStart + i
					if tag, ok := tf.GetTag(lineNum); ok {
						if tag != hashline.ComputeTag(l.Text) {
							result.Errors = append(result.Errors,
								fmt.Sprintf("%s: hashline mismatch at line %d (concurrent edit detected)", path, lineNum))
						}
					}
				}
			}
		}

		lines := strings.Split(string(content), "\n")
		hunks := fp.Hunks
		if reverse {
			hunks = reverseHunks(hunks)
		}

		newLines, err := applyHunks(lines, hunks)
		if err != nil {
			result.Failed = append(result.Failed, path)
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
			continue
		}

		if !dryRun {
			output := strings.Join(newLines, "\n")
			if err := os.WriteFile(fullPath, []byte(output), 0644); err != nil { // #nosec G306 -- patch target (existing source file); 0644 preserves source perms.
				result.Failed = append(result.Failed, path)
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
				continue
			}
		}
		result.Applied = append(result.Applied, path)
	}

	return result
}

func applyNewFile(fp FilePatch, fullPath string, dryRun bool) error {
	if dryRun {
		return nil
	}

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var lines []string
	for _, h := range fp.Hunks {
		for _, l := range h.Lines {
			if l.Op == OpAdd || l.Op == OpContext {
				lines = append(lines, l.Text)
			}
		}
	}
	return os.WriteFile(fullPath, []byte(strings.Join(lines, "\n")), 0644) // #nosec G306 -- patch target (existing source file); 0644 preserves source perms.
}

func applyHunks(lines []string, hunks []Hunk) ([]string, error) {
	offset := 0

	for _, hunk := range hunks {
		start := hunk.OldStart - 1 + offset
		if start < 0 {
			start = 0
		}

		// Try exact match first, then fuzzy within ±3 lines
		matchStart := findMatch(lines, hunk, start, 3)
		if matchStart < 0 {
			return nil, fmt.Errorf("hunk at line %d: context mismatch", hunk.OldStart)
		}

		// Apply the hunk
		var newSection []string
		oldIdx := matchStart
		for _, l := range hunk.Lines {
			switch l.Op {
			case OpContext:
				if oldIdx < len(lines) {
					newSection = append(newSection, lines[oldIdx])
					oldIdx++
				}
			case OpDelete:
				oldIdx++ // skip old line
			case OpAdd:
				newSection = append(newSection, l.Text)
			}
		}

		// Splice into lines
		tail := make([]string, len(lines[oldIdx:]))
		copy(tail, lines[oldIdx:])
		newLines := make([]string, 0, len(lines[:matchStart])+len(newSection)+len(tail))
		newLines = append(newLines, lines[:matchStart]...)
		newLines = append(newLines, newSection...)
		newLines = append(newLines, tail...)

		offset += len(newLines) - len(lines)
		lines = newLines
	}

	return lines, nil
}

func findMatch(lines []string, hunk Hunk, start, fuzz int) int {
	// Extract context + delete lines for matching
	var matchLines []string
	for _, l := range hunk.Lines {
		if l.Op == OpContext || l.Op == OpDelete {
			matchLines = append(matchLines, l.Text)
		}
	}

	if len(matchLines) == 0 {
		return start
	}

	// Try offsets: 0, -1, +1, -2, +2, ...
	for delta := 0; delta <= fuzz; delta++ {
		for _, d := range []int{delta, -delta} {
			pos := start + d
			if pos < 0 || pos+len(matchLines) > len(lines) {
				continue
			}
			if matchAt(lines, pos, matchLines) {
				return pos
			}
		}
	}
	return -1
}

func matchAt(lines []string, pos int, expected []string) bool {
	for i, exp := range expected {
		if pos+i >= len(lines) {
			return false
		}
		if lines[pos+i] != exp {
			return false
		}
	}
	return true
}

func reverseHunks(hunks []Hunk) []Hunk {
	reversed := make([]Hunk, len(hunks))
	for i, h := range hunks {
		rh := Hunk{
			OldStart: h.NewStart,
			OldCount: h.NewCount,
			NewStart: h.OldStart,
			NewCount: h.OldCount,
		}
		for _, l := range h.Lines {
			switch l.Op {
			case OpAdd:
				rh.Lines = append(rh.Lines, Line{Op: OpDelete, Text: l.Text})
			case OpDelete:
				rh.Lines = append(rh.Lines, Line{Op: OpAdd, Text: l.Text})
			case OpContext:
				rh.Lines = append(rh.Lines, l)
			}
		}
		reversed[i] = rh
	}
	return reversed
}

func parseHunkHeader(line string) (*Hunk, error) {
	// @@ -old_start,old_count +new_start,new_count @@
	line = strings.TrimPrefix(line, "@@")
	atIdx := strings.Index(line, "@@")
	if atIdx >= 0 {
		line = line[:atIdx]
	}
	line = strings.TrimSpace(line)

	parts := strings.Fields(line)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid hunk header")
	}

	hunk := &Hunk{}

	old := strings.TrimPrefix(parts[0], "-")
	oldParts := strings.SplitN(old, ",", 2)
	hunk.OldStart, _ = strconv.Atoi(oldParts[0])
	if len(oldParts) > 1 {
		hunk.OldCount, _ = strconv.Atoi(oldParts[1])
	} else {
		hunk.OldCount = 1
	}

	newRange := strings.TrimPrefix(parts[1], "+")
	newParts := strings.SplitN(newRange, ",", 2)
	hunk.NewStart, _ = strconv.Atoi(newParts[0])
	if len(newParts) > 1 {
		hunk.NewCount, _ = strconv.Atoi(newParts[1])
	} else {
		hunk.NewCount = 1
	}

	return hunk, nil
}

func stripPrefix(path string) string {
	// Remove a/ or b/ prefix from diff paths
	if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
		return path[2:]
	}
	return path
}

// Stats returns summary statistics for a patch.
func (p *Patch) Stats() (files, additions, deletions int) {
	files = len(p.Files)
	for _, f := range p.Files {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				switch l.Op {
				case OpAdd:
					additions++
				case OpDelete:
					deletions++
				case OpContext:
					// Context lines are neither additions nor deletions.
				}
			}
		}
	}
	return
}

// Summary returns a human-readable patch summary.
func (p *Patch) Summary() string {
	files, adds, dels := p.Stats()
	var b strings.Builder
	fmt.Fprintf(&b, "%d file(s) changed, %d insertions(+), %d deletions(-)\n", files, adds, dels)
	for _, f := range p.Files {
		if f.IsNew {
			fmt.Fprintf(&b, "  new file: %s\n", f.NewPath)
		} else if f.IsDelete {
			fmt.Fprintf(&b, "  deleted:  %s\n", f.OldPath)
		} else {
			a, d := 0, 0
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					if l.Op == OpAdd {
						a++
					} else if l.Op == OpDelete {
						d++
					}
				}
			}
			fmt.Fprintf(&b, "  modified: %s (+%d -%d)\n", f.NewPath, a, d)
		}
	}
	return b.String()
}
