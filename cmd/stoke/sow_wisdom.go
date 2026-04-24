package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/jsonutil"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/wisdom"
)

// sessionWisdomExtractPrompt asks the model to distill a completed session
// into a handful of reusable learnings. The output is strict JSON the
// wisdom store can ingest.
const sessionWisdomExtractPrompt = `You just finished a session on an autonomous code-build task. Before moving on, distill what you learned into at most 8 concise, reusable facts that the NEXT session should know. Focus on project-specific knowledge, not general programming advice.

Categories (pick one per learning):
  - "pattern": a recurring convention you discovered in this codebase (e.g. "this project puts handlers under cmd/*/http")
  - "decision": an architectural or implementation choice made by this session (e.g. "token refresh uses a 10-minute leeway")
  - "gotcha": something surprising or failure-prone worth avoiding next time (e.g. "the lint target fails if go.sum isn't committed")

RULES:
1. Output ONLY a JSON object. No markdown fences, no prose.
2. Every learning description must be one sentence, specific, and actionable.
3. Include a file path when the learning is tied to a specific file.
4. Do NOT include generic facts ("use proper error handling", "write tests"). Only project-specific things.
5. Maximum 8 learnings. Prefer 3-5 high-quality entries over a long list.

Schema:

{
  "learnings": [
    {
      "category": "pattern | decision | gotcha",
      "description": "one sentence",
      "file": "optional: path/to/file"
    }
  ]
}

SESSION CONTEXT:
`

// extractWisdomResponse matches the expected LLM output.
type extractWisdomResponse struct {
	Learnings []struct {
		Category    string `json:"category"`
		Description string `json:"description"`
		File        string `json:"file,omitempty"`
	} `json:"learnings"`
}

// CaptureSessionWisdom asks the model to extract reusable learnings from a
// completed session and adds them to the wisdom store. Returns the number
// of learnings captured. Safe to call with a nil store (no-op) or a nil
// provider (returns 0, nil).
//
// The session-context blob includes: session title/description, task
// descriptions, acceptance criteria pass/fail, and the list of files
// touched (derived from declared task.Files + session.Outputs). The model
// then proposes learnings; we stamp the task ID on each and record them.
func CaptureSessionWisdom(ctx context.Context, session plan.Session, results []plan.TaskExecResult, acceptance []plan.AcceptanceResult, store *wisdom.Store, prov provider.Provider, model string, promptPrefix ...string) (int, error) {
	if store == nil || prov == nil {
		return 0, nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	ctxBlob := buildWisdomContext(session, results, acceptance)
	prefix := ""
	for _, p := range promptPrefix {
		if s := strings.TrimSpace(p); s != "" {
			if prefix != "" {
				prefix += "\n\n"
			}
			prefix += s
		}
	}
	if prefix != "" {
		prefix += "\n\n"
	}
	userText := prefix + sessionWisdomExtractPrompt + ctxBlob
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 6000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return 0, fmt.Errorf("wisdom extraction chat: %w", err)
	}

	raw := ""
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw += c.Text
		}
	}
	var out extractWisdomResponse
	if _, err := jsonutil.ExtractJSONInto(raw, &out); err != nil {
		return 0, fmt.Errorf("parse wisdom response: %w", err)
	}

	count := 0
	for _, l := range out.Learnings {
		if strings.TrimSpace(l.Description) == "" {
			continue
		}
		store.Record(session.ID, wisdom.Learning{
			Category:    wisdom.ParseCategory(l.Category),
			Description: l.Description,
			File:        l.File,
			ValidFrom:   time.Now(),
		})
		count++
	}
	return count, nil
}

// buildWisdomContext assembles a short blob describing the session so the
// extraction model has enough to work with.
func buildWisdomContext(session plan.Session, results []plan.TaskExecResult, acceptance []plan.AcceptanceResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SESSION %s: %s\n", session.ID, session.Title)
	if session.Description != "" {
		fmt.Fprintf(&b, "%s\n", session.Description)
	}
	b.WriteString("\nTASKS:\n")
	for _, t := range session.Tasks {
		status := "unknown"
		for _, r := range results {
			if r.TaskID == t.ID {
				if r.Success {
					status = "ok"
				} else {
					status = "failed"
				}
				break
			}
		}
		fmt.Fprintf(&b, "- [%s] %s: %s", status, t.ID, t.Description)
		if len(t.Files) > 0 {
			fmt.Fprintf(&b, " (files: %s)", strings.Join(t.Files, ", "))
		}
		b.WriteString("\n")
	}
	if len(acceptance) > 0 {
		b.WriteString("\nACCEPTANCE:\n")
		for _, a := range acceptance {
			status := "PASS"
			if !a.Passed {
				status = "FAIL"
			}
			fmt.Fprintf(&b, "- [%s] %s: %s\n", status, a.CriterionID, a.Description)
		}
	}
	return b.String()
}

// wisdomPathForSOW is the on-disk location of the persistent wisdom snapshot
// for a given SOW run. Stored under .stoke/wisdom/<sow-id>.json so a resume
// picks up the prior learnings automatically.
func wisdomPathForSOW(projectRoot, sowID string) string {
	safeID := strings.ReplaceAll(sowID, "/", "_")
	return filepath.Join(projectRoot, ".stoke", "wisdom", safeID+".json")
}

// SaveWisdom persists the wisdom store to disk. Atomic via temp + rename.
func SaveWisdom(projectRoot, sowID string, store *wisdom.Store) error {
	if store == nil {
		return nil
	}
	path := wisdomPathForSOW(projectRoot, sowID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store.Learnings(), "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadWisdom re-hydrates a wisdom store from disk. Returns an empty store
// if no prior snapshot exists.
func LoadWisdom(projectRoot, sowID string) (*wisdom.Store, error) {
	path := wisdomPathForSOW(projectRoot, sowID)
	store := wisdom.NewStore()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return store, err
	}
	var learnings []wisdom.Learning
	if err := json.Unmarshal(data, &learnings); err != nil {
		return store, err
	}
	for _, l := range learnings {
		store.Record(l.TaskID, l)
	}
	return store, nil
}
