package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHonestyCLI(t *testing.T) {
	repo := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runHonestyCmd([]string{
		"refuse", "--repo", repo, "--task", "task-1", "--claim", "LIVE-VERIFIED", "--reason", "missing curl evidence",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("refuse exit=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = runHonestyCmd([]string{"list", "--repo", repo, "--task", "task-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "missing curl evidence") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}
