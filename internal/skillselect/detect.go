// Package skillselect auto-detects a repository's technology stack from its
// file structure and maps detected technologies to relevant skills.
//
// Detection uses a three-layer approach ordered by cost:
//   - Layer 1: File presence checks (fastest)
//   - Layer 2: Manifest/lockfile parsing (medium cost)
//   - Layer 3: Content sampling (highest cost, ambiguous cases only)
package skillselect

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RelayOne/r1-agent/internal/skill"
)

// StackInfo holds the detected technology stack of a repository.
type StackInfo struct {
	Languages      []string `json:"languages"`
	Frameworks     []string `json:"frameworks"`
	Databases      []string `json:"databases"`
	CloudProviders []string `json:"cloud_providers"`
	Infra          []string `json:"infra"` // Docker, Kubernetes, Terraform, CI/CD, etc.
}

// HasLanguage reports whether lang is in the detected languages.
func (s *StackInfo) HasLanguage(lang string) bool {
	return containsLower(s.Languages, lang)
}

// HasFramework reports whether fw is in the detected frameworks.
func (s *StackInfo) HasFramework(fw string) bool {
	return containsLower(s.Frameworks, fw)
}

// HasDatabase reports whether db is in the detected databases.
func (s *StackInfo) HasDatabase(db string) bool {
	return containsLower(s.Databases, db)
}

// HasCloudProvider reports whether cp is in the detected cloud providers.
func (s *StackInfo) HasCloudProvider(cp string) bool {
	return containsLower(s.CloudProviders, cp)
}

// HasInfra reports whether tool is in the detected infrastructure tools.
func (s *StackInfo) HasInfra(tool string) bool {
	return containsLower(s.Infra, tool)
}

// Tags returns a deduplicated sorted list of all detected technology names,
// useful for matching against skill keywords.
func (s *StackInfo) Tags() []string {
	seen := make(map[string]bool)
	var tags []string
	for _, list := range [][]string{s.Languages, s.Frameworks, s.Databases, s.CloudProviders, s.Infra} {
		for _, t := range list {
			lower := strings.ToLower(t)
			if !seen[lower] {
				seen[lower] = true
				tags = append(tags, lower)
			}
		}
	}
	sort.Strings(tags)
	return tags
}

// DetectStack examines repoRoot and returns a StackInfo describing the
// repository's technology stack. It never returns a nil StackInfo.
func DetectStack(repoRoot string) (*StackInfo, error) {
	info := &StackInfo{}

	// --- Layer 1: File presence ---
	layer1(repoRoot, info)

	// --- Layer 2: Manifest parsing ---
	layer2(repoRoot, info)

	// --- Layer 3: Content sampling ---
	layer3(repoRoot, info)

	dedupAll(info)
	return info, nil
}

// layer1 checks for well-known file/directory presence (fastest).
func layer1(root string, info *StackInfo) {
	// Languages
	if fileExists(root, "go.mod") {
		info.Languages = append(info.Languages, "go")
	}
	if fileExists(root, "package.json") {
		info.Languages = append(info.Languages, "javascript")
		if fileExists(root, "tsconfig.json") {
			info.Languages = append(info.Languages, "typescript")
		}
	}
	if fileExists(root, "Cargo.toml") {
		info.Languages = append(info.Languages, "rust")
	}
	if fileExists(root, "requirements.txt") || fileExists(root, "pyproject.toml") || fileExists(root, "setup.py") {
		info.Languages = append(info.Languages, "python")
	}
	if fileExists(root, "pom.xml") || fileExists(root, "build.gradle") || fileExists(root, "build.gradle.kts") {
		info.Languages = append(info.Languages, "java")
	}

	// Infrastructure
	if fileExists(root, "Dockerfile") || fileExists(root, ".dockerignore") {
		info.Infra = append(info.Infra, "docker")
	}
	if fileExists(root, "docker-compose.yml") || fileExists(root, "docker-compose.yaml") {
		info.Infra = append(info.Infra, "docker-compose")
	}
	if dirExists(root, "terraform") || dirExists(root, ".terraform") {
		info.Infra = append(info.Infra, "terraform")
	}
	if dirExists(root, ".github", "workflows") {
		info.Infra = append(info.Infra, "github-actions")
	}
	if fileExists(root, ".gitlab-ci.yml") {
		info.Infra = append(info.Infra, "gitlab-ci")
	}
	if fileExists(root, "Jenkinsfile") {
		info.Infra = append(info.Infra, "jenkins")
	}

	// Kubernetes directories
	if dirExists(root, "k8s") || dirExists(root, "kubernetes") {
		info.Infra = append(info.Infra, "kubernetes")
	}

	// Cloud provider hints from dotfiles/directories
	if dirExists(root, ".aws") {
		info.CloudProviders = append(info.CloudProviders, "aws")
	}
	if dirExists(root, ".gcloud") {
		info.CloudProviders = append(info.CloudProviders, "gcp")
	}
}

// layer2 parses manifests and lockfiles at medium cost.
func layer2(root string, info *StackInfo) {
	parsePackageJSON(root, info)
	parseDockerCompose(root, info)
	parseGoMod(root, info)
	parseTerraform(root, info)
}

// layer3 does content sampling for cases that layers 1-2 leave ambiguous.
func layer3(root string, info *StackInfo) {
	// Kubernetes: scan YAML files in root for "kind: Deployment" / "kind: Service"
	if !containsLower(info.Infra, "kubernetes") {
		detectK8sYAML(root, info)
	}

	// PostgreSQL/Redis hints from .env files
	detectEnvDatabases(root, info)
}

// --- Layer 2 helpers ---

func parsePackageJSON(root string, info *StackInfo) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	all := mergeMaps(pkg.Dependencies, pkg.DevDependencies)

	// Frameworks
	if _, ok := all["next"]; ok {
		info.Frameworks = append(info.Frameworks, "nextjs")
	}
	if _, ok := all["react"]; ok {
		info.Frameworks = append(info.Frameworks, "react")
	}
	if _, ok := all["vue"]; ok {
		info.Frameworks = append(info.Frameworks, "vue")
	}
	if _, ok := all["@angular/core"]; ok {
		info.Frameworks = append(info.Frameworks, "angular")
	}
	if _, ok := all["svelte"]; ok {
		info.Frameworks = append(info.Frameworks, "svelte")
	}
	if _, ok := all["express"]; ok {
		info.Frameworks = append(info.Frameworks, "express")
	}

	// Database clients
	if _, ok := all["pg"]; ok {
		info.Databases = append(info.Databases, "postgresql")
	}
	if _, ok := all["redis"]; ok {
		info.Databases = append(info.Databases, "redis")
	}
	if _, ok := all["ioredis"]; ok {
		info.Databases = append(info.Databases, "redis")
	}
	if _, ok := all["kafkajs"]; ok {
		info.Databases = append(info.Databases, "kafka")
	}
	if _, ok := all["mongodb"]; ok {
		info.Databases = append(info.Databases, "mongodb")
	}
	if _, ok := all["mongoose"]; ok {
		info.Databases = append(info.Databases, "mongodb")
	}

	// Cloud SDKs
	if _, ok := all["aws-sdk"]; ok {
		info.CloudProviders = append(info.CloudProviders, "aws")
	}
	if _, ok := all["@aws-sdk/client-s3"]; ok {
		info.CloudProviders = append(info.CloudProviders, "aws")
	}
	if _, ok := all["@google-cloud/storage"]; ok {
		info.CloudProviders = append(info.CloudProviders, "gcp")
	}
}

func parseDockerCompose(root string, info *StackInfo) {
	var data []byte
	var err error
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml"} {
		data, err = os.ReadFile(filepath.Join(root, name))
		if err == nil {
			break
		}
	}
	if err != nil {
		return
	}

	// Simple line-based scanning for image references. Full YAML parsing would
	// pull in a dependency; line scanning is sufficient for common patterns.
	content := strings.ToLower(string(data))
	if strings.Contains(content, "postgres") {
		info.Databases = append(info.Databases, "postgresql")
	}
	if strings.Contains(content, "redis") {
		info.Databases = append(info.Databases, "redis")
	}
	if strings.Contains(content, "kafka") {
		info.Databases = append(info.Databases, "kafka")
	}
	if strings.Contains(content, "mongo") {
		info.Databases = append(info.Databases, "mongodb")
	}
	if strings.Contains(content, "mysql") {
		info.Databases = append(info.Databases, "mysql")
	}
	if strings.Contains(content, "elasticsearch") || strings.Contains(content, "elastic") {
		info.Databases = append(info.Databases, "elasticsearch")
	}
}

func parseGoMod(root string, info *StackInfo) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return
	}
	content := string(data)

	goModDeps := map[string]struct {
		category string
		value    string
	}{
		"github.com/gin-gonic/gin":     {"framework", "gin"},
		"github.com/labstack/echo":     {"framework", "echo"},
		"github.com/gofiber/fiber":     {"framework", "fiber"},
		"github.com/gorilla/mux":       {"framework", "gorilla"},
		"github.com/lib/pq":            {"database", "postgresql"},
		"github.com/jackc/pgx":         {"database", "postgresql"},
		"github.com/go-redis/redis":    {"database", "redis"},
		"github.com/segmentio/kafka-go": {"database", "kafka"},
		"github.com/aws/aws-sdk-go":    {"cloud", "aws"},
		"cloud.google.com/go":          {"cloud", "gcp"},
		"github.com/Azure/azure-sdk-for-go": {"cloud", "azure"},
	}

	for dep, entry := range goModDeps {
		if strings.Contains(content, dep) {
			switch entry.category {
			case "framework":
				info.Frameworks = append(info.Frameworks, entry.value)
			case "database":
				info.Databases = append(info.Databases, entry.value)
			case "cloud":
				info.CloudProviders = append(info.CloudProviders, entry.value)
			}
		}
	}
}

func parseTerraform(root string, info *StackInfo) {
	tfDir := filepath.Join(root, "terraform")
	entries, err := os.ReadDir(tfDir)
	if err != nil {
		// Also check root for *.tf files
		entries, err = os.ReadDir(root)
		if err != nil {
			return
		}
		tfDir = root
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tfDir, e.Name()))
		if err != nil {
			continue
		}
		content := strings.ToLower(string(data))
		if strings.Contains(content, "provider \"aws\"") || strings.Contains(content, "aws_") {
			info.CloudProviders = append(info.CloudProviders, "aws")
		}
		if strings.Contains(content, "provider \"google\"") || strings.Contains(content, "google_") {
			info.CloudProviders = append(info.CloudProviders, "gcp")
		}
		if strings.Contains(content, "provider \"azurerm\"") || strings.Contains(content, "azurerm_") {
			info.CloudProviders = append(info.CloudProviders, "azure")
		}
	}
}

// --- Layer 3 helpers ---

func detectK8sYAML(root string, info *StackInfo) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	k8sKinds := []string{"kind: deployment", "kind: service", "kind: statefulset", "kind: ingress", "kind: configmap"}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		lower := strings.ToLower(string(data))
		for _, kind := range k8sKinds {
			if strings.Contains(lower, kind) {
				info.Infra = append(info.Infra, "kubernetes")
				return
			}
		}
	}
}

func detectEnvDatabases(root string, info *StackInfo) {
	for _, envFile := range []string{".env", ".env.example", ".env.local"} {
		f, err := os.Open(filepath.Join(root, envFile))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.ToUpper(scanner.Text())
			if strings.HasPrefix(line, "POSTGRES") || strings.HasPrefix(line, "PG_") || strings.HasPrefix(line, "PGHOST") || strings.HasPrefix(line, "DATABASE_URL") && strings.Contains(line, "POSTGRES") {
				info.Databases = append(info.Databases, "postgresql")
			}
			if strings.HasPrefix(line, "REDIS") {
				info.Databases = append(info.Databases, "redis")
			}
		}
		f.Close()
	}
}

// --- SelectSkills ---

// SelectSkills maps a detected StackInfo to relevant skills from the registry.
// It matches stack tags against skill keywords.
func SelectSkills(stack *StackInfo, registry *skill.Registry) []*skill.Skill {
	if stack == nil || registry == nil {
		return nil
	}
	tags := stack.Tags()
	if len(tags) == 0 {
		return nil
	}

	// Build a combined text from all tags for keyword matching.
	text := strings.Join(tags, " ")
	matches := registry.Match(text)

	// Deduplicate (Match already returns unique skills, but be safe).
	seen := make(map[string]bool)
	var result []*skill.Skill
	for _, s := range matches {
		if !seen[s.Name] {
			seen[s.Name] = true
			result = append(result, s)
		}
	}
	return result
}

// --- Utilities ---

func fileExists(parts ...string) bool {
	info, err := os.Stat(filepath.Join(parts...))
	return err == nil && !info.IsDir()
}

func dirExists(parts ...string) bool {
	info, err := os.Stat(filepath.Join(parts...))
	return err == nil && info.IsDir()
}

func containsLower(slice []string, val string) bool {
	lower := strings.ToLower(val)
	for _, s := range slice {
		if strings.ToLower(s) == lower {
			return true
		}
	}
	return false
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

func dedupAll(info *StackInfo) {
	info.Languages = dedupStrings(info.Languages)
	info.Frameworks = dedupStrings(info.Frameworks)
	info.Databases = dedupStrings(info.Databases)
	info.CloudProviders = dedupStrings(info.CloudProviders)
	info.Infra = dedupStrings(info.Infra)
}

func dedupStrings(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, s := range ss {
		lower := strings.ToLower(s)
		if !seen[lower] {
			seen[lower] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
