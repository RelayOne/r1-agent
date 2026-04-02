package config

import (
	"os"
	"path/filepath"
)

// Commands holds build/test/lint commands for a project.
type Commands struct {
	Build string
	Test  string
	Lint  string
}

// DetectCommands examines the project root and infers build/test/lint commands.
func DetectCommands(projectRoot string) Commands {
	// Node.js (package.json)
	if fileExists(filepath.Join(projectRoot, "package.json")) {
		cmds := Commands{Test: "npm test"}
		if fileExists(filepath.Join(projectRoot, "tsconfig.json")) {
			cmds.Build = "npm run build"
		}
		// Check for common lint scripts
		for _, lint := range []string{"lint", "eslint"} {
			if fileExists(filepath.Join(projectRoot, "node_modules", ".bin", lint)) {
				cmds.Lint = "npm run lint"
				break
			}
		}
		// Fallback: if package.json exists, npm run lint is safe to try
		if cmds.Lint == "" {
			cmds.Lint = "npm run lint"
		}
		return cmds
	}

	// Go (go.mod)
	if fileExists(filepath.Join(projectRoot, "go.mod")) {
		return Commands{
			Build: "go build ./...",
			Test:  "go test ./...",
			Lint:  "go vet ./...",
		}
	}

	// Rust (Cargo.toml)
	if fileExists(filepath.Join(projectRoot, "Cargo.toml")) {
		return Commands{
			Build: "cargo build",
			Test:  "cargo test",
			Lint:  "cargo clippy -- -D warnings",
		}
	}

	// Python (pyproject.toml or setup.py)
	if fileExists(filepath.Join(projectRoot, "pyproject.toml")) || fileExists(filepath.Join(projectRoot, "setup.py")) {
		cmds := Commands{Test: "python -m pytest"}
		if fileExists(filepath.Join(projectRoot, "pyproject.toml")) {
			cmds.Lint = "python -m ruff check ."
		}
		return cmds
	}

	return Commands{}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
