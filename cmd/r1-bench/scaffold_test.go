package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveMissionID(t *testing.T) {
	cases := []struct {
		upstream, want string
	}{
		{"swebench-pro/django__django-12345", "swebench-pro--django__django-12345"},
		{"flask/12345", "flask--12345"},
		{"plain-id", "plain-id"},
		{"two slashes here/x/y", "two-slashes-here--x--y"},
	}
	for _, tc := range cases {
		if got := deriveMissionID(tc.upstream); got != tc.want {
			t.Errorf("deriveMissionID(%q) = %q, want %q", tc.upstream, got, tc.want)
		}
	}
}

func TestScaffoldMission_RequiresUpstreamTaskID(t *testing.T) {
	_, err := ScaffoldMission(ScaffoldSpec{
		Difficulty: "easy", Category: "bugfix",
		OutputRoot: t.TempDir(),
	})
	if err == nil {
		t.Errorf("missing UpstreamTaskID should error")
	}
}

func TestScaffoldMission_RejectsInvalidCategory(t *testing.T) {
	_, err := ScaffoldMission(ScaffoldSpec{
		UpstreamTaskID: "x/y",
		Difficulty:     "easy",
		Category:       "bug-fix", // hyphen instead of valid "bugfix"
		OutputRoot:     t.TempDir(),
	})
	if err == nil {
		t.Errorf("invalid category should error")
	}
	if !strings.Contains(err.Error(), "category") {
		t.Errorf("error should mention category; got %q", err)
	}
}

func TestScaffoldMission_RejectsInvalidDifficulty(t *testing.T) {
	_, err := ScaffoldMission(ScaffoldSpec{
		UpstreamTaskID: "x/y",
		Difficulty:     "trivial",
		Category:       "bugfix",
		OutputRoot:     t.TempDir(),
	})
	if err == nil {
		t.Errorf("invalid difficulty should error")
	}
}

func TestScaffoldMission_RejectsEmptyOutputRoot(t *testing.T) {
	_, err := ScaffoldMission(ScaffoldSpec{
		UpstreamTaskID: "x/y",
		Difficulty:     "easy",
		Category:       "bugfix",
	})
	if err == nil {
		t.Errorf("empty OutputRoot should error")
	}
}

func TestScaffoldMission_HappyPathNoPatch(t *testing.T) {
	root := t.TempDir()
	dir, err := ScaffoldMission(ScaffoldSpec{
		UpstreamTaskID: "swebench-pro/django__django-12345",
		Difficulty:     "medium",
		Category:       "bugfix",
		OutputRoot:     root,
	})
	if err != nil {
		t.Fatalf("ScaffoldMission: %v", err)
	}
	if !strings.HasSuffix(dir, "swebench-pro--django__django-12345") {
		t.Errorf("dir = %q, expected suffix swebench-pro--django__django-12345", dir)
	}
	for _, fname := range []string{"mission.yaml", "README.md", "gold.patch"} {
		path := filepath.Join(dir, fname)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s: %v", fname, err)
		}
	}
	body, _ := os.ReadFile(filepath.Join(dir, "gold.patch"))
	if !strings.Contains(string(body), "CURATOR") {
		t.Errorf("gold.patch should contain CURATOR marker when no --patch supplied; got %q", body)
	}
	yamlBody, _ := os.ReadFile(filepath.Join(dir, "mission.yaml"))
	if !strings.Contains(string(yamlBody), "category: bugfix") {
		t.Errorf("mission.yaml missing category: bugfix")
	}
	if !strings.Contains(string(yamlBody), "judge_agree: advisory") {
		t.Errorf("mission.yaml should default to advisory for easy/medium bugfix; got: %q", yamlBody)
	}
}

func TestScaffoldMission_HardCategoryGetsRequiredJudge(t *testing.T) {
	root := t.TempDir()
	dir, err := ScaffoldMission(ScaffoldSpec{
		UpstreamTaskID: "swebench-pro/x",
		Difficulty:     "hard",
		Category:       "bugfix",
		OutputRoot:     root,
	})
	if err != nil {
		t.Fatalf("ScaffoldMission: %v", err)
	}
	yamlBody, _ := os.ReadFile(filepath.Join(dir, "mission.yaml"))
	if !strings.Contains(string(yamlBody), "judge_agree: required") {
		t.Errorf("hard difficulty should default to required judge; got: %q", yamlBody)
	}
}

func TestScaffoldMission_RefactorAndMigrationGetRequiredJudge(t *testing.T) {
	for _, cat := range []string{"refactor", "migration"} {
		dir, err := ScaffoldMission(ScaffoldSpec{
			UpstreamTaskID: "x/y-" + cat,
			Difficulty:     "easy", // even easy: refactor/migration default to required
			Category:       cat,
			OutputRoot:     t.TempDir(),
		})
		if err != nil {
			t.Fatalf("%s: %v", cat, err)
		}
		yamlBody, _ := os.ReadFile(filepath.Join(dir, "mission.yaml"))
		if !strings.Contains(string(yamlBody), "judge_agree: required") {
			t.Errorf("category=%s should default to required judge", cat)
		}
	}
}

func TestScaffoldMission_WithRealPatchCopiesVerbatim(t *testing.T) {
	root := t.TempDir()
	patchPath := filepath.Join(root, "input.patch")
	patchBody := "diff --git a/foo b/foo\n+real patch content\n"
	if err := os.WriteFile(patchPath, []byte(patchBody), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}
	dir, err := ScaffoldMission(ScaffoldSpec{
		UpstreamTaskID: "x/with-patch",
		Difficulty:     "easy",
		Category:       "bugfix",
		PatchPath:      patchPath,
		OutputRoot:     root,
	})
	if err != nil {
		t.Fatalf("ScaffoldMission: %v", err)
	}
	gotBody, _ := os.ReadFile(filepath.Join(dir, "gold.patch"))
	if string(gotBody) != patchBody {
		t.Errorf("gold.patch = %q, want verbatim copy %q", gotBody, patchBody)
	}
}

func TestScaffoldMission_RefusesToOverwrite(t *testing.T) {
	root := t.TempDir()
	spec := ScaffoldSpec{
		UpstreamTaskID: "x/dup",
		Difficulty:     "easy",
		Category:       "bugfix",
		OutputRoot:     root,
	}
	if _, err := ScaffoldMission(spec); err != nil {
		t.Fatalf("first ScaffoldMission: %v", err)
	}
	_, err := ScaffoldMission(spec)
	if err == nil {
		t.Errorf("second ScaffoldMission should refuse to overwrite")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists'; got %q", err)
	}
}

func TestScaffoldMission_ExplicitMissionIDOverride(t *testing.T) {
	root := t.TempDir()
	dir, err := ScaffoldMission(ScaffoldSpec{
		UpstreamTaskID: "x/derived-would-be-this",
		MissionID:      "custom-mission-id",
		Difficulty:     "easy",
		Category:       "bugfix",
		OutputRoot:     root,
	})
	if err != nil {
		t.Fatalf("ScaffoldMission: %v", err)
	}
	if !strings.HasSuffix(dir, "custom-mission-id") {
		t.Errorf("dir = %q, expected suffix custom-mission-id", dir)
	}
}
