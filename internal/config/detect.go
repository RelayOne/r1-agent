package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Commands holds build/test/lint commands for a project.
type Commands struct {
	Build string
	Test  string
	Lint  string
}

// ProjectType identifies the detected project framework/type.
type ProjectType string

const (
	ProjectUnknown  ProjectType = ""
	ProjectNodeJS   ProjectType = "nodejs"
	ProjectReact    ProjectType = "react"
	ProjectNextJS   ProjectType = "nextjs"
	ProjectVue      ProjectType = "vue"
	ProjectSvelte   ProjectType = "svelte"
	ProjectAngular  ProjectType = "angular"
	ProjectGo       ProjectType = "go"
	ProjectRust     ProjectType = "rust"
	ProjectPython   ProjectType = "python"
)

// ProjectInfo describes the detected project type and its capabilities.
type ProjectInfo struct {
	Type          ProjectType // framework/language detected
	HasFrontend   bool        // true if project includes UI components
	UIFramework   string      // e.g. "react", "vue", "svelte", "angular"
	HasTests      bool        // true if test infrastructure detected
	HasStyles     bool        // true if CSS/SCSS/etc found
	HasHTML       bool        // true if HTML entry points found
	TestFramework string      // e.g. "jest", "vitest", "playwright", "cypress", "pytest", "go-test"
	HasStorybook  bool        // true if Storybook detected in devDependencies

	// Monorepo fields
	IsMonorepo     bool     // true if a monorepo layout was detected
	MonorepoTool   string   // "turborepo", "nx", "lerna", "cargo-workspace", "pnpm-workspaces"
	PackageManager string   // "pnpm", "yarn", "npm" (JS/TS only)
	Workspaces     []string // workspace member globs/paths
}

// DetectProject examines the project root and returns detailed project info.
func DetectProject(projectRoot string) ProjectInfo {
	info := ProjectInfo{}

	// Check for package.json-based projects (Node.js ecosystem)
	pkgPath := filepath.Join(projectRoot, "package.json")
	if fileExists(pkgPath) {
		info.Type = ProjectNodeJS
		info.HasTests = true // npm test is available

		// Read package.json to detect framework from dependencies
		if data, err := os.ReadFile(pkgPath); err == nil {
			var pkg struct {
				Dependencies    map[string]string `json:"dependencies"`
				DevDependencies map[string]string `json:"devDependencies"`
				Workspaces      json.RawMessage   `json:"workspaces"`
			}
			if json.Unmarshal(data, &pkg) == nil {
				allDeps := mergeMaps(pkg.Dependencies, pkg.DevDependencies)

				if _, ok := allDeps["next"]; ok {
					info.Type = ProjectNextJS
					info.HasFrontend = true
					info.UIFramework = "react"
				} else if _, ok := allDeps["react"]; ok {
					info.Type = ProjectReact
					info.HasFrontend = true
					info.UIFramework = "react"
				} else if _, ok := allDeps["vue"]; ok {
					info.Type = ProjectVue
					info.HasFrontend = true
					info.UIFramework = "vue"
				} else if _, ok := allDeps["svelte"]; ok {
					info.Type = ProjectSvelte
					info.HasFrontend = true
					info.UIFramework = "svelte"
				} else if _, ok := allDeps["@angular/core"]; ok {
					info.Type = ProjectAngular
					info.HasFrontend = true
					info.UIFramework = "angular"
				}

				// Check for style infrastructure
				for dep := range allDeps {
					if dep == "tailwindcss" || dep == "sass" || dep == "styled-components" ||
						dep == "@emotion/react" || dep == "less" || dep == "postcss" {
						info.HasStyles = true
						break
					}
				}

				// Detect test framework
				switch {
				case allDeps["vitest"] != "":
					info.TestFramework = "vitest"
				case allDeps["jest"] != "":
					info.TestFramework = "jest"
				case allDeps["@playwright/test"] != "" || allDeps["playwright"] != "":
					info.TestFramework = "playwright"
				case allDeps["cypress"] != "":
					info.TestFramework = "cypress"
				}

				// Storybook detection
				for dep := range allDeps {
					if dep == "storybook" || dep == "@storybook/react" ||
						dep == "@storybook/vue3" || dep == "@storybook/svelte" {
						info.HasStorybook = true
						break
					}
				}

				// Monorepo detection
				if fileExists(filepath.Join(projectRoot, "turbo.json")) {
					info.IsMonorepo = true
					info.MonorepoTool = "turborepo"
				} else if fileExists(filepath.Join(projectRoot, "nx.json")) {
					info.IsMonorepo = true
					info.MonorepoTool = "nx"
				} else if fileExists(filepath.Join(projectRoot, "lerna.json")) {
					info.IsMonorepo = true
					info.MonorepoTool = "lerna"
				} else if pkg.Workspaces != nil {
					info.IsMonorepo = true
					info.MonorepoTool = "workspaces"
				}

				// Package manager detection
				if fileExists(filepath.Join(projectRoot, "pnpm-lock.yaml")) || fileExists(filepath.Join(projectRoot, "pnpm-workspace.yaml")) {
					info.PackageManager = "pnpm"
					if !info.IsMonorepo && fileExists(filepath.Join(projectRoot, "pnpm-workspace.yaml")) {
						info.IsMonorepo = true
						info.MonorepoTool = "pnpm-workspaces"
					}
				} else if fileExists(filepath.Join(projectRoot, "yarn.lock")) {
					info.PackageManager = "yarn"
				} else {
					info.PackageManager = "npm"
				}

				// Parse workspace globs
				if info.IsMonorepo && pkg.Workspaces != nil {
					var ws []string
					if json.Unmarshal(pkg.Workspaces, &ws) == nil {
						info.Workspaces = ws
					} else {
						var wsObj struct {
							Packages []string `json:"packages"`
						}
						if json.Unmarshal(pkg.Workspaces, &wsObj) == nil {
							info.Workspaces = wsObj.Packages
						}
					}
				}
			}
		}

		// Check for HTML entry points
		for _, htmlPath := range []string{"public/index.html", "index.html", "src/index.html"} {
			if fileExists(filepath.Join(projectRoot, htmlPath)) {
				info.HasHTML = true
				break
			}
		}

		// If no framework detected but has .vue/.tsx/.jsx files, mark as frontend.
		// filepath.Glob doesn't support ** recursion, so walk the src directory.
		if !info.HasFrontend {
			frontendExts := map[string]bool{".tsx": true, ".jsx": true, ".vue": true, ".svelte": true}
			srcDir := filepath.Join(projectRoot, "src")
			if _, err := os.Stat(srcDir); err == nil {
				filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					if frontendExts[filepath.Ext(path)] {
						info.HasFrontend = true
						return filepath.SkipAll
					}
					return nil
				})
			}
		}

		return info
	}

	// Go
	if fileExists(filepath.Join(projectRoot, "go.mod")) {
		info.Type = ProjectGo
		info.HasTests = true
		info.TestFramework = "go-test"
		return info
	}

	// Rust
	if fileExists(filepath.Join(projectRoot, "Cargo.toml")) {
		info.Type = ProjectRust
		info.HasTests = true
		info.TestFramework = "cargo-test"
		// Detect Cargo workspace
		if data, err := os.ReadFile(filepath.Join(projectRoot, "Cargo.toml")); err == nil {
			content := string(data)
			if strings.Contains(content, "[workspace]") {
				info.IsMonorepo = true
				info.MonorepoTool = "cargo-workspace"
				info.Workspaces = parseCargoMembers(content)
			}
		}
		return info
	}

	// Python
	if fileExists(filepath.Join(projectRoot, "pyproject.toml")) || fileExists(filepath.Join(projectRoot, "setup.py")) {
		info.Type = ProjectPython
		info.HasTests = true
		info.TestFramework = "pytest"
		// Python web apps (Django, Flask, FastAPI) may have templates
		for _, tmplDir := range []string{"templates", "static"} {
			if dirExists(filepath.Join(projectRoot, tmplDir)) {
				info.HasFrontend = true
				info.HasHTML = true
				break
			}
		}
		return info
	}

	return info
}

// DetectCommands examines the project root and infers build/test/lint commands.
func DetectCommands(projectRoot string) Commands {
	info := DetectProject(projectRoot)

	switch info.Type {
	case ProjectNodeJS, ProjectReact, ProjectNextJS, ProjectVue, ProjectSvelte, ProjectAngular:
		scripts := npmScripts(projectRoot)
		cmds := Commands{}

		// Select the right runner for monorepos
		runner := "npm"
		if info.IsMonorepo {
			switch info.PackageManager {
			case "pnpm":
				runner = "pnpm"
			case "yarn":
				runner = "yarn"
			}
		}

		if info.IsMonorepo && info.MonorepoTool == "turborepo" {
			// Turborepo: use turbo run for orchestrated builds
			if scripts["build"] {
				cmds.Build = runner + " run build"
			}
			if scripts["test"] {
				cmds.Test = runner + " run test"
			}
			if scripts["lint"] {
				cmds.Lint = runner + " run lint"
			}
			// turbo run handles workspace-aware task execution
		} else if runner != "npm" {
			// pnpm/yarn monorepo without turbo
			if scripts["test"] {
				cmds.Test = runner + " test"
			}
			if scripts["build"] {
				cmds.Build = runner + " run build"
			} else if fileExists(filepath.Join(projectRoot, "tsconfig.json")) || info.Type == ProjectNextJS {
				cmds.Build = runner + " run build"
			}
			if scripts["lint"] {
				cmds.Lint = runner + " run lint"
			}
		} else {
			// Plain npm
			if scripts["test"] {
				cmds.Test = "npm test"
			}
			if scripts["build"] {
				cmds.Build = "npm run build"
			} else if fileExists(filepath.Join(projectRoot, "tsconfig.json")) || info.Type == ProjectNextJS {
				cmds.Build = "npm run build"
			}
			if scripts["lint"] {
				cmds.Lint = "npm run lint"
			}
		}
		return cmds

	case ProjectGo:
		return Commands{
			Build: "go build ./...",
			Test:  "go test ./...",
			Lint:  "go vet ./...",
		}

	case ProjectRust:
		// Cargo workspace or single crate — cargo handles both transparently
		return Commands{
			Build: "cargo build",
			Test:  "cargo test",
			Lint:  "cargo clippy -- -D warnings",
		}

	case ProjectPython:
		cmds := Commands{Test: "python -m pytest"}
		if fileExists(filepath.Join(projectRoot, "pyproject.toml")) {
			cmds.Lint = "python -m ruff check ."
		}
		return cmds
	case ProjectUnknown:
		return Commands{}
	}

	return Commands{}
}

// npmScripts returns a set of script names defined in package.json.
func npmScripts(projectRoot string) map[string]bool {
	result := make(map[string]bool)
	data, err := os.ReadFile(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		return result
	}
	var pkg struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return result
	}
	for name := range pkg.Scripts {
		result[name] = true
	}
	return result
}

func mergeMaps(a, b map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range a {
		result[k] = v
	}
	for k, v := range b {
		result[k] = v
	}
	return result
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// parseCargoMembers extracts workspace member paths from Cargo.toml content.
func parseCargoMembers(content string) []string {
	var members []string
	inMembers := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "members") && strings.Contains(trimmed, "=") {
			inMembers = true
		}
		if inMembers {
			for _, part := range strings.Split(trimmed, "\"") {
				clean := strings.TrimSpace(part)
				if clean != "" && !strings.ContainsAny(clean, "=[],") {
					members = append(members, clean)
				}
			}
			if strings.Contains(trimmed, "]") {
				break
			}
		}
	}
	return members
}
