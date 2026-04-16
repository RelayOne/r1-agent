package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/plan"
)

func TestExtractACTargets_FileExists(t *testing.T) {
	crit := plan.AcceptanceCriterion{FileExists: "packages/types/package.json"}
	got := extractACTargets(crit)
	want := []string{"packages/types/package.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_ContentMatch(t *testing.T) {
	crit := plan.AcceptanceCriterion{
		ContentMatch: &plan.ContentMatchCriterion{File: "package.json", Pattern: "clsx"},
	}
	got := extractACTargets(crit)
	want := []string{"package.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_GrepSingleFile(t *testing.T) {
	// Exact shape that broke run 40: grep -q pattern file.
	crit := plan.AcceptanceCriterion{
		Command: `grep -q "clsx" packages/design-tokens/package.json`,
	}
	got := extractACTargets(crit)
	want := []string{"packages/design-tokens/package.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_GrepMultipleFiles(t *testing.T) {
	crit := plan.AcceptanceCriterion{
		Command: `grep -l "turbo" root/package.json apps/web/package.json`,
	}
	got := extractACTargets(crit)
	sort.Strings(got)
	want := []string{"apps/web/package.json", "root/package.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_ChainedGrep(t *testing.T) {
	// AC4 in run 40: test -f file && grep X file && grep Y file.
	crit := plan.AcceptanceCriterion{
		Command: `test -f .github/workflows/ci.yml && grep -q 'lint:' .github/workflows/ci.yml && grep -q 'typecheck:' .github/workflows/ci.yml`,
	}
	got := extractACTargets(crit)
	want := []string{".github/workflows/ci.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_PnpmFilter(t *testing.T) {
	crit := plan.AcceptanceCriterion{
		Command: `pnpm install && pnpm --filter @sentinel/design-tokens build`,
	}
	got := extractACTargets(crit)
	want := []string{"@sentinel/design-tokens/package.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_TestF(t *testing.T) {
	crit := plan.AcceptanceCriterion{Command: `test -f turbo.json`}
	got := extractACTargets(crit)
	want := []string{"turbo.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_BracketFile(t *testing.T) {
	crit := plan.AcceptanceCriterion{Command: `[ -f turbo.json ]`}
	got := extractACTargets(crit)
	want := []string{"turbo.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractACTargets_UnrecognizedShape(t *testing.T) {
	crit := plan.AcceptanceCriterion{Command: `go test ./...`}
	if got := extractACTargets(crit); len(got) != 0 {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestExtractACTargets_PnpmInstallHasNoTarget(t *testing.T) {
	crit := plan.AcceptanceCriterion{Command: `pnpm install`}
	if got := extractACTargets(crit); len(got) != 0 {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestAcTargetBlurb_Empty(t *testing.T) {
	crit := plan.AcceptanceCriterion{Command: `go test ./...`}
	if blurb := acTargetBlurb(crit); blurb != "" {
		t.Fatalf("expected empty blurb, got %q", blurb)
	}
}

func TestAcTargetBlurb_Single(t *testing.T) {
	crit := plan.AcceptanceCriterion{Command: `grep -q "clsx" packages/design-tokens/package.json`}
	blurb := acTargetBlurb(crit)
	if !strings.Contains(blurb, "EDIT TARGET (") {
		t.Errorf("missing singular header in blurb %q", blurb)
	}
	if !strings.Contains(blurb, "packages/design-tokens/package.json") {
		t.Errorf("missing target path in blurb %q", blurb)
	}
}

func TestFindCriterionByID(t *testing.T) {
	sess := plan.Session{
		AcceptanceCriteria: []plan.AcceptanceCriterion{
			{ID: "AC1", Description: "first"},
			{ID: "AC2", Description: "second"},
		},
	}
	c, ok := findCriterionByID(sess, "AC2")
	if !ok || c.Description != "second" {
		t.Fatalf("lookup AC2: ok=%v desc=%q", ok, c.Description)
	}
	if _, ok := findCriterionByID(sess, "AC99"); ok {
		t.Fatalf("expected miss for AC99")
	}
}
