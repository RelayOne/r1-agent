package concern_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/concern"
	"github.com/ericmacdougall/stoke/internal/concern/templates"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func setupTestLedger(t *testing.T) (*ledger.Ledger, string) {
	t.Helper()
	dir := t.TempDir()
	ledgerDir := filepath.Join(dir, "ledger")
	if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.New(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l, dir
}

func setupTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bus")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func TestBuildConcernField_DevTicket(t *testing.T) {
	ctx := context.Background()
	l, _ := setupTestLedger(t)
	b := setupTestBus(t)

	// Seed a mission node.
	missionID, err := l.AddNode(ctx, ledger.Node{
		Type:          "mission",
		SchemaVersion: 1,
		CreatedBy:     "user",
		MissionID:     "m-1",
		Content:       mustJSON(map[string]string{"goal": "Build a REST API for user management"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed a task node.
	taskID, err := l.AddNode(ctx, ledger.Node{
		Type:          "task",
		SchemaVersion: 1,
		CreatedBy:     "planner",
		MissionID:     "m-1",
		Content:       mustJSON(map[string]string{"summary": "Implement user CRUD endpoints"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed a decision node.
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "decision",
		SchemaVersion: 1,
		CreatedBy:     "lead",
		MissionID:     "m-1",
		Content:       mustJSON(map[string]string{"rationale": "Use PostgreSQL for persistence"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed a skill node.
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "skill",
		SchemaVersion: 1,
		CreatedBy:     "system",
		MissionID:     "m-1",
		Content:       mustJSON(map[string]string{"description": "REST API scaffolding pattern"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = missionID // used for seeding

	builder := concern.NewBuilder(l, b)
	templates.RegisterAll(builder)

	scope := concern.Scope{
		MissionID: "m-1",
		TaskID:    taskID,
	}

	cf, err := builder.BuildConcernField(ctx, concern.RoleDev, concern.FaceProposing, scope)
	if err != nil {
		t.Fatal(err)
	}

	if cf.Role != concern.RoleDev {
		t.Errorf("role = %q, want %q", cf.Role, concern.RoleDev)
	}
	if cf.Face != concern.FaceProposing {
		t.Errorf("face = %q, want %q", cf.Face, concern.FaceProposing)
	}
	if len(cf.Sections) == 0 {
		t.Fatal("expected at least one section")
	}

	// Verify expected section names are present.
	sectionNames := make(map[string]bool)
	for _, s := range cf.Sections {
		sectionNames[s.Name] = true
	}
	for _, want := range []string{"original_user_intent", "task_dag_scope", "prior_decisions"} {
		if !sectionNames[want] {
			t.Errorf("missing section %q", want)
		}
	}

	// Verify content is populated.
	for _, s := range cf.Sections {
		if s.Name == "original_user_intent" && s.Content == "" {
			t.Error("original_user_intent section is empty")
		}
	}
}

func TestBuildConcernField_NoTemplate(t *testing.T) {
	ctx := context.Background()
	l, _ := setupTestLedger(t)
	b := setupTestBus(t)

	builder := concern.NewBuilder(l, b)
	// Don't register any templates.

	_, err := builder.BuildConcernField(ctx, concern.RoleDev, concern.FaceProposing, concern.Scope{MissionID: "m-1"})
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestBuildConcernField_RendersCorrectly(t *testing.T) {
	ctx := context.Background()
	l, _ := setupTestLedger(t)
	b := setupTestBus(t)

	_, err := l.AddNode(ctx, ledger.Node{
		Type:          "mission",
		SchemaVersion: 1,
		CreatedBy:     "user",
		MissionID:     "m-2",
		Content:       mustJSON(map[string]string{"goal": "Refactor auth module"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	taskID, err := l.AddNode(ctx, ledger.Node{
		Type:          "task",
		SchemaVersion: 1,
		CreatedBy:     "planner",
		MissionID:     "m-2",
		Content:       mustJSON(map[string]string{"summary": "Refactor auth flow"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	builder := concern.NewBuilder(l, b)
	templates.RegisterAll(builder)

	scope := concern.Scope{MissionID: "m-2", TaskID: taskID}

	cf, err := builder.BuildConcernField(ctx, concern.RoleDev, concern.FaceProposing, scope)
	if err != nil {
		t.Fatal(err)
	}

	rendered := concern.Render(cf)

	if rendered == "" {
		t.Fatal("rendered output is empty")
	}

	// Check for concern_field wrapper.
	if !contains(rendered, "<concern_field") {
		t.Error("missing <concern_field> tag")
	}
	if !contains(rendered, "</concern_field>") {
		t.Error("missing </concern_field> tag")
	}
	if !contains(rendered, `role="dev"`) {
		t.Error("missing role attribute")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
