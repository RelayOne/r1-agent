// powershell_tools_test.go — tests for the powershell tool (T-R1P-015).
package tools

import (
	"context"
	"strings"
	"testing"
)

func TestPowerShellHandleRouting(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	// Routing check: an empty command should return an error, not "unknown tool".
	_, err := r.Handle(context.Background(), "powershell", toJSON(map[string]string{"command": ""}))
	if err != nil && strings.Contains(err.Error(), "unknown tool") {
		t.Error("powershell not wired into Handle()")
	}
}

func TestPowerShellEmptyCommand(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	_, err := r.Handle(context.Background(), "powershell", toJSON(map[string]string{"command": ""}))
	if err == nil {
		t.Error("expected error for empty command")
	}
}

func TestPowerShellGracefulAbsent(t *testing.T) {
	// When pwsh is absent the tool must return a "not available" string, not an error.
	dir := t.TempDir()
	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "powershell", toJSON(map[string]string{"command": "1+1"}))
	if err != nil {
		t.Fatal(err)
	}
	// Either executed (pwsh present) or gracefully degraded.
	if strings.Contains(result, "unknown tool") {
		t.Error("powershell not wired into Handle()")
	}
}
