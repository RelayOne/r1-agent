package prompts

import (
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/vecindex"
)

// TestExecutePromptDoesNotMentionCommit ensures the execute prompt
// does not tell the model to commit, push, or rebase.
// These operations are handled by the harness after review.
func TestExecutePromptDoesNotMentionCommit(t *testing.T) {
	prompt := BuildExecutePrompt("test task", "", "")
	lower := strings.ToLower(prompt)

	forbidden := []string{
		"git add -a",
		"git commit",
		"git push",
		"git rebase",
	}
	for _, f := range forbidden {
		if strings.Contains(lower, f) {
			t.Errorf("execute prompt contains forbidden instruction %q", f)
		}
	}
}

// TestVerifyPromptDoesNotMentionGitMutation ensures the verify prompt
// does not tell the reviewer to use git commands that verify policy forbids.
func TestVerifyPromptDoesNotMentionGitMutation(t *testing.T) {
	prompt := BuildVerifyPrompt("test task", []string{"check 1"})
	lower := strings.ToLower(prompt)

	forbidden := []string{
		"git diff head~1",
		"git add",
		"git commit",
		"git push",
		"git checkout",
		"git stash",
	}
	for _, f := range forbidden {
		if strings.Contains(lower, f) {
			t.Errorf("verify prompt contains forbidden git instruction %q", f)
		}
	}

	// Verify it tells the reviewer NOT to use git
	if !strings.Contains(lower, "do not") || !strings.Contains(lower, "git") {
		t.Error("verify prompt should explicitly tell reviewer not to use git commands")
	}
}

// stubRetriever is a deterministic test retriever.
type stubRetriever struct {
	hits []vecindex.Hit
}

func (s stubRetriever) Retrieve(_ string, _ int) []vecindex.Hit { return s.hits }

func TestRenderRelevantTools_EmptyOnNilRetriever(t *testing.T) {
	got := RenderRelevantTools(nil, "anything", 5)
	if got != "" {
		t.Errorf("nil retriever should produce empty string, got %q", got)
	}
}

func TestRenderRelevantTools_EmptyOnEmptyQuery(t *testing.T) {
	got := RenderRelevantTools(stubRetriever{}, "   ", 5)
	if got != "" {
		t.Errorf("empty query should produce empty string, got %q", got)
	}
}

func TestRenderRelevantTools_EmptyOnNoHits(t *testing.T) {
	got := RenderRelevantTools(stubRetriever{hits: nil}, "query", 5)
	if got != "" {
		t.Errorf("zero-hit retriever should produce empty string, got %q", got)
	}
}

func TestRenderRelevantTools_FormatsHits(t *testing.T) {
	r := stubRetriever{hits: []vecindex.Hit{
		{Descriptor: vecindex.ToolDescriptor{Name: "search", Description: "Code search", Tags: []string{"find", "grep"}}, Score: 0.87},
		{Descriptor: vecindex.ToolDescriptor{Name: "edit", Description: "Apply patch", Tags: nil}, Score: 0.55},
	}}
	got := RenderRelevantTools(r, "find code", 2)
	if !strings.Contains(got, "Relevant capabilities") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "search") || !strings.Contains(got, "edit") {
		t.Errorf("missing hit names: %q", got)
	}
	if !strings.Contains(got, "0.87") || !strings.Contains(got, "0.55") {
		t.Errorf("missing scores: %q", got)
	}
	if !strings.Contains(got, "find, grep") {
		t.Errorf("missing tags line for search: %q", got)
	}
}

func TestRenderRelevantTools_KZero(t *testing.T) {
	r := stubRetriever{hits: []vecindex.Hit{{Descriptor: vecindex.ToolDescriptor{Name: "x"}}}}
	got := RenderRelevantTools(r, "q", 0)
	if got != "" {
		t.Errorf("k=0 should short-circuit, got %q", got)
	}
}
