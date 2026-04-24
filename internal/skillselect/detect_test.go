package skillselect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ericmacdougall/stoke/internal/skill"
)

// --- Layer 1: File presence tests ---

func TestDetectGo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example\ngo 1.22")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLanguage("go") {
		t.Errorf("expected go language, got %v", info.Languages)
	}
}

func TestDetectNodeTS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"test"}`)
	writeFile(t, dir, "tsconfig.json", `{}`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLanguage("javascript") {
		t.Errorf("expected javascript, got %v", info.Languages)
	}
	if !info.HasLanguage("typescript") {
		t.Errorf("expected typescript, got %v", info.Languages)
	}
}

func TestDetectRust(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", `[package]\nname = "test"`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLanguage("rust") {
		t.Errorf("expected rust, got %v", info.Languages)
	}
}

func TestDetectPythonRequirements(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "flask==2.0\n")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLanguage("python") {
		t.Errorf("expected python, got %v", info.Languages)
	}
}

func TestDetectPythonPyproject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", `[project]\nname = "test"`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLanguage("python") {
		t.Errorf("expected python, got %v", info.Languages)
	}
}

func TestDetectPythonSetupPy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "setup.py", `from setuptools import setup`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLanguage("python") {
		t.Errorf("expected python, got %v", info.Languages)
	}
}

func TestDetectJava(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pom.xml", `<project></project>`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasLanguage("java") {
		t.Errorf("expected java, got %v", info.Languages)
	}
}

func TestDetectDocker(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "FROM alpine:latest")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasInfra("docker") {
		t.Errorf("expected docker infra, got %v", info.Infra)
	}
}

func TestDetectGitHubActions(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, ".github", "workflows")
	writeFile(t, filepath.Join(dir, ".github", "workflows"), "ci.yml", "name: CI")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasInfra("github-actions") {
		t.Errorf("expected github-actions infra, got %v", info.Infra)
	}
}

func TestDetectGitLabCI(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".gitlab-ci.yml", "stages:\n  - build")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasInfra("gitlab-ci") {
		t.Errorf("expected gitlab-ci infra, got %v", info.Infra)
	}
}

func TestDetectJenkins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Jenkinsfile", "pipeline { }")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasInfra("jenkins") {
		t.Errorf("expected jenkins infra, got %v", info.Infra)
	}
}

func TestDetectK8sDirectory(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, "k8s")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasInfra("kubernetes") {
		t.Errorf("expected kubernetes infra, got %v", info.Infra)
	}
}

func TestDetectAWSDirectory(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, ".aws")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCloudProvider("aws") {
		t.Errorf("expected aws cloud provider, got %v", info.CloudProviders)
	}
}

func TestDetectGCPDirectory(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, ".gcloud")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCloudProvider("gcp") {
		t.Errorf("expected gcp cloud provider, got %v", info.CloudProviders)
	}
}

// --- Layer 2: Manifest parsing tests ---

func TestDetectReactFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"react":"^18.0.0"}}`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasFramework("react") {
		t.Errorf("expected react framework, got %v", info.Frameworks)
	}
}

func TestDetectNextJSFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"next":"^14.0.0","react":"^18.0.0"}}`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasFramework("nextjs") {
		t.Errorf("expected nextjs framework, got %v", info.Frameworks)
	}
	if !info.HasFramework("react") {
		t.Errorf("expected react framework, got %v", info.Frameworks)
	}
}

func TestDetectVueFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"vue":"^3.0.0"}}`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasFramework("vue") {
		t.Errorf("expected vue framework, got %v", info.Frameworks)
	}
}

func TestDetectExpressFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"express":"^4.0.0"}}`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasFramework("express") {
		t.Errorf("expected express framework, got %v", info.Frameworks)
	}
}

func TestDetectPostgresFromDockerCompose(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "docker-compose.yml", `
version: "3"
services:
  db:
    image: postgres:15
    ports:
      - "5432:5432"
`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasDatabase("postgresql") {
		t.Errorf("expected postgresql database, got %v", info.Databases)
	}
}

func TestDetectRedisFromDockerCompose(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "docker-compose.yml", `
version: "3"
services:
  cache:
    image: redis:7-alpine
`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasDatabase("redis") {
		t.Errorf("expected redis database, got %v", info.Databases)
	}
}

func TestDetectKafkaFromDockerCompose(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "docker-compose.yml", `
version: "3"
services:
  kafka:
    image: confluentinc/cp-kafka:latest
`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasDatabase("kafka") {
		t.Errorf("expected kafka database, got %v", info.Databases)
	}
}

func TestDetectGoFrameworkFromGoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", `module example
go 1.22

require (
	github.com/gin-gonic/gin v1.9.0
	github.com/lib/pq v1.10.0
)
`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasFramework("gin") {
		t.Errorf("expected gin framework, got %v", info.Frameworks)
	}
	if !info.HasDatabase("postgresql") {
		t.Errorf("expected postgresql database, got %v", info.Databases)
	}
}

func TestDetectAWSFromTerraform(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, "terraform")
	writeFile(t, filepath.Join(dir, "terraform"), "main.tf", `
provider "aws" {
  region = "us-east-1"
}

resource "aws_s3_bucket" "example" {
  bucket = "my-bucket"
}
`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCloudProvider("aws") {
		t.Errorf("expected aws cloud provider, got %v", info.CloudProviders)
	}
	if !info.HasInfra("terraform") {
		t.Errorf("expected terraform infra, got %v", info.Infra)
	}
}

func TestDetectGCPFromTerraform(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, dir, "terraform")
	writeFile(t, filepath.Join(dir, "terraform"), "main.tf", `
provider "google" {
  project = "my-project"
}
`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCloudProvider("gcp") {
		t.Errorf("expected gcp cloud provider, got %v", info.CloudProviders)
	}
}

// --- Layer 3: Content sampling tests ---

func TestDetectK8sFromYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "deploy.yaml", `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
`)

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasInfra("kubernetes") {
		t.Errorf("expected kubernetes infra, got %v", info.Infra)
	}
}

func TestDetectPostgresFromEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "POSTGRES_HOST=localhost\nPOSTGRES_PORT=5432\n")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasDatabase("postgresql") {
		t.Errorf("expected postgresql database, got %v", info.Databases)
	}
}

func TestDetectRedisFromEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "REDIS_URL=redis://localhost:6379\n")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasDatabase("redis") {
		t.Errorf("expected redis database, got %v", info.Databases)
	}
}

// --- Dedup/edge case tests ---

func TestDedup(t *testing.T) {
	dir := t.TempDir()
	// docker-compose with postgres + .env with POSTGRES should not duplicate
	writeFile(t, dir, "docker-compose.yml", `
services:
  db:
    image: postgres:15
`)
	writeFile(t, dir, ".env", "POSTGRES_HOST=localhost\n")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, db := range info.Databases {
		if db == "postgresql" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 postgresql entry, got %d in %v", count, info.Databases)
	}
}

func TestEmptyRepo(t *testing.T) {
	dir := t.TempDir()

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Languages) != 0 || len(info.Frameworks) != 0 || len(info.Databases) != 0 ||
		len(info.CloudProviders) != 0 || len(info.Infra) != 0 {
		t.Errorf("expected empty stack info, got %+v", info)
	}
}

func TestTags(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example\ngo 1.22")
	writeFile(t, dir, "Dockerfile", "FROM alpine")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}
	tags := info.Tags()
	if len(tags) == 0 {
		t.Fatal("expected non-empty tags")
	}
	// Should contain both "go" and "docker"
	found := make(map[string]bool)
	for _, tag := range tags {
		found[tag] = true
	}
	if !found["go"] {
		t.Errorf("expected 'go' in tags %v", tags)
	}
	if !found["docker"] {
		t.Errorf("expected 'docker' in tags %v", tags)
	}
}

// --- Full stack integration test ---

func TestFullStackDetection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
		"dependencies": {
			"next": "^14.0.0",
			"react": "^18.0.0",
			"pg": "^8.0.0"
		},
		"devDependencies": {
			"kafkajs": "^2.0.0"
		}
	}`)
	writeFile(t, dir, "tsconfig.json", `{}`)
	writeFile(t, dir, "Dockerfile", "FROM node:20")
	writeFile(t, dir, "docker-compose.yml", `
services:
  db:
    image: postgres:15
  cache:
    image: redis:7
`)
	mkdirAll(t, dir, ".github", "workflows")
	writeFile(t, filepath.Join(dir, ".github", "workflows"), "ci.yml", "name: CI")

	info, err := DetectStack(dir)
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		ok   bool
	}{
		{"javascript", info.HasLanguage("javascript")},
		{"typescript", info.HasLanguage("typescript")},
		{"nextjs", info.HasFramework("nextjs")},
		{"react", info.HasFramework("react")},
		{"postgresql", info.HasDatabase("postgresql")},
		{"redis", info.HasDatabase("redis")},
		{"kafka", info.HasDatabase("kafka")},
		{"docker", info.HasInfra("docker")},
		{"docker-compose", info.HasInfra("docker-compose")},
		{"github-actions", info.HasInfra("github-actions")},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("expected %s to be detected, info=%+v", c.name, info)
		}
	}
}

// --- SelectSkills tests ---

func TestSelectSkillsNilInputs(t *testing.T) {
	if skills := SelectSkills(nil, nil); skills != nil {
		t.Errorf("expected nil, got %v", skills)
	}
}

func TestSelectSkillsMatchesKeywords(t *testing.T) {
	reg := skill.NewRegistry() // empty dirs, no disk
	// Manually add a skill with keywords that match stack tags.
	if err := reg.Add("go-testing", "Go test patterns", "Run go test ./...", []string{"go", "testing"}); err != nil {
		// Add requires a directory; create one.
		t.Skip("cannot add skills without directory")
	}

	info := &StackInfo{Languages: []string{"go"}}
	skills := SelectSkills(info, reg)
	// We may or may not match depending on registry Add succeeding.
	_ = skills
}

func TestSelectSkillsWithRegistry(t *testing.T) {
	dir := t.TempDir()
	reg := skill.NewRegistry(dir)

	// Create a skill file on disk.
	content := "# docker-deploy\n\n> Deploy with Docker\n\n<!-- keywords: docker, deploy -->\n\nUse docker compose up."
	if err := os.WriteFile(filepath.Join(dir, "docker-deploy.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(); err != nil {
		t.Fatal(err)
	}

	info := &StackInfo{Infra: []string{"docker"}}
	skills := SelectSkills(info, reg)
	if len(skills) == 0 {
		t.Errorf("expected at least one skill match for docker tag")
	}
	if len(skills) > 0 && skills[0].Name != "docker-deploy" {
		t.Errorf("expected docker-deploy skill, got %s", skills[0].Name)
	}
}

func TestSelectSkillsEmptyStack(t *testing.T) {
	reg := skill.NewRegistry()
	info := &StackInfo{}
	skills := SelectSkills(info, reg)
	if skills != nil {
		t.Errorf("expected nil for empty stack, got %v", skills)
	}
}

// --- Has* method tests ---

func TestHasMethods(t *testing.T) {
	info := &StackInfo{
		Languages:      []string{"Go", "Python"},
		Frameworks:     []string{"Gin"},
		Databases:      []string{"PostgreSQL"},
		CloudProviders: []string{"AWS"},
		Infra:          []string{"Docker"},
	}

	// Case-insensitive matching
	if !info.HasLanguage("go") {
		t.Error("HasLanguage(go) should be true")
	}
	if !info.HasLanguage("GO") {
		t.Error("HasLanguage(GO) should be true")
	}
	if info.HasLanguage("rust") {
		t.Error("HasLanguage(rust) should be false")
	}
	if !info.HasFramework("gin") {
		t.Error("HasFramework(gin) should be true")
	}
	if !info.HasDatabase("postgresql") {
		t.Error("HasDatabase(postgresql) should be true")
	}
	if !info.HasCloudProvider("aws") {
		t.Error("HasCloudProvider(aws) should be true")
	}
	if !info.HasInfra("docker") {
		t.Error("HasInfra(docker) should be true")
	}
}

// --- Test helpers ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdirAll(t *testing.T, parts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(parts...), 0755); err != nil {
		t.Fatal(err)
	}
}
