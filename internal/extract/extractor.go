// Package extract parses structured content from LLM output.
// Inspired by Aider's edit block parsing and claw-code's output handling:
//
// LLMs return a mix of natural language and structured content:
// - Code blocks (```lang ... ```)
// - Tool calls (JSON objects with name/arguments)
// - Reasoning blocks (<thinking>...</thinking>)
// - File edits (SEARCH/REPLACE blocks, unified diffs)
// - Structured data (JSON, YAML embedded in text)
//
// This parser extracts all structured content from LLM responses,
// making it available for downstream processing.
package extract

import (
	"encoding/json"
	"regexp"
	"strings"
)

// BlockType classifies extracted content.
type BlockType string

const (
	BlockCode      BlockType = "code"
	BlockJSON      BlockType = "json"
	BlockToolCall  BlockType = "tool_call"
	BlockThinking  BlockType = "thinking"
	BlockEdit      BlockType = "edit"
	BlockText      BlockType = "text"
)

// Block is a chunk of extracted content.
type Block struct {
	Type     BlockType `json:"type"`
	Content  string    `json:"content"`
	Language string    `json:"language,omitempty"` // for code blocks
	File     string    `json:"file,omitempty"`     // for edits
	Meta     map[string]string `json:"meta,omitempty"`
}

// ToolCall is a parsed tool invocation.
type ToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// EditBlock represents a SEARCH/REPLACE edit.
type EditBlock struct {
	File    string `json:"file"`
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

var (
	codeBlockRe    = regexp.MustCompile("(?s)```(\\w*)\\s*\\n(.*?)```")
	thinkingRe     = regexp.MustCompile("(?s)<thinking>(.*?)</thinking>")
	jsonObjectRe   = regexp.MustCompile("(?s)\\{[^{}]*(?:\\{[^{}]*\\}[^{}]*)*\\}")
	editBlockRe    = regexp.MustCompile("(?s)<<<<<<< SEARCH\\n(.*?)=======\\n(.*?)>>>>>>> REPLACE")
	fileEditRe     = regexp.MustCompile("(?m)^(.+?)\\n<<<<<<< SEARCH")
)

// ExtractAll parses all structured blocks from LLM output.
func ExtractAll(text string) []Block {
	var blocks []Block

	// Extract thinking blocks
	for _, m := range thinkingRe.FindAllStringSubmatch(text, -1) {
		blocks = append(blocks, Block{
			Type:    BlockThinking,
			Content: strings.TrimSpace(m[1]),
		})
	}

	// Extract code blocks
	for _, m := range codeBlockRe.FindAllStringSubmatch(text, -1) {
		lang := m[1]
		content := m[2]
		btype := BlockCode
		if lang == "json" {
			btype = BlockJSON
		}
		blocks = append(blocks, Block{
			Type:     btype,
			Language: lang,
			Content:  strings.TrimSpace(content),
		})
	}

	// Extract SEARCH/REPLACE edit blocks
	edits := ExtractEdits(text)
	for _, edit := range edits {
		blocks = append(blocks, Block{
			Type:    BlockEdit,
			File:    edit.File,
			Content: edit.Search + "\n---\n" + edit.Replace,
			Meta:    map[string]string{"search": edit.Search, "replace": edit.Replace},
		})
	}

	return blocks
}

// ExtractCode extracts all code blocks from LLM output.
func ExtractCode(text string) []Block {
	var blocks []Block
	for _, m := range codeBlockRe.FindAllStringSubmatch(text, -1) {
		blocks = append(blocks, Block{
			Type:     BlockCode,
			Language: m[1],
			Content:  strings.TrimSpace(m[2]),
		})
	}
	return blocks
}

// ExtractCodeByLang extracts code blocks of a specific language.
func ExtractCodeByLang(text, lang string) []string {
	var result []string
	for _, m := range codeBlockRe.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(m[1], lang) {
			result = append(result, strings.TrimSpace(m[2]))
		}
	}
	return result
}

// ExtractFirstCode returns the first code block content, or empty string.
func ExtractFirstCode(text string) string {
	m := codeBlockRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[2])
}

// ExtractJSON finds and parses JSON objects from text.
func ExtractJSON(text string) []map[string]any {
	var results []map[string]any

	// First try code blocks marked as JSON
	for _, code := range ExtractCodeByLang(text, "json") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(code), &obj); err == nil {
			results = append(results, obj)
		}
	}

	// Then try inline JSON objects
	for _, m := range jsonObjectRe.FindAllString(text, -1) {
		var obj map[string]any
		if err := json.Unmarshal([]byte(m), &obj); err == nil {
			// Avoid duplicates from code blocks
			if !isDuplicate(results, obj) {
				results = append(results, obj)
			}
		}
	}

	return results
}

// ExtractFirstJSON returns the first valid JSON object, or nil.
func ExtractFirstJSON(text string) map[string]any {
	results := ExtractJSON(text)
	if len(results) == 0 {
		return nil
	}
	return results[0]
}

// ExtractToolCalls parses tool call JSON from LLM output.
// Expects objects with "name" and "arguments" or "input" fields.
func ExtractToolCalls(text string) []ToolCall {
	var calls []ToolCall
	for _, obj := range ExtractJSON(text) {
		name, _ := obj["name"].(string)
		if name == "" {
			continue
		}

		var args map[string]any
		if a, ok := obj["arguments"].(map[string]any); ok {
			args = a
		} else if a, ok := obj["input"].(map[string]any); ok {
			args = a
		}
		if args == nil {
			continue
		}

		calls = append(calls, ToolCall{Name: name, Arguments: args})
	}
	return calls
}

// ExtractEdits parses SEARCH/REPLACE edit blocks.
func ExtractEdits(text string) []EditBlock {
	var edits []EditBlock

	// Find file names before SEARCH markers
	parts := strings.Split(text, "<<<<<<< SEARCH")
	for i := 1; i < len(parts); i++ {
		// File name is on the line before the marker
		before := parts[i-1]
		lines := strings.Split(strings.TrimRight(before, "\n"), "\n")
		file := ""
		if len(lines) > 0 {
			file = strings.TrimSpace(lines[len(lines)-1])
		}

		// Find the REPLACE content
		replParts := strings.SplitN(parts[i], "=======", 2)
		if len(replParts) != 2 {
			continue
		}
		search := strings.TrimPrefix(replParts[0], "\n")
		search = strings.TrimSuffix(search, "\n")

		endParts := strings.SplitN(replParts[1], ">>>>>>> REPLACE", 2)
		if len(endParts) < 1 {
			continue
		}
		replace := strings.TrimPrefix(endParts[0], "\n")
		replace = strings.TrimSuffix(replace, "\n")

		edits = append(edits, EditBlock{
			File:    file,
			Search:  search,
			Replace: replace,
		})
	}

	return edits
}

// ExtractThinking extracts thinking/reasoning blocks.
func ExtractThinking(text string) []string {
	var result []string
	for _, m := range thinkingRe.FindAllStringSubmatch(text, -1) {
		result = append(result, strings.TrimSpace(m[1]))
	}
	return result
}

// StripCode removes code blocks from text, leaving just prose.
func StripCode(text string) string {
	result := codeBlockRe.ReplaceAllString(text, "")
	result = thinkingRe.ReplaceAllString(result, "")
	// Clean up multiple blank lines
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(result)
}

// SplitResponse separates prose from code in a response.
func SplitResponse(text string) (prose string, code []Block) {
	code = ExtractCode(text)
	prose = StripCode(text)
	return
}

// isDuplicate checks if an object is already in the list (by JSON comparison).
func isDuplicate(list []map[string]any, obj map[string]any) bool {
	objJSON, err := json.Marshal(obj)
	if err != nil {
		return false
	}
	for _, existing := range list {
		existJSON, err := json.Marshal(existing)
		if err != nil {
			continue
		}
		if string(objJSON) == string(existJSON) {
			return true
		}
	}
	return false
}
