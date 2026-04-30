package wizard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWizardHeadlessOnRealSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "skill.md")
	if err := os.WriteFile(source, []byte("---\nname: demo\n---\nChecks coverage"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), RunOptions{
		Mode:         "headless",
		SourcePath:   source,
		SourceFormat: "r1-markdown-legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skill.SkillID == "" || result.Decisions.SessionID == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
