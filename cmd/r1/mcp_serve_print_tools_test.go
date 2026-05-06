package main

// mcp_serve_print_tools_test.go — exercises `r1 mcp serve --print-tools`
// per spec 8 §12 item 9. The lint at tools/lint-view-without-api/ uses
// this output to learn the wire catalog without spawning the daemon.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/mcp"
)

func TestMCPServe_PrintToolsJSON_Returns38Tools(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{"--print-tools"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}
	var got []mcp.ToolDefinition
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v\nstdout=%s", err, stdout.String())
	}
	if len(got) != 38 {
		t.Errorf("got %d tools, want 38", len(got))
	}
}

func TestMCPServe_PrintToolsJSON_AllSchemasValid(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMCPServe([]string{"--print-tools"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got []mcp.ToolDefinition
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, td := range got {
		if !strings.HasPrefix(td.Name, "r1.") {
			t.Errorf("tool %q missing r1. prefix", td.Name)
		}
		if !json.Valid(td.InputSchema) {
			t.Errorf("tool %q has invalid InputSchema", td.Name)
		}
	}
}

func TestMCPServe_NoFlagsPrintsNoticeAndExitsNonzero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMCPServe([]string{}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit, got 0; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "back-end not yet wired") {
		t.Errorf("expected back-end-not-wired notice on stderr; got %q", stderr.String())
	}
}
