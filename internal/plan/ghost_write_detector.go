// Package plan — ghost_write_detector.go
//
// Spec-1 item 7: post-tool-use ghost-write detector.
//
// After any tool whose name matches /^(edit|write|str_replace|create_file|
// apply_patch)$/ returns success with a `path` / `file_path` / `target_file`
// input field, verify the file actually exists with non-zero size on
// disk. When the check fails, inject a user-role reminder telling the
// worker to re-run the edit with full contents (or declare BLOCKED).
// This catches the failure mode where a tool reports success (200 OK)
// but the file on disk is empty — either the model wrote "" as contents
// or the tool silently dropped the write.
//
// The detector is wired as an engine.ExtraMidturnCheck callback.
package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MidturnToolCall is the subset of a tool-call pair the ghost-write
// detector needs. Mirrors engine.MidturnToolCall but lives here to
// avoid an import cycle between plan → engine (native runner already
// imports plan for acceptance helpers). The adapter in the engine
// package copies its own MidturnToolCall into this shape at wire time.
type MidturnToolCall struct {
	Name    string
	Input   []byte
	Result  string
	IsError bool
}

// writeToolNameRE matches the tool names that write to the filesystem.
// Deliberately a closed set — we don't want to false-positive on `bash`
// (which can also create files, but the output is always "ran") or on
// `read_file`.
var writeToolNameRE = regexp.MustCompile(`^(edit|write|str_replace|create_file|apply_patch|write_file|edit_file)$`)

// pathFieldCandidates lists the JSON keys each write tool might use
// for its target path. Order matters: more specific first.
var pathFieldCandidates = []string{
	"file_path",
	"path",
	"target_file",
	"filename",
}

// GhostWriteEvent describes one ghost-write detection suitable for bus
// publication. Callers wire OnGhostWrite to their event emitter.
type GhostWriteEvent struct {
	ToolName string
	Path     string
	Reason   string // "missing", "empty"
}

// NewGhostWriteCheck returns an engine.RunSpec.ExtraMidturnCheck closure
// that scans the last assistant turn's tool calls for successful write
// ops whose target path is missing or empty on disk. When any are
// found, it returns a reminder string appended to the next user
// message as a [SUPERVISOR NOTE] (alongside the spec-faithfulness note
// when both fire).
//
// repoRoot: absolute path used to resolve relative file_path inputs.
// onDetected: optional bus-publish callback. nil = silent detection.
func NewGhostWriteCheck(repoRoot string, onDetected func(GhostWriteEvent)) func(tools []MidturnToolCall, turn int) string {
	return func(tools []MidturnToolCall, turn int) string {
		var reminders []string
		for _, tc := range tools {
			if tc.IsError {
				continue
			}
			if !writeToolNameRE.MatchString(tc.Name) {
				continue
			}
			path := extractPathFromToolInput(tc.Input)
			if path == "" {
				// Also try result — some tools echo the path they wrote.
				path = extractPathFromToolResult(tc.Result)
			}
			if path == "" {
				continue
			}
			abs := path
			if !filepath.IsAbs(abs) && repoRoot != "" {
				abs = filepath.Join(repoRoot, abs)
			}
			reason := classifyPath(abs)
			if reason == "" {
				continue // file exists with content — good
			}
			evt := GhostWriteEvent{ToolName: tc.Name, Path: path, Reason: reason}
			if onDetected != nil {
				onDetected(evt)
			}
			reminders = append(reminders,
				fmt.Sprintf("Ghost-write detected: %s is %s after your %s tool reported success. Re-run the edit with full file contents, or declare BLOCKED.",
					path, reason, tc.Name))
		}
		if len(reminders) == 0 {
			return ""
		}
		return strings.Join(reminders, "\n")
	}
}

// extractPathFromToolInput decodes the raw JSON tool input and
// returns the first field matching pathFieldCandidates.
func extractPathFromToolInput(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	for _, key := range pathFieldCandidates {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// extractPathFromToolResult is a best-effort fallback: some tool
// results include a quoted path like `wrote "src/foo.ts"`. Returns
// empty when nothing matches.
var resultPathRE = regexp.MustCompile(`(?:wrote|created|updated|edited)\s+["']?([^"'\s]+\.[a-zA-Z]{1,5})["']?`)

func extractPathFromToolResult(result string) string {
	m := resultPathRE.FindStringSubmatch(result)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// classifyPath returns "missing" if the path doesn't exist, "empty"
// if it's zero bytes, or "" if it's a real non-empty file.
func classifyPath(abs string) string {
	fi, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "" // inaccessible — not our signal to emit
	}
	if fi.IsDir() {
		return "" // directory operations can't ghost-write a file
	}
	if fi.Size() == 0 {
		return "empty"
	}
	return ""
}
