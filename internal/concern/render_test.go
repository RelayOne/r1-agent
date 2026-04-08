package concern

import (
	"strings"
	"testing"
)

func TestRender_BasicOutput(t *testing.T) {
	cf := &ConcernField{
		Role: RoleDev,
		Face: FaceProposing,
		Scope: Scope{
			MissionID: "m-1",
			TaskID:    "task-abc123",
		},
		Sections: []Section{
			{Name: "original_user_intent", Content: "Build a REST API"},
			{Name: "prior_decisions", Content: "- Use PostgreSQL\n- Use Go"},
		},
	}

	out := Render(cf)

	if !strings.Contains(out, `<concern_field role="dev" face="proposing" scope="task-abc123">`) {
		t.Errorf("missing or incorrect concern_field opening tag in:\n%s", out)
	}
	if !strings.Contains(out, `</concern_field>`) {
		t.Error("missing </concern_field> closing tag")
	}
	if !strings.Contains(out, `<section name="original_user_intent">`) {
		t.Error("missing original_user_intent section tag")
	}
	if !strings.Contains(out, "Build a REST API") {
		t.Error("missing user intent content")
	}
	if !strings.Contains(out, `<section name="prior_decisions">`) {
		t.Error("missing prior_decisions section tag")
	}
	if !strings.Contains(out, "</section>") {
		t.Error("missing </section> closing tag")
	}
}

func TestRender_SkillsObservability(t *testing.T) {
	cf := &ConcernField{
		Role: RoleDev,
		Face: FaceProposing,
		Scope: Scope{MissionID: "m-1"},
		Sections: []Section{
			{Name: "applicable_skills", Content: "- REST scaffolding pattern"},
		},
	}

	out := Render(cf)

	if !strings.Contains(out, "<skills_observability>") {
		t.Error("missing skills_observability block when applicable_skills present")
	}
	if !strings.Contains(out, "skill.applied") {
		t.Error("missing skill.applied instruction in observability block")
	}
}

func TestRender_NoSkillsNoObservability(t *testing.T) {
	cf := &ConcernField{
		Role: RoleDev,
		Face: FaceProposing,
		Scope: Scope{MissionID: "m-1"},
		Sections: []Section{
			{Name: "original_user_intent", Content: "Do something"},
		},
	}

	out := Render(cf)

	if strings.Contains(out, "<skills_observability>") {
		t.Error("skills_observability should not appear without applicable_skills section")
	}
}

func TestRender_EmptySkillsNoObservability(t *testing.T) {
	cf := &ConcernField{
		Role: RoleDev,
		Face: FaceProposing,
		Scope: Scope{MissionID: "m-1"},
		Sections: []Section{
			{Name: "applicable_skills", Content: ""},
		},
	}

	out := Render(cf)

	if strings.Contains(out, "<skills_observability>") {
		t.Error("skills_observability should not appear when applicable_skills content is empty")
	}
}

func TestRender_ScopeRendering(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		want  string
	}{
		{"task scoped", Scope{MissionID: "m-1", TaskID: "task-x"}, `scope="task-x"`},
		{"mission scoped", Scope{MissionID: "m-1"}, `scope="m-1"`},
		{"global", Scope{}, `scope="global"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := &ConcernField{
				Role:     RoleDev,
				Face:     FaceProposing,
				Scope:    tt.scope,
				Sections: []Section{{Name: "test", Content: "x"}},
			}
			out := Render(cf)
			if !strings.Contains(out, tt.want) {
				t.Errorf("output should contain %s, got:\n%s", tt.want, out)
			}
		})
	}
}

func TestRender_AllSections(t *testing.T) {
	cf := &ConcernField{
		Role:  RoleReviewer,
		Face:  FaceReviewing,
		Scope: Scope{MissionID: "m-1", TaskID: "t-1"},
		Sections: []Section{
			{Name: "a", Content: "content a"},
			{Name: "b", Content: "content b"},
			{Name: "c", Content: "content c"},
		},
	}

	out := Render(cf)

	for _, name := range []string{"a", "b", "c"} {
		tag := `<section name="` + name + `">`
		if !strings.Contains(out, tag) {
			t.Errorf("missing section tag %s", tag)
		}
	}

	// Count closing tags.
	count := strings.Count(out, "</section>")
	if count != 3 {
		t.Errorf("expected 3 </section> tags, got %d", count)
	}
}
