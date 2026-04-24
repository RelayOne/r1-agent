// Package plan — content_judge.go
//
// LLM-based content-faithfulness judge. Given a task's description, the
// SOW spec excerpt for that task, and the current on-disk contents of
// every file the task declared, returns a verdict on whether the files
// contain a real implementation of the task's required behavior or a
// plausible-looking placeholder.
//
// Purpose: close the hole left by deterministic stub-marker matching.
// A worker can write code that is free of TODO / FIXME / NotImplementedError
// markers yet still fails to implement the spec — e.g. a handler that
// returns a hardcoded success response without doing the real work, a
// Zod schema that accepts any input, a React component that renders an
// empty div, copy-pasted sibling code that doesn't match the task's
// required shape. No amount of regex matching catches those. A second
// LLM pass with full spec context does.
//
// Called defensively: only when the task is already suspicious (zombie-
// already-done state where zero writes happened this dispatch but all
// declared files exist on disk). This keeps cost bounded — we're not
// paying a second review LLM call on every task, only on the ones
// whose completion claim rests on pre-existing content.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// ContentJudgeVerdict is the output of a single JudgeDeclaredContent call.
type ContentJudgeVerdict struct {
	// Real is true when the content looks like a genuine implementation
	// of the task's required behavior. False when it looks like a
	// placeholder (stub function, hardcoded return, copy-pasted sibling,
	// trivial re-export pretending to be substantive logic, etc.).
	// When the judge is uncertain, Real defaults true (non-gating):
	// we want the judge to be a sharpshooter for obvious fakes, not
	// a second-guessing veto layer.
	Real bool `json:"real"`
	// Reason is the judge's one- or two-sentence explanation.
	Reason string `json:"reason"`
	// FakeFile is the relative path of the most obvious fake file
	// when Real is false. Empty when Real is true.
	FakeFile string `json:"fake_file,omitempty"`
}

// JudgeDeclaredContent calls the reasoning provider and returns a
// verdict on whether the declared files genuinely implement the
// task. Returns (nil, nil) when no provider is configured; callers
// treat that as "don't override" and continue.
//
// File contents are truncated to keep the prompt bounded; the judge
// is told about truncation so it doesn't penalize a long file for
// looking sparse at the tail.
func JudgeDeclaredContent(ctx context.Context, prov provider.Provider, model string, task Task, sowExcerpt string, repoRoot string) (*ContentJudgeVerdict, error) {
	if prov == nil {
		return nil, nil
	}
	if len(task.Files) == 0 {
		return &ContentJudgeVerdict{Real: true, Reason: "no declared files to judge"}, nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Load every declared file, truncated. Skip files that don't
	// exist or are empty — those should have been caught by the
	// zombie-missing classification before this function was called.
	type fileBlob struct {
		Path      string
		Content   string
		Truncated bool
	}
	var blobs []fileBlob
	const perFileBudget = 6000
	for _, rel := range task.Files {
		full := filepath.Join(repoRoot, rel)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		text := string(data)
		truncated := false
		if len(text) > perFileBudget {
			text = text[:perFileBudget] + "\n... (truncated)"
			truncated = true
		}
		blobs = append(blobs, fileBlob{Path: rel, Content: text, Truncated: truncated})
	}
	if len(blobs) == 0 {
		return &ContentJudgeVerdict{Real: true, Reason: "no file content could be loaded"}, nil
	}

	var b strings.Builder
	b.WriteString("You are auditing a task's declared output files for authenticity. A worker agent just claimed the task is complete without writing any new files — all declared files existed on disk before the worker started. Your job: decide whether the existing content genuinely implements the task's required behavior, or whether it is a plausible-looking placeholder (stub handler, hardcoded return, trivial re-export, empty component, copy-pasted sibling code).\n\n")
	b.WriteString("Return STRICT JSON with this shape and nothing else:\n")
	b.WriteString("{\n  \"real\": true|false,\n  \"reason\": \"one or two sentences\",\n  \"fake_file\": \"relative/path\"  // optional, only when real=false\n}\n\n")
	b.WriteString("Decision rules:\n")
	b.WriteString("- real=true when the files contain substantive logic that matches the task spec's required behavior, even if imperfect.\n")
	b.WriteString("- real=false when a file's content is clearly a placeholder: single-line trivial implementation, hardcoded return that doesn't match spec, empty function body, `return null` where real logic is required, `throw new Error('not implemented')`, copy-pasted identical logic from a sibling file, or a re-export that pretends to satisfy a complex spec.\n")
	b.WriteString("- DEFAULT TO real=true when uncertain. Only flag false when the fake pattern is obvious. A critic that vetoes on ambiguity produces noise; a critic that catches obvious fakes produces value.\n\n")

	fmt.Fprintf(&b, "TASK %s: %s\n\n", task.ID, task.Description)
	if strings.TrimSpace(sowExcerpt) != "" {
		b.WriteString("SOW SPEC EXCERPT (what this task is supposed to deliver):\n")
		if len(sowExcerpt) > 4000 {
			sowExcerpt = sowExcerpt[:4000] + "\n... (truncated)"
		}
		b.WriteString(sowExcerpt)
		b.WriteString("\n\n")
	}
	b.WriteString("DECLARED FILES (current on-disk content):\n")
	for _, bl := range blobs {
		fmt.Fprintf(&b, "\n--- %s ---\n", bl.Path)
		b.WriteString(bl.Content)
		if bl.Truncated {
			b.WriteString("\n")
		}
	}

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 3000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("content judge: %w", err)
	}
	// Use collectModelText so extended-thinking providers that return
	// only thinking / redacted_thinking blocks still yield usable text
	// for JSON parsing. Without this fallback the judge silently
	// defaults to Real=true on every suspect task when wired to a
	// thinking-emitting model — codex-review P2.
	raw, _ := collectModelText(resp)
	var verdict ContentJudgeVerdict
	_, parseErr := jsonutil.ExtractJSONInto(raw, &verdict)
	if parseErr != nil {
		// Non-JSON verdict: the judge MUST NOT fail the pipeline on
		// its own parse errors — that would make it a source of
		// spurious failures. Default to non-gating and surface the
		// parse failure in Reason so callers can see what happened.
		fallback := &ContentJudgeVerdict{
			Real:   true,
			Reason: fmt.Sprintf("judge returned non-JSON (%v); defaulting to non-gating", parseErr),
		}
		return fallback, nil
	}
	return &verdict, nil
}
