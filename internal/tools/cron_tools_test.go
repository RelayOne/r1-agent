package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// skipIfNoCrontab skips the test when crontab is not available on the system.
func skipIfNoCrontab(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("crontab"); err != nil {
		t.Skip("crontab not available on this system")
	}
}

func TestCronCreateAndList(t *testing.T) {
	skipIfNoCrontab(t)

	reg := NewRegistry(t.TempDir())
	ctx := context.Background()

	// Create a cron entry.
	result, err := reg.Handle(ctx, "cron_create", toJSON(map[string]string{
		"id":       "test-r1p-006",
		"schedule": "0 9 * * 1",
		"command":  "echo hello-from-r1",
	}))
	if err != nil {
		t.Fatalf("cron_create error: %v", err)
	}
	if !strings.Contains(result, "scheduled") {
		t.Errorf("cron_create result should confirm scheduling, got: %s", result)
	}

	// List — should see our entry.
	listResult, err := reg.Handle(ctx, "cron_list", toJSON(map[string]string{}))
	if err != nil {
		t.Fatalf("cron_list error: %v", err)
	}
	if !strings.Contains(listResult, "test-r1p-006") {
		t.Errorf("cron_list should show our entry, got: %s", listResult)
	}
	if !strings.Contains(listResult, "0 9 * * 1") {
		t.Errorf("cron_list should show schedule, got: %s", listResult)
	}

	// Delete.
	delResult, err := reg.Handle(ctx, "cron_delete", toJSON(map[string]string{"id": "test-r1p-006"}))
	if err != nil {
		t.Fatalf("cron_delete error: %v", err)
	}
	if !strings.Contains(delResult, "removed") {
		t.Errorf("cron_delete result should confirm removal, got: %s", delResult)
	}

	// List again — should no longer contain our entry.
	listAfter, err := reg.Handle(ctx, "cron_list", toJSON(map[string]string{}))
	if err != nil {
		t.Fatalf("cron_list after delete error: %v", err)
	}
	if strings.Contains(listAfter, "test-r1p-006") {
		t.Errorf("cron_list after delete should not show deleted entry, got: %s", listAfter)
	}
}

func TestCronCreateIdempotent(t *testing.T) {
	skipIfNoCrontab(t)

	reg := NewRegistry(t.TempDir())
	ctx := context.Background()

	// Create twice with same ID — should not duplicate.
	reg.Handle(ctx, "cron_create", toJSON(map[string]string{ //nolint:errcheck
		"id": "test-idem-r1p", "schedule": "0 1 * * *", "command": "echo first",
	}))
	reg.Handle(ctx, "cron_create", toJSON(map[string]string{ //nolint:errcheck
		"id": "test-idem-r1p", "schedule": "0 2 * * *", "command": "echo second",
	}))

	listResult, err := reg.Handle(ctx, "cron_list", toJSON(map[string]string{}))
	if err != nil {
		t.Fatalf("cron_list error: %v", err)
	}

	// Should appear exactly once.
	count := strings.Count(listResult, "test-idem-r1p")
	if count != 1 {
		t.Errorf("idempotent create: expected 1 occurrence of id, got %d in: %s", count, listResult)
	}
	// Should have the updated schedule.
	if !strings.Contains(listResult, "0 2 * * *") {
		t.Errorf("upserted cron should have updated schedule, got: %s", listResult)
	}

	// Cleanup.
	reg.Handle(ctx, "cron_delete", toJSON(map[string]string{"id": "test-idem-r1p"})) //nolint:errcheck
}

func TestCronDeleteMissing(t *testing.T) {
	skipIfNoCrontab(t)

	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "cron_delete",
		toJSON(map[string]string{"id": "no-such-entry-r1p"}))
	if err != nil {
		t.Fatalf("cron_delete of missing entry should not return Go error: %v", err)
	}
	if !strings.Contains(result, "no entry") {
		t.Errorf("cron_delete of missing entry should say no entry, got: %s", result)
	}
}

func TestCronCreateInvalidID(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "cron_create", toJSON(map[string]string{
		"id": "bad:id", "schedule": "* * * * *", "command": "echo x",
	}))
	if err == nil {
		t.Error("cron_create with colon in id should return error")
	}
	if !strings.Contains(err.Error(), "colon") {
		t.Errorf("error should mention colon, got: %v", err)
	}
}

func TestRemoveCronEntry(t *testing.T) {
	crontab := "# other entry\n0 5 * * * some-other-cmd\n" +
		"# r1-cron:my-job\n0 9 * * 1 echo hello\n" +
		"# r1-cron:other-job\n0 10 * * 2 echo world\n"

	result := removeCronEntry(crontab, "my-job")

	if strings.Contains(result, "my-job") {
		t.Errorf("removeCronEntry should remove the marker line, got: %s", result)
	}
	if strings.Contains(result, "0 9 * * 1 echo hello") {
		t.Errorf("removeCronEntry should remove the cron expression line, got: %s", result)
	}
	// Other entries must survive.
	if !strings.Contains(result, "other-job") {
		t.Errorf("removeCronEntry should keep other r1-cron entries, got: %s", result)
	}
	if !strings.Contains(result, "some-other-cmd") {
		t.Errorf("removeCronEntry should keep non-r1 entries, got: %s", result)
	}
}
