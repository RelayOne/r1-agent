package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSOW(t *testing.T) {
	dir := t.TempDir()
	sow := &SOW{
		ID:   "persys",
		Name: "PERSYS",
		Stack: StackSpec{
			Language: "rust",
			Monorepo: &MonorepoSpec{
				Tool:     "cargo-workspace",
				Packages: []string{"crates/core", "crates/api", "crates/cli"},
			},
			Infra: []InfraRequirement{
				{Name: "postgres", Version: "15", Extensions: []string{"pgvector"}, EnvVars: []string{"DATABASE_URL"}},
			},
		},
		Sessions: []Session{
			{
				ID:    "S1",
				Phase: "foundation",
				Title: "Core data model",
				Tasks: []Task{
					{ID: "S1-T1", Description: "Define core entity structs"},
					{ID: "S1-T2", Description: "Implement database schema", Dependencies: []string{"S1-T1"}},
				},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "cargo build succeeds", Command: "cargo build"},
					{ID: "AC2", Description: "schema file exists", FileExists: "migrations/001_init.sql"},
				},
			},
			{
				ID:    "S2",
				Phase: "foundation",
				Title: "API layer",
				Tasks: []Task{
					{ID: "S2-T1", Description: "Add REST endpoints"},
				},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC3", Description: "cargo test passes", Command: "cargo test"},
				},
				InfraNeeded: []string{"postgres"},
			},
		},
	}

	path := filepath.Join(dir, "stoke-sow.json")
	if err := SaveSOW(path, sow); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadSOW(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "persys" {
		t.Errorf("id=%q", loaded.ID)
	}
	if loaded.Stack.Language != "rust" {
		t.Errorf("language=%q", loaded.Stack.Language)
	}
	if loaded.Stack.Monorepo == nil || loaded.Stack.Monorepo.Tool != "cargo-workspace" {
		t.Error("monorepo not loaded")
	}
	if len(loaded.Sessions) != 2 {
		t.Fatalf("sessions=%d", len(loaded.Sessions))
	}
	if len(loaded.Sessions[0].Tasks) != 2 {
		t.Errorf("S1 tasks=%d", len(loaded.Sessions[0].Tasks))
	}
	if len(loaded.Sessions[0].AcceptanceCriteria) != 2 {
		t.Errorf("S1 criteria=%d", len(loaded.Sessions[0].AcceptanceCriteria))
	}
}

func TestLoadSOWFromDir(t *testing.T) {
	dir := t.TempDir()
	sow := &SOW{
		ID:   "test",
		Name: "Test",
		Sessions: []Session{
			{ID: "S1", Title: "Setup", Tasks: []Task{{ID: "T1", Description: "init"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "pass"}}},
		},
	}
	data, _ := json.MarshalIndent(sow, "", "  ")
	os.WriteFile(filepath.Join(dir, "stoke-sow.json"), data, 0o600)

	loaded, err := LoadSOWFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "test" {
		t.Errorf("id=%q", loaded.ID)
	}
}

func TestLoadSOWMissing(t *testing.T) {
	_, err := LoadSOW("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestValidateSOWGood(t *testing.T) {
	sow := &SOW{
		ID:   "good",
		Name: "Good SOW",
		Sessions: []Session{
			{
				ID: "S1", Title: "Phase 1",
				Tasks:              []Task{{ID: "T1", Description: "do thing"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "thing done"}},
			},
		},
	}
	errs := ValidateSOW(sow)
	if len(errs) != 0 {
		t.Errorf("valid SOW should have no errors: %v", errs)
	}
}

func TestValidateSOWMissingFields(t *testing.T) {
	sow := &SOW{} // empty
	errs := ValidateSOW(sow)
	if len(errs) < 3 {
		t.Errorf("empty SOW should have multiple errors, got %d: %v", len(errs), errs)
	}
	foundNoID := false
	foundNoName := false
	foundNoSessions := false
	for _, e := range errs {
		if strings.Contains(e, "no ID") {
			foundNoID = true
		}
		if strings.Contains(e, "no name") {
			foundNoName = true
		}
		if strings.Contains(e, "no sessions") {
			foundNoSessions = true
		}
	}
	if !foundNoID || !foundNoName || !foundNoSessions {
		t.Errorf("expected ID, name, sessions errors: %v", errs)
	}
}

func TestValidateSOWDuplicateIDs(t *testing.T) {
	sow := &SOW{
		ID:   "dup",
		Name: "Dup",
		Sessions: []Session{
			{ID: "S1", Title: "A", Tasks: []Task{{ID: "T1", Description: "a"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
			{ID: "S1", Title: "B", Tasks: []Task{{ID: "T1", Description: "b"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "ok"}}},
		},
	}
	errs := ValidateSOW(sow)
	foundDupSession := false
	foundDupTask := false
	for _, e := range errs {
		if strings.Contains(e, "duplicate session") {
			foundDupSession = true
		}
		if strings.Contains(e, "duplicate task") {
			foundDupTask = true
		}
	}
	if !foundDupSession {
		t.Errorf("expected duplicate session error: %v", errs)
	}
	if !foundDupTask {
		t.Errorf("expected duplicate task error: %v", errs)
	}
}

func TestValidateSOWBadInfraRef(t *testing.T) {
	sow := &SOW{
		ID:   "bad-infra",
		Name: "Bad Infra",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "a"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}},
				InfraNeeded:        []string{"ghost-db"}},
		},
	}
	errs := ValidateSOW(sow)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "unknown infra") && strings.Contains(e, "ghost-db") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown infra error: %v", errs)
	}
}

func TestValidateSOWBadDep(t *testing.T) {
	sow := &SOW{
		ID:   "bad-dep",
		Name: "Bad Dep",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "a", Dependencies: []string{"GHOST"}}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
		},
	}
	errs := ValidateSOW(sow)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "unknown task GHOST") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown dep error: %v", errs)
	}
}

func TestAllTasksAddsCrossDeps(t *testing.T) {
	sow := &SOW{
		ID:   "cross",
		Name: "Cross",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "a"}, {ID: "T2", Description: "b"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
			{ID: "S2", Title: "B",
				Tasks:              []Task{{ID: "T3", Description: "c"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "ok"}}},
		},
	}
	all := sow.AllTasks()
	if len(all) != 3 {
		t.Fatalf("tasks=%d, want 3", len(all))
	}
	// T3 (first task of S2) should depend on T2 (last task of S1)
	foundDep := false
	for _, dep := range all[2].Dependencies {
		if dep == "T2" {
			foundDep = true
		}
	}
	if !foundDep {
		t.Errorf("T3 should depend on T2 (session gate): deps=%v", all[2].Dependencies)
	}
}

func TestToPlan(t *testing.T) {
	sow := &SOW{
		ID:          "convert",
		Name:        "Convert Test",
		Description: "testing conversion",
		Sessions: []Session{
			{ID: "S1", Title: "A",
				Tasks:              []Task{{ID: "T1", Description: "first"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC1", Description: "ok"}}},
		},
	}
	p := sow.ToPlan()
	if p.ID != "convert" {
		t.Errorf("plan id=%q", p.ID)
	}
	if len(p.Tasks) != 1 {
		t.Fatalf("plan tasks=%d", len(p.Tasks))
	}
	if p.Tasks[0].ID != "T1" {
		t.Errorf("task id=%q", p.Tasks[0].ID)
	}
}

func TestSessionByID(t *testing.T) {
	sow := &SOW{
		ID: "lookup", Name: "Lookup",
		Sessions: []Session{
			{ID: "S1", Title: "A"},
			{ID: "S2", Title: "B"},
		},
	}
	if s := sow.SessionByID("S2"); s == nil || s.Title != "B" {
		t.Error("SessionByID(S2) failed")
	}
	if s := sow.SessionByID("S99"); s != nil {
		t.Error("SessionByID(S99) should be nil")
	}
}

func TestSessionForTask(t *testing.T) {
	sow := &SOW{
		ID: "find", Name: "Find",
		Sessions: []Session{
			{ID: "S1", Title: "A", Tasks: []Task{{ID: "T1", Description: "a"}}},
			{ID: "S2", Title: "B", Tasks: []Task{{ID: "T2", Description: "b"}}},
		},
	}
	if s := sow.SessionForTask("T2"); s == nil || s.ID != "S2" {
		t.Error("SessionForTask(T2) should return S2")
	}
	if s := sow.SessionForTask("TX"); s != nil {
		t.Error("SessionForTask(TX) should be nil")
	}
}

func TestPhaseGroups(t *testing.T) {
	sow := &SOW{
		ID: "phases", Name: "Phases",
		Sessions: []Session{
			{ID: "S1", Phase: "foundation", Title: "A"},
			{ID: "S2", Phase: "foundation", Title: "B"},
			{ID: "S3", Phase: "core", Title: "C"},
			{ID: "S4", Title: "D"}, // no phase -> "default"
		},
	}
	groups := sow.PhaseGroups()
	if len(groups["foundation"]) != 2 {
		t.Errorf("foundation=%d", len(groups["foundation"]))
	}
	if len(groups["core"]) != 1 {
		t.Errorf("core=%d", len(groups["core"]))
	}
	if len(groups["default"]) != 1 {
		t.Errorf("default=%d", len(groups["default"]))
	}
}

func TestInfraForSession(t *testing.T) {
	sow := &SOW{
		ID: "infra", Name: "Infra",
		Stack: StackSpec{
			Infra: []InfraRequirement{
				{Name: "postgres", Version: "15"},
				{Name: "redis", Version: "7"},
			},
		},
		Sessions: []Session{
			{ID: "S1", Title: "A", InfraNeeded: []string{"postgres"}},
			{ID: "S2", Title: "B", InfraNeeded: []string{"postgres", "redis"}},
		},
	}
	infra := sow.InfraForSession("S1")
	if len(infra) != 1 || infra[0].Name != "postgres" {
		t.Errorf("S1 infra=%v", infra)
	}
	infra = sow.InfraForSession("S2")
	if len(infra) != 2 {
		t.Errorf("S2 infra=%d", len(infra))
	}
	infra = sow.InfraForSession("S99")
	if infra != nil {
		t.Error("S99 should have no infra")
	}
}

func TestDetectStackRustWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(`
[workspace]
members = [
    "crates/core",
    "crates/api",
    "crates/cli",
]
`), 0o600)

	spec := DetectStackFromRepo(dir)
	if spec.Language != "rust" {
		t.Errorf("language=%q", spec.Language)
	}
	if spec.Monorepo == nil {
		t.Fatal("monorepo should be detected")
	}
	if spec.Monorepo.Tool != "cargo-workspace" {
		t.Errorf("tool=%q", spec.Monorepo.Tool)
	}
	if len(spec.Monorepo.Packages) != 3 {
		t.Errorf("packages=%v", spec.Monorepo.Packages)
	}
}

func TestDetectStackTurborepo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"name": "framebright",
		"devDependencies": {"next": "14.0.0", "turbo": "latest"},
		"workspaces": ["apps/*", "packages/*"]
	}`), 0o600)
	os.WriteFile(filepath.Join(dir, "turbo.json"), []byte(`{}`), 0o600)
	os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(`lockfileVersion: '9.0'`), 0o600)

	spec := DetectStackFromRepo(dir)
	if spec.Language != "typescript" {
		t.Errorf("language=%q", spec.Language)
	}
	if spec.Framework != "next" {
		t.Errorf("framework=%q", spec.Framework)
	}
	if spec.Monorepo == nil {
		t.Fatal("monorepo should be detected")
	}
	if spec.Monorepo.Tool != "turborepo" {
		t.Errorf("tool=%q", spec.Monorepo.Tool)
	}
	if spec.Monorepo.Manager != "pnpm" {
		t.Errorf("manager=%q", spec.Monorepo.Manager)
	}
	if len(spec.Monorepo.Packages) != 2 {
		t.Errorf("packages=%v", spec.Monorepo.Packages)
	}
}

func TestDetectStackReactNative(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {"react-native": "0.73.0", "react": "18.0.0"}
	}`), 0o600)

	spec := DetectStackFromRepo(dir)
	if spec.Framework != "react-native" {
		t.Errorf("framework=%q", spec.Framework)
	}
}

func TestDetectStackPnpmWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"name": "mono",
		"devDependencies": {}
	}`), 0o600)
	os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(`packages:
  - 'apps/*'
  - 'packages/*'
  - 'tools/*'
`), 0o600)
	os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(``), 0o600)

	spec := DetectStackFromRepo(dir)
	if spec.Monorepo == nil {
		t.Fatal("monorepo should be detected")
	}
	if spec.Monorepo.Manager != "pnpm" {
		t.Errorf("manager=%q", spec.Monorepo.Manager)
	}
	if len(spec.Monorepo.Packages) != 3 {
		t.Errorf("packages=%v", spec.Monorepo.Packages)
	}
}

func TestDetectStackGo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\ngo 1.22\n"), 0o600)

	spec := DetectStackFromRepo(dir)
	if spec.Language != "go" {
		t.Errorf("language=%q", spec.Language)
	}
	if spec.Monorepo != nil {
		t.Error("Go project should not be monorepo")
	}
}

func TestDetectStackPython(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"test\"\n"), 0o600)

	spec := DetectStackFromRepo(dir)
	if spec.Language != "python" {
		t.Errorf("language=%q", spec.Language)
	}
}

func TestDetectStackEmpty(t *testing.T) {
	dir := t.TempDir()
	spec := DetectStackFromRepo(dir)
	if spec.Language != "" {
		t.Errorf("empty dir should have no language, got %q", spec.Language)
	}
}

func TestParseCargoWorkspaceMembers(t *testing.T) {
	content := `[workspace]
members = [
    "crates/core",
    "crates/api",
]
`
	members := parseCargoWorkspaceMembers(content)
	if len(members) != 2 {
		t.Fatalf("members=%v", members)
	}
	if members[0] != "crates/core" || members[1] != "crates/api" {
		t.Errorf("members=%v", members)
	}
}

func TestParsePnpmWorkspace(t *testing.T) {
	content := `packages:
  - 'apps/*'
  - 'packages/*'
# comment
other_key: value
`
	packages := parsePnpmWorkspace(content)
	if len(packages) != 2 {
		t.Fatalf("packages=%v", packages)
	}
	if packages[0] != "apps/*" || packages[1] != "packages/*" {
		t.Errorf("packages=%v", packages)
	}
}

func TestSaveAndLoadSOWRoundtrip(t *testing.T) {
	dir := t.TempDir()
	orig := &SOW{
		ID:   "roundtrip",
		Name: "Roundtrip Test",
		Stack: StackSpec{
			Language: "typescript",
			Monorepo: &MonorepoSpec{
				Tool:     "turborepo",
				Manager:  "pnpm",
				Packages: []string{"apps/*", "packages/*"},
			},
			Infra: []InfraRequirement{
				{Name: "firebase", EnvVars: []string{"FIREBASE_PROJECT_ID"}},
			},
		},
		Sessions: []Session{
			{
				ID: "S1", Phase: "setup", Title: "Project setup",
				Tasks: []Task{
					{ID: "T1", Description: "init turbo"},
					{ID: "T2", Description: "add shared types", Dependencies: []string{"T1"}},
				},
				AcceptanceCriteria: []AcceptanceCriterion{
					{ID: "AC1", Description: "pnpm build works", Command: "pnpm build"},
				},
				Outputs: []string{"packages/shared/types.ts"},
			},
			{
				ID: "S2", Phase: "core", Title: "Core features",
				Tasks:              []Task{{ID: "T3", Description: "build auth"}},
				AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC2", Description: "auth works"}},
				Inputs:             []string{"packages/shared/types.ts"},
				InfraNeeded:        []string{"firebase"},
			},
		},
	}

	path := filepath.Join(dir, "test-sow.json")
	if err := SaveSOW(path, orig); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadSOW(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Stack.Monorepo.Manager != "pnpm" {
		t.Errorf("manager=%q", loaded.Stack.Monorepo.Manager)
	}
	if len(loaded.Stack.Infra) != 1 || loaded.Stack.Infra[0].Name != "firebase" {
		t.Errorf("infra=%v", loaded.Stack.Infra)
	}
	if len(loaded.Sessions[1].InfraNeeded) != 1 {
		t.Errorf("infra_needed=%v", loaded.Sessions[1].InfraNeeded)
	}
}

func TestAllTasksPreservesExplicitDeps(t *testing.T) {
	// If T3 already explicitly depends on T1 from S1, don't add another gate dep
	sow := &SOW{
		ID: "explicit", Name: "Explicit",
		Sessions: []Session{
			{ID: "S1", Title: "A", Tasks: []Task{{ID: "T1", Description: "a"}, {ID: "T2", Description: "b"}}},
			{ID: "S2", Title: "B", Tasks: []Task{{ID: "T3", Description: "c", Dependencies: []string{"T1"}}}},
		},
	}
	all := sow.AllTasks()
	// T3 has explicit dep on T1 (which is in S1), so no additional gate dep should be added
	depCount := 0
	for _, dep := range all[2].Dependencies {
		if dep == "T1" || dep == "T2" {
			depCount++
		}
	}
	// Should have exactly T1 (explicit) - the gate check sees T1 is from prev session
	if depCount != 1 {
		t.Errorf("T3 deps should be [T1], got %v", all[2].Dependencies)
	}
}
