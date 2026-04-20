package plan

import (
	"strings"
	"testing"
)

func TestScrubSOW_H64_JSONFileExistsCompound(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{
				ID: "S2",
				AcceptanceCriteria: []AcceptanceCriterion{
					{
						ID:      "AC6",
						Command: `{"file_exists": "packages/types/src/auth.ts"} && {"file_exists": "packages/api-client/src/auth.ts"} && {"file_exists": "app/login/page.tsx"}`,
					},
				},
			},
		},
	}
	_, diag := ScrubSOW(sow)
	ac := sow.Sessions[0].AcceptanceCriteria[0]
	if strings.Contains(ac.Command, "{") || strings.Contains(ac.Command, "file_exists") {
		t.Fatalf("expected bare test -f form, got %q", ac.Command)
	}
	if !strings.Contains(ac.Command, "test -f packages/types/src/auth.ts") {
		t.Fatalf("expected first path rewritten, got %q", ac.Command)
	}
	if !strings.Contains(ac.Command, "test -f app/login/page.tsx") {
		t.Fatalf("expected third path rewritten, got %q", ac.Command)
	}
	if len(diag) < 2 {
		t.Fatalf("expected both normalize+rewrite diagnostics, got %d: %v", len(diag), diag)
	}
}

func TestScrubSOW_H64_JSONFileExistsSingle(t *testing.T) {
	sow := &SOW{
		Sessions: []Session{
			{
				ID: "S1",
				AcceptanceCriteria: []AcceptanceCriterion{
					{
						ID:      "AC1",
						Command: `{"file_exists": "src/foo.ts"}`,
					},
				},
			},
		},
	}
	_, _ = ScrubSOW(sow)
	ac := sow.Sessions[0].AcceptanceCriteria[0]
	if ac.Command != "" {
		t.Fatalf("expected Command promoted to empty, got %q", ac.Command)
	}
	if ac.FileExists != "src/foo.ts" {
		t.Fatalf("expected FileExists=src/foo.ts, got %q", ac.FileExists)
	}
}
