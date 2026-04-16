package skill

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/skillmfr"
)

func TestToManifest_PopulatesRequiredFields(t *testing.T) {
	s := &Skill{
		Name:        "api-design",
		Description: "REST API patterns",
		Keywords:    []string{"rest", "api", "endpoint"},
		Triggers:    []string{"when designing an API"},
		Content:     "# api-design\n\n> REST API patterns\n",
	}
	m, err := s.ToManifest()
	if err != nil {
		t.Fatalf("ToManifest err: %v", err)
	}
	if m.Name != "api-design" {
		t.Errorf("name=%q want api-design", m.Name)
	}
	if m.Version == "" {
		t.Errorf("version empty")
	}
	if m.Description != "REST API patterns" {
		t.Errorf("description=%q", m.Description)
	}
	if len(m.InputSchema) == 0 || len(m.OutputSchema) == 0 {
		t.Errorf("schemas empty")
	}
	if len(m.WhenToUse) < 1 {
		t.Errorf("whenToUse empty")
	}
	if len(m.WhenNotToUse) < 2 {
		t.Errorf("whenNotToUse len=%d want ≥2", len(m.WhenNotToUse))
	}
	if err := m.Validate(); err != nil {
		t.Errorf("derived manifest invalid: %v", err)
	}
}

func TestToManifest_FallsBackToContentFirstLine(t *testing.T) {
	s := &Skill{
		Name:    "foo",
		Content: "# foo\n\n> Fallback description from content\n\nMore stuff.",
	}
	m, err := s.ToManifest()
	if err != nil {
		t.Fatalf("ToManifest err: %v", err)
	}
	if !strings.Contains(m.Description, "foo") && !strings.Contains(m.Description, "Fallback") {
		t.Errorf("expected description pulled from content, got %q", m.Description)
	}
}

func TestToManifest_EmptyTriggersUsesDescription(t *testing.T) {
	s := &Skill{
		Name:        "bare",
		Description: "Bare minimum skill",
	}
	m, err := s.ToManifest()
	if err != nil {
		t.Fatalf("ToManifest err: %v", err)
	}
	if len(m.WhenToUse) != 1 || m.WhenToUse[0] != "Bare minimum skill" {
		t.Errorf("whenToUse=%v want [Bare minimum skill]", m.WhenToUse)
	}
}

func TestToManifest_NilSkillErrors(t *testing.T) {
	var s *Skill
	_, err := s.ToManifest()
	if err == nil {
		t.Fatal("expected error on nil skill")
	}
}

func TestBackfillManifests_RegistersAndSkips(t *testing.T) {
	sr := NewRegistry()
	sr.skills = map[string]*Skill{
		"a": {Name: "a", Description: "A skill", Keywords: []string{"x"}},
		"b": {Name: "b", Description: "B skill", Keywords: []string{"y"}},
	}
	mr := skillmfr.NewRegistry()
	registered, skipped, err := BackfillManifests(sr, mr)
	if err != nil {
		t.Fatalf("first backfill err: %v", err)
	}
	if registered != 2 || skipped != 0 {
		t.Errorf("first pass: registered=%d skipped=%d want 2/0", registered, skipped)
	}
	// Second pass should skip both.
	registered2, skipped2, err := BackfillManifests(sr, mr)
	if err != nil {
		t.Fatalf("second backfill err: %v", err)
	}
	if registered2 != 0 || skipped2 != 2 {
		t.Errorf("second pass: registered=%d skipped=%d want 0/2", registered2, skipped2)
	}
}

func TestBackfillManifests_BuiltinsAllValid(t *testing.T) {
	sr := NewRegistry()
	if err := sr.LoadBuiltins(); err != nil {
		t.Fatalf("LoadBuiltins: %v", err)
	}
	mr := skillmfr.NewRegistry()
	registered, skipped, err := BackfillManifests(sr, mr)
	if err != nil {
		t.Fatalf("backfill err: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped=%d on empty manifest registry", skipped)
	}
	if registered < 50 {
		t.Errorf("registered=%d; expected ~61 builtin skills", registered)
	}
	// Every registered manifest must validate.
	for _, m := range mr.List() {
		if err := m.Validate(); err != nil {
			t.Errorf("builtin %q manifest fails Validate: %v", m.Name, err)
		}
	}
}

func TestBackfillManifests_NilRegistryErrors(t *testing.T) {
	_, _, err := BackfillManifests(nil, skillmfr.NewRegistry())
	if err == nil {
		t.Fatal("expected error on nil skill registry")
	}
	_, _, err = BackfillManifests(NewRegistry(), nil)
	if err == nil {
		t.Fatal("expected error on nil manifest registry")
	}
}

func TestToManifest_MissingDescriptionAndContent(t *testing.T) {
	s := &Skill{Name: "emptiest"}
	m, err := s.ToManifest()
	if err != nil {
		t.Fatalf("ToManifest err: %v", err)
	}
	if m.Description == "" {
		t.Fatal("description should not be empty after fallbacks")
	}
}

func TestDerivedManifest_PassesSkillmfrValidate(t *testing.T) {
	// Defensive: confirm every declared skillmfr validation
	// rule passes on a minimally-populated skill.
	s := &Skill{
		Name:        "check",
		Description: "Minimal check",
	}
	m, _ := s.ToManifest()
	if err := m.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	// Specific error types shouldn't surface.
	if errors.Is(m.Validate(), skillmfr.ErrIncompleteManifest) {
		t.Error("unexpected ErrIncompleteManifest")
	}
}
