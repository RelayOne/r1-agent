// Package viewport implements a constrained file viewport for agents.
// Inspired by SWE-agent's 100-line viewport pattern:
// - Agents see a fixed window of code at a time (default 100 lines)
// - Navigation via scroll up/down, goto line, search
// - Prevents context window pollution from large files
// - Forces agents to reason about what they need to see
//
// This is critical for large codebases: dumping entire files into context
// wastes tokens and dilutes attention. A viewport focuses the agent.
package viewport

import (
	"fmt"
	"os"
	"strings"
)

// Config controls viewport behavior.
type Config struct {
	Height    int // visible lines (default 100)
	Overlap   int // lines overlap on scroll (default 5)
	MaxFileKB int // skip files larger than this (default 1024)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Height:    100,
		Overlap:   5,
		MaxFileKB: 1024,
	}
}

// Viewport provides a scrollable window into a file.
type Viewport struct {
	config Config
	path   string
	lines  []string
	top    int // 0-indexed first visible line
}

// Open creates a viewport for a file.
func Open(path string, cfg Config) (*Viewport, error) {
	if cfg.Height == 0 {
		cfg = DefaultConfig()
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > int64(cfg.MaxFileKB)*1024 {
		return nil, fmt.Errorf("file too large: %d KB (max %d KB)", info.Size()/1024, cfg.MaxFileKB)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	return &Viewport{
		config: cfg,
		path:   path,
		lines:  lines,
		top:    0,
	}, nil
}

// FromString creates a viewport from raw content (for testing).
func FromString(content string, cfg Config) *Viewport {
	if cfg.Height == 0 {
		cfg = DefaultConfig()
	}
	return &Viewport{
		config: cfg,
		path:   "<string>",
		lines:  strings.Split(content, "\n"),
		top:    0,
	}
}

// View returns the currently visible lines with line numbers.
func (v *Viewport) View() string {
	end := v.top + v.config.Height
	if end > len(v.lines) {
		end = len(v.lines)
	}

	var b strings.Builder
	for i := v.top; i < end; i++ {
		fmt.Fprintf(&b, "%4d | %s\n", i+1, v.lines[i])
	}
	return b.String()
}

// VisibleLines returns the current visible line range (1-indexed, inclusive).
func (v *Viewport) VisibleLines() (start, end int) {
	end2 := v.top + v.config.Height
	if end2 > len(v.lines) {
		end2 = len(v.lines)
	}
	return v.top + 1, end2
}

// ScrollDown moves the viewport down by (height - overlap) lines.
func (v *Viewport) ScrollDown() bool {
	step := v.config.Height - v.config.Overlap
	if step < 1 {
		step = 1
	}
	newTop := v.top + step
	if newTop >= len(v.lines) {
		return false // already at bottom
	}
	v.top = newTop
	return true
}

// ScrollUp moves the viewport up by (height - overlap) lines.
func (v *Viewport) ScrollUp() bool {
	if v.top == 0 {
		return false
	}
	step := v.config.Height - v.config.Overlap
	if step < 1 {
		step = 1
	}
	v.top -= step
	if v.top < 0 {
		v.top = 0
	}
	return true
}

// GotoLine centers the viewport on the given line number (1-indexed).
func (v *Viewport) GotoLine(line int) {
	line-- // to 0-indexed
	if line < 0 {
		line = 0
	}
	if line >= len(v.lines) {
		line = len(v.lines) - 1
	}
	// Center the target line in the viewport
	v.top = line - v.config.Height/2
	if v.top < 0 {
		v.top = 0
	}
	if v.top+v.config.Height > len(v.lines) && len(v.lines) > v.config.Height {
		v.top = len(v.lines) - v.config.Height
	}
}

// Search finds the next occurrence of a string from the current position.
// Returns the 1-indexed line number, or 0 if not found.
func (v *Viewport) Search(query string) int {
	lower := strings.ToLower(query)
	// Search from current viewport position
	for i := v.top; i < len(v.lines); i++ {
		if strings.Contains(strings.ToLower(v.lines[i]), lower) {
			v.GotoLine(i + 1)
			return i + 1
		}
	}
	// Wrap around
	for i := 0; i < v.top; i++ {
		if strings.Contains(strings.ToLower(v.lines[i]), lower) {
			v.GotoLine(i + 1)
			return i + 1
		}
	}
	return 0
}

// SearchNext finds the next occurrence after the current viewport.
func (v *Viewport) SearchNext(query string) int {
	lower := strings.ToLower(query)
	startLine := v.top + 1 // skip current top line
	for i := startLine; i < len(v.lines); i++ {
		if strings.Contains(strings.ToLower(v.lines[i]), lower) {
			v.GotoLine(i + 1)
			return i + 1
		}
	}
	return 0
}

// TotalLines returns the total number of lines in the file.
func (v *Viewport) TotalLines() int {
	return len(v.lines)
}

// Path returns the file path.
func (v *Viewport) Path() string {
	return v.path
}

// AtTop returns true if the viewport is at the top.
func (v *Viewport) AtTop() bool {
	return v.top == 0
}

// AtBottom returns true if the viewport shows the last line.
func (v *Viewport) AtBottom() bool {
	return v.top+v.config.Height >= len(v.lines)
}

// Context returns a summary string for agent prompts.
func (v *Viewport) Context() string {
	start, end := v.VisibleLines()
	return fmt.Sprintf("[%s lines %d-%d of %d]", v.path, start, end, len(v.lines))
}

// GetLine returns a specific line (1-indexed).
func (v *Viewport) GetLine(num int) (string, bool) {
	idx := num - 1
	if idx < 0 || idx >= len(v.lines) {
		return "", false
	}
	return v.lines[idx], true
}

// GetRange returns lines in a range (1-indexed, inclusive).
func (v *Viewport) GetRange(start, end int) []string {
	start-- // to 0-indexed
	if start < 0 {
		start = 0
	}
	if end > len(v.lines) {
		end = len(v.lines)
	}
	if start >= end {
		return nil
	}
	result := make([]string, end-start)
	copy(result, v.lines[start:end])
	return result
}
