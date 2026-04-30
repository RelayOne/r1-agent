package main

import (
	"bytes"
	"testing"
)

func TestCostReportEmptyDB(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("STOKE_EVENTS_DB", repo+"/events.db")
	var stdout, stderr bytes.Buffer
	code := runCostCmd([]string{"report", "--repo", repo, "--task", "task-1", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("stdout empty")
	}
}
