// notebook_tools_test.go — tests for notebook_read and notebook_cell_run tools (T-R1P-005).
package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestNotebook writes a minimal .ipynb file with one code and one markdown cell.
func writeTestNotebook(t *testing.T, dir, name string) string {
	t.Helper()
	nb := map[string]interface{}{
		"nbformat": 4,
		"cells": []map[string]interface{}{
			{
				"cell_type": "code",
				"source":    "x = 42",
				"outputs":   []interface{}{},
				"execution_count": 1,
			},
			{
				"cell_type": "markdown",
				"source":    "## heading",
				"outputs":   []interface{}{},
			},
		},
	}
	data, err := json.Marshal(nb)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNotebookReadBasic(t *testing.T) {
	dir := t.TempDir()
	writeTestNotebook(t, dir, "nb.ipynb")

	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "notebook_read", toJSON(map[string]string{"path": "nb.ipynb"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Cell 1") {
		t.Error("result should mention Cell 1")
	}
	if !strings.Contains(result, "x = 42") {
		t.Error("result should contain cell source")
	}
	if !strings.Contains(result, "markdown") {
		t.Error("result should contain markdown cell type")
	}
}

func TestNotebookReadMissing(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	_, err := r.Handle(context.Background(), "notebook_read", toJSON(map[string]string{"path": "no.ipynb"}))
	if err == nil {
		t.Error("expected error for missing notebook")
	}
}

func TestNotebookReadHandleRouting(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	_, err := r.Handle(context.Background(), "notebook_read", toJSON(map[string]string{"path": "x.ipynb"}))
	if err != nil && strings.Contains(err.Error(), "unknown tool") {
		t.Error("notebook_read not wired into Handle()")
	}
}

func TestNotebookCellRunJupyterAbsent(t *testing.T) {
	// If jupyter is absent (common in CI), expect a graceful "not available" message.
	dir := t.TempDir()
	writeTestNotebook(t, dir, "nb.ipynb")

	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "notebook_cell_run", toJSON(map[string]interface{}{
		"path":   "nb.ipynb",
		"source": "1+1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	// Either succeeds (jupyter present) or returns "not available" gracefully.
	if strings.Contains(result, "unknown tool") {
		t.Error("notebook_cell_run not wired into Handle()")
	}
}

func TestNotebookCellRunEmptySource(t *testing.T) {
	dir := t.TempDir()
	writeTestNotebook(t, dir, "nb.ipynb")

	r := NewRegistry(dir)
	_, err := r.Handle(context.Background(), "notebook_cell_run", toJSON(map[string]interface{}{
		"path":   "nb.ipynb",
		"source": "",
	}))
	if err == nil {
		t.Error("expected error for empty source")
	}
}
