package stances

import "testing"

func TestAllReturnsElevenTemplates(t *testing.T) {
	all := All()
	if got := len(all); got != 11 {
		t.Fatalf("All() returned %d templates, want 11", got)
	}
}

func TestGetReturnsCorrectTemplate(t *testing.T) {
	roles := []struct {
		role        string
		displayName string
	}{
		{"product_owner", "Product Owner"},
		{"lead_engineer", "Lead Engineer"},
		{"lead_designer", "Lead Designer"},
		{"vp_eng", "VP Engineering"},
		{"cto", "CTO"},
		{"sdm", "SDM"},
		{"qa_lead", "QA Lead"},
		{"dev", "Developer"},
		{"reviewer", "Reviewer"},
		{"judge", "Judge"},
		{"stakeholder", "Stakeholder"},
	}
	for _, tc := range roles {
		t.Run(tc.role, func(t *testing.T) {
			tmpl, ok := Get(tc.role)
			if !ok {
				t.Fatalf("Get(%q) returned false", tc.role)
			}
			if tmpl.DisplayName != tc.displayName {
				t.Errorf("Get(%q).DisplayName = %q, want %q", tc.role, tmpl.DisplayName, tc.displayName)
			}
		})
	}
}

func TestGetReturnsFalseForUnknownRole(t *testing.T) {
	_, ok := Get("nonexistent_role")
	if ok {
		t.Fatal("Get(\"nonexistent_role\") returned true, want false")
	}
}

func TestAllTemplatesHaveNonEmptySystemPrompt(t *testing.T) {
	for role, tmpl := range All() {
		if len(tmpl.SystemPrompt) == 0 {
			t.Errorf("template %q has empty SystemPrompt", role)
		}
		if len(tmpl.SystemPrompt) < 500 {
			t.Errorf("template %q SystemPrompt is only %d chars, want at least 500", role, len(tmpl.SystemPrompt))
		}
	}
}

func TestAllTemplatesHaveValidConsensusPosture(t *testing.T) {
	valid := map[string]bool{
		"absolute_completion_and_quality": true,
		"balanced":                        true,
		"pragmatic":                       true,
	}
	for role, tmpl := range All() {
		if !valid[tmpl.ConsensusPosture] {
			t.Errorf("template %q has invalid ConsensusPosture %q", role, tmpl.ConsensusPosture)
		}
	}
}
