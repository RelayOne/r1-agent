package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/harness"
	"github.com/RelayOne/r1/internal/ledger"
)

// setupRoleTemplateHarness builds a NewWithRoleTemplates harness — the
// production spawn path carrying the full concern role-template registry —
// over a ledger seeded with a mission and task node, and returns the task
// node ID for TaskDAGScope.
func setupRoleTemplateHarness(t *testing.T) (*harness.Harness, string) {
	t.Helper()
	ctx := context.Background()

	tmp := t.TempDir()
	l, err := ledger.New(tmp + "/ledger")
	if err != nil {
		t.Fatal(err)
	}
	b, err := bus.New(tmp + "/bus")
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b.Close()
		l.Close()
	})

	mustContent := func(v map[string]string) json.RawMessage {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	if _, err := l.AddNode(ctx, ledger.Node{
		Type:          "mission",
		SchemaVersion: 1,
		CreatedBy:     "user",
		MissionID:     "m-role",
		Content:       mustContent(map[string]string{"goal": "Ship the payments retry queue"}),
	}); err != nil {
		t.Fatal(err)
	}
	taskID, err := l.AddNode(ctx, ledger.Node{
		Type:          "task",
		SchemaVersion: 1,
		CreatedBy:     "planner",
		MissionID:     "m-role",
		Content:       mustContent(map[string]string{"summary": "Implement retry backoff"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	h := harness.NewWithRoleTemplates(harness.Config{
		MissionID:    "m-role",
		DefaultModel: "claude-opus-4-6",
	}, l, b)
	return h, taskID
}

// TestNewWithRoleTemplates_DevRendersRoleTemplate is the A099/A100
// activation proof at the harness layer: the production spawn path selects
// dev/proposing's registered role template (dev_implementing_ticket),
// renders its ledger-backed sections, and the StanceRunner delivers the
// result to the model as the system prompt.
func TestNewWithRoleTemplates_DevRendersRoleTemplate(t *testing.T) {
	h, taskID := setupRoleTemplateHarness(t)
	ctx := context.Background()

	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "dev",
		Face:         "proposing",
		TaskDAGScope: taskID,
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	mock := &harness.MockProvider{Responses: []*harness.ChatResponse{{Content: "ack"}}}
	if _, err := h.NewStanceRunner(mock, nil, harness.RunnerConfig{}).Run(ctx, handle.ID, "implement it"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := mock.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	sp := calls[0].SystemPrompt
	for _, want := range []string{
		`<section name="original_user_intent">`,
		"Ship the payments retry queue",
		`<section name="task_dag_scope">`,
		"Implement retry backoff",
	} {
		if !strings.Contains(sp, want) {
			t.Errorf("SystemPrompt missing %q", want)
		}
	}
}

// TestNewWithRoleTemplates_CTOSnapshotConsultation proves a second role/face
// pair resolves through the registry: cto/reviewing selects
// templates.CTOSnapshotConsultation and renders its sections.
func TestNewWithRoleTemplates_CTOSnapshotConsultation(t *testing.T) {
	h, taskID := setupRoleTemplateHarness(t)
	ctx := context.Background()

	handle, err := h.SpawnStance(ctx, harness.SpawnRequest{
		Role:         "cto",
		Face:         "reviewing",
		TaskDAGScope: taskID,
	})
	if err != nil {
		t.Fatalf("SpawnStance: %v", err)
	}

	mock := &harness.MockProvider{Responses: []*harness.ChatResponse{{Content: "reviewed"}}}
	if _, err := h.NewStanceRunner(mock, nil, harness.RunnerConfig{}).Run(ctx, handle.ID, "consult on the snapshot"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := mock.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	sp := calls[0].SystemPrompt
	if !strings.Contains(sp, "CTO") {
		t.Error("SystemPrompt missing the CTO role prompt")
	}
	for _, want := range []string{
		`<section name="original_user_intent">`,
		"Ship the payments retry queue",
		`role="cto" face="reviewing"`,
	} {
		if !strings.Contains(sp, want) {
			t.Errorf("SystemPrompt missing %q", want)
		}
	}
}

// TestNewWithRoleTemplates_UncoveredRoleErrors documents the registry
// boundary honestly: roles with a system prompt but no registered concern
// template (lead_designer, vp_eng, sdm) fail the spawn instead of silently
// rendering nothing.
func TestNewWithRoleTemplates_UncoveredRoleErrors(t *testing.T) {
	h, taskID := setupRoleTemplateHarness(t)

	_, err := h.SpawnStance(context.Background(), harness.SpawnRequest{
		Role:         "sdm",
		Face:         "proposing",
		TaskDAGScope: taskID,
	})
	if err == nil {
		t.Fatal("expected spawn error for role without a registered template")
	}
	if !strings.Contains(err.Error(), "no template for") {
		t.Errorf("error = %v, want the concern registry miss", err)
	}
}
