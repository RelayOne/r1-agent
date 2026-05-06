package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/rules"
	"github.com/RelayOne/r1/internal/rules/monitor"
)

func TestRulesListShowDisableAndTail(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	reg := rules.NewRepoRegistry(repo, nil)
	if _, err := reg.AddWithOptions(context.Background(), rules.AddRequest{
		Name: "block-prod",
		Text: "never call tool delete_branch with name matching ^prod$",
	}); err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}
	if err := monitor.NewRepo(repo).Record(monitor.Decision{
		ToolName: "delete_branch",
		Verdict:  "BLOCK",
		Reason:   "rule fired",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var out, errBuf bytes.Buffer
	if code := runRulesCmd([]string{"list", "--repo", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("list exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "block-prod") {
		t.Fatalf("list output missing rule name: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := runRulesCmd([]string{"show", "--repo", repo, "block-prod"}, &out, &errBuf); code != 0 {
		t.Fatalf("show exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "strategy: argument_validator") {
		t.Fatalf("show output missing strategy: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := runRulesCmd([]string{"disable", "--repo", repo, "block-prod"}, &out, &errBuf); code != 0 {
		t.Fatalf("disable exit=%d stderr=%q", code, errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := runRulesCmd([]string{"tail", "--repo", repo, "--follow=false", "--last", "1"}, &out, &errBuf); code != 0 {
		t.Fatalf("tail exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "delete_branch") {
		t.Fatalf("tail output missing decision: %q", out.String())
	}
}
