package main

import (
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// TestBuildSOWPromptsContainsContract verifies the TRUTHFULNESS_CONTRACT
// and PRE_COMPLETION_GATE constants are injected into BOTH the standard
// dispatch and the repair-mode dispatch inside buildSOWNativePromptsWithOpts.
// Spec: specs/descent-hardening.md items 1 + 2 (C4 — no opt-out).
func TestBuildSOWPromptsContainsContract(t *testing.T) {
	sow := &plan.SOW{
		Name:        "test-project",
		Description: "test project for contract injection",
	}
	session := plan.Session{
		ID:          "S1",
		Title:       "test session",
		Description: "test session description",
	}
	task := plan.Task{
		ID:          "T1",
		Description: "implement something",
	}

	t.Run("standard dispatch contains contract and gate", func(t *testing.T) {
		sys, _ := buildSOWNativePromptsWithOpts(sow, session, task, promptOpts{})
		if !strings.Contains(sys, "TRUTHFULNESS CONTRACT (non-negotiable)") {
			t.Errorf("standard prompt missing TRUTHFULNESS CONTRACT header")
		}
		if !strings.Contains(sys, "BLOCKED is an honourable outcome") {
			t.Errorf("standard prompt missing BLOCKED clause from contract")
		}
		if !strings.Contains(sys, "PRE-COMPLETION GATE") {
			t.Errorf("standard prompt missing PRE-COMPLETION GATE header")
		}
		if !strings.Contains(sys, "<pre_completion>") {
			t.Errorf("standard prompt missing <pre_completion> XML marker")
		}
	})

	t.Run("repair dispatch contains contract and gate", func(t *testing.T) {
		directive := "fix the failing AC"
		sys, _ := buildSOWNativePromptsWithOpts(sow, session, task, promptOpts{
			Repair: &directive,
		})
		if !strings.Contains(sys, "TRUTHFULNESS CONTRACT (non-negotiable)") {
			t.Errorf("repair prompt missing TRUTHFULNESS CONTRACT header")
		}
		if !strings.Contains(sys, "PRE-COMPLETION GATE") {
			t.Errorf("repair prompt missing PRE-COMPLETION GATE header")
		}
		if !strings.Contains(sys, "<pre_completion>") {
			t.Errorf("repair prompt missing <pre_completion> XML marker")
		}
		// Contract must come BEFORE the REPAIR-mode preamble so
		// the model sees honesty rules first (spec item 1).
		contractIdx := strings.Index(sys, "TRUTHFULNESS CONTRACT")
		repairIdx := strings.Index(sys, "You are an autonomous coding agent in REPAIR mode")
		if contractIdx < 0 || repairIdx < 0 {
			t.Fatalf("missing one of the expected markers; contractIdx=%d repairIdx=%d", contractIdx, repairIdx)
		}
		if contractIdx >= repairIdx {
			t.Errorf("contract must precede REPAIR preamble; contract@%d repair@%d", contractIdx, repairIdx)
		}
	})

	t.Run("contract never removed by flag", func(t *testing.T) {
		// C4: no opt-out, no flag. Contract is always present even
		// when the caller sets every optional field.
		sys, _ := buildSOWNativePromptsWithOpts(sow, session, task, promptOpts{
			RepoMap:              nil,
			RepoMapBudget:        0,
			RawSOW:               "custom sow content",
			RepoRoot:             "/tmp/fake",
			LiveBuildState:       "",
			UniversalPromptBlock: "",
		})
		if !strings.Contains(sys, "TRUTHFULNESS CONTRACT") {
			t.Errorf("contract must be present regardless of opts")
		}
	})
}
