package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SOW represents a Statement of Work: a multi-session, multi-phase project plan.
// Sessions execute sequentially (S1 before S2), tasks within a session respect
// dependency ordering, and acceptance criteria gate session transitions.
type SOW struct {
	ID          string    `json:"id" yaml:"id"`
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description,omitempty" yaml:"description"`
	Stack       StackSpec `json:"stack,omitempty" yaml:"stack"`
	Sessions    []Session `json:"sessions" yaml:"sessions"`
}

// StackSpec describes the project's technology stack for command inference.
type StackSpec struct {
	Language    string            `json:"language" yaml:"language"`             // "rust", "typescript", "go", "python"
	Framework   string            `json:"framework,omitempty" yaml:"framework"` // "next", "react-native", "actix-web"
	Monorepo    *MonorepoSpec     `json:"monorepo,omitempty" yaml:"monorepo"`
	Infra       []InfraRequirement `json:"infra,omitempty" yaml:"infra"`
}

// MonorepoSpec describes a monorepo layout (Cargo workspace, Turborepo, etc.).
type MonorepoSpec struct {
	Tool     string   `json:"tool" yaml:"tool"`         // "cargo-workspace", "turborepo", "nx", "lerna"
	Manager  string   `json:"manager,omitempty" yaml:"manager"` // "pnpm", "npm", "yarn"
	Packages []string `json:"packages,omitempty" yaml:"packages"` // workspace member paths
}

// InfraRequirement declares an external service dependency (database, cache, etc.).
type InfraRequirement struct {
	Name       string            `json:"name" yaml:"name"`               // "postgres", "redis", "firebase"
	Version    string            `json:"version,omitempty" yaml:"version"` // "15", "7.x"
	Extensions []string          `json:"extensions,omitempty" yaml:"extensions"` // e.g. ["pgvector"]
	EnvVars    []string          `json:"env_vars,omitempty" yaml:"env_vars"`     // required env vars
	Config     map[string]string `json:"config,omitempty" yaml:"config"`
}

// Session groups tasks into a sequential execution unit with acceptance criteria.
// Sessions execute in order: all tasks in S(n) must pass acceptance criteria
// before S(n+1) begins.
type Session struct {
	ID                 string               `json:"id" yaml:"id"`
	Phase              string               `json:"phase,omitempty" yaml:"phase"`       // "foundation", "core", "integration", etc.
	PhaseNumber        int                  `json:"phase_number,omitempty" yaml:"phase_number"`
	Title              string               `json:"title" yaml:"title"`
	Description        string               `json:"description,omitempty" yaml:"description"`
	Tasks              []Task               `json:"tasks" yaml:"tasks"`
	AcceptanceCriteria []AcceptanceCriterion `json:"acceptance_criteria" yaml:"acceptance_criteria"`
	Inputs             []string             `json:"inputs,omitempty" yaml:"inputs"`   // outputs from prior sessions this session needs
	Outputs            []string             `json:"outputs,omitempty" yaml:"outputs"` // artifacts this session produces
	InfraNeeded        []string             `json:"infra_needed,omitempty" yaml:"infra_needed"` // references to SOW.Stack.Infra by name
}

// AcceptanceCriterion is a verifiable gate condition at session boundaries.
type AcceptanceCriterion struct {
	ID          string `json:"id" yaml:"id"`
	Description string `json:"description" yaml:"description"`
	// Command, if set, is a shell command that must exit 0 for the criterion to pass.
	Command string `json:"command,omitempty" yaml:"command"`
	// FileExists, if set, checks that the path exists in the repo.
	FileExists string `json:"file_exists,omitempty" yaml:"file_exists"`
	// ContentMatch checks that a file contains a specific string.
	ContentMatch *ContentMatchCriterion `json:"content_match,omitempty" yaml:"content_match"`
}

// ContentMatchCriterion checks that a file contains expected content.
type ContentMatchCriterion struct {
	File    string `json:"file" yaml:"file"`
	Pattern string `json:"pattern" yaml:"pattern"` // substring or regex
}

// LoadSOW reads a SOW from a file. Supports both JSON and YAML: the format
// is detected from the file extension, falling back to a content sniff if
// the extension is ambiguous.
func LoadSOW(path string) (*SOW, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SOW: %w", err)
	}
	return ParseSOW(data, path)
}

// ParseSOW parses a SOW from bytes. pathHint is used only to pick the parser
// (json vs yaml); it may be empty, in which case the content is sniffed.
func ParseSOW(data []byte, pathHint string) (*SOW, error) {
	useYAML := false
	switch strings.ToLower(filepath.Ext(pathHint)) {
	case ".yaml", ".yml":
		useYAML = true
	case ".json":
		useYAML = false
	default:
		// Sniff: JSON docs start with '{' or '[' after leading whitespace.
		trimmed := strings.TrimLeft(string(data), " \t\r\n")
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			useYAML = true
		}
	}

	var sow SOW
	if useYAML {
		if err := yaml.Unmarshal(data, &sow); err != nil {
			return nil, fmt.Errorf("parse SOW (yaml): %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &sow); err != nil {
			return nil, fmt.Errorf("parse SOW (json): %w", err)
		}
	}
	return &sow, nil
}

// LoadSOWFromDir looks for stoke-sow.json or stoke-sow.yaml in the project root.
// Tries JSON first, then YAML, returning the first one that exists.
func LoadSOWFromDir(projectRoot string) (*SOW, error) {
	for _, name := range []string{"stoke-sow.json", "stoke-sow.yaml", "stoke-sow.yml"} {
		path := filepath.Join(projectRoot, name)
		if _, err := os.Stat(path); err == nil {
			return LoadSOW(path)
		}
	}
	return nil, fmt.Errorf("no stoke-sow.{json,yaml,yml} in %s", projectRoot)
}

// SaveSOW writes a SOW to disk as JSON.
func SaveSOW(path string, sow *SOW) error {
	data, err := json.MarshalIndent(sow, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ValidateSOW checks a SOW for structural problems.
func ValidateSOW(sow *SOW) []string {
	var errs []string

	if sow.ID == "" {
		errs = append(errs, "SOW has no ID")
	}
	if sow.Name == "" {
		errs = append(errs, "SOW has no name")
	}
	if len(sow.Sessions) == 0 {
		errs = append(errs, "SOW has no sessions")
		return errs
	}

	sessionIDs := map[string]bool{}
	allTaskIDs := map[string]bool{}

	for si, s := range sow.Sessions {
		if s.ID == "" {
			errs = append(errs, fmt.Sprintf("session[%d] has no ID", si))
		}
		if sessionIDs[s.ID] {
			errs = append(errs, fmt.Sprintf("duplicate session ID: %s", s.ID))
		}
		sessionIDs[s.ID] = true

		if s.Title == "" {
			errs = append(errs, fmt.Sprintf("session %s has no title", s.ID))
		}
		if len(s.Tasks) == 0 {
			errs = append(errs, fmt.Sprintf("session %s has no tasks", s.ID))
		}
		if len(s.AcceptanceCriteria) == 0 {
			errs = append(errs, fmt.Sprintf("session %s has no acceptance criteria", s.ID))
		}

		// Validate tasks within session
		localIDs := map[string]bool{}
		for ti, t := range s.Tasks {
			if t.ID == "" {
				errs = append(errs, fmt.Sprintf("session %s task[%d] has no ID", s.ID, ti))
			}
			if allTaskIDs[t.ID] {
				errs = append(errs, fmt.Sprintf("duplicate task ID across sessions: %s", t.ID))
			}
			allTaskIDs[t.ID] = true
			localIDs[t.ID] = true

			if t.Description == "" {
				errs = append(errs, fmt.Sprintf("session %s task %s has no description", s.ID, t.ID))
			}
		}

		// Validate intra-session dependencies
		for _, t := range s.Tasks {
			for _, dep := range t.Dependencies {
				if !localIDs[dep] && !allTaskIDs[dep] {
					errs = append(errs, fmt.Sprintf("session %s task %s depends on unknown task %s", s.ID, t.ID, dep))
				}
			}
		}

		// Validate acceptance criteria
		for ci, ac := range s.AcceptanceCriteria {
			if ac.ID == "" {
				errs = append(errs, fmt.Sprintf("session %s criterion[%d] has no ID", s.ID, ci))
			}
			if ac.Description == "" {
				errs = append(errs, fmt.Sprintf("session %s criterion %s has no description", s.ID, ac.ID))
			}
		}

		// Validate infra references
		infraNames := map[string]bool{}
		for _, inf := range sow.Stack.Infra {
			infraNames[inf.Name] = true
		}
		for _, needed := range s.InfraNeeded {
			if !infraNames[needed] {
				errs = append(errs, fmt.Sprintf("session %s references unknown infra: %s", s.ID, needed))
			}
		}
	}

	// Check for dependency cycles across all tasks
	allTasks := sow.AllTasks()
	if cycle := detectCycle(allTasks); cycle != "" {
		errs = append(errs, "dependency cycle: "+cycle)
	}

	return errs
}

// AllTasks returns a flat list of all tasks across all sessions, preserving
// session ordering. Tasks in session N+1 get implicit dependencies on all
// tasks in session N (unless they already have explicit cross-session deps).
func (sow *SOW) AllTasks() []Task {
	var all []Task
	var prevSessionTaskIDs []string

	for _, s := range sow.Sessions {
		for i, t := range s.Tasks {
			// If this is the first task in a session (with no explicit deps from prior session),
			// add implicit dependency on completion of all prior session tasks.
			if i == 0 && len(prevSessionTaskIDs) > 0 {
				hasExplicitCrossDep := false
				prevSet := map[string]bool{}
				for _, id := range prevSessionTaskIDs {
					prevSet[id] = true
				}
				for _, dep := range t.Dependencies {
					if prevSet[dep] {
						hasExplicitCrossDep = true
						break
					}
				}
				if !hasExplicitCrossDep {
					// Add dependency on last task of previous session as a gate
					t.Dependencies = append(t.Dependencies, prevSessionTaskIDs[len(prevSessionTaskIDs)-1])
				}
			}
			all = append(all, t)
		}
		prevSessionTaskIDs = nil
		for _, t := range s.Tasks {
			prevSessionTaskIDs = append(prevSessionTaskIDs, t.ID)
		}
	}
	return all
}

// ToPlan converts a SOW into a flat Plan suitable for the existing scheduler.
// Session boundaries are encoded as task dependencies: the first task of
// session N+1 depends on the last task of session N.
func (sow *SOW) ToPlan() *Plan {
	return &Plan{
		ID:          sow.ID,
		Description: sow.Name + ": " + sow.Description,
		Tasks:       sow.AllTasks(),
	}
}

// SessionByID returns the session with the given ID, or nil.
func (sow *SOW) SessionByID(id string) *Session {
	for i := range sow.Sessions {
		if sow.Sessions[i].ID == id {
			return &sow.Sessions[i]
		}
	}
	return nil
}

// SessionForTask returns the session containing the given task ID, or nil.
func (sow *SOW) SessionForTask(taskID string) *Session {
	for i := range sow.Sessions {
		for _, t := range sow.Sessions[i].Tasks {
			if t.ID == taskID {
				return &sow.Sessions[i]
			}
		}
	}
	return nil
}

// PhaseGroups returns sessions grouped by phase name.
func (sow *SOW) PhaseGroups() map[string][]Session {
	groups := map[string][]Session{}
	for _, s := range sow.Sessions {
		phase := s.Phase
		if phase == "" {
			phase = "default"
		}
		groups[phase] = append(groups[phase], s)
	}
	return groups
}

// InfraForSession returns the infra requirements needed by a specific session.
func (sow *SOW) InfraForSession(sessionID string) []InfraRequirement {
	s := sow.SessionByID(sessionID)
	if s == nil {
		return nil
	}
	infraMap := map[string]InfraRequirement{}
	for _, inf := range sow.Stack.Infra {
		infraMap[inf.Name] = inf
	}
	var result []InfraRequirement
	for _, name := range s.InfraNeeded {
		if inf, ok := infraMap[name]; ok {
			result = append(result, inf)
		}
	}
	return result
}

// ValidateInfraEnvVars checks that all environment variables required by the
// SOW's infrastructure dependencies are set. Returns a list of missing vars.
func (sow *SOW) ValidateInfraEnvVars() []string {
	var missing []string
	seen := map[string]bool{}
	for _, inf := range sow.Stack.Infra {
		for _, v := range inf.EnvVars {
			if seen[v] {
				continue
			}
			seen[v] = true
			if os.Getenv(v) == "" {
				missing = append(missing, fmt.Sprintf("%s (required by %s)", v, inf.Name))
			}
		}
	}
	return missing
}

// DetectStackFromRepo examines a project directory and returns a StackSpec
// based on the files found. This auto-detects monorepo layouts.
func DetectStackFromRepo(projectRoot string) StackSpec {
	spec := StackSpec{}

	// Rust / Cargo workspace
	cargoPath := filepath.Join(projectRoot, "Cargo.toml")
	if data, err := os.ReadFile(cargoPath); err == nil {
		spec.Language = "rust"
		content := string(data)
		if strings.Contains(content, "[workspace]") {
			spec.Monorepo = &MonorepoSpec{Tool: "cargo-workspace"}
			// Extract workspace members
			spec.Monorepo.Packages = parseCargoWorkspaceMembers(content)
		}
		return spec
	}

	// TypeScript / JavaScript monorepos
	pkgPath := filepath.Join(projectRoot, "package.json")
	if data, err := os.ReadFile(pkgPath); err == nil {
		spec.Language = "typescript"

		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
			Workspaces      json.RawMessage   `json:"workspaces"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			allDeps := mergeStringMaps(pkg.Dependencies, pkg.DevDependencies)

			// Detect framework
			if allDeps["next"] != "" {
				spec.Framework = "next"
			} else if allDeps["react-native"] != "" {
				spec.Framework = "react-native"
			} else if allDeps["react"] != "" {
				spec.Framework = "react"
			}

			// Detect monorepo tool
			if fileExistsAt(filepath.Join(projectRoot, "turbo.json")) {
				spec.Monorepo = &MonorepoSpec{Tool: "turborepo"}
			} else if fileExistsAt(filepath.Join(projectRoot, "nx.json")) {
				spec.Monorepo = &MonorepoSpec{Tool: "nx"}
			} else if fileExistsAt(filepath.Join(projectRoot, "lerna.json")) {
				spec.Monorepo = &MonorepoSpec{Tool: "lerna"}
			}

			// Detect package manager
			if fileExistsAt(filepath.Join(projectRoot, "pnpm-lock.yaml")) || fileExistsAt(filepath.Join(projectRoot, "pnpm-workspace.yaml")) {
				if spec.Monorepo == nil {
					spec.Monorepo = &MonorepoSpec{}
				}
				spec.Monorepo.Manager = "pnpm"
			} else if fileExistsAt(filepath.Join(projectRoot, "yarn.lock")) {
				if spec.Monorepo == nil && pkg.Workspaces != nil {
					spec.Monorepo = &MonorepoSpec{}
				}
				if spec.Monorepo != nil {
					spec.Monorepo.Manager = "yarn"
				}
			}

			// Parse workspace packages
			if spec.Monorepo != nil && pkg.Workspaces != nil {
				spec.Monorepo.Packages = parseWorkspacePackages(pkg.Workspaces)
			}

			// Also check pnpm-workspace.yaml
			if spec.Monorepo != nil && len(spec.Monorepo.Packages) == 0 {
				if pnpmWS, err := os.ReadFile(filepath.Join(projectRoot, "pnpm-workspace.yaml")); err == nil {
					spec.Monorepo.Packages = parsePnpmWorkspace(string(pnpmWS))
				}
			}

			if spec.Monorepo != nil && spec.Monorepo.Tool == "" {
				spec.Monorepo.Tool = "workspaces" // generic npm/yarn/pnpm workspaces
			}
		}
		return spec
	}

	// Go
	if fileExistsAt(filepath.Join(projectRoot, "go.mod")) {
		spec.Language = "go"
		return spec
	}

	// Python
	if fileExistsAt(filepath.Join(projectRoot, "pyproject.toml")) || fileExistsAt(filepath.Join(projectRoot, "setup.py")) {
		spec.Language = "python"
		return spec
	}

	return spec
}

// parseCargoWorkspaceMembers extracts member paths from a Cargo.toml [workspace] section.
func parseCargoWorkspaceMembers(content string) []string {
	// Simple parser: look for members = ["crate1", "crate2"]
	var members []string
	inMembers := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "members") && strings.Contains(trimmed, "=") {
			inMembers = true
		}
		if inMembers {
			// Extract quoted strings
			for _, part := range strings.Split(trimmed, "\"") {
				// Every other part (index 1, 3, 5...) is inside quotes
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

// parseWorkspacePackages parses npm/yarn workspace package globs from package.json.
func parseWorkspacePackages(raw json.RawMessage) []string {
	// Workspaces can be string array or object with "packages" field
	var packages []string
	if json.Unmarshal(raw, &packages) == nil {
		return packages
	}
	var wsObj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(raw, &wsObj) == nil {
		return wsObj.Packages
	}
	return nil
}

// parsePnpmWorkspace extracts package globs from pnpm-workspace.yaml.
func parsePnpmWorkspace(content string) []string {
	// Simple YAML parser for:
	// packages:
	//   - 'apps/*'
	//   - 'packages/*'
	var packages []string
	inPackages := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "packages:" {
			inPackages = true
			continue
		}
		if inPackages {
			if strings.HasPrefix(trimmed, "- ") {
				pkg := strings.TrimPrefix(trimmed, "- ")
				pkg = strings.Trim(pkg, "'\"")
				if pkg != "" {
					packages = append(packages, pkg)
				}
			} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				break // new YAML key
			}
		}
	}
	return packages
}

func fileExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func mergeStringMaps(a, b map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range a {
		result[k] = v
	}
	for k, v := range b {
		result[k] = v
	}
	return result
}
