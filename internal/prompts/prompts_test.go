package prompts

import (
	"strings"
	"testing"
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
