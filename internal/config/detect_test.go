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
