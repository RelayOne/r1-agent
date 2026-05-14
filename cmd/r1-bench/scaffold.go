// scaffold.go — `r1-bench --scaffold-mission` operator tool.
//
// Stubs out a new mission directory from an upstream task ID + a real
// gold patch file. Closes the documented gap in plans/corpus-100.md
// §"Authoring workflow": curators still own the plan decomposition +
// judge mode + required_symbols decisions, but the boilerplate
// (directory creation, README skeleton with provenance, mission.yaml
// scaffold with curator-fill blocks, gold.patch copy) is automated.
//
// Usage:
//
//	r1-bench --scaffold-mission \
//	  --upstream-task-id swebench-pro/django__django-12345 \
//	  --difficulty medium \
//	  --category bugfix \
//	  --patch /path/to/upstream.patch \
//	  --output internal/bench/golden/truthful-completion
//
// The command refuses to overwrite an existing mission directory —
// curators bump the upstream task ID or pick a different output path.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ScaffoldSpec describes one new-mission scaffold operation.
type ScaffoldSpec struct {
	UpstreamTaskID string
	Difficulty     string // easy|medium|hard
	Category       string // bugfix|feature|refactor|migration|test-addition
	PatchPath      string
	OutputRoot     string // typically internal/bench/golden/truthful-completion
	// MissionID is derived from UpstreamTaskID if empty.
	MissionID string
}

// validCategories + validDifficulties pin the closed sets the methodology
// doc + scorer recognise. The scaffold command refuses unknown values so
// a curator can't accidentally ship a mission with category="bug-fix"
// instead of "bugfix" and have it silently bucket into a different row
// on the leaderboard.
var validCategories = map[string]bool{
	"bugfix":         true,
	"feature":        true,
	"refactor":       true,
	"migration":      true,
	"test-addition":  true,
	"greenfield":     true, // matches the seed-hello-easy fixture
}

var validDifficulties = map[string]bool{
	"easy":   true,
	"medium": true,
	"hard":   true,
}

// ScaffoldMission writes the mission directory + README + mission.yaml +
// gold.patch under spec.OutputRoot. Returns the absolute path to the new
// mission directory.
//
// Refuses to overwrite an existing directory. The PatchPath, if non-empty,
// is copied to <missionDir>/gold.patch verbatim — the curator's job is to
// produce the patch upstream-style; the scaffold tool doesn't transform it.
func ScaffoldMission(spec ScaffoldSpec) (string, error) {
	if spec.UpstreamTaskID == "" {
		return "", errors.New("ScaffoldMission: --upstream-task-id is required")
	}
	if !validCategories[spec.Category] {
		return "", fmt.Errorf("ScaffoldMission: invalid category %q (allowed: bugfix, feature, refactor, migration, test-addition, greenfield)", spec.Category)
	}
	if !validDifficulties[spec.Difficulty] {
		return "", fmt.Errorf("ScaffoldMission: invalid difficulty %q (allowed: easy, medium, hard)", spec.Difficulty)
	}
	if spec.OutputRoot == "" {
		return "", errors.New("ScaffoldMission: --output (corpus root) is required")
	}

	missionID := spec.MissionID
	if missionID == "" {
		missionID = deriveMissionID(spec.UpstreamTaskID)
	}
	missionDir := filepath.Join(spec.OutputRoot, missionID)

	if _, err := os.Stat(missionDir); err == nil {
		return "", fmt.Errorf("ScaffoldMission: %s already exists (refusing to overwrite); pick a different mission ID or remove the existing directory", missionDir)
	}
	if err := os.MkdirAll(missionDir, 0o755); err != nil {
		return "", fmt.Errorf("ScaffoldMission: mkdir %s: %w", missionDir, err)
	}

	if err := os.WriteFile(filepath.Join(missionDir, "mission.yaml"), []byte(scaffoldMissionYAML(spec, missionID)), 0o644); err != nil {
		return "", fmt.Errorf("ScaffoldMission: write mission.yaml: %w", err)
	}
	if err := os.WriteFile(filepath.Join(missionDir, "README.md"), []byte(scaffoldReadme(spec, missionID)), 0o644); err != nil {
		return "", fmt.Errorf("ScaffoldMission: write README.md: %w", err)
	}

	// Copy the patch file if supplied; otherwise leave a TODO marker.
	if spec.PatchPath != "" {
		body, err := os.ReadFile(spec.PatchPath)
		if err != nil {
			return "", fmt.Errorf("ScaffoldMission: read patch %s: %w", spec.PatchPath, err)
		}
		if err := os.WriteFile(filepath.Join(missionDir, "gold.patch"), body, 0o644); err != nil {
			return "", fmt.Errorf("ScaffoldMission: write gold.patch: %w", err)
		}
	} else {
		stub := "# CURATOR: paste the upstream gold patch here.\n# See plans/corpus-100.md \"Authoring workflow\" step 2.\n"
		if err := os.WriteFile(filepath.Join(missionDir, "gold.patch"), []byte(stub), 0o644); err != nil {
			return "", fmt.Errorf("ScaffoldMission: write gold.patch stub: %w", err)
		}
	}

	return missionDir, nil
}

// deriveMissionID converts an upstream task ID like
// "swebench-pro/django__django-12345" to "swebench-pro--django__django-12345"
// (filesystem-safe — slashes are illegal in mission directory names).
func deriveMissionID(upstream string) string {
	id := strings.ReplaceAll(upstream, "/", "--")
	id = strings.ReplaceAll(id, " ", "-")
	return id
}

// scaffoldMissionYAML returns the mission.yaml body. Curator fills in
// the TODO blocks for intent, plan items, and completion criteria.
func scaffoldMissionYAML(spec ScaffoldSpec, missionID string) string {
	judge := "advisory"
	if spec.Difficulty == "hard" || spec.Category == "migration" || spec.Category == "refactor" {
		judge = "required"
	}
	return fmt.Sprintf(`id: %s
title: "TODO: short mission title (curator)"
description: "TODO: 1-2 sentence problem statement (curator)"
category: %s
difficulty: %s
intent: |
  TODO (curator): full agent-facing problem statement. Mention the
  package/files involved, the observable bug or missing feature, and the
  expected post-fix behavior. Match the level of detail in the existing
  seed mission intents.

plan:
  - id: P1
    description: "TODO: first atomic action"
    changed_files: []
    required_symbols: []
  - id: P2
    description: "TODO: second atomic action"
    changed_files: []
    required_symbols: []

gold_diff_path: gold.patch

completion_criteria:
  plan_completion_threshold: 1.0
  delivery_ratio_min: 60
  judge_agree: %s

acceptance_criteria:
  - "TODO (curator): observable invariant 1"
  - "TODO (curator): observable invariant 2"
`, missionID, spec.Category, spec.Difficulty, judge)
}

// scaffoldReadme returns the README.md body with provenance + TODOs.
func scaffoldReadme(spec ScaffoldSpec, missionID string) string {
	return fmt.Sprintf(`# %s

## Upstream task

Derived from upstream task: **%s**

(SWE-bench Pro citation: TODO — curator paste the upstream task URL +
date pulled here.)

## Plan rationale

TODO (curator): explain why these specific plan items decompose the
upstream patch. The decomposition is the load-bearing curation artifact;
agents who claim completion without addressing each item get scored
proportionally.

## Test strategy

TODO (curator): for each plan item, explain which verification strategy
applies — test_command (preferred), required_symbols (cheap structural),
or changed_files (last-resort fallback). See plans/corpus-100.md §
"Authoring workflow" step 4.

## Known limitations

TODO (curator): any caveats — e.g., the upstream test command is slow,
the workspace requires a non-default Go toolchain, the gold patch
depends on a follow-up commit landing first, etc.

## How this maps to the 95-mission roadmap

This is one of the deferred 95 SWE-bench Pro derivations tracked in
plans/corpus-100.md. After validation, increment the "Authored" counter
in that file.
`, missionID, spec.UpstreamTaskID)
}
