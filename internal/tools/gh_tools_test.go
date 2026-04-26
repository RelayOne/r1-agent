// gh_tools_test.go — tests for GitHub CLI tools (T-R1P-016).
package tools

import (
	"context"
	"strings"
	"testing"
)

func TestGHPRListRouting(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "gh_pr_list", toJSON(map[string]interface{}{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "unknown tool") {
		t.Error("gh_pr_list not wired into Handle()")
	}
}

func TestGHPRDiffRequiresNumber(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	_, err := r.Handle(context.Background(), "gh_pr_diff", toJSON(map[string]interface{}{"number": 0}))
	if err == nil {
		t.Error("expected error when number is 0")
	}
}

func TestGHRunListRouting(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	result, err := r.Handle(context.Background(), "gh_run_list", toJSON(map[string]interface{}{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "unknown tool") {
		t.Error("gh_run_list not wired into Handle()")
	}
}

func TestGHRunViewRequiresID(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	_, err := r.Handle(context.Background(), "gh_run_view", toJSON(map[string]interface{}{"run_id": 0}))
	if err == nil {
		t.Error("expected error when run_id is 0")
	}
}

func TestGHRunViewRouting(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	// Valid run_id with gh absent should return "gh not available", not "unknown tool".
	result, err := r.Handle(context.Background(), "gh_run_view", toJSON(map[string]interface{}{"run_id": 1}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "unknown tool") {
		t.Error("gh_run_view not wired into Handle()")
	}
}
