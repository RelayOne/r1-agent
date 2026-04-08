package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectNode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test","scripts":{"test":"jest","lint":"eslint ."}}`), 0644)
	cmds := DetectCommands(dir)
	if cmds.Test != "npm test" {
		t.Errorf("test=%q", cmds.Test)
	}
	if cmds.Lint != "npm run lint" {
		t.Errorf("lint=%q", cmds.Lint)
	}
}

func TestDetectNodeMissingScripts(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0644)
	cmds := DetectCommands(dir)
	// No scripts section → no commands emitted
	if cmds.Test != "" {
		t.Errorf("expected empty test, got %q", cmds.Test)
	}
	if cmds.Lint != "" {
		t.Errorf("expected empty lint, got %q", cmds.Lint)
	}
}

func TestDetectNodeWithTS(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"tsc"}}`), 0644)
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0644)
	cmds := DetectCommands(dir)
	if cmds.Build != "npm run build" {
		t.Errorf("build=%q", cmds.Build)
	}
}

func TestDetectGo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\ngo 1.22"), 0644)
	cmds := DetectCommands(dir)
	if cmds.Build != "go build ./..." {
		t.Errorf("build=%q", cmds.Build)
	}
	if cmds.Test != "go test ./..." {
		t.Errorf("test=%q", cmds.Test)
	}
	if cmds.Lint != "go vet ./..." {
		t.Errorf("lint=%q", cmds.Lint)
	}
}

func TestDetectRust(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"test\""), 0644)
	cmds := DetectCommands(dir)
	if cmds.Build != "cargo build" {
		t.Errorf("build=%q", cmds.Build)
	}
	if cmds.Test != "cargo test" {
		t.Errorf("test=%q", cmds.Test)
	}
}

func TestDetectPython(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.poetry]"), 0644)
	cmds := DetectCommands(dir)
	if cmds.Test != "python -m pytest" {
		t.Errorf("test=%q", cmds.Test)
	}
	if cmds.Lint != "python -m ruff check ." {
		t.Errorf("lint=%q", cmds.Lint)
	}
}

func TestDetectEmpty(t *testing.T) {
	dir := t.TempDir()
	cmds := DetectCommands(dir)
	if cmds.Build != "" || cmds.Test != "" || cmds.Lint != "" {
		t.Errorf("expected empty commands for empty dir, got build=%q test=%q lint=%q", cmds.Build, cmds.Test, cmds.Lint)
	}
}

// --- Project Detection ---

func TestDetectProjectReact(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {"react": "^18.0.0", "react-dom": "^18.0.0"}
	}`), 0644)
	info := DetectProject(dir)
	if info.Type != ProjectReact {
		t.Errorf("type=%q, want react", info.Type)
	}
	if !info.HasFrontend {
		t.Error("React project should have HasFrontend=true")
	}
	if info.UIFramework != "react" {
		t.Errorf("UIFramework=%q, want react", info.UIFramework)
	}
}

func TestDetectProjectNextJS(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {"next": "^14.0.0", "react": "^18.0.0"}
	}`), 0644)
	info := DetectProject(dir)
	if info.Type != ProjectNextJS {
		t.Errorf("type=%q, want nextjs", info.Type)
	}
	if !info.HasFrontend {
		t.Error("Next.js project should have HasFrontend=true")
	}
	// Next.js should get build command even without tsconfig
	cmds := DetectCommands(dir)
	if cmds.Build != "npm run build" {
		t.Errorf("build=%q, want 'npm run build'", cmds.Build)
	}
}

func TestDetectProjectVue(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {"vue": "^3.0.0"}
	}`), 0644)
	info := DetectProject(dir)
	if info.Type != ProjectVue {
		t.Errorf("type=%q, want vue", info.Type)
	}
	if info.UIFramework != "vue" {
		t.Errorf("UIFramework=%q, want vue", info.UIFramework)
	}
}

func TestDetectProjectSvelte(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"devDependencies": {"svelte": "^4.0.0"}
	}`), 0644)
	info := DetectProject(dir)
	if info.Type != ProjectSvelte {
		t.Errorf("type=%q, want svelte", info.Type)
	}
	if !info.HasFrontend {
		t.Error("Svelte project should have HasFrontend=true")
	}
}

func TestDetectProjectAngular(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {"@angular/core": "^17.0.0"}
	}`), 0644)
	info := DetectProject(dir)
	if info.Type != ProjectAngular {
		t.Errorf("type=%q, want angular", info.Type)
	}
	if info.UIFramework != "angular" {
		t.Errorf("UIFramework=%q, want angular", info.UIFramework)
	}
}

func TestDetectProjectGo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\ngo 1.22"), 0644)
	info := DetectProject(dir)
	if info.Type != ProjectGo {
		t.Errorf("type=%q, want go", info.Type)
	}
	if info.HasFrontend {
		t.Error("Go project should not have HasFrontend by default")
	}
}

func TestDetectProjectWithStyles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {"react": "^18.0.0"},
		"devDependencies": {"tailwindcss": "^3.0.0"}
	}`), 0644)
	info := DetectProject(dir)
	if !info.HasStyles {
		t.Error("project with tailwindcss should have HasStyles=true")
	}
}

func TestDetectProjectWithHTML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"dependencies": {"react": "^18.0.0"}
	}`), 0644)
	os.MkdirAll(filepath.Join(dir, "public"), 0755)
	os.WriteFile(filepath.Join(dir, "public", "index.html"), []byte("<html></html>"), 0644)
	info := DetectProject(dir)
	if !info.HasHTML {
		t.Error("project with public/index.html should have HasHTML=true")
	}
}

func TestDetectProjectPythonWithTemplates(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.poetry]"), 0644)
	os.MkdirAll(filepath.Join(dir, "templates"), 0755)
	info := DetectProject(dir)
	if info.Type != ProjectPython {
		t.Errorf("type=%q, want python", info.Type)
	}
	if !info.HasFrontend {
		t.Error("Python project with templates/ should have HasFrontend=true")
	}
}

func TestDetectProjectUnknown(t *testing.T) {
	dir := t.TempDir()
	info := DetectProject(dir)
	if info.Type != ProjectUnknown {
		t.Errorf("type=%q, want empty", info.Type)
	}
	if info.HasFrontend {
		t.Error("unknown project should not have HasFrontend")
	}
}

// --- Monorepo Detection ---

func TestDetectTurborepoMonorepo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"name": "monorepo",
		"devDependencies": {"next": "14.0.0", "turbo": "latest"},
		"workspaces": ["apps/*", "packages/*"],
		"scripts": {"build": "turbo run build", "test": "turbo run test", "lint": "turbo run lint"}
	}`), 0644)
	os.WriteFile(filepath.Join(dir, "turbo.json"), []byte(`{}`), 0644)
	os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(``), 0644)

	info := DetectProject(dir)
	if !info.IsMonorepo {
		t.Error("should detect monorepo")
	}
	if info.MonorepoTool != "turborepo" {
		t.Errorf("tool=%q, want turborepo", info.MonorepoTool)
	}
	if info.PackageManager != "pnpm" {
		t.Errorf("manager=%q, want pnpm", info.PackageManager)
	}
	if len(info.Workspaces) != 2 {
		t.Errorf("workspaces=%v", info.Workspaces)
	}

	cmds := DetectCommands(dir)
	if cmds.Build != "pnpm run build" {
		t.Errorf("build=%q, want 'pnpm run build'", cmds.Build)
	}
	if cmds.Test != "pnpm run test" {
		t.Errorf("test=%q, want 'pnpm run test'", cmds.Test)
	}
	if cmds.Lint != "pnpm run lint" {
		t.Errorf("lint=%q, want 'pnpm run lint'", cmds.Lint)
	}
}

func TestDetectCargoWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(`[workspace]
members = [
    "crates/core",
    "crates/api",
]
`), 0644)

	info := DetectProject(dir)
	if info.Type != ProjectRust {
		t.Errorf("type=%q, want rust", info.Type)
	}
	if !info.IsMonorepo {
		t.Error("should detect Cargo workspace as monorepo")
	}
	if info.MonorepoTool != "cargo-workspace" {
		t.Errorf("tool=%q, want cargo-workspace", info.MonorepoTool)
	}
	if len(info.Workspaces) != 2 {
		t.Errorf("workspaces=%v", info.Workspaces)
	}

	// Cargo handles workspaces transparently
	cmds := DetectCommands(dir)
	if cmds.Build != "cargo build" {
		t.Errorf("build=%q", cmds.Build)
	}
}

func TestDetectYarnWorkspaces(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"workspaces": ["packages/*"],
		"scripts": {"build": "tsc", "test": "jest"}
	}`), 0644)
	os.WriteFile(filepath.Join(dir, "yarn.lock"), []byte(``), 0644)

	info := DetectProject(dir)
	if !info.IsMonorepo {
		t.Error("should detect yarn workspaces")
	}
	if info.PackageManager != "yarn" {
		t.Errorf("manager=%q, want yarn", info.PackageManager)
	}

	cmds := DetectCommands(dir)
	if cmds.Build != "yarn run build" {
		t.Errorf("build=%q, want 'yarn run build'", cmds.Build)
	}
	if cmds.Test != "yarn test" {
		t.Errorf("test=%q, want 'yarn test'", cmds.Test)
	}
}

func TestDetectPnpmWorkspaceOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"scripts": {"build": "tsc", "test": "vitest"}
	}`), 0644)
	os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(`packages:
  - 'apps/*'
  - 'libs/*'
`), 0644)
	os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(``), 0644)

	info := DetectProject(dir)
	if !info.IsMonorepo {
		t.Error("should detect pnpm workspace")
	}
	if info.PackageManager != "pnpm" {
		t.Errorf("manager=%q, want pnpm", info.PackageManager)
	}
}

func TestDetectNxMonorepo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"scripts": {"build": "nx build", "test": "nx test"}
	}`), 0644)
	os.WriteFile(filepath.Join(dir, "nx.json"), []byte(`{}`), 0644)

	info := DetectProject(dir)
	if !info.IsMonorepo {
		t.Error("should detect nx monorepo")
	}
	if info.MonorepoTool != "nx" {
		t.Errorf("tool=%q, want nx", info.MonorepoTool)
	}
}

func TestDetectNonMonorepoNode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
		"name": "simple-app",
		"scripts": {"build": "tsc", "test": "jest"}
	}`), 0644)

	info := DetectProject(dir)
	if info.IsMonorepo {
		t.Error("simple project should not be monorepo")
	}
	if info.PackageManager != "npm" {
		t.Errorf("default manager=%q, want npm", info.PackageManager)
	}
}
