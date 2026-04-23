package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeACs is a small helper: returns a builder that ignores its args
// and yields the given ACs. Tests use this via DescentGate.acsBuilder
// to sidestep toolchain detection.
func fakeACs(acs []AcceptanceCriterion) func(string, []string) []AcceptanceCriterion {
	return func(_ string, _ []string) []AcceptanceCriterion {
		return acs
	}
}

func TestRun_AllPass(t *testing.T) {
	g := &DescentGate{
		Repo: t.TempDir(),
		acsBuilder: fakeACs([]AcceptanceCriterion{
			{ID: "ac.a", Command: "exit 0"},
			{ID: "ac.b", Command: "echo hi"},
		}),
	}
	v, err := g.Run(context.Background(), []string{"x.go"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(v.Outcomes) != 2 {
		t.Fatalf("want 2 outcomes, got %d", len(v.Outcomes))
	}
	for _, o := range v.Outcomes {
		if !o.Passed {
			t.Fatalf("AC %s: want passed, got failed (stderr=%q)", o.AC.ID, o.Stderr)
		}
	}
	if v.SoftPass || v.EditPrompt || v.FatalErr != nil {
		t.Fatalf("all-pass verdict should have no soft flags / err: %+v", v)
	}
}

func TestRun_FailNoRepair_AsksOperator_Retry(t *testing.T) {
	asked := 0
	g := &DescentGate{
		Repo: t.TempDir(),
		acsBuilder: fakeACs([]AcceptanceCriterion{
			{ID: "ac.fail", Command: "exit 1"},
		}),
		Ask: func(_ context.Context, _ string) string {
			asked++
			return "retry"
		},
	}
	v, err := g.Run(context.Background(), []string{"x.go"})
	if err == nil {
		t.Fatalf("expected err, got nil; verdict=%+v", v)
	}
	if v.FatalErr == nil {
		t.Fatalf("expected FatalErr on verdict")
	}
	if asked != 1 {
		t.Fatalf("Ask should have fired once, got %d", asked)
	}
	// Final outcome is the retry attempt, which must also have failed.
	if len(v.Outcomes) == 0 || v.Outcomes[len(v.Outcomes)-1].Passed {
		t.Fatalf("expected final outcome to be a failure: %+v", v.Outcomes)
	}
}

func TestRun_FailWithRepair_PassesAfterRepair(t *testing.T) {
	// The AC shells `sh script.sh` where script.sh initially `exit 1`s.
	// RepairFunc rewrites the file to `exit 0`. The second invocation
	// should therefore pass.
	dir := t.TempDir()
	script := filepath.Join(dir, "gate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	repaired := false
	g := &DescentGate{
		Repo: dir,
		acsBuilder: fakeACs([]AcceptanceCriterion{
			{ID: "ac.repair", Command: "sh " + script},
		}),
		RepairFunc: func(_ context.Context, _ AcceptanceCriterion, _ string) error {
			repaired = true
			return os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755)
		},
		Ask: func(_ context.Context, _ string) string {
			t.Fatal("Ask should not be reached when repair succeeds")
			return ""
		},
	}
	v, err := g.Run(context.Background(), []string{"x.go"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !repaired {
		t.Fatalf("RepairFunc was not invoked")
	}
	if len(v.Outcomes) != 1 {
		t.Fatalf("want 1 outcome, got %d", len(v.Outcomes))
	}
	o := v.Outcomes[0]
	if !o.Passed {
		t.Fatalf("expected pass after repair, got %+v", o)
	}
	if !o.RepairTried {
		t.Fatalf("expected RepairTried=true on post-repair outcome")
	}
}

func TestRun_SoftPass_AcceptAsIs(t *testing.T) {
	g := &DescentGate{
		Repo: t.TempDir(),
		acsBuilder: fakeACs([]AcceptanceCriterion{
			{ID: "ac.soft", Command: "exit 1"},
		}),
		RepairFunc: func(_ context.Context, _ AcceptanceCriterion, _ string) error {
			return errors.New("cannot repair")
		},
		Ask: func(_ context.Context, _ string) string {
			return "accept-as-is"
		},
	}
	v, err := g.Run(context.Background(), []string{"x.go"})
	if err != nil {
		t.Fatalf("accept-as-is should return nil err, got %v", err)
	}
	if !v.SoftPass {
		t.Fatalf("expected SoftPass=true")
	}
	if v.FatalErr != nil {
		t.Fatalf("no FatalErr expected on soft-pass: %v", v.FatalErr)
	}
}

func TestRun_EditPrompt(t *testing.T) {
	g := &DescentGate{
		Repo: t.TempDir(),
		acsBuilder: fakeACs([]AcceptanceCriterion{
			{ID: "ac.edit", Command: "exit 1"},
		}),
		RepairFunc: func(_ context.Context, _ AcceptanceCriterion, _ string) error {
			return errors.New("cannot repair")
		},
		Ask: func(_ context.Context, _ string) string {
			return "edit-prompt"
		},
	}
	v, err := g.Run(context.Background(), []string{"x.go"})
	if err != nil {
		t.Fatalf("edit-prompt should return nil err, got %v", err)
	}
	if !v.EditPrompt {
		t.Fatalf("expected EditPrompt=true")
	}
	if v.SoftPass {
		t.Fatalf("SoftPass must be false on edit-prompt")
	}
}

func TestRun_NoACs_NoOp(t *testing.T) {
	g := &DescentGate{
		Repo:       t.TempDir(),
		acsBuilder: fakeACs(nil),
	}
	v, err := g.Run(context.Background(), []string{"README.md"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(v.Outcomes) != 0 || v.SoftPass || v.EditPrompt || v.FatalErr != nil {
		t.Fatalf("expected empty verdict, got %+v", v)
	}
}

func TestRun_LogsStreamed(t *testing.T) {
	var logs []string
	g := &DescentGate{
		Repo: t.TempDir(),
		OnLog: func(line string) {
			logs = append(logs, line)
		},
		acsBuilder: fakeACs([]AcceptanceCriterion{
			{ID: "ac.one", Command: "exit 0"},
			{ID: "ac.two", Command: "exit 0"},
		}),
	}
	if _, err := g.Run(context.Background(), []string{"x.go"}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(logs) < 2 {
		t.Fatalf("expected ≥2 log lines (one per AC), got %d: %v", len(logs), logs)
	}
}
