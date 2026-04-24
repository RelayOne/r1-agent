// Package hashline implements hash-anchored line verification for concurrent edits.
// Inspired by oh-my-opencode's hashline system: each line is tagged with a short
// content hash. When an agent attempts an edit, the system verifies that the target
// lines haven't been modified since the agent last read them. This prevents silent
// edit corruption in multi-agent scenarios.
//
// OmO's hashline improved edit success from 6.7% to 68.3% (+61.6pp).
// Uses FNV-1a (fast, non-cryptographic) truncated to 2 chars for line tagging.
package hashline

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
)

// Tag is a 2-character content hash for a single line.
type Tag string

// TaggedLine pairs a line number, content, and its hash tag.
type TaggedLine struct {
	Num     int    `json:"num"`
	Content string `json:"content"`
	Tag     Tag    `json:"tag"`
}

// TaggedFile holds all tagged lines for a file.
type TaggedFile struct {
	Path  string       `json:"path"`
	Lines []TaggedLine `json:"lines"`
}

// ComputeTag generates a 2-character FNV-1a hash tag for a line.
func ComputeTag(content string) Tag {
	h := fnv.New32a()
	h.Write([]byte(content))
	b := h.Sum(nil)
	encoded := base64.RawURLEncoding.EncodeToString(b[:2])
	if len(encoded) > 2 {
		encoded = encoded[:2]
	}
	return Tag(encoded)
}

// TagFile reads a file and returns tagged lines.
func TagFile(path string) (*TaggedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tf := &TaggedFile{Path: path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	num := 0
	for scanner.Scan() {
		num++
		line := scanner.Text()
		tf.Lines = append(tf.Lines, TaggedLine{
			Num:     num,
			Content: line,
			Tag:     ComputeTag(line),
		})
	}
	return tf, scanner.Err()
}

// Render produces human/agent-readable tagged output: "NUM#TAG: content"
func (tf *TaggedFile) Render() string {
	var sb strings.Builder
	for _, l := range tf.Lines {
		sb.WriteString(fmt.Sprintf("%d#%s: %s\n", l.Num, l.Tag, l.Content))
	}
	return sb.String()
}

// RenderRange produces tagged output for a line range (1-indexed, inclusive).
func (tf *TaggedFile) RenderRange(start, end int) string {
	var sb strings.Builder
	for _, l := range tf.Lines {
		if l.Num >= start && l.Num <= end {
			sb.WriteString(fmt.Sprintf("%d#%s: %s\n", l.Num, l.Tag, l.Content))
		}
	}
	return sb.String()
}

// GetTag returns the tag for a specific line number (1-indexed).
func (tf *TaggedFile) GetTag(lineNum int) (Tag, bool) {
	if lineNum < 1 || lineNum > len(tf.Lines) {
		return "", false
	}
	return tf.Lines[lineNum-1].Tag, true
}

// EditRequest describes a proposed edit with hash verification.
type EditRequest struct {
	Path       string `json:"path"`
	StartLine  int    `json:"start_line"`   // 1-indexed
	EndLine    int    `json:"end_line"`      // 1-indexed, inclusive
	ExpectedTags []Tag `json:"expected_tags"` // tags the agent saw when reading
	NewContent []string `json:"new_content"`  // replacement lines
}

// EditResult describes the outcome of a verified edit.
type EditResult struct {
	Applied   bool     `json:"applied"`
	Conflicts []string `json:"conflicts,omitempty"` // human-readable conflict descriptions
}

// Verifier validates edits against current file content using hash tags.
// Thread-safe: multiple agents can verify concurrently; file writes are serialized.
type Verifier struct {
	mu sync.Mutex
}

// NewVerifier creates a new edit verifier.
func NewVerifier() *Verifier {
	return &Verifier{}
}

// Verify checks that an edit's expected tags match the current file content.
// Returns conflicts if any lines have changed since the agent read them.
func (v *Verifier) Verify(req EditRequest) EditResult {
	v.mu.Lock()
	defer v.mu.Unlock()

	tf, err := TagFile(req.Path)
	if err != nil {
		return EditResult{Applied: false, Conflicts: []string{fmt.Sprintf("cannot read file: %v", err)}}
	}

	// Validate line range
	if req.StartLine < 1 || req.EndLine > len(tf.Lines) || req.StartLine > req.EndLine {
		return EditResult{
			Applied:   false,
			Conflicts: []string{fmt.Sprintf("invalid line range %d-%d (file has %d lines)", req.StartLine, req.EndLine, len(tf.Lines))},
		}
	}

	// Check tags match
	rangeLen := req.EndLine - req.StartLine + 1
	if len(req.ExpectedTags) != rangeLen {
		return EditResult{
			Applied:   false,
			Conflicts: []string{fmt.Sprintf("expected %d tags for lines %d-%d, got %d", rangeLen, req.StartLine, req.EndLine, len(req.ExpectedTags))},
		}
	}

	var conflicts []string
	for i := 0; i < rangeLen; i++ {
		lineNum := req.StartLine + i
		currentTag := tf.Lines[lineNum-1].Tag
		expectedTag := req.ExpectedTags[i]
		if currentTag != expectedTag {
			conflicts = append(conflicts, fmt.Sprintf(
				"line %d: content changed (expected tag %s, current tag %s: %q)",
				lineNum, expectedTag, currentTag, tf.Lines[lineNum-1].Content,
			))
		}
	}

	if len(conflicts) > 0 {
		return EditResult{Applied: false, Conflicts: conflicts}
	}

	// Apply the edit
	if err := applyEdit(tf, req); err != nil {
		return EditResult{Applied: false, Conflicts: []string{fmt.Sprintf("apply failed: %v", err)}}
	}

	return EditResult{Applied: true}
}

// applyEdit writes the modified file content.
func applyEdit(tf *TaggedFile, req EditRequest) error {
	lines := make([]string, 0, len(tf.Lines)+len(req.NewContent))
	for _, l := range tf.Lines[:req.StartLine-1] {
		lines = append(lines, l.Content)
	}
	lines = append(lines, req.NewContent...)
	if req.EndLine < len(tf.Lines) {
		for _, l := range tf.Lines[req.EndLine:] {
			lines = append(lines, l.Content)
		}
	}

	content := strings.Join(lines, "\n")
	if len(tf.Lines) > 0 {
		content += "\n"
	}

	tmp := tf.Path + ".hashline.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, tf.Path)
}
