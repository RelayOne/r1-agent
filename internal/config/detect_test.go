package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectNode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0644)
	cmds := DetectCommands(dir)
	if cmds.Test != "npm test" {
		t.Errorf("test=%q", cmds.Test)
	}
}

func TestDetectNodeWithTS(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0644)
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
