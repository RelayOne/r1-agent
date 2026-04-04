package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
			}
		}

		// Check for HTML entry points
		for _, htmlPath := range []string{"public/index.html", "index.html", "src/index.html"} {
			if fileExists(filepath.Join(projectRoot, htmlPath)) {
				info.HasHTML = true
				break
			}
		}

		// If no framework detected but has .vue/.tsx/.jsx files, mark as frontend
		if !info.HasFrontend {
			for _, pattern := range []string{"src/**/*.tsx", "src/**/*.jsx", "src/**/*.vue", "src/**/*.svelte"} {
				matches, _ := filepath.Glob(filepath.Join(projectRoot, pattern))
				if len(matches) > 0 {
					info.HasFrontend = true
					break
				}
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
		cmds := Commands{Test: "npm test"}
		if fileExists(filepath.Join(projectRoot, "tsconfig.json")) {
			cmds.Build = "npm run build"
		} else if info.Type == ProjectNextJS {
			cmds.Build = "npm run build"
		}
		cmds.Lint = "npm run lint"
		return cmds

	case ProjectGo:
		return Commands{
			Build: "go build ./...",
			Test:  "go test ./...",
			Lint:  "go vet ./...",
		}

	case ProjectRust:
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
	}

	return Commands{}
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
